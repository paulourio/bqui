package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/store"
)

// DefaultTabID is the workspace's single editor tab (the schema already
// supports several).
const DefaultTabID = "main"

const (
	activeTabKey   = "active_tab"
	autosaveDelay  = 2 * time.Second
	searchRunLimit = 200
	dbTimeout      = 5 * time.Second
)

// ErrNoFile is returned by Save when the tab is not linked to a file yet.
var ErrNoFile = errors.New("app: query has no file yet; use Save As")

// WorkspaceOptions configures a Workspace.
type WorkspaceOptions struct {
	DB         *store.DB // nil: nothing persists (fake/smoke runs)
	QueriesDir string
	// Library holds workbooks and queries (M15). With a library every
	// editor tab is a query file, autosaved.
	Library *store.Library
	// InitialSQL fills a brand-new workspace.
	InitialSQL string
	// Delay is the autosave debounce (0 = 2s).
	Delay  time.Duration
	Logger *slog.Logger
}

// Workspace is the persisted editor state: the tab text and cursor, its
// linked saved-query file, BigQuery session mode, and local run history.
// Methods are safe for concurrent use; disk writes happen off the caller's
// goroutine after a debounce.
type Workspace struct {
	opts WorkspaceOptions

	mu        sync.Mutex
	tab       store.Tab
	fileText  string // text as last read from/written to tab.File
	fileID    string // tab.File's stable ID (run history)
	timer     *time.Timer
	pending   bool
	saveErr   error
	saveCount int
}

// OpenWorkspace restores the last workspace (or starts a new one).
func OpenWorkspace(opts WorkspaceOptions) (*Workspace, error) {
	if opts.Delay <= 0 {
		opts.Delay = autosaveDelay
	}

	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	w := &Workspace{opts: opts, tab: store.Tab{ID: DefaultTabID, Title: "Untitled", SQL: opts.InitialSQL}}

	if opts.DB == nil {
		return w, w.adopt()
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	tabs, err := opts.DB.Tabs(ctx)
	if err != nil {
		return nil, err
	}

	active, _ := opts.DB.Meta(ctx, activeTabKey)

	for i, t := range tabs {
		if i == 0 || t.ID == active {
			w.tab = t
		}
	}

	if w.tab.File != "" {
		if _, body, err := store.ReadQuery(w.tab.File); err == nil {
			w.fileText = body
		} else {
			// The file went away: keep the text, drop the link.
			w.tab.File = ""
		}
	}

	return w, w.adopt()
}

// adopt makes the restored tab a library query: an existing library file is
// reopened; otherwise its text becomes a new query (the first launch
// creates workbook-YYYY-MM-DD/Untitled-1). Studio-only tabs stay as they are.
func (w *Workspace) adopt() error {
	l := w.opts.Library
	if l == nil {
		return nil
	}

	if w.tab.File != "" {
		if _, ok := l.Find(w.tab.File); ok {
			_, err := w.Open(w.tab.File)

			return err
		}
	}

	if w.tab.Remote != "" && w.tab.File == "" {
		return nil
	}

	q, err := l.EnsureStarted(w.tab.SQL, time.Now())
	if err != nil {
		return err
	}

	if q.Path == "" {
		wbs, werr := l.Workbooks()
		if werr != nil || len(wbs) == 0 {
			return werr
		}

		if q, err = l.CreateQuery(wbs[0].Name, w.tab.SQL); err != nil {
			return err
		}
	}

	_, err = w.Open(q.Path)

	return err
}

// Library returns the workbook library (nil without one).
func (w *Workspace) Library() *store.Library { return w.opts.Library }

// CurrentQuery returns the open library query, if any.
func (w *Workspace) CurrentQuery() (store.Query, bool) {
	if w.opts.Library == nil {
		return store.Query{}, false
	}

	return w.opts.Library.Find(w.Tab().File)
}

// FileID is the open query file's stable ID ("" if none).
func (w *Workspace) FileID() string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.fileID
}

// Switch saves the open query if edited and opens another.
func (w *Workspace) Switch(path string) (string, error) {
	if w.Tab().File != "" && w.Dirty() {
		if err := w.Save(); err != nil {
			return "", err
		}
	}

	return w.Open(path)
}

// NewQuery creates Untitled-N in a workbook and switches to it.
func (w *Workspace) NewQuery(workbook string) (store.Query, error) {
	q, err := w.opts.Library.CreateQuery(workbook, "")
	if err != nil {
		return q, err
	}

	_, err = w.Switch(q.Path)

	return q, err
}

// Relink follows a rename or move of the open query.
func (w *Workspace) Relink(from, to store.Query) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.tab.File == from.Path {
		w.tab.File, w.tab.Title = to.Path, to.Name
		w.scheduleLocked()
	}
}

// FileRuns returns the open query's runs, newest first.
func (w *Workspace) FileRuns(ctx context.Context) ([]store.Run, error) {
	id := w.FileID()
	if w.opts.DB == nil || id == "" {
		return nil, nil
	}

	return w.opts.DB.FileRuns(ctx, id, searchRunLimit)
}

// Tab returns a copy of the current tab state.
func (w *Workspace) Tab() store.Tab {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.tab
}

// QueriesDir is the saved-queries folder.
func (w *Workspace) QueriesDir() string { return w.opts.QueriesDir }

// Edited records new editor text and cursor and schedules an autosave.
func (w *Workspace) Edited(text string, line, col int) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.tab.SQL, w.tab.CursorLine, w.tab.CursorCol = text, line, col
	w.scheduleLocked()
}

func (w *Workspace) scheduleLocked() {
	if w.opts.DB == nil && w.opts.Library == nil {
		return
	}

	w.pending = true

	if w.timer == nil {
		w.timer = time.AfterFunc(w.opts.Delay, func() { _ = w.Flush() })
	} else {
		w.timer.Reset(w.opts.Delay)
	}
}

// Flush writes pending state now (also called on quit).
func (w *Workspace) Flush() error {
	// Library queries autosave to their files.
	if w.opts.Library != nil && w.Tab().File != "" && w.Dirty() {
		if err := w.Save(); err != nil {
			w.opts.Logger.Warn("query autosave failed", "err", err)
		}
	}

	w.mu.Lock()
	if !w.pending || w.opts.DB == nil {
		w.pending = false
		w.mu.Unlock()

		return nil
	}

	w.pending = false
	tab := w.tab
	w.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	err := w.opts.DB.SaveTab(ctx, tab)
	if err == nil {
		err = w.opts.DB.SetMeta(ctx, activeTabKey, tab.ID)
	}

	w.mu.Lock()
	w.saveErr = err
	w.saveCount++
	w.mu.Unlock()

	if err != nil {
		w.opts.Logger.Warn("workspace autosave failed", "err", err)
	}

	return err
}

// Saves reports how many autosaves ran and the last error (tests, smoke).
func (w *Workspace) Saves() (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.saveCount, w.saveErr
}

// Dirty reports whether the text differs from the linked file (or, for an
// unlinked tab, whether it has any text worth keeping).
func (w *Workspace) Dirty() bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	switch {
	case w.tab.File == "" && w.tab.Remote != "":
		return !sameSQL(w.tab.SQL, w.tab.RemoteText)
	case w.tab.File == "":
		return strings.TrimSpace(w.tab.SQL) != "" && w.tab.SQL != w.opts.InitialSQL
	}

	return strings.TrimRight(w.tab.SQL, "\n") != strings.TrimRight(w.fileText, "\n")
}

// Title is the window/tab title: file title plus an edited marker.
func (w *Workspace) Title() string {
	t := w.Tab()

	title := t.Title
	if t.File == "" && t.Remote == "" {
		title = "Untitled"
	}

	if q, ok := w.libraryQuery(t.File); ok {
		title = q.Workbook + " / " + q.Name
	}

	if w.Dirty() && (t.File != "" || t.Remote != "") {
		title += " — Edited"
	}

	return title
}

// New starts an empty, unlinked query (the session is kept).
func (w *Workspace) New() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.tab.SQL, w.tab.File, w.tab.Title, w.tab.CursorLine, w.tab.CursorCol = "", "", "Untitled", 0, 0
	w.tab.Remote, w.tab.RemoteHead, w.tab.RemoteText = "", "", ""
	w.fileText = ""
	w.scheduleLocked()
}

// Open loads a saved query into the tab and returns its SQL.
func (w *Workspace) Open(path string) (string, error) {
	q, body, err := store.ReadQuery(path)
	if err != nil {
		return "", err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	id := q.ID
	if id == "" {
		// Give the file a stable ID for its run history.
		id, _ = store.EnsureID(path)
	}

	title := q.Title
	if lq, ok := w.libraryQuery(path); ok {
		title = lq.Name
	}

	w.tab.SQL, w.tab.File, w.tab.Title, w.tab.CursorLine, w.tab.CursorCol = body, path, title, 0, 0
	w.fileText, w.fileID = body, id
	w.linkFromFile(q, body)
	w.scheduleLocked()

	return body, nil
}

// Save writes the tab to its linked file, keeping the file's metadata.
func (w *Workspace) Save() error {
	t := w.Tab()
	if t.File == "" {
		return ErrNoFile
	}

	meta, _, err := store.ReadQuery(t.File)
	if err != nil {
		meta = store.SavedQuery{Title: t.Title}
	}

	meta.Path = t.File

	return w.write(meta, t.SQL)
}

// SaveAs writes the tab to a new file and links it. An empty title derives
// one from the file name.
func (w *Workspace) SaveAs(path, title, project string) error {
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	t := w.Tab()

	return w.write(store.SavedQuery{Path: path, Title: title, Project: project, LinkID: t.Remote}, t.SQL)
}

func (w *Workspace) write(q store.SavedQuery, sql string) error {
	if err := store.WriteQuery(q, sql); err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	w.tab.File, w.tab.Title, w.fileText = q.Path, q.Title, sql
	w.scheduleLocked()

	return nil
}

// SavedQueries lists the saved-queries folder.
func (w *Workspace) SavedQueries() ([]store.SavedQuery, error) {
	if w.opts.QueriesDir == "" {
		return nil, nil
	}

	return store.ListQueries(w.opts.QueriesDir)
}

// SessionMode reports whether runs join a BigQuery session, and its ID.
func (w *Workspace) SessionMode() (on bool, id string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.tab.SessionMode, w.tab.SessionID
}

// SetSessionMode turns session mode on or off; turning it off forgets the
// session (EndSessionSQL aborts it server-side).
func (w *Workspace) SetSessionMode(on bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.tab.SessionMode = on
	if !on {
		w.tab.SessionID = ""
	}

	w.scheduleLocked()
}

// EndSessionSQL aborts the current BigQuery session.
const EndSessionSQL = "CALL BQ.ABORT_SESSION();"

// Prepare applies session mode to a query request.
func (w *Workspace) Prepare(req *bq.QueryRequest) {
	on, id := w.SessionMode()
	if !on {
		return
	}

	if id != "" {
		req.SessionID = id
	} else {
		req.CreateSession = true
	}
}

// Finished records a completed run: it keeps a new session ID, the last
// job, and appends to the local run history under fileID (the query file
// open when the run started; "" for none).
func (w *Workspace) Finished(snap RunSnapshot, project, fileID string) {
	if snap.Kind != "query" {
		return
	}

	w.mu.Lock()

	if snap.Result != nil && snap.Result.SessionID != "" && w.tab.SessionMode {
		w.tab.SessionID = snap.Result.SessionID
	}

	if snap.Job != nil {
		w.tab.LastJob = snap.Job.Project + ":" + snap.Job.Location + "." + snap.Job.JobID
	}

	w.scheduleLocked()
	w.mu.Unlock()

	if w.opts.DB == nil {
		return
	}

	r := store.Run{Started: snap.Started, Project: project, SQL: snap.SQL, State: snap.State.String(), Elapsed: snap.Elapsed, FileID: fileID}
	if snap.Job != nil {
		r.JobID, r.Location, r.Project = snap.Job.JobID, snap.Job.Location, snap.Job.Project
	}

	if snap.Grid != nil {
		r.Rows, _, _ = snap.Grid.Size()
	}

	if snap.Result != nil {
		r.Bytes = snap.Result.BytesProc
	}

	if snap.Err != nil {
		r.Error = snap.Err.Error()
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
		defer cancel()

		if err := w.opts.DB.RecordRun(ctx, r); err != nil {
			w.opts.Logger.Warn("recording run failed", "err", err)
		}
	}()
}

// SearchRuns searches local run history (newest first).
func (w *Workspace) SearchRuns(ctx context.Context, query string) ([]store.Run, error) {
	if w.opts.DB == nil {
		return nil, nil
	}

	return w.opts.DB.SearchRuns(ctx, query, searchRunLimit)
}

// String describes the workspace for logs.
func (w *Workspace) String() string {
	t := w.Tab()

	return fmt.Sprintf("tab %s (%s, %d bytes)", t.ID, t.File, len(t.SQL))
}

// ApplyPreferences sets the location, byte limit and default dataset on a
// query request (explicit request values win).
func ApplyPreferences(p store.Preferences, req *bq.QueryRequest) {
	if req.Location == "" {
		req.Location = p.Location
	}

	if req.MaxBytesBilled == 0 {
		req.MaxBytesBilled = p.MaxBytesBilled
	}

	if req.DefaultDataset == nil && p.DefaultDataset != "" {
		if project, dataset, ok := strings.Cut(p.DefaultDataset, "."); ok {
			req.DefaultDataset = &bq.DatasetRef{Project: project, Dataset: dataset}
		}
	}
}

func (w *Workspace) libraryQuery(path string) (store.Query, bool) {
	if w.opts.Library == nil || path == "" {
		return store.Query{}, false
	}

	return w.opts.Library.Find(path)
}
