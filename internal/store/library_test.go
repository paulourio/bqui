package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestLibraryWorkbooksAndQueries(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)

	l, err := OpenLibrary(root, DefaultLibrary)
	if err != nil {
		t.Fatal(err)
	}

	first, err := l.EnsureStarted("SELECT 1", now)
	if err != nil || first.Name != "Untitled-1" || first.Workbook != "workbook-2026-11-27" {
		t.Fatalf("started = %+v %v", first, err)
	}

	if again, _ := l.EnsureStarted("x", now); again.Path != "" {
		t.Errorf("second start created %+v", again)
	}

	w2, _ := l.CreateWorkbook("", now)
	if w2.Name != "workbook-2026-11-27-2" {
		t.Errorf("collision name = %s", w2.Name)
	}

	for range 10 {
		if _, err = l.CreateQuery(first.Workbook, ""); err != nil {
			t.Fatal(err)
		}
	}

	qs, _ := l.Queries(first.Workbook)
	if len(qs) != 11 || qs[1].Name != "Untitled-2" || qs[10].Name != "Untitled-11" {
		t.Fatalf("natural order = %v", names(qs))
	}

	// Renames keep the file ID; .sql typed by the user is dropped.
	id, err := EnsureID(first.Path)
	if err != nil || id == "" {
		t.Fatal(err)
	}

	renamed, err := l.MoveQuery(first, first.Workbook, "top words.sql")
	if err != nil || renamed.Name != "top words" {
		t.Fatalf("rename = %+v %v", renamed, err)
	}

	moved, err := l.MoveQuery(renamed, w2.Name, "top words")
	if err != nil {
		t.Fatal(err)
	}

	if got, _ := EnsureID(moved.Path); got != id {
		t.Errorf("id after rename+move = %q, want %q", got, id)
	}

	if _, body, _ := ReadQuery(moved.Path); body != "SELECT 1\n" {
		t.Errorf("body = %q", body)
	}

	if _, err := l.MoveQuery(qs[1], w2.Name, "top words"); !errors.Is(err, ErrExists) {
		t.Errorf("collision = %v", err)
	}

	for _, bad := range []string{"", "a/b", ".hidden", "x:y"} {
		if _, err := l.MoveQuery(qs[1], first.Workbook, bad); !errors.Is(err, ErrBadName) {
			t.Errorf("name %q = %v", bad, err)
		}
	}

	dup, _ := l.DuplicateQuery(moved)
	if dupID, _ := EnsureID(dup.Path); dup.Name != "top words copy" || dupID == id {
		t.Errorf("duplicate = %+v id %s", dup, dupID)
	}

	if _, err := l.RenameWorkbook(w2.Name, "reports"); err != nil {
		t.Fatal(err)
	}

	if q, ok := l.Find(filepath.Join(l.Root, "reports", "top words.sql")); !ok || q.Workbook != "reports" || q.Name != "top words" {
		t.Errorf("find = %+v %v", q, ok)
	}

	if _, ok := l.Find("/elsewhere/x.sql"); ok {
		t.Error("found a file outside the library")
	}

	wbs, _ := l.Workbooks()
	if len(wbs) != 2 || wbs[0].Name != "reports" {
		t.Errorf("workbooks = %+v", wbs)
	}
}

func TestImportQueries(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "queries")
	_ = os.MkdirAll(old, 0o700)
	_ = WriteQuery(SavedQuery{Path: filepath.Join(old, "a.sql"), Title: "Kept"}, "SELECT 1")

	l, _ := OpenLibrary(filepath.Join(root, "libraries"), DefaultLibrary)

	moved, err := l.ImportQueries(old)
	if err != nil || !moved {
		t.Fatalf("import = %v %v", moved, err)
	}

	qs, _ := l.Queries("imported")
	if len(qs) != 1 || qs[0].Name != "a" {
		t.Fatalf("imported = %v", names(qs))
	}

	if meta, _, _ := ReadQuery(qs[0].Path); meta.Title != "Kept" {
		t.Errorf("header lost: %+v", meta)
	}

	if moved, _ := l.ImportQueries(old); moved {
		t.Error("imported twice")
	}
}

func TestValidNameAndQueryFileNameBoundaries(t *testing.T) {
	for _, bad := range []string{"", "   ", " leading", "trailing ", "  spaced  ", "\ttab", ".dot", "a/b", "a:b"} {
		if err := ValidName(bad); !errors.Is(err, ErrBadName) {
			t.Errorf("ValidName(%q) = %v, want ErrBadName", bad, err)
		}
	}

	if err := ValidName("valid name"); err != nil {
		t.Errorf("ValidName(\"valid name\") = %v, want nil", err)
	}

	l, err := OpenLibrary(t.TempDir(), DefaultLibrary)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := l.CreateWorkbook("  spaced  ", time.Now()); !errors.Is(err, ErrBadName) {
		t.Errorf("CreateWorkbook(\"  spaced  \") = %v, want ErrBadName", err)
	}

	for _, title := range []string{
		strings.Repeat("a", 300),
		strings.Repeat("é", 200),
		strings.Repeat("🚀", 100),
		strings.Repeat("a", 250) + "-tail",
	} {
		fn := QueryFileName(title)
		if len(fn) > 255 {
			t.Errorf("QueryFileName len = %d > 255 (%q)", len(fn), fn)
		}

		if !utf8.ValidString(fn) {
			t.Errorf("QueryFileName invalid UTF-8: %q", fn)
		}

		if !strings.HasSuffix(fn, ".sql") {
			t.Errorf("QueryFileName missing .sql: %q", fn)
		}
	}
}

func names(qs []Query) []string {
	var out []string
	for _, q := range qs {
		out = append(out, q.Name)
	}

	return out
}
