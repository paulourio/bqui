package store

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

// SavedQuery is a .sql file in the saved-queries folder. An optional
// header comment holds metadata as TOML:
//
//	/* bqui
//	title = "Top words"
//	project = "my-project"
//	*/
//	SELECT ...
type SavedQuery struct {
	Path     string    `toml:"-"`
	Modified time.Time `toml:"-"`

	Title          string            `toml:"title,omitempty"`
	Project        string            `toml:"project,omitempty"`
	DefaultDataset string            `toml:"default_dataset,omitempty"`
	Labels         map[string]string `toml:"labels,omitempty"`
	// ID identifies the file across renames and moves (run history).
	ID string `toml:"id,omitempty"`
}

const (
	headerOpen  = "/* bqui\n"
	headerClose = "*/\n"
	queriesDir  = "bqui/queries"
	sqlExt      = ".sql"
)

// DefaultQueriesDir is ~/Documents/bqui/queries.
func DefaultQueriesDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("store: %w", err)
	}

	return filepath.Join(home, "Documents", filepath.FromSlash(queriesDir)), nil
}

// ListQueries returns the .sql files under dir (recursively), by path.
// The header is parsed; bodies are not read.
func ListQueries(dir string) ([]SavedQuery, error) {
	var out []SavedQuery

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && path == dir {
				return filepath.SkipAll
			}

			return err
		}

		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && path != dir {
				return filepath.SkipDir
			}

			return nil
		}

		if !strings.EqualFold(filepath.Ext(d.Name()), sqlExt) {
			return nil
		}

		q, _, rerr := ReadQuery(path)
		if rerr != nil {
			return nil //nolint:nilerr // unreadable files are skipped, not fatal for the list
		}

		out = append(out, q)

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: list queries: %w", err)
	}

	slices.SortFunc(out, func(a, b SavedQuery) int { return strings.Compare(a.Path, b.Path) })

	return out, nil
}

// ReadQuery reads a saved query: metadata and the SQL body.
func ReadQuery(path string) (SavedQuery, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return SavedQuery{}, "", fmt.Errorf("store: read query: %w", err)
	}

	info, _ := os.Stat(path)
	q := SavedQuery{Path: path}

	if info != nil {
		q.Modified = info.ModTime()
	}

	body := string(b)

	if rest, ok := strings.CutPrefix(body, headerOpen); ok {
		if meta, sqlText, ok := strings.Cut(rest, headerClose); ok {
			if _, err := toml.Decode(meta, &q); err != nil {
				return SavedQuery{}, "", fmt.Errorf("store: %s header: %w", filepath.Base(path), err)
			}

			body = strings.TrimPrefix(sqlText, "\n")
		}
	}

	q.Path = path
	if q.Title == "" {
		q.Title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	return q, body, nil
}

// WriteQuery writes a saved query atomically with a stable header (keys in
// a fixed order) so files diff cleanly in git.
func WriteQuery(q SavedQuery, sqlText string) error {
	var buf bytes.Buffer

	header := SavedQuery{Title: q.Title, Project: q.Project, DefaultDataset: q.DefaultDataset, Labels: q.Labels, ID: q.ID}

	// The file name is the default title; don't freeze it into the header
	// (it would go stale after a rename).
	if header.Title == strings.TrimSuffix(filepath.Base(q.Path), filepath.Ext(q.Path)) {
		header.Title = ""
	}

	var meta bytes.Buffer
	if err := toml.NewEncoder(&meta).Encode(header); err != nil {
		return fmt.Errorf("store: encode header: %w", err)
	}

	if meta.Len() > 0 {
		buf.WriteString(headerOpen)
		buf.Write(meta.Bytes())
		buf.WriteString(headerClose)
	}

	buf.WriteString(strings.TrimRight(sqlText, "\n"))
	buf.WriteString("\n")

	if err := os.MkdirAll(filepath.Dir(q.Path), dirPerm); err != nil {
		return fmt.Errorf("store: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(q.Path), ".query-*.sql")
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}

	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("store: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: %w", err)
	}

	if err := os.Rename(tmp.Name(), q.Path); err != nil {
		return fmt.Errorf("store: %w", err)
	}

	return nil
}

// QueryFileName turns a title into a safe file name ("Top words" →
// "top-words.sql").
func QueryFileName(title string) string {
	var b strings.Builder

	dash := false

	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r > 127:
			b.WriteRune(r)

			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')

			dash = true
		}
	}

	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "query"
	}

	const maxStemBytes = 255 - len(sqlExt)
	if len(name) > maxStemBytes {
		cut := maxStemBytes
		for cut > 0 && !utf8.RuneStart(name[cut]) {
			cut--
		}

		name = strings.TrimRight(name[:cut], "-")
		if name == "" {
			name = "query"
		}
	}

	return name + sqlExt
}
