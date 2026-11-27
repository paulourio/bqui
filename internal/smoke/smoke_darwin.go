//go:build darwin

// Package smoke runs bqui's offscreen UI smoke test: it renders the main
// window in light and dark appearance to PNGs, snapshots the accessibility
// tree, checks structural invariants, and writes report.json.
package smoke

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/editor"
	"github.com/paulourio/bqui/internal/export"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/results"
	"github.com/paulourio/bqui/internal/store"
	"github.com/paulourio/bqui/internal/ui"
)

const (
	dirPerm  = 0o750
	filePerm = 0o600

	maxDispatchDelay = 2 * time.Second

	editorReadyTimeout = 45 * time.Second // no GPU in the QEMU VM: Monaco boots slowly
	selfTestTimeout    = 20 * time.Second
	runTimeout         = 10 * time.Second
	loadTimeout        = 15 * time.Second
	maxReload          = 500 * time.Millisecond // QEMU without GPU; real hardware is far faster
	pollInterval       = 100 * time.Millisecond
)

// selfTestSQL has a valid statement, a syntax error, and a cursor position
// (line 3, column 8) right after "SELECT " where completion must offer items.
const selfTestSQL = "SELECT 1 AS one;\nSELEC broken;\nSELECT \n-- ligatures: -> => != <= >= <> :: || ==\n"

// ErrChecksFailed is returned when at least one smoke check fails.
var ErrChecksFailed = errors.New("smoke: checks failed")

// Check is one named assertion.
type Check struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// Report is written to report.json.
type Report struct {
	Version       string        `json:"version"`
	GoVersion     string        `json:"goVersion"`
	Arch          string        `json:"arch"`
	OSVersion     string        `json:"osVersion"`
	WindowFrame   appkit.Rect   `json:"windowFrame"`
	DispatchDelay time.Duration `json:"dispatchDelayNs"`
	Screenshots   []string      `json:"screenshots"`
	Checks        []Check       `json:"checks"`
	Failed        int           `json:"failed"`
}

func (r *Report) check(name string, ok bool, format string, args ...any) {
	c := Check{Name: name, OK: ok, Detail: fmt.Sprintf(format, args...)}
	if !ok {
		r.Failed++
	}

	r.Checks = append(r.Checks, c)
}

func (r *Report) skip(name, format string, args ...any) {
	r.Checks = append(r.Checks, Check{Name: name, OK: true, Skipped: true, Detail: fmt.Sprintf(format, args...)})
}

// Start must be called from a worker goroutine. It waits for the Monaco
// editor, runs its self-test, hops to the main thread (exercising
// objc.Main), runs the remaining checks, then calls done on the main thread.
func Start(s *ui.Shell, dir, version string, done func(error)) {
	bootStart := time.Now()
	ready := s.Editor.WaitReady(editorReadyTimeout)
	bootTime := time.Since(bootStart)

	var (
		selfTest json.RawMessage
		stErr    error
	)

	var runStatus string

	if ready {
		selfTest, stErr = s.Editor.RunSelfTest(selfTestSQL, 3, 8, selfTestTimeout)
		runStatus = runAtCursor(s)
	}

	phase("explorer")
	ex := exploreFlow(s)
	phase("results")
	rs := resultsFlow(s)
	phase("jobs")
	js := jobsFlow(s)
	phase("value")
	vs := valueFlow(s)
	phase("plan")
	ps := planFlow(s)
	phase("export")
	xs := exportFlow(s, dir)
	phase("workspace")
	ws := workspaceFlow(s)
	phase("theme")
	ts := themeFlow(s, dir)
	phase("prefs")
	pr := prefsFlow(s, dir)
	phase("checks")

	queued := time.Now()

	objc.Main(func() {
		rep := &Report{
			Version:       version,
			GoVersion:     runtime.Version(),
			Arch:          runtime.GOARCH,
			OSVersion:     appkit.OSVersion(),
			DispatchDelay: time.Since(queued),
		}

		editorChecks(s, rep, ready, bootTime, selfTest, stErr)

		explorerChecks(rep, ex)

		if ready {
			// Cursor is on line 3 ("SELECT "): only that statement runs.
			rep.check("editor-run-at-cursor", runStatus == "SELECT", "ran statement %q", runStatus)
		}

		resultsChecks(rep, rs)
		jobsChecks(rep, js)
		valueChecks(rep, vs)
		planChecks(rep, ps)
		exportChecks(rep, xs)
		workspaceChecks(rep, ws)
		themeChecks(rep, ts)
		prefsChecks(rep, pr)

		if err := run(s, dir, rep, done); err != nil {
			done(err)
		}
	})
}

// run performs the synchronous checks, then captures screenshots
// asynchronously and calls done exactly once (unless it returns an error).
func run(s *ui.Shell, dir string, rep *Report, done func(error)) error {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("smoke: %w", err)
	}

	rep.WindowFrame = objc.Send[appkit.Rect](s.Window, "frame")
	rep.check("dispatch", rep.DispatchDelay < maxDispatchDelay, "objc.Main delay %v", rep.DispatchDelay)

	structuralChecks(s, rep)

	ax := appkit.AXTree(s.Window)
	accessibilityChecks(ax, rep)

	if err := writeJSON(filepath.Join(dir, "ax.json"), ax); err != nil {
		return err
	}

	screenshots(s, dir, rep, func(err error) {
		if err == nil {
			err = writeJSON(filepath.Join(dir, "report.json"), rep)
		}

		if err == nil && rep.Failed > 0 {
			err = fmt.Errorf("%w: %d of %d", ErrChecksFailed, rep.Failed, len(rep.Checks))
		}

		done(err)
	})

	return nil
}

func structuralChecks(s *ui.Shell, rep *Report) {
	menu := objc.Msg(s.App, "mainMenu")
	menus := objc.Send[int64](menu, "numberOfItems")
	rep.check("main-menu", menus == 7, "%d top-level menus", menus)

	tb := objc.Msg(s.Window, "toolbar")
	items := 0
	if tb != objc.Nil {
		items = len(objc.ArrayItems(objc.Msg(tb, "items")))
	}

	rep.check("toolbar", items >= 8, "%d toolbar items", items)

	panes := len(objc.ArrayItems(objc.Msg(s.Split, "splitViewItems")))
	rep.check("split-panes", panes == 3, "%d panes (sidebar, content, inspector)", panes)

	rows := s.Explorer.Outline.Rows()
	rep.check("sidebar-rows", rows >= 2, "%d visible outline rows", rows)
}

// phase logs a timestamped marker so AppKit log lines can be attributed.
func phase(name string) {
	fmt.Fprintf(os.Stderr, "%s smoke phase: %s\n", time.Now().Format("15:04:05.000"), name)
}

// onMain runs fn on the main thread and waits for it.
func onMain(fn func()) {
	done := make(chan struct{})

	objc.Main(func() { fn(); close(done) })
	<-done
}

// waitMain polls cond on the main thread until it holds or timeout elapses.
func waitMain(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		var ok bool

		onMain(func() { ok = cond() })

		if ok {
			return true
		}

		time.Sleep(pollInterval)
	}

	return false
}

// exploreResult captures what the explorer flow observed.
type exploreResult struct {
	Roots, Datasets, SampleTables, ScaleRows int
	ShardGroup                               bool
	ScaleReload, MaxReload                   time.Duration
	SchemaFields                             int
	SchemaRows                               int64
	SchemaSegment                            int
	DatasetDetails                           int64
	DatasetSegment                           int
	FilterRows                               int64
	SegGeometry                              string
	Errors                                   []string
}

// exploreFlow drives the explorer through the real UI path: expanding via
// the outline view (which fires the will-expand delegate), selecting rows
// (which fires the inspector), filtering, and loading a 2000-table dataset.
func exploreFlow(s *ui.Shell) exploreResult {
	var r exploreResult

	m := s.Explorer.Model
	out := s.Explorer.Outline

	find := func(project, dataset, name string) uint64 {
		var id uint64

		onMain(func() { id, _ = m.Find(project, dataset, name) })

		if id == 0 {
			r.Errors = append(r.Errors, "missing node "+project+"."+dataset+"."+name)
		}

		return id
	}

	expand := func(id uint64) bool {
		if id == 0 {
			return false
		}

		onMain(func() { out.Expand(id, false) })

		ok := waitMain(loadTimeout, func() bool { return m.Loaded(id) })
		if !ok {
			r.Errors = append(r.Errors, fmt.Sprintf("node %d did not load", id))
		}

		return ok
	}

	onMain(func() { r.Roots = len(m.Children(app.RootID)) })

	pub := find("bigquery-public-data", "", "")
	if expand(pub) {
		onMain(func() { r.Datasets = len(m.Children(pub)) })
	}

	samples := find("bigquery-public-data", "samples", "")
	if expand(samples) {
		onMain(func() { r.SampleTables = len(m.Children(samples)) })
	}

	if ga := find("bigquery-public-data", "google_analytics_sample", ""); expand(ga) {
		var id uint64

		onMain(func() { id, _ = m.Find("bigquery-public-data", "google_analytics_sample", "ga_sessions_") })

		r.ShardGroup = id != 0
	}

	demo := find(app.FakeProject, "", "")
	expand(demo)

	if scale := find(app.FakeProject, app.FakeScaleDataset, ""); expand(scale) {
		onMain(func() {
			r.ScaleRows = len(m.Children(scale))
			r.ScaleReload = s.Explorer.LastReload
		})
	}

	onMain(func() { s.Explorer.SetFilter("shakes") })
	onMain(func() { r.FilterRows = out.Rows() })
	onMain(func() { s.Explorer.SetFilter("") })

	// Dataset: details only.
	onMain(func() { out.Select(samples) })
	waitMain(loadTimeout, func() bool { return s.Inspector.DetailRows() > 0 && s.Inspector.Current().Kind == app.KindDataset })
	onMain(func() {
		r.DatasetDetails = s.Inspector.DetailRows()
		r.DatasetSegment = s.Inspector.Segment()
	})

	// Nested table: schema tab.
	if nested := find("bigquery-public-data", "samples", "github_nested"); nested != 0 {
		onMain(func() { out.Select(nested) })
		waitMain(loadTimeout, func() bool {
			return s.Inspector.Current().Name == "github_nested" && s.Inspector.SchemaFields() > 0
		})
		onMain(func() {
			r.SchemaFields = s.Inspector.SchemaFields()
			r.SchemaRows = s.Inspector.SchemaRows()
			r.SchemaSegment = s.Inspector.Segment()
		})
	}

	onMain(func() {
		r.MaxReload = s.Explorer.MaxReload
		r.SegGeometry = s.Inspector.SegmentGeometry()
	})

	return r
}

func explorerChecks(rep *Report, r exploreResult) {
	rep.check("explorer-flow", len(r.Errors) == 0, "errors: %v", r.Errors)
	rep.check("explorer-projects", r.Roots == 2, "%d pinned projects", r.Roots)
	rep.check("explorer-datasets", r.Datasets == 10, "%d datasets in bigquery-public-data", r.Datasets)
	rep.check("explorer-tables", r.SampleTables == 7, "%d tables in samples", r.SampleTables)
	rep.check("explorer-shards", r.ShardGroup, "ga_sessions_ shard group present: %v", r.ShardGroup)
	rep.check("explorer-scale", r.ScaleRows == app.FakeScaleTables && r.ScaleReload < maxReload,
		"%d tables; main-thread reload %v (max over run %v)", r.ScaleRows, r.ScaleReload.Round(time.Millisecond), r.MaxReload.Round(time.Millisecond))
	rep.check("explorer-filter", r.FilterRows >= 3 && r.FilterRows <= 6, "%d rows visible for filter \"shakes\"", r.FilterRows)
	rep.check("inspector-dataset", r.DatasetDetails > 3 && r.DatasetSegment == 1, "%d detail rows, segment %d", r.DatasetDetails, r.DatasetSegment)
	rep.skip("inspector-segments", "%s", r.SegGeometry)
	rep.check("inspector-schema", r.SchemaFields > 20 && r.SchemaRows > 5 && r.SchemaSegment == 0,
		"%d fields (%d top-level rows), segment %d", r.SchemaFields, r.SchemaRows, r.SchemaSegment)
}

// runAtCursor triggers "Run Statement at Cursor" (page → Go → bqls
// statements → shell) and waits for the results status to change.
// runAtCursor triggers "Run Statement at Cursor" (page → Go → bqls
// statements → runner) and returns the title of the run it started (the
// statement's first line) once that run finished.
func runAtCursor(s *ui.Shell) string {
	var before *app.Run

	onMain(func() { before = s.Results.Current() })
	onMain(func() { s.Editor.RequestRun(true) })

	var run *app.Run

	waitMain(runTimeout, func() bool {
		run = s.Results.Current()

		return run != nil && run != before
	})

	if run == nil || run == before {
		return ""
	}

	<-run.Done()

	return run.Snapshot().Title
}

// resultsResult captures what the results flow observed.
type resultsResult struct {
	ErrorStatus, CancelStatus, PreviewStatus, DoneStatus string
	PreviewRows                                          int64
	Rows, Lines                                          int64
	TableRows                                            int
	Gutter                                               []string
	NestedLines                                          int
	NullText                                             appkit.CellText
	Separators                                           int
	JSONRow                                              string
	SepInside                                            bool
	SepDetail                                            string
}

// waitRun runs start on the main thread and waits for the resulting run.
func waitRun(s *ui.Shell, start func()) *app.Run {
	var before, run *app.Run

	onMain(func() { before = s.Results.Current() })
	onMain(start)
	onMain(func() { run = s.Results.Current() })

	if run == nil || run == before {
		return nil
	}

	select {
	case <-run.Done():
	case <-time.After(loadTimeout):
	}

	// Let the final OnUpdate land on the main thread.
	waitMain(loadTimeout, func() bool {
		return s.Results.StatusText() != "" && !strings.HasPrefix(s.Results.StatusText(), "Running")
	})

	return run
}

// resultsFlow runs queries through the shell and inspects the grid exactly
// as the table renders it.
func resultsFlow(s *ui.Shell) resultsResult {
	var r resultsResult

	waitRun(s, func() { s.RunSQL("SELEC broken") })
	onMain(func() { r.ErrorStatus = s.Status() })

	onMain(func() { s.RunSQL("SELECT order_id FROM orders LIMIT 10") })
	time.Sleep(100 * time.Millisecond)
	onMain(s.Stop)
	waitMain(loadTimeout, func() bool { return strings.HasPrefix(s.Status(), "Cancelled") })
	onMain(func() { r.CancelStatus = s.Status() })

	if pr := waitRun(s, func() {
		s.Preview(app.ObjectInfo{Kind: app.KindTable, Project: app.FakeProject, Dataset: "analytics", Name: "events"})
	}); pr != nil {
		if g := pr.Snapshot().Grid; g != nil {
			r.PreviewRows, _, _ = g.Size()
		}
	}

	onMain(func() { r.PreviewStatus = s.Status() })

	run := waitRun(s, func() { s.RunSQL("SELECT order_id, created_at, customer, items, total FROM orders LIMIT 5000") })
	if run == nil {
		return r
	}

	onMain(func() { inspectGrid(s, run, &r) })

	return r
}

// inspectGrid reads the finished grid back through the table (main thread).
func inspectGrid(s *ui.Shell, run *app.Run, r *resultsResult) {
	r.DoneStatus = s.Status()
	g := run.Snapshot().Grid

	if g == nil {
		return
	}

	r.Rows, r.Lines, _ = g.Size()
	t := s.Results.Table
	r.TableRows = t.Rows()

	for line := range 8 {
		r.Gutter = append(r.Gutter, t.VisibleText(line, "__row").Text)

		if t.VisibleText(line, "__row").Text == "" {
			r.NestedLines++
		}
	}

	// Order 3 (row index 2) has a NULL customer.tier (column c3).
	if first, err := g.FirstLine(2); err == nil {
		r.NullText = t.VisibleText(int(first), "c3")
	}

	for line := range 40 {
		if s.Results.Table.VisibleText(line, "__row").Text != "" && line > 0 {
			r.Separators++
		}
	}

	if row, err := g.Row(3); err == nil {
		r.JSONRow = string(results.AppendRowJSON(nil, g.Fields(), row))
	}

	// Line 0 is order 1's only line: its separator must be visible and lie
	// on the row view's bottom edge (inside its bounds).
	rb, sf, vis := t.SeparatorGeometry(0)
	inside := sf.Size.Height > 0 && sf.Origin.Y >= rb.Origin.Y && sf.Origin.Y+sf.Size.Height <= rb.Origin.Y+rb.Size.Height+0.5
	r.SepInside = vis && inside && sf.Size.Width >= rb.Size.Width-1
	r.SepDetail = fmt.Sprintf("row bounds %+v, separator %+v, visible %v", rb, sf, vis)

	// Leave the nested items.* columns in view for screen captures.
	t.ScrollToColumn("c" + strconv.Itoa(len(g.Columns())-1))
}

// jobsResult captures what the job-history flow observed.
type jobsResult struct {
	JobRows                 int64
	HistoryMine, HistoryAll int
	FirstJob, LastRun       string
	FirstUser               string
	InspectorTitle          string
	InspectorRows           int64
	ReopenedRows            int64
	ReopenedStatus          string
}

// jobsFlow exercises Job information, History and reopening past results.
func jobsFlow(s *ui.Shell) jobsResult {
	var r jobsResult

	onMain(func() {
		if run := s.Results.Current(); run != nil && run.Snapshot().Job != nil {
			r.LastRun = run.Snapshot().Job.JobID
		}

		s.SelectTab(ui.TabJob)
	})
	waitMain(loadTimeout, func() bool { return s.Job.Rows() > 5 })
	onMain(func() { r.JobRows = s.Job.Rows() })

	onMain(func() { s.SelectTab(ui.TabHistory) })
	waitMain(loadTimeout, func() bool { return len(s.History.Model.Rows()) > 0 && !s.History.Model.Loading() })
	onMain(func() {
		rows := s.History.Model.Rows()
		r.HistoryMine = len(rows)
		r.FirstJob, r.FirstUser = rows[0].Ref.JobID, rows[0].User
		s.History.Table.SelectRows(0, 0)
	})

	waitMain(loadTimeout, func() bool { return s.Inspector.DetailRows() > 3 })
	onMain(func() {
		r.InspectorTitle = s.Inspector.Current().Name
		r.InspectorRows = s.Inspector.DetailRows()
	})

	// Reopen the newest job's results.
	var ref bq.JobRef

	onMain(func() { ref = s.History.Model.Rows()[0].Ref })

	if run := waitRun(s, func() { s.Results.Show(s.Runner.OpenJob(ref)) }); run != nil {
		if g := run.Snapshot().Grid; g != nil {
			r.ReopenedRows, _, _ = g.Size()
		}
	}

	onMain(func() { r.ReopenedStatus = s.Status() })

	// Project-wide history includes other users and pages on.
	onMain(func() { s.History.SetScope(1) })
	waitMain(loadTimeout, func() bool { return len(s.History.Model.Rows()) >= app.HistoryPage && !s.History.Model.Loading() })
	onMain(func() { r.HistoryAll = len(s.History.Model.Rows()) })

	return r
}

// valueResult captures the JSON view and value inspector flow.
type valueResult struct {
	JSONRows     int64
	FirstJSON    string
	ValueRows    int64
	SelectedPath string
}

func valueFlow(s *ui.Shell) valueResult {
	var r valueResult

	onMain(func() { s.SelectTab(ui.TabJSON) })
	waitMain(loadTimeout, func() bool { return s.JSON.Rows() == 5000 })
	onMain(func() {
		r.JSONRows = s.JSON.Rows()
		r.FirstJSON = s.JSON.Line(0)
	})

	// Order 3 (row 2): item 0 has two tags, so its second visual line holds
	// items[0].tags[1]. items.tags is leaf column 7.
	onMain(func() {
		g := s.Results.Current().Snapshot().Grid
		if first, err := g.FirstLine(2); err == nil {
			s.Results.InspectCell(int(first)+1, 7)
		}
	})
	waitMain(loadTimeout, func() bool { _, p := s.Inspector.ValueRows(); return p != "" })
	onMain(func() { r.ValueRows, r.SelectedPath = s.Inspector.ValueRows() })

	return r
}

// planResult captures the execution details/graph flow.
type planResult struct {
	DetailRows int64
	Nodes      int
	Inspector  string
}

func planFlow(s *ui.Shell) planResult {
	var r planResult

	onMain(func() { s.SelectTab(ui.TabPlan) })
	waitMain(loadTimeout, func() bool { return s.Plan.Rows() > 0 })
	onMain(func() { r.DetailRows = s.Plan.Rows() })

	onMain(func() { s.SelectTab(ui.TabGraph) })
	onMain(func() {
		r.Nodes = s.Graph.Nodes()
		s.Graph.SelectStage(9) // the join
	})
	waitMain(loadTimeout, func() bool { return strings.HasPrefix(s.Inspector.Current().Name, "S09") })
	onMain(func() { r.Inspector = s.Inspector.Current().Name })

	return r
}

// exportResult captures the export flow.
type exportResult struct {
	CSVLines, JSONLines, GridLines int64
	Status                         string
	Err                            string
}

func exportFlow(s *ui.Shell, dir string) exportResult {
	var r exportResult

	if err := os.MkdirAll(dir, dirPerm); err != nil {
		r.Err = err.Error()

		return r
	}

	exportOne := func(name string, f export.Format) string {
		path := filepath.Join(dir, name)

		onMain(func() {
			src := app.ExportSource{Grid: s.Results.Current().Snapshot().Grid}
			_, r.GridLines, _ = src.Grid.Size()
			s.ExportTo(src, path, f, export.Options{})
		})
		waitMain(loadTimeout, func() bool { return s.LastExport().Done && s.LastExport().Path == path })
		onMain(func() {
			r.Status = s.Status()
			if err := s.LastExport().Err; err != nil {
				r.Err = err.Error()
			}
		})

		return path
	}

	countLines := func(path string) int64 {
		b, err := os.ReadFile(path)
		if err != nil {
			return -1
		}

		return int64(strings.Count(string(b), "\n"))
	}

	r.CSVLines = countLines(exportOne("export.csv", export.CSV))
	r.JSONLines = countLines(exportOne("export.jsonl", export.NDJSON))

	return r
}

// workspaceResult captures the saved-query, autosave, session and local
// history flow.
type workspaceResult struct {
	Edited, Saved, Listed, Dirty, Reopened bool
	Autosaves                              int
	AutosaveErr, Title                     string
	SessionID, SessionAfter                string
	SessionEnded                           bool
	LocalRuns, LocalMatches                int
	LocalSQL                               string
}

func workspaceFlow(s *ui.Shell) workspaceResult {
	const text = "SELECT 1 AS smoke_saved"

	var r workspaceResult

	sqlIs := func(want string) func() bool {
		return func() bool { return s.Workspace.Tab().SQL == want }
	}

	// A real edit (insertText) must come back as a change and autosave.
	onMain(func() { s.Editor.SetTextAt("", 0, 0); s.Editor.InsertText(text) })
	r.Edited = waitMain(loadTimeout, sqlIs(text))

	path := filepath.Join(s.Workspace.QueriesDir(), "smoke-saved.sql")

	onMain(func() {
		r.Saved = s.SaveQueryTo(path)
		s.SelectSidebar(ui.SidebarSaved)
		r.Listed = len(s.Saved.Queries()) == 1 && s.Saved.Queries()[0].Path == path
		s.SelectSidebar(ui.SidebarExplorer)
		s.Editor.InsertText(" -- edited")
	})
	waitMain(loadTimeout, func() bool { return s.Workspace.Dirty() })
	onMain(func() { r.Dirty, r.Title = s.Workspace.Dirty(), s.Workspace.Title() })

	_ = s.Workspace.Flush()
	n, err := s.Workspace.Saves()
	r.Autosaves = n

	if err != nil {
		r.AutosaveErr = err.Error()
	}

	onMain(func() { s.NewQuery(); s.OpenQuery(path) })
	r.Reopened = waitMain(loadTimeout, sqlIs(text+"\n")) && !s.Workspace.Dirty()

	// Session mode: the first run creates a session, the next joins it.
	onMain(s.ToggleSessionMode)
	waitRun(s, func() { s.RunSQL("CREATE TEMP TABLE smoke_t AS SELECT 1 AS x") })
	_, r.SessionID = s.Workspace.SessionMode()
	waitRun(s, func() { s.RunSQL("SELECT x FROM smoke_t") })
	_, r.SessionAfter = s.Workspace.SessionMode()
	waitRun(s, s.EndSession)
	on, id := s.Workspace.SessionMode()
	r.SessionEnded = !on && id == ""

	// Local run history: everything above was recorded; FTS finds it.
	onMain(s.RunHistory)
	waitMain(loadTimeout, func() bool { return len(s.History.Rows()) >= 3 })
	onMain(func() { r.LocalRuns = len(s.History.Rows()); s.History.SetFilter("smoke_t create") })
	waitMain(loadTimeout, func() bool { return len(s.History.Rows()) == 1 })
	onMain(func() {
		r.LocalMatches = len(s.History.Rows())
		if r.LocalMatches > 0 {
			r.LocalSQL = s.History.Rows()[0].SQL
		}

		s.History.SetFilter("")
		s.SelectTab(ui.TabResults)
	})

	return r
}

// themeResult captures switching, importing and restoring themes.
type themeResult struct {
	MidnightEditor, ImportEditor, DefaultEditor string
	MidnightDark, ImportDark                    bool
	NullColored, KeyColored                     bool
	JSONSpans                                   int
	Imported, ImportErr                         string
	ImportSaved                                 bool
	Themes                                      int
	Shot                                        string
}

const smokeVSCodeTheme = `{
	// Light VS Code theme with comments and trailing commas.
	"name": "Smoke Light", "type": "light",
	"colors": { "editor.background": "#fdf6e3", "editor.foreground": "#586e75", },
	"tokenColors": [
		{ "scope": "keyword", "settings": { "foreground": "#859900", "fontStyle": "bold" } },
		{ "scope": "string", "settings": { "foreground": "#2aa198" } },
	],
}`

func themeFlow(s *ui.Shell, dir string) themeResult {
	var r themeResult

	editorTheme := func(want string) string {
		waitMain(loadTimeout, func() bool { return s.Editor.AppliedTheme() == want })

		return s.Editor.AppliedTheme()
	}

	// Midnight is dark-only: app, editor, grid and JSON all switch.
	onMain(func() { s.ApplyTheme("Midnight"); s.SelectTab(ui.TabJSON) })
	r.MidnightEditor = editorTheme("bqui-dark")
	onMain(func() {
		r.MidnightDark = s.Dark()
		r.NullColored = s.Results.NullColor != objc.Nil

		if s.JSON.Rows() > 0 {
			c := s.JSON.Cell(0)
			r.JSONSpans = len(c.Spans)

			for _, sp := range c.Spans {
				if sp.Color != objc.Nil && sp.Color == s.JSON.KeyColor() {
					r.KeyColored = true
				}
			}
		}
	})

	time.Sleep(settleDelay)
	onMain(func() {
		r.Shot = "theme-midnight.png"
		if err := appkit.WriteWindowPNG(s.Window, filepath.Join(dir, r.Shot)); err != nil {
			r.Shot = "✕ " + err.Error()
		}
	})

	path := filepath.Join(dir, "smoke-vscode-theme.json")
	if err := os.WriteFile(path, []byte(smokeVSCodeTheme), 0o600); err != nil {
		r.ImportErr = err.Error()

		return r
	}

	onMain(func() {
		t, err := s.ImportVSCodeTheme(path)
		if err != nil {
			r.ImportErr = err.Error()

			return
		}

		r.Imported = s.Theme.Name
		_, statErr := os.Stat(t.Path)
		r.ImportSaved = statErr == nil
		r.Themes = len(s.Themes)
	})
	r.ImportEditor = editorTheme("bqui-light")
	onMain(func() { r.ImportDark = s.Dark() })

	onMain(func() { s.ApplyTheme("Default"); s.SelectTab(ui.TabResults) })
	r.DefaultEditor = s.Editor.AppliedTheme()

	return r
}

// prefsResult captures the Settings window and query guards.
type prefsResult struct {
	BadErr, LimitedErr, Shot string
	Limited, Unlimited       app.RunState
	WindowShown              bool
}

func prefsFlow(s *ui.Shell, dir string) prefsResult {
	var r prefsResult

	onMain(func() {
		s.ShowPreferences()
		r.WindowShown = objc.Send[bool](s.PreferencesWindow(), "isVisible")

		if err := s.ApplyPreferences(store.PreferenceText{MaxBytesBilled: "lots"}); err != nil {
			r.BadErr = err.Error()
		}
	})

	time.Sleep(settleDelay)
	onMain(func() {
		r.Shot = "settings.png"
		if err := appkit.WriteWindowPNG(s.PreferencesWindow(), filepath.Join(dir, r.Shot)); err != nil {
			r.Shot = "✕ " + err.Error()
		}

		objc.Msg(s.PreferencesWindow(), "orderOut:", objc.Nil)
	})

	// The fake bills 10 MiB: a 1 MB guard must fail the run like BigQuery.
	onMain(func() { _ = s.ApplyPreferences(store.PreferenceText{MaxBytesBilled: "1 MB", Location: "US"}) })

	if run := waitRun(s, func() { s.RunSQL("SELECT order_id FROM orders LIMIT 5") }); run != nil {
		snap := run.Snapshot()
		r.Limited = snap.State

		if snap.Err != nil {
			r.LimitedErr = snap.Err.Error()
		}
	}

	onMain(func() { _ = s.ApplyPreferences(store.PreferenceText{}) })

	if run := waitRun(s, func() { s.RunSQL("SELECT order_id FROM orders LIMIT 5") }); run != nil {
		r.Unlimited = run.Snapshot().State
	}

	return r
}

func prefsChecks(rep *Report, r prefsResult) {
	rep.check("prefs-window", r.WindowShown && !strings.HasPrefix(r.Shot, "✕") && strings.Contains(r.BadErr, "maximum bytes billed"),
		"shown %v, %s, invalid input: %q", r.WindowShown, r.Shot, r.BadErr)
	rep.check("prefs-bytes-guard", r.Limited == app.RunFailed && strings.Contains(r.LimitedErr, "bytes billed") && r.Unlimited == app.RunDone,
		"limited run %v (%s); unlimited run %v", r.Limited, r.LimitedErr, r.Unlimited)
}

func themeChecks(rep *Report, r themeResult) {
	rep.check("theme-switch", r.MidnightEditor == "bqui-dark" && r.MidnightDark && r.NullColored,
		"editor %q, app dark %v, grid null colored %v", r.MidnightEditor, r.MidnightDark, r.NullColored)
	rep.check("theme-json", r.JSONSpans > 1 && r.KeyColored, "%d spans, keys colored %v", r.JSONSpans, r.KeyColored)
	rep.check("theme-screenshot", !strings.HasPrefix(r.Shot, "✕"), "%s", r.Shot)
	rep.check("theme-import", r.Imported == "Smoke Light" && r.ImportSaved && r.ImportEditor == "bqui-light" && !r.ImportDark && r.ImportErr == "",
		"imported %q saved %v, editor %q, dark %v, %d themes %s", r.Imported, r.ImportSaved, r.ImportEditor, r.ImportDark, r.Themes, r.ImportErr)
}

func workspaceChecks(rep *Report, r workspaceResult) {
	rep.check("workspace-save", r.Edited && r.Saved && r.Listed, "edited %v, saved %v, listed in Saved %v", r.Edited, r.Saved, r.Listed)
	rep.check("workspace-dirty", r.Dirty && strings.HasSuffix(r.Title, "— Edited"), "dirty %v, title %q", r.Dirty, r.Title)
	rep.check("workspace-autosave", r.Autosaves > 0 && r.AutosaveErr == "", "%d autosaves %s", r.Autosaves, r.AutosaveErr)
	rep.check("workspace-open", r.Reopened, "reopened saved query clean: %v", r.Reopened)
	rep.check("workspace-session", r.SessionID != "" && r.SessionAfter == r.SessionID && r.SessionEnded,
		"session %q, after second run %q, ended %v", r.SessionID, r.SessionAfter, r.SessionEnded)
	rep.check("workspace-history", r.LocalRuns >= 3 && r.LocalMatches == 1 && strings.Contains(r.LocalSQL, "CREATE TEMP TABLE smoke_t"),
		"%d local runs; search found %d: %q", r.LocalRuns, r.LocalMatches, r.LocalSQL)
}

func exportChecks(rep *Report, r exportResult) {
	rep.check("export-csv", r.CSVLines == r.GridLines+1, "%d CSV lines for %d grid lines (+ header)", r.CSVLines, r.GridLines)
	rep.check("export-json", r.JSONLines == 5000 && strings.HasPrefix(r.Status, "✓ Exported") && r.Err == "",
		"%d JSON lines; status %q %s", r.JSONLines, r.Status, r.Err)
}

func planChecks(rep *Report, r planResult) {
	rep.check("plan-details", r.DetailRows == 13, "%d stage rows", r.DetailRows)
	rep.check("plan-graph", r.Nodes == 13 && strings.HasPrefix(r.Inspector, "S09: Join"), "%d nodes; inspector %q", r.Nodes, r.Inspector)
}

func valueChecks(rep *Report, r valueResult) {
	rep.check("json-view", r.JSONRows == 5000 && strings.HasPrefix(r.FirstJSON, `{"order_id":"1",`), "%d rows; first %.60s", r.JSONRows, r.FirstJSON)
	rep.check("value-inspector", r.SelectedPath == "items[0].tags[1]" && r.ValueRows >= 5, "selected %q, %d rows visible", r.SelectedPath, r.ValueRows)
}

func jobsChecks(rep *Report, r jobsResult) {
	rep.check("jobs-information", r.JobRows > 5, "%d job information rows", r.JobRows)
	rep.check("jobs-history", r.HistoryMine > 0 && r.FirstJob == r.LastRun && r.FirstUser == "you@example.com",
		"%d personal jobs; newest %s by %s (last run %s)", r.HistoryMine, r.FirstJob, r.FirstUser, r.LastRun)
	rep.check("jobs-inspector", r.InspectorTitle == r.FirstJob && r.InspectorRows > 3, "inspector %q with %d rows", r.InspectorTitle, r.InspectorRows)
	rep.check("jobs-reopen", r.ReopenedRows == 5000, "reopened %d rows; status %q", r.ReopenedRows, r.ReopenedStatus)
	rep.check("jobs-project", r.HistoryAll >= app.HistoryPage, "%d project jobs loaded", r.HistoryAll)
}

func resultsChecks(rep *Report, r resultsResult) {
	rep.check("results-error", strings.HasPrefix(r.ErrorStatus, "✕") && strings.Contains(r.ErrorStatus, "Syntax error"), "status %q", r.ErrorStatus)
	rep.check("results-cancel", strings.HasPrefix(r.CancelStatus, "Cancelled"), "status %q", r.CancelStatus)
	rep.check("results-preview", r.PreviewRows == 1000 && strings.Contains(r.PreviewStatus, "Preview of"), "%d rows; status %q", r.PreviewRows, r.PreviewStatus)
	rep.check("results-done", strings.Contains(r.DoneStatus, "✓ 5,000 rows"), "status %q", r.DoneStatus)
	rep.check("results-grid", r.Rows == 5000 && r.Lines > r.Rows && int64(r.TableRows) == r.Lines,
		"%d rows → %d visual lines; table shows %d", r.Rows, r.Lines, r.TableRows)
	rep.check("results-nesting", r.NestedLines > 0 && len(r.Gutter) > 0 && r.Gutter[0] == "1",
		"gutter %q (%d continuation lines in the first 8)", r.Gutter, r.NestedLines)
	rep.check("results-null", r.NullText.Text == "null" && r.NullText.Italic && r.NullText.Secondary, "NULL cell %+v", r.NullText)
	rep.check("results-separator", r.SepInside, "%s", r.SepDetail)
	rep.check("results-json", strings.HasPrefix(r.JSONRow, `{"order_id":"4"`) && strings.Contains(r.JSONRow, `"items":[{`), "row 4 JSON %s", r.JSONRow)
}

func editorChecks(s *ui.Shell, rep *Report, ready bool, boot time.Duration, selfTest json.RawMessage, stErr error) {
	consoleErrs, bootErr := s.Editor.Diagnostics()

	rep.check("editor-ready", ready, "Monaco ready after %v (boot error: %q)", boot.Round(time.Millisecond), bootErr)

	if !ready {
		return
	}

	var st struct {
		Suggestions int      `json:"suggestions"`
		Labels      []string `json:"labels"`
		Markers     int      `json:"markers"`
		FirstMarker string   `json:"firstMarker"`
		Spans       int      `json:"spans"`
		FontLoaded  bool     `json:"fontLoaded"`
		FontFamily  string   `json:"fontFamily"`
		Features    string   `json:"fontFeatures"`
	}

	if stErr == nil {
		stErr = json.Unmarshal(selfTest, &st)
	}

	rep.check("editor-selftest", stErr == nil, "self-test: %v", errText(stErr))
	rep.check("editor-completion", st.Suggestions > 0, "%d suggestions, first: %s", st.Suggestions, strings.Join(st.Labels, " | "))
	rep.check("editor-diagnostics", st.Markers > 0, "%d markers, first: %q", st.Markers, st.FirstMarker)
	rep.check("editor-tokens", st.Spans > 0, "%d colored token spans", st.Spans)
	rep.check("editor-font", st.FontLoaded && strings.Contains(st.FontFamily, "bqui mono") && strings.Contains(st.Features, "calt"),
		"embedded font %s loaded=%v; family %q; features %q", editor.FontVersion(), st.FontLoaded, st.FontFamily, st.Features)

	first := ""
	if len(consoleErrs) > 0 {
		first = consoleErrs[0]
	}

	rep.check("editor-console", len(consoleErrs) == 0, "%d console errors; first: %q", len(consoleErrs), first)
}

func accessibilityChecks(ax *appkit.AXNode, rep *Report) {
	for _, role := range []string{"AXOutline", "AXSplitGroup", "AXToolbar"} {
		found := ax.Find(func(n *appkit.AXNode) bool { return n.Role == role }) != nil
		rep.check("ax-"+role, found, "role %s present: %v", role, found)
	}

	// The editor WKWebView is an AXGroup host as the first pane of the inner
	// (editor | results) split; its web content lives in WebKit's process.
	inner := ax.Find(func(n *appkit.AXNode) bool {
		return n.Role == "AXSplitGroup" && len(n.Children) > 0 && n.Children[0].Role == "AXGroup"
	})
	rep.check("ax-editor-host", inner != nil, "editor web view host present: %v", inner != nil)
}

// screenshots captures the window per appearance. The WindowServer capture
// shows real Liquid Glass; the offscreen render is a fallback that needs no
// compositor (materials render flat there).
func screenshots(s *ui.Shell, dir string, rep *Report, next func(error)) {
	// The frame view (content view's superview) includes titlebar and toolbar.
	frameView := objc.Msg(objc.Msg(s.Window, "contentView"), "superview")

	if err := appkit.WritePNG(s.Inspector.SegmentView(), filepath.Join(dir, "segmented.png")); err == nil {
		rep.Screenshots = append(rep.Screenshots, "segmented.png")
	}
	looks := []struct{ name, appearance string }{
		{"light", "NSAppearanceNameAqua"},
		{"dark", "NSAppearanceNameDarkAqua"},
	}

	var step func(i int)

	step = func(i int) {
		if i == len(looks) {
			objc.Msg(s.Window, "setAppearance:", objc.Nil)
			next(nil)

			return
		}

		look := looks[i]
		objc.Msg(s.Window, "setAppearance:", objc.Msg(objc.Cls("NSAppearance"), "appearanceNamed:", objc.String(look.appearance)))
		objc.Msg(frameView, "layoutSubtreeIfNeeded")
		objc.Msg(s.Window, "displayIfNeeded")

		// Let the WindowServer composite the new appearance before capturing.
		afterMain(settleDelay, func() {
			name := "window-" + look.name + ".png"
			err := appkit.WriteWindowPNG(s.Window, filepath.Join(dir, name))

			switch {
			case errors.Is(err, appkit.ErrCaptureBlank):
				rep.skip("capture-"+look.name, "%v", err)
			default:
				rep.check("capture-"+look.name, err == nil, "WindowServer capture: %v", errText(err))
			}

			if err == nil {
				rep.Screenshots = append(rep.Screenshots, name)
			}

			offscreen := "offscreen-" + look.name + ".png"
			if err := appkit.WritePNG(frameView, filepath.Join(dir, offscreen)); err != nil {
				next(err)

				return
			}

			rep.Screenshots = append(rep.Screenshots, offscreen)
			step(i + 1)
		})
	}

	step(0)
}

const settleDelay = 600 * time.Millisecond

func afterMain(d time.Duration, fn func()) {
	go func() {
		time.Sleep(d)
		objc.Main(fn)
	}()
}

func errText(err error) string {
	if err == nil {
		return "ok"
	}

	return err.Error()
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("smoke: %w", err)
	}

	if err := os.WriteFile(path, append(data, '\n'), filePerm); err != nil {
		return fmt.Errorf("smoke: %w", err)
	}

	return nil
}
