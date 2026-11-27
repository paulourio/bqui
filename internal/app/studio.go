package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/paulourio/bqui/internal/dataform"
	"github.com/paulourio/bqui/internal/store"
)

// StudioBackend reads and writes BigQuery Studio saved queries
// (*dataform.Client, or FakeStudio).
type StudioBackend interface {
	List(ctx context.Context, project, region string) ([]dataform.SavedQuery, error)
	Read(ctx context.Context, name string) (sql, head string, err error)
	Save(ctx context.Context, name, sql, base string, author dataform.Author, message string) (string, error)
	Create(ctx context.Context, project, region, title, sql string, author dataform.Author) (dataform.SavedQuery, string, error)
	Rename(ctx context.Context, name, title string) error
	Delete(ctx context.Context, name string) error
}

// Studio commit messages.
const (
	studioSaveMessage = "Saved from bqui"
	studioTimeout     = 30 * time.Second
)

// OpenRemote loads a BigQuery Studio saved query into the tab and links it.
// The local file link is dropped unless the file is already linked to the
// same query.
func (w *Workspace) OpenRemote(ctx context.Context, sb StudioBackend, q dataform.SavedQuery) (string, error) {
	sql, head, err := sb.Read(ctx, q.Name)
	if err != nil {
		return "", err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	w.clearJobDocLocked()

	if w.tab.Remote != q.Name {
		w.tab.File, w.fileText = "", ""
	}

	w.tab.SQL, w.tab.Title, w.tab.CursorLine, w.tab.CursorCol = sql, q.Title, 0, 0
	w.tab.Remote, w.tab.RemoteHead, w.tab.RemoteText = q.Name, head, sql
	w.scheduleLocked()

	return sql, nil
}

// RemoteDirty reports unsynced changes to the linked saved query.
func (w *Workspace) RemoteDirty() bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.tab.Remote != "" && !sameSQL(w.tab.SQL, w.tab.RemoteText)
}

// sameSQL compares query texts ignoring trailing newlines (files end with
// one; the console does not).
func sameSQL(a, b string) bool {
	return strings.TrimRight(a, "\n") == strings.TrimRight(b, "\n")
}

// SaveRemote commits the tab text to its linked saved query. It returns
// dataform.ErrConflict if the query changed in BigQuery since it was last
// synced; overwrite then forces the save over the newer version.
func (w *Workspace) SaveRemote(ctx context.Context, sb StudioBackend, author dataform.Author, overwrite bool) error {
	t := w.Tab()
	if t.Remote == "" {
		return errors.New("app: this query is not linked to BigQuery Studio")
	}

	base, err := w.remoteBase(ctx, sb, t, overwrite)
	if err != nil {
		return err
	}

	head, err := sb.Save(ctx, t.Remote, t.SQL, base, author, studioSaveMessage)
	if err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.tab.Remote == t.Remote {
		w.tab.RemoteHead, w.tab.RemoteText = head, t.SQL
		w.scheduleLocked()
	}

	return nil
}

// remoteBase is the commit a save must apply on top of. Without a known
// head (a linked file opened elsewhere), the current head is accepted only
// if its text is what we last synced.
func (w *Workspace) remoteBase(ctx context.Context, sb StudioBackend, t store.Tab, overwrite bool) (string, error) {
	if t.RemoteHead != "" && !overwrite {
		return t.RemoteHead, nil
	}

	sql, head, err := sb.Read(ctx, t.Remote)
	if err != nil {
		return "", err
	}

	if !overwrite && !sameSQL(sql, t.RemoteText) {
		return "", dataform.ErrConflict
	}

	return head, nil
}

// ReloadRemote replaces the tab text with the saved query's current
// version (resolving a conflict in BigQuery's favor).
func (w *Workspace) ReloadRemote(ctx context.Context, sb StudioBackend) (string, error) {
	t := w.Tab()

	return w.OpenRemote(ctx, sb, dataform.SavedQuery{Name: t.Remote, Title: t.Title})
}

// PublishRemote creates a saved query from the tab and links it. A linked
// local file records the link in its header (link_id).
func (w *Workspace) PublishRemote(ctx context.Context, sb StudioBackend, project, region, title string, author dataform.Author) (dataform.SavedQuery, error) {
	// Save the file first so it and the new saved query start identical.
	if w.Tab().File != "" && w.Dirty() {
		if err := w.Save(); err != nil {
			return dataform.SavedQuery{}, err
		}
	}

	t := w.Tab()

	q, head, err := sb.Create(ctx, project, region, title, t.SQL, author)
	if err != nil {
		return q, err
	}

	w.mu.Lock()
	w.tab.Remote, w.tab.RemoteHead, w.tab.RemoteText = q.Name, head, t.SQL

	if t.File == "" {
		w.tab.Title = q.Title
	}

	w.scheduleLocked()
	w.mu.Unlock()

	if t.File != "" {
		if err := w.writeLink(t.File, q.Name); err != nil {
			return q, fmt.Errorf("saved to BigQuery Studio, but linking %s failed: %w", t.File, err)
		}
	}

	return q, nil
}

// writeLink sets link_id in a saved-query file header (body unchanged).
func (w *Workspace) writeLink(path, remote string) error {
	meta, body, err := store.ReadQuery(path)
	if err != nil {
		return err
	}

	meta.LinkID = remote

	return store.WriteQuery(meta, body)
}

// linkFromFile restores a file's BigQuery Studio link when it is opened:
// the synced text is the file text, the head unknown (checked on save).
func (w *Workspace) linkFromFile(q store.SavedQuery, body string) {
	if q.LinkID == "" {
		w.tab.Remote, w.tab.RemoteHead, w.tab.RemoteText = "", "", ""

		return
	}

	if w.tab.Remote != q.LinkID {
		w.tab.Remote, w.tab.RemoteHead, w.tab.RemoteText = q.LinkID, "", body
	}
}

// FakeStudio is an in-memory StudioBackend (fake mode, tests, smoke).
type FakeStudio struct {
	mu      sync.Mutex
	queries map[string]*fakeStudioQuery
	next    int
}

type fakeStudioQuery struct {
	q       dataform.SavedQuery
	sql     string
	commits []string // newest first
}

// NewFakeStudio returns a fake with two saved queries in project.
func NewFakeStudio(project string) *FakeStudio {
	f := &FakeStudio{queries: map[string]*fakeStudioQuery{}}

	for _, s := range []struct{ title, sql string }{
		{"Daily active users", "SELECT DATE(created_at) AS day, COUNT(DISTINCT customer.name) AS users\nFROM orders\nGROUP BY day\nORDER BY day"},
		{"Top items", "SELECT i.sku, SUM(i.qty) AS qty\nFROM orders, UNNEST(items) AS i\nGROUP BY i.sku\nORDER BY qty DESC"},
	} {
		_, _, _ = f.Create(context.Background(), project, dataform.DefaultRegion, s.title, s.sql, dataform.Author{})
	}

	return f
}

// List implements StudioBackend.
func (f *FakeStudio) List(_ context.Context, project, region string) ([]dataform.SavedQuery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []dataform.SavedQuery

	for _, q := range f.queries {
		if q.q.Project == project && q.q.Region == region {
			out = append(out, q.q)
		}
	}

	slices.SortFunc(out, func(a, b dataform.SavedQuery) int { return strings.Compare(a.Title, b.Title) })

	return out, nil
}

// Read implements StudioBackend.
func (f *FakeStudio) Read(_ context.Context, name string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	q, ok := f.queries[name]
	if !ok {
		return "", "", dataform.ErrNotFound
	}

	return q.sql, q.commits[0], nil
}

// Save implements StudioBackend.
func (f *FakeStudio) Save(_ context.Context, name, sql, base string, _ dataform.Author, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	q, ok := f.queries[name]
	if !ok {
		return "", dataform.ErrNotFound
	}

	if base != "" && base != q.commits[0] {
		return "", dataform.ErrConflict
	}

	return f.commitLocked(q, sql), nil
}

func (f *FakeStudio) commitLocked(q *fakeStudioQuery, sql string) string {
	f.next++
	sha := fmt.Sprintf("fakesha%04d", f.next)
	q.sql, q.commits = sql, append([]string{sha}, q.commits...)
	q.q.Modified = time.Now()

	return sha
}

// Create implements StudioBackend.
func (f *FakeStudio) Create(_ context.Context, project, region, title, sql string, _ dataform.Author) (dataform.SavedQuery, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.next++
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", f.next)
	q := &fakeStudioQuery{q: dataform.SavedQuery{
		Name: fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, region, id), ID: id,
		Project: project, Region: region, Title: title, Created: time.Now(), Modified: time.Now(),
	}}
	f.queries[q.q.Name] = q

	return q.q, f.commitLocked(q, sql), nil
}

// Rename implements StudioBackend.
func (f *FakeStudio) Rename(_ context.Context, name, title string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	q, ok := f.queries[name]
	if !ok {
		return dataform.ErrNotFound
	}

	q.q.Title = title

	return nil
}

// Delete implements StudioBackend.
func (f *FakeStudio) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.queries, name)

	return nil
}

// EditInConsole simulates someone saving the query in BigQuery Studio.
func (f *FakeStudio) EditInConsole(name, sql string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if q, ok := f.queries[name]; ok {
		f.commitLocked(q, sql)
	}
}

// StudioContext bounds one Studio call.
func StudioContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), studioTimeout)
}
