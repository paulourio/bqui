package app

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/store"
)

func TestJobTooltip(t *testing.T) {
	sql := strings.Repeat("SELECT 1\n", 14) + "FROM t"
	r := JobRow{
		Ref: bq.JobRef{Project: "p", Location: "US", JobID: "job_1"}, State: "done", Type: "QUERY",
		Created: time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC), Duration: 1234 * time.Millisecond,
		Processed: 2 << 20, Statement: "SELECT", SQL: sql,
	}

	got := JobTooltip(r)
	for _, want := range []string{"SELECT 1\nSELECT 1", "… 5 more lines", "Done · ", "1.23s", "2.00 MiB processed", "SELECT\np · US · job_1"} {
		if !strings.Contains(got, want) {
			t.Errorf("tooltip lacks %q:\n%s", want, got)
		}
	}

	if strings.Count(got, "SELECT 1") != tooltipSQLLines {
		t.Errorf("tooltip shows %d SQL lines", strings.Count(got, "SELECT 1"))
	}

	r = JobRow{Ref: bq.JobRef{JobID: "j"}, State: "error", Type: "LOAD", CacheHit: true, Error: "boom"}
	if got := JobTooltip(r); !strings.Contains(got, "(no SQL: load job)") || !strings.Contains(got, "cached") || !strings.HasSuffix(got, "✕ boom") {
		t.Errorf("load tooltip:\n%s", got)
	}
}

func TestJobDocIsReadOnlyAndNotPersisted(t *testing.T) {
	tmp := t.TempDir()

	lib, _ := store.OpenLibrary(filepath.Join(tmp, "libraries"), store.DefaultLibrary)
	db, err := store.Open(filepath.Join(tmp, "bqui.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	open := func() *Workspace {
		w, oerr := OpenWorkspace(WorkspaceOptions{DB: db, Library: lib, InitialSQL: "SELECT 'mine'", Delay: time.Hour})
		if oerr != nil {
			t.Fatal(oerr)
		}

		return w
	}

	w := open()
	mine, _ := w.CurrentQuery()
	w.Edited("SELECT 'edited'", 0, 0)

	ref := bq.JobRef{Project: "p", Location: "US", JobID: "job_42"}
	if err = w.OpenJobDoc(ref, "SELECT 42"); err != nil {
		t.Fatal(err)
	}

	// The open query was saved on the way; the job doc is read-only.
	if _, body, _ := store.ReadQuery(mine.Path); strings.TrimSpace(body) != "SELECT 'edited'" {
		t.Fatalf("query not saved before job doc: %q", body)
	}

	w.Edited("SELECT 'typed into job'", 0, 0)

	if w.Tab().SQL != "SELECT 42" || w.Dirty() || w.Title() != "Job History / job_42" {
		t.Fatalf("job doc = %+v dirty=%v title=%q", w.Tab(), w.Dirty(), w.Title())
	}

	if got, ok := w.JobDoc(); !ok || got != ref {
		t.Fatalf("JobDoc = %+v %v", got, ok)
	}

	if err = w.Flush(); err != nil {
		t.Fatal(err)
	}

	// Relaunch restores the last real query, not the job document.
	if w2 := open(); w2.Tab().File != mine.Path {
		t.Fatalf("relaunch = %+v", w2.Tab())
	}

	// Save to Workbook makes an editable query with the job's SQL.
	q, err := w.SaveJobDocTo(mine.Workbook)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := w.JobDoc(); ok || !strings.Contains(w.Tab().SQL, "SELECT 42") || w.Tab().File != q.Path {
		t.Fatalf("after save = %+v", w.Tab())
	}

	if _, body, _ := store.ReadQuery(q.Path); !strings.HasPrefix(body, "-- From job job_42\nSELECT 42") {
		t.Errorf("saved body = %q", body)
	}
}
