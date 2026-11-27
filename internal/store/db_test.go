package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTest(t *testing.T) (*DB, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "bqui.db")

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = db.Close() })

	return db, path
}

func TestTabsPersistAcrossReopen(t *testing.T) {
	ctx := context.Background()
	db, path := openTest(t)

	tabs := []Tab{
		{ID: "b", Position: 1, Title: "Two", SQL: "SELECT 2"},
		{ID: "a", Position: 0, Title: "One", SQL: "SELECT 1", CursorLine: 3, SessionMode: true, SessionID: "s1"},
	}

	for _, tab := range tabs {
		if err := db.SaveTab(ctx, tab); err != nil {
			t.Fatal(err)
		}
	}

	tabs[1].SQL = "SELECT 1 -- edited"
	if err := db.SaveTab(ctx, tabs[1]); err != nil {
		t.Fatal(err)
	}

	if err := db.SetMeta(ctx, "active_tab", "b"); err != nil {
		t.Fatal(err)
	}

	_ = db.Close()

	db2, err := Open(path) // reopen: migrations must be idempotent
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	got, err := db2.Tabs(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 || got[0].ID != "a" || got[0].SQL != "SELECT 1 -- edited" || !got[0].SessionMode || got[0].SessionID != "s1" || got[0].CursorLine != 3 {
		t.Fatalf("tabs = %+v", got)
	}

	if v, _ := db2.Meta(ctx, "active_tab"); v != "b" {
		t.Errorf("active tab = %q", v)
	}

	if v, _ := db2.Meta(ctx, "missing"); v != "" {
		t.Errorf("missing meta = %q", v)
	}

	if err := db2.DeleteTab(ctx, "a"); err != nil {
		t.Fatal(err)
	}

	if got, _ := db2.Tabs(ctx); len(got) != 1 {
		t.Errorf("after delete = %+v", got)
	}
}

func TestRunHistoryFullTextSearch(t *testing.T) {
	ctx := context.Background()
	db, _ := openTest(t)
	base := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)

	runs := []Run{
		{Started: base, Project: "p", JobID: "job_aaa", SQL: "SELECT word FROM shakespeare", State: "done", Rows: 10},
		{Started: base.Add(time.Minute), Project: "p", JobID: "job_bbb", SQL: "SELECT user_id FROM events WHERE day = '2026-11-01'", State: "failed", Error: "boom"},
		{Started: base.Add(2 * time.Minute), Project: "p", JobID: "job_ccc", SQL: "CREATE TABLE d.events_copy AS SELECT * FROM events", State: "done"},
	}

	for _, r := range runs {
		if err := db.RecordRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	all, err := db.SearchRuns(ctx, "", 10)
	if err != nil || len(all) != 3 || all[0].JobID != "job_ccc" {
		t.Fatalf("all = %+v, %v", all, err)
	}

	cases := map[string][]string{
		"events":       {"job_ccc", "job_bbb"},
		"shakes":       {"job_aaa"}, // prefix match
		"job_bbb":      {"job_bbb"},
		"user_id day":  {"job_bbb"},
		`"); DROP --"`: {}, // never FTS syntax
		"nothing-here": {},
	}

	for q, want := range cases {
		got, err := db.SearchRuns(ctx, q, 10)
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}

		var ids []string
		for _, r := range got {
			ids = append(ids, r.JobID)
		}

		if strings.Join(ids, ",") != strings.Join(want, ",") {
			t.Errorf("search %q = %v, want %v", q, ids, want)
		}
	}
}

func TestSavedQueriesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	q := SavedQuery{Path: filepath.Join(dir, QueryFileName("Top words!")), Title: "Top words!", Project: "p", Labels: map[string]string{"team": "data"}}

	if filepath.Base(q.Path) != "top-words.sql" {
		t.Fatalf("file name = %s", filepath.Base(q.Path))
	}

	if err := WriteQuery(q, "SELECT 1;\n\n"); err != nil {
		t.Fatal(err)
	}

	b, _ := os.ReadFile(q.Path)
	if !strings.HasPrefix(string(b), "/* bqui\ntitle = \"Top words!\"\n") || !strings.HasSuffix(string(b), "*/\nSELECT 1;\n") {
		t.Fatalf("file =\n%s", b)
	}

	if err := os.WriteFile(filepath.Join(dir, "plain.sql"), []byte("SELECT 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}

	_ = os.WriteFile(filepath.Join(dir, ".git", "x.sql"), nil, 0o600)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o600)

	list, err := ListQueries(dir)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}

	if list[0].Title != "plain" || list[1].Title != "Top words!" || list[1].Labels["team"] != "data" {
		t.Errorf("list = %+v", list)
	}

	got, body, err := ReadQuery(q.Path)
	if err != nil || body != "SELECT 1;\n" || got.Project != "p" {
		t.Errorf("read = %+v %q %v", got, body, err)
	}

	if l, err := ListQueries(filepath.Join(dir, "missing")); err != nil || len(l) != 0 {
		t.Errorf("missing dir = %v %v", l, err)
	}
}
