package store

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// A Library is a folder of workbooks (folders) holding queries (.sql
// files): ~/Documents/bqui/libraries/<name>/<workbook>/<query>.sql. Disk is
// the source of truth, so the tree can be edited in Finder or kept in git.
type Library struct {
	Name string
	Root string
}

// Workbook is a folder of queries.
type Workbook struct {
	Name string
	Path string
}

// Query is one .sql file. Name is the file name without .sql.
type Query struct {
	Name     string
	Path     string
	Workbook string
	Modified time.Time
}

// DefaultLibrary is used when no --library is given.
const DefaultLibrary = "default"

// Errors.
var (
	ErrBadName = errors.New("store: names cannot be empty, start with a dot, or contain a slash or colon")
	ErrExists  = errors.New("store: an item with that name already exists")
)

const (
	untitled       = "Untitled"
	workbookPrefix = "workbook-"
	importedName   = "imported"
)

// DefaultLibrariesDir is ~/Documents/bqui/libraries.
func DefaultLibrariesDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("store: %w", err)
	}

	return filepath.Join(home, "Documents", appDir, "libraries"), nil
}

// OpenLibrary opens (creating) librariesDir/name.
func OpenLibrary(librariesDir, name string) (*Library, error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}

	l := &Library{Name: name, Root: filepath.Join(librariesDir, name)}
	if err := os.MkdirAll(l.Root, dirPerm); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}

	return l, nil
}

// ValidName checks a workbook or query name typed by the user.
func ValidName(name string) error {
	if strings.TrimSpace(name) == "" || strings.HasPrefix(name, ".") || strings.ContainsAny(name, "/:\x00") {
		return ErrBadName
	}

	return nil
}

// Workbooks lists the workbooks by name.
func (l *Library) Workbooks() ([]Workbook, error) {
	entries, err := os.ReadDir(l.Root)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}

	var out []Workbook

	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, Workbook{Name: e.Name(), Path: filepath.Join(l.Root, e.Name())})
		}
	}

	slices.SortFunc(out, func(a, b Workbook) int { return compareNames(a.Name, b.Name) })

	return out, nil
}

// Queries lists a workbook's queries by name.
func (l *Library) Queries(workbook string) ([]Query, error) {
	dir := filepath.Join(l.Root, workbook)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}

	var out []Query

	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), sqlExt)
		if e.IsDir() || !ok || strings.HasPrefix(name, ".") {
			continue
		}

		q := Query{Name: name, Path: filepath.Join(dir, e.Name()), Workbook: workbook}
		if info, err := e.Info(); err == nil {
			q.Modified = info.ModTime()
		}

		out = append(out, q)
	}

	slices.SortFunc(out, func(a, b Query) int { return compareNames(a.Name, b.Name) })

	return out, nil
}

// compareNames orders case-insensitively with numbers in numeric order
// (Untitled-2 before Untitled-10).
func compareNames(a, b string) int {
	ap, an := splitNumber(strings.ToLower(a))
	bp, bn := splitNumber(strings.ToLower(b))

	if c := strings.Compare(ap, bp); c != 0 || an == bn {
		if c != 0 {
			return c
		}

		return strings.Compare(a, b)
	}

	if an < bn {
		return -1
	}

	return 1
}

func splitNumber(s string) (string, int) {
	i := len(s)
	for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
		i--
	}

	n := 0
	for _, c := range s[i:] {
		n = n*10 + int(c-'0')
	}

	return s[:i], n
}

// uniqueName returns base, or base-2, base-3, … that does not exist.
func uniqueName(exists func(string) bool, base string) string {
	if !exists(base) {
		return base
	}

	for i := 2; ; i++ {
		if n := fmt.Sprintf("%s-%d", base, i); !exists(n) {
			return n
		}
	}
}

func pathExists(p string) bool {
	_, err := os.Stat(p)

	return err == nil
}

// CreateWorkbook makes a workbook; an empty name uses workbook-YYYY-MM-DD
// (with -2, -3… on collisions).
func (l *Library) CreateWorkbook(name string, now time.Time) (Workbook, error) {
	if name == "" {
		name = workbookPrefix + now.Format(time.DateOnly)
	} else if err := ValidName(name); err != nil {
		return Workbook{}, err
	}

	name = uniqueName(func(n string) bool { return pathExists(filepath.Join(l.Root, n)) }, name)
	w := Workbook{Name: name, Path: filepath.Join(l.Root, name)}

	if err := os.Mkdir(w.Path, dirPerm); err != nil {
		return Workbook{}, fmt.Errorf("store: %w", err)
	}

	return w, nil
}

// CreateQuery adds Untitled-N.sql (the lowest free N) with text.
func (l *Library) CreateQuery(workbook, text string) (Query, error) {
	dir := filepath.Join(l.Root, workbook)

	var name string

	for i := 1; name == ""; i++ {
		if n := fmt.Sprintf("%s-%d", untitled, i); !pathExists(filepath.Join(dir, n+sqlExt)) {
			name = n
		}
	}

	q := Query{Name: name, Path: filepath.Join(dir, name+sqlExt), Workbook: workbook}
	if err := WriteQuery(SavedQuery{Path: q.Path, ID: NewID()}, text); err != nil {
		return Query{}, err
	}

	return q, nil
}

// RenameWorkbook renames a workbook folder.
func (l *Library) RenameWorkbook(old, name string) (Workbook, error) {
	if err := ValidName(name); err != nil {
		return Workbook{}, err
	}

	to := filepath.Join(l.Root, name)
	if old == name {
		return Workbook{Name: name, Path: to}, nil
	}

	if pathExists(to) {
		return Workbook{}, ErrExists
	}

	if err := os.Rename(filepath.Join(l.Root, old), to); err != nil {
		return Workbook{}, fmt.Errorf("store: %w", err)
	}

	return Workbook{Name: name, Path: to}, nil
}

// MoveQuery renames a query and/or moves it to another workbook. name is
// without .sql (a typed ".sql" suffix is dropped).
func (l *Library) MoveQuery(q Query, workbook, name string) (Query, error) {
	name = strings.TrimSuffix(strings.TrimSpace(name), sqlExt)
	if err := ValidName(name); err != nil {
		return Query{}, err
	}

	to := Query{Name: name, Workbook: workbook, Path: filepath.Join(l.Root, workbook, name+sqlExt)}
	if to.Path == q.Path {
		return to, nil
	}

	if pathExists(to.Path) {
		return Query{}, ErrExists
	}

	if err := os.Rename(q.Path, to.Path); err != nil {
		return Query{}, fmt.Errorf("store: %w", err)
	}

	return to, nil
}

// DuplicateQuery copies a query as "<name> copy" (new file ID).
func (l *Library) DuplicateQuery(q Query) (Query, error) {
	meta, body, err := ReadQuery(q.Path)
	if err != nil {
		return Query{}, err
	}

	dir := filepath.Join(l.Root, q.Workbook)
	name := uniqueName(func(n string) bool { return pathExists(filepath.Join(dir, n+sqlExt)) }, q.Name+" copy")
	meta.Path, meta.ID, meta.Title = filepath.Join(dir, name+sqlExt), NewID(), ""

	if err := WriteQuery(meta, body); err != nil {
		return Query{}, err
	}

	return Query{Name: name, Path: meta.Path, Workbook: q.Workbook}, nil
}

// EnsureStarted creates the first workbook and query when the library is
// empty, holding text; it returns the query to open ("" if not empty).
func (l *Library) EnsureStarted(text string, now time.Time) (Query, error) {
	wbs, err := l.Workbooks()
	if err != nil || len(wbs) > 0 {
		return Query{}, err
	}

	w, err := l.CreateWorkbook("", now)
	if err != nil {
		return Query{}, err
	}

	return l.CreateQuery(w.Name, text)
}

// ImportQueries moves an old saved-queries folder (M10) into the
// "imported" workbook, once. Missing or empty folders are ignored.
func (l *Library) ImportQueries(oldDir string) (bool, error) {
	list, err := ListQueries(oldDir)
	if err != nil || len(list) == 0 {
		return false, err
	}

	dest := filepath.Join(l.Root, importedName)
	if pathExists(dest) {
		return false, nil
	}

	if err := os.Rename(oldDir, dest); err != nil {
		return false, fmt.Errorf("store: import %s: %w", oldDir, err)
	}

	return true, nil
}

// Find returns the query at path, if it is inside the library.
func (l *Library) Find(path string) (Query, bool) {
	rel, err := filepath.Rel(l.Root, path)
	if err != nil {
		return Query{}, false
	}

	workbook, file, ok := strings.Cut(filepath.ToSlash(rel), "/")
	name, isSQL := strings.CutSuffix(file, sqlExt)

	if !ok || !isSQL || strings.Contains(file, "/") || strings.HasPrefix(workbook, "..") {
		return Query{}, false
	}

	return Query{Name: name, Path: path, Workbook: workbook}, true
}

// EnsureID gives a query file a stable ID (header) and returns it.
func EnsureID(path string) (string, error) {
	meta, body, err := ReadQuery(path)
	if err != nil {
		return "", err
	}

	if meta.ID != "" {
		return meta.ID, nil
	}

	meta.ID = NewID()
	if meta.Title == strings.TrimSuffix(filepath.Base(path), sqlExt) {
		meta.Title = "" // the default title is the file name; don't freeze it
	}

	return meta.ID, WriteQuery(meta, body)
}

// NewID returns a random UUID.
func NewID() string {
	var b [16]byte

	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// MustWorkbooks lists workbooks, or none on error (UI conveniences).
func (l *Library) MustWorkbooks() []Workbook {
	wbs, _ := l.Workbooks()

	return wbs
}
