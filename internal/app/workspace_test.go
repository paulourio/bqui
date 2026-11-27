package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/store"
)

func openWorkspace(t *testing.T, dbPath, dir string) *Workspace {
	t.Helper()

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = db.Close() })

	w, err := OpenWorkspace(WorkspaceOptions{DB: db, QueriesDir: dir, InitialSQL: "SELECT 1", Delay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	return w
}

func TestWorkspaceRestoresAfterCrash(t *testing.T) {
	tmp := t.TempDir()
	dbPath, dir := filepath.Join(tmp, "bqui.db"), filepath.Join(tmp, "queries")

	w := openWorkspace(t, dbPath, dir)
	if w.Tab().SQL != "SELECT 1" || w.Dirty() {
		t.Fatalf("new workspace = %+v dirty=%v", w.Tab(), w.Dirty())
	}

	w.Edited("SELECT 42 -- unsaved", 0, 9)
	w.SetSessionMode(true)

	// Autosave runs on its own after the debounce: no explicit Flush (the
	// app may be killed at any time).
	deadline := time.Now().Add(5 * time.Second)
	for n, _ := w.Saves(); n == 0; n, _ = w.Saves() {
		if time.Now().After(deadline) {
			t.Fatal("autosave never ran")
		}

		time.Sleep(5 * time.Millisecond)
	}

	w2 := openWorkspace(t, dbPath, dir)
	tab := w2.Tab()

	if tab.SQL != "SELECT 42 -- unsaved" || tab.CursorCol != 9 || !tab.SessionMode {
		t.Fatalf("restored = %+v", tab)
	}
}

func TestWorkspaceSaveOpenAndDirty(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "queries")
	w := openWorkspace(t, filepath.Join(tmp, "bqui.db"), dir)

	w.Edited("SELECT word FROM shakespeare", 0, 0)

	if err := w.Save(); !errors.Is(err, ErrNoFile) {
		t.Fatalf("save unlinked = %v", err)
	}

	path := filepath.Join(dir, "words.sql")
	if err := w.SaveAs(path, "", "p"); err != nil {
		t.Fatal(err)
	}

	if w.Dirty() || w.Tab().Title != "words" || w.Title() != "words" {
		t.Fatalf("after save as: dirty=%v title=%q", w.Dirty(), w.Title())
	}

	w.Edited("SELECT word, word_count FROM shakespeare", 0, 0)

	if !w.Dirty() || w.Title() != "words — Edited" {
		t.Fatalf("after edit: dirty=%v title=%q", w.Dirty(), w.Title())
	}

	if err := w.Save(); err != nil {
		t.Fatal(err)
	}

	q, body, err := store.ReadQuery(path)
	if err != nil || body != "SELECT word, word_count FROM shakespeare\n" || q.Project != "p" {
		t.Fatalf("file = %+v %q %v", q, body, err)
	}

	w.New()
	if w.Tab().File != "" || w.Dirty() {
		t.Fatalf("new = %+v", w.Tab())
	}

	sql, err := w.Open(path)
	if err != nil || sql != body || w.Dirty() {
		t.Fatalf("open = %q %v dirty=%v", sql, err, w.Dirty())
	}

	list, err := w.SavedQueries()
	if err != nil || len(list) != 1 || list[0].Title != "words" {
		t.Fatalf("saved = %+v %v", list, err)
	}

	// A linked file deleted behind our back: text survives, link drops.
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	_ = os.Remove(path)

	w2 := openWorkspace(t, filepath.Join(tmp, "bqui.db"), dir)
	if w2.Tab().File != "" || w2.Tab().SQL != body {
		t.Errorf("missing file restore = %+v", w2.Tab())
	}
}

func TestWorkspaceSessionsAndRunHistory(t *testing.T) {
	tmp := t.TempDir()
	w := openWorkspace(t, filepath.Join(tmp, "bqui.db"), tmp)
	rn, _ := newTestRunner(t)

	run := func(sql string) {
		req := bq.QueryRequest{SQL: sql}
		w.Prepare(&req)
		w.Finished(wait(t, rn.Start(req)), FakeProject, "")
	}

	run("SELECT 1 FROM orders")

	if on, id := w.SessionMode(); on || id != "" {
		t.Fatalf("session without mode = %v %q", on, id)
	}

	w.SetSessionMode(true)
	run("CREATE TEMP TABLE t AS SELECT 1")

	_, id := w.SessionMode()
	if id == "" {
		t.Fatal("session not captured")
	}

	req := bq.QueryRequest{SQL: "SELECT * FROM t"}
	w.Prepare(&req)

	if req.SessionID != id || req.CreateSession {
		t.Fatalf("joined request = %+v", req)
	}

	w.SetSessionMode(false)

	if _, id := w.SessionMode(); id != "" {
		t.Error("turning session mode off keeps the session")
	}

	deadline := time.Now().Add(5 * time.Second)

	for {
		runs, err := w.SearchRuns(context.Background(), "temp")
		if err != nil {
			t.Fatal(err)
		}

		if len(runs) == 1 && runs[0].SQL == "CREATE TEMP TABLE t AS SELECT 1" && runs[0].JobID != "" {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("runs = %+v", runs)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func TestJobRowsFromRunsAndFilter(t *testing.T) {
	rows := JobRowsFromRuns([]store.Run{
		{Project: "p", JobID: "j1", SQL: "SELECT word FROM shakespeare", State: "done"},
		{Project: "p", JobID: "j2", SQL: "SELEC 1", State: "failed", Error: "syntax"},
	})

	if rows[1].State != "error" || rows[0].User != LocalUser || rows[0].Ref.JobID != "j1" {
		t.Fatalf("rows = %+v", rows)
	}

	if got := FilterJobRows(rows, "WORD shakes"); len(got) != 1 || got[0].Ref.JobID != "j1" {
		t.Errorf("filter = %+v", got)
	}

	if got := FilterJobRows(rows, " "); len(got) != 2 {
		t.Errorf("empty filter = %d", len(got))
	}
}

func TestApplyPreferencesAndBytesLimit(t *testing.T) {
	p := store.Preferences{Location: "EU", DefaultDataset: "proj.ds", MaxBytesBilled: 1 << 20}

	req := bq.QueryRequest{SQL: "SELECT 1 FROM orders"}
	ApplyPreferences(p, &req)

	if req.Location != "EU" || req.MaxBytesBilled != 1<<20 || req.DefaultDataset == nil || req.DefaultDataset.Dataset != "ds" {
		t.Fatalf("req = %+v", req)
	}

	keep := bq.QueryRequest{Location: "US", MaxBytesBilled: 5}
	ApplyPreferences(p, &keep)

	if keep.Location != "US" || keep.MaxBytesBilled != 5 {
		t.Errorf("explicit values overridden: %+v", keep)
	}

	// The fake bills 10 MiB: a 1 MiB limit fails like BigQuery does.
	rn, _ := newTestRunner(t)
	if s := wait(t, rn.Start(req)); s.State != RunFailed || !strings.Contains(s.Err.Error(), "exceeded limit for bytes billed") {
		t.Errorf("limited run = %v %v", s.State, s.Err)
	}
}

func TestWorkspaceLibrary(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	lib, err := store.OpenLibrary(filepath.Join(tmp, "libraries"), store.DefaultLibrary)
	if err != nil {
		t.Fatal(err)
	}

	db, err := store.Open(filepath.Join(tmp, "bqui.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	open := func() *Workspace {
		w, oerr := OpenWorkspace(WorkspaceOptions{DB: db, Library: lib, InitialSQL: "SELECT 'sample'", Delay: time.Hour})
		if oerr != nil {
			t.Fatal(oerr)
		}

		return w
	}

	// First launch: the sample becomes workbook-<today>/Untitled-1.
	w := open()
	q, ok := w.CurrentQuery()

	if !ok || q.Name != "Untitled-1" || !strings.HasPrefix(q.Workbook, "workbook-") || strings.TrimSpace(w.Tab().SQL) != "SELECT 'sample'" || w.FileID() == "" {
		t.Fatalf("first query = %+v %v, tab %+v", q, ok, w.Tab())
	}

	if w.Title() != q.Workbook+" / Untitled-1" {
		t.Errorf("title = %q", w.Title())
	}

	// Edits autosave to the file on Flush (no explicit save).
	w.Edited("SELECT 42", 0, 0)

	if err = w.Flush(); err != nil {
		t.Fatal(err)
	}

	if _, body, _ := store.ReadQuery(q.Path); body != "SELECT 42\n" {
		t.Fatalf("autosaved body = %q", body)
	}

	// New query; switching back keeps edits made in the second one.
	q2, err := w.NewQuery(q.Workbook)
	if err != nil || q2.Name != "Untitled-2" || w.Tab().SQL != "" {
		t.Fatalf("new query = %+v %v", q2, err)
	}

	w.Edited("SELECT 2", 0, 0)

	if _, err = w.Switch(q.Path); err != nil || w.Tab().SQL != "SELECT 42\n" {
		t.Fatalf("switch back = %q %v", w.Tab().SQL, err)
	}

	if _, body, _ := store.ReadQuery(q2.Path); body != "SELECT 2\n" {
		t.Errorf("switching away did not save: %q", body)
	}

	// Runs attach to the file; renames keep the link (stable ID).
	id := w.FileID()
	w.Finished(RunSnapshot{Kind: "query", SQL: "SELECT 42", State: RunDone, Started: time.Now()}, "p", id)

	renamed, err := lib.MoveQuery(q, q.Workbook, "answer")
	if err != nil {
		t.Fatal(err)
	}

	w.Relink(q, renamed)

	if w.Tab().File != renamed.Path || !strings.HasSuffix(w.Title(), "/ answer") {
		t.Fatalf("relinked = %+v %q", w.Tab(), w.Title())
	}

	deadline := time.Now().Add(5 * time.Second)

	for {
		runs, _ := w.FileRuns(ctx)
		if len(runs) == 1 && runs[0].SQL == "SELECT 42" {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("file runs = %+v", runs)
		}

		time.Sleep(5 * time.Millisecond)
	}

	if _, err := w.Switch(q2.Path); err != nil {
		t.Fatal(err)
	}

	if runs, _ := w.FileRuns(ctx); len(runs) != 0 {
		t.Errorf("other file has %d runs", len(runs))
	}

	// Relaunch restores the open query.
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	if w2 := open(); w2.Tab().File != q2.Path || w2.Tab().SQL != "SELECT 2\n" {
		t.Errorf("relaunch = %+v", w2.Tab())
	}
}
