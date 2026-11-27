//go:build darwin

// Package ui builds bqui's AppKit interface on top of internal/appkit and the
// UI-agnostic models in internal/app.
package ui

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/paulourio/bqlang/bqconfig"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/plan"
	"github.com/paulourio/bqui/internal/results"
	"github.com/paulourio/bqui/internal/store"
)

// AppName is the user-visible application name.
const AppName = "bqui"

const (
	defaultWidth     = 1280
	defaultHeight    = 800
	sidebarMin       = 220
	sidebarMax       = 480
	inspectorMin     = 300
	inspectorMax     = 720
	inspectorWidth   = 440
	editorMin        = 120
	resultsMin       = 120
	resultsHolding   = 240 // results pane gives space before the editor
	editorHolding    = 260
	windowAutosaveID = "BQUIMainWindow"
	editorShare      = 0.55
	projectsTimeout  = 30 * time.Second
	projectPickerW   = 220
)

// Options configures the shell.
type Options struct {
	// Deterministic disables frame/split autosave so screenshots are stable.
	Deterministic bool
	// Inspectable enables the Safari Web Inspector for the editor.
	Inspectable bool
	// Backend serves BigQuery metadata (real client or fixtures).
	Backend app.Backend
	// Live is true when Backend talks to BigQuery (enables bqls API access).
	Live bool
	// Settings and SettingsPath persist pinned projects and the billing project.
	Settings     *store.Settings
	SettingsPath string
	// Queries runs queries; Storage (optional) streams large results and
	// previews through the Storage Read API.
	Queries app.QueryBackend
	Storage app.StorageOpener
	// Jobs lists and inspects job history.
	Jobs app.JobsBackend
	// SpillDir holds result spill files ("" = OS temp dir).
	SpillDir string
}

// Shell is the main window and its panes.
type Shell struct {
	App       objc.ID
	Window    objc.ID
	Split     objc.ID // outer NSSplitViewController (sidebar | content | inspector)
	Explorer  *ExplorerPane
	Inspector *InspectorPane
	Editor    *EditorPane
	Cost      objc.ID // toolbar cost estimate label
	Results   *ResultsPane
	JSON      *JSONPane
	Plan      *PlanPane
	Graph     *GraphPane
	Job       *JobPane
	History   *HistoryPane
	Runner    *app.Runner
	tabs      *appkit.Segmented
	tabViews  []objc.ID

	exportCancel func()
	lastExport   app.ExportProgress

	opts     Options
	settings *store.Settings
	billing  atomic.Pointer[string] // readable from any goroutine
	projects objc.ID                // toolbar project picker (NSPopUpButton)
	all      []string
}

// NewShell builds the application menu, main window, toolbar and panes.
func NewShell(opts Options) (*Shell, error) {
	if opts.Settings == nil {
		opts.Settings = &store.Settings{PinnedProjects: []string{store.DefaultPinned}}
	}

	s := &Shell{App: appkit.SharedApplication(), opts: opts, settings: opts.Settings}
	s.billing.Store(&opts.Settings.BillingProject)

	appkit.SetMainMenu(s.App, s.menus())

	ed, err := NewEditorPane(EditorOptions{
		LSP:         s.lspOptions(),
		Inspectable: opts.Inspectable,
		OnRun:       s.onRun,
		OnCost:      s.onCost,
		OnConsole: func(level, msg string) {
			if level == "error" || level == "warn" {
				fmt.Fprintf(os.Stderr, "bqui editor [%s] %s\n", level, msg)
			}
		},
	})
	if err != nil {
		return nil, err
	}

	s.Editor = ed
	s.Editor.SetText(sampleSQL)
	appkit.SetIdentifier(s.Editor.Web.View, "editor")

	s.Inspector = newInspectorPane(s, opts.Backend)
	s.Inspector.jobs = opts.Jobs
	s.Explorer = newExplorerPane(s, opts.Backend)

	s.Results = newResultsPane()
	s.Results.Inspect = func(f []results.Field, row []any, i int64, path string) { s.Inspector.ShowRow(f, row, i, path) }
	s.Results.ExportMenu = func() appkit.MenuItem { return s.exportMenu("Export Results", s.resultsExportSource) }
	s.JSON = newJSONPane(s)
	s.Plan = newPlanPane(s)
	s.Graph = newGraphPane(s)
	s.Job = newJobPane(opts.Jobs)
	s.History = newHistoryPane(s, opts.Jobs)
	s.Runner = app.NewRunner(app.RunnerOptions{
		Backend:  opts.Queries,
		Metadata: opts.Backend,
		Jobs:     opts.Jobs,
		Storage:  opts.Storage,
		Post:     objc.Main,
		OnUpdate: s.onRunUpdate,
		SpillDir: opts.SpillDir,
	})

	autosave := func(name string) string {
		if opts.Deterministic {
			return ""
		}

		return name
	}

	content := appkit.SplitViewController(false, autosave("BQUIEditorResults"), []appkit.SplitItem{
		{Controller: appkit.ViewController(s.Editor.Web.View), MinThickness: editorMin, HoldingWeight: editorHolding},
		{Controller: appkit.ViewController(s.bottomPane()), MinThickness: resultsMin, HoldingWeight: resultsHolding},
	})

	s.Split = appkit.SplitViewController(true, autosave("BQUIMainSplit"), []appkit.SplitItem{
		{Kind: appkit.SplitSidebar, Controller: appkit.ViewController(s.Explorer.View), MinThickness: sidebarMin, MaxThickness: sidebarMax},
		{Kind: appkit.SplitContent, Controller: content},
		{Kind: appkit.SplitInspector, Controller: appkit.ViewController(s.Inspector.View), MinThickness: inspectorMin, MaxThickness: inspectorMax},
	})

	s.Window = appkit.NewWindow(AppName, appkit.MakeRect(0, 0, defaultWidth, defaultHeight))
	objc.Msg(s.Window, "setContentViewController:", s.Split)
	objc.Msg(s.Window, "setContentSize:", appkit.Size{Width: defaultWidth, Height: defaultHeight})
	objc.Msg(s.Window, "center")
	appkit.SetToolbar(s.Window, "BQUIMainToolbar", s.toolbarItems())

	// Give the editor ~55% of the content height on first launch; autosave
	// restores the user's divider afterwards.
	objc.Msg(objc.Msg(s.Window, "contentView"), "layoutSubtreeIfNeeded")
	innerSplit := objc.Msg(content, "splitView")
	h := objc.Send[appkit.Rect](innerSplit, "bounds").Size.Height
	objc.Msg(innerSplit, "setPosition:ofDividerAtIndex:", h*editorShare, int64(0))

	// Inspector starts wide enough for a schema (divider 1: content | inspector).
	outer := objc.Msg(s.Split, "splitView")
	w := objc.Send[appkit.Rect](outer, "bounds").Size.Width
	objc.Msg(outer, "setPosition:ofDividerAtIndex:", w-inspectorWidth, int64(1))

	if !opts.Deterministic {
		objc.Send[bool](s.Window, "setFrameAutosaveName:", objc.String(windowAutosaveID))
	}

	s.Explorer.Model.SetProjects(s.settings.PinnedProjects)
	s.rebuildProjectPicker()
	s.loadProjects()

	objc.Msg(s.Window, "makeKeyAndOrderFront:", objc.Nil)
	objc.Msg(s.Window, "makeFirstResponder:", s.Editor.Web.View)

	return s, nil
}

// lspOptions configures the embedded bqls. Live BigQuery metadata and dry
// runs use bqlang's ADC/gcloud credentials; with the fake backend bqls stays
// offline.
func (s *Shell) lspOptions() *bqconfig.LSPOptions {
	o := bqconfig.DefaultLSPOptions()
	o.DefaultProject = s.settings.BillingProject
	o.BigQueryAPIEnabled = s.opts.Live && o.DefaultProject != ""

	return o
}

// BillingProject returns the project jobs run in. Safe from any goroutine.
func (s *Shell) BillingProject() string { return *s.billing.Load() }

// SetBillingProject switches the billing project, pins it, retargets bqls
// and persists the choice.
func (s *Shell) SetBillingProject(project string) {
	if project == "" {
		return
	}

	s.settings.BillingProject = project
	s.billing.Store(&project)

	if s.settings.Pin(project) {
		s.Explorer.Model.SetProjects(s.settings.PinnedProjects)
	}

	s.Editor.SetDefaultProject(project, s.opts.Live)
	s.rebuildProjectPicker()
	s.saveSettings()
}

// UnpinProject removes a project from the explorer.
func (s *Shell) UnpinProject(project string) {
	if s.settings.Unpin(project) {
		s.Explorer.Model.SetProjects(s.settings.PinnedProjects)
		s.rebuildProjectPicker()
		s.saveSettings()
	}
}

func (s *Shell) saveSettings() {
	if s.opts.SettingsPath == "" {
		return
	}

	if err := s.settings.Save(s.opts.SettingsPath); err != nil {
		fmt.Fprintln(os.Stderr, "bqui:", err)
	}
}

// loadProjects lists all accessible projects in the background for the picker.
func (s *Shell) loadProjects() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), projectsTimeout)
		defer cancel()

		ps, err := s.opts.Backend.ListProjects(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "bqui: list projects:", err)

			return
		}

		ids := make([]string, 0, len(ps))
		for _, p := range ps {
			ids = append(ids, p.ID)
		}

		slices.Sort(ids)

		objc.Main(func() {
			s.all = ids
			s.rebuildProjectPicker()
		})
	}()
}

// rebuildProjectPicker lists pinned projects first, then every other
// accessible project; the selection is the billing project.
func (s *Shell) rebuildProjectPicker() {
	if s.projects == objc.Nil {
		return
	}

	objc.Msg(s.projects, "removeAllItems")
	menu := objc.Msg(s.projects, "menu")

	add := func(title string, enabled bool) {
		item := objc.Msg(menu, "addItemWithTitle:action:keyEquivalent:", objc.String(title), objc.SEL(0), objc.String(""))
		objc.Msg(item, "setEnabled:", enabled)
	}

	if s.settings.BillingProject == "" {
		add("Choose billing project…", true)
	}

	for _, p := range s.settings.PinnedProjects {
		add(p, true)
	}

	others := slices.DeleteFunc(slices.Clone(s.all), func(p string) bool { return slices.Contains(s.settings.PinnedProjects, p) })
	if len(others) > 0 {
		objc.Msg(menu, "addItem:", objc.Msg(objc.Cls("NSMenuItem"), "separatorItem"))
		add("Other projects", false)

		for _, p := range others {
			add(p, true)
		}
	}

	if s.settings.BillingProject != "" {
		objc.Msg(s.projects, "selectItemWithTitle:", objc.String(s.settings.BillingProject))
	}
}

func (s *Shell) onProjectPicked() {
	title := objc.GoString(objc.Msg(s.projects, "titleOfSelectedItem"))
	if title == "" || strings.HasSuffix(title, "…") {
		return
	}

	s.SetBillingProject(title)
}

func (s *Shell) menus() []appkit.Menu {
	cmd, shift, ctrl, opt := appkit.ModCommand, appkit.ModShift, appkit.ModControl, appkit.ModOption

	return []appkit.Menu{
		{Role: appkit.MenuApp, Title: AppName, Items: []appkit.MenuItem{
			{Title: "About " + AppName, Action: "orderFrontStandardAboutPanel:"},
			{},
			{Title: "Settings…", Key: ",", Run: func() { s.status("Settings arrive in M13.") }},
			{},
			{Title: "Hide " + AppName, Action: "hide:", Key: "h"},
			{Title: "Hide Others", Action: "hideOtherApplications:", Key: "h", Mods: cmd | opt},
			{Title: "Show All", Action: "unhideAllApplications:"},
			{},
			{Title: "Quit " + AppName, Action: "terminate:", Key: "q"},
		}},
		{Title: "File", Items: []appkit.MenuItem{
			{Title: "New Query Tab", Key: "t", Run: func() { s.status("Tabs arrive with sessions (M10).") }},
			{},
			{Title: "Close Window", Action: "performClose:", Key: "w"},
		}},
		{Title: "Edit", Items: []appkit.MenuItem{
			{Title: "Undo", Action: "undo:", Key: "z"},
			{Title: "Redo", Action: "redo:", Key: "z", Mods: cmd | shift},
			{},
			{Title: "Cut", Action: "cut:", Key: "x"},
			{Title: "Copy", Action: "copy:", Key: "c"},
			{Title: "Paste", Action: "paste:", Key: "v"},
			{Title: "Select All", Action: "selectAll:", Key: "a"},
			{},
			// Monaco handles these keys itself; the menu items cover mouse use.
			{Title: "Find…", Key: "f", Run: func() { s.Editor.Action("actions.find") }},
			{Title: "Find and Replace…", Key: "f", Mods: cmd | opt, Run: func() { s.Editor.Action("editor.action.startFindReplaceAction") }},
			{Title: "Find Next", Key: "g", Run: func() { s.Editor.Action("editor.action.nextMatchFindAction") }},
			{Title: "Find Previous", Key: "g", Mods: cmd | shift, Run: func() { s.Editor.Action("editor.action.previousMatchFindAction") }},
			{},
			{Title: "Command Palette…", Key: "p", Mods: cmd | shift, Run: func() { s.Editor.Action("editor.action.quickCommand") }},
		}},
		{Title: "View", Items: []appkit.MenuItem{
			{Title: "Toggle Sidebar", Action: "toggleSidebar:", Key: "s", Mods: cmd | ctrl},
			{Title: "Toggle Inspector", Action: "toggleInspector:", Key: "i", Mods: cmd | ctrl},
			{},
			{Title: "Enter Full Screen", Action: "toggleFullScreen:", Key: "f", Mods: cmd | ctrl},
		}},
		{Title: "Query", Items: []appkit.MenuItem{
			{Title: "Run", Key: "\r", Run: func() { s.Editor.RequestRun(false) }},
			{Title: "Run Statement at Cursor", Key: "\r", Mods: cmd | shift, Run: func() { s.Editor.RequestRun(true) }},
			{Title: "Stop", Key: ".", Run: s.Stop},
			{},
			{Title: "Format", Key: "f", Mods: cmd | shift, Run: func() { s.Editor.Format() }},
			{},
			s.exportMenu("Export Results", s.resultsExportSource),
		}},
		{Role: appkit.MenuWindows, Title: "Window", Items: []appkit.MenuItem{
			{Title: "Minimize", Action: "performMiniaturize:", Key: "m"},
			{Title: "Zoom", Action: "performZoom:"},
			{},
			{Title: "Bring All to Front", Action: "arrangeInFront:"},
		}},
		{Role: appkit.MenuHelp, Title: "Help", Items: []appkit.MenuItem{
			{Title: AppName + " Help", Run: func() { s.status("Help is not written yet.") }},
		}},
	}
}

func (s *Shell) toolbarItems() []*appkit.ToolbarItem {
	s.projects = objc.Msg(objc.Msg(objc.Cls("NSPopUpButton"), "alloc"), "initWithFrame:pullsDown:", appkit.MakeRect(0, 0, projectPickerW, 24), false)
	objc.Msg(s.projects, "setToolTip:", objc.String("Billing project for queries (also bqls' default project)"))
	objc.Msg(s.projects, "setAutoenablesItems:", false)
	objc.Msg(s.projects, "setTarget:", objc.NewTarget(func(objc.ID) { s.onProjectPicked() }))
	objc.Msg(s.projects, "setAction:", objc.Sel(objc.ActionSelector))

	cost := appkit.SecondaryLabel("—")
	objc.Msg(cost, "setToolTip:", objc.String("Estimated bytes processed (bqls dry run)"))
	s.Cost = cost

	return []*appkit.ToolbarItem{
		{Kind: appkit.ToolbarToggleSidebar},
		{Kind: appkit.ToolbarSidebarSeparator},
		{ID: "project", Kind: appkit.ToolbarCustomView, Label: "Project", View: s.projects},
		{Kind: appkit.ToolbarFlexibleSpace},
		{ID: "run", Kind: appkit.ToolbarButton, Label: "Run", Symbol: "play.fill", ToolTip: "Run (⌘↩)", Run: func() { s.Editor.RequestRun(false) }},
		{ID: "stop", Kind: appkit.ToolbarButton, Label: "Stop", Symbol: "stop.fill", ToolTip: "Stop (⌘.)", Run: s.Stop},
		{ID: "format", Kind: appkit.ToolbarButton, Label: "Format", Symbol: "text.alignleft", ToolTip: "Format SQL (⇧⌘F)", Run: func() { s.Editor.Format() }},
		{ID: "cost", Kind: appkit.ToolbarCustomView, Label: "Estimate", View: cost},
		{ID: "search", Kind: appkit.ToolbarSearch, Label: "Search"},
		{Kind: appkit.ToolbarInspectorSeparator},
		{Kind: appkit.ToolbarToggleInspector},
	}
}

// onRun receives the SQL the editor resolved (selection, statement at the
// cursor, or whole script) and runs it in the billing project.
func (s *Shell) onRun(req RunRequest) {
	s.RunSQL(req.SQL)
}

// firstPageRows is how many rows the first jobs.query page returns inline;
// larger results stream through the Storage Read API.
const firstPageRows = 5000

// RunSQL runs a query and shows its results.
func (s *Shell) RunSQL(sql string) {
	sql = strings.TrimSpace(sql)
	if sql == "" {
		s.status("Nothing to run.")

		return
	}

	if s.settings.BillingProject == "" {
		s.status("Choose a billing project in the toolbar first.")

		return
	}

	s.Results.Show(s.Runner.Start(bq.QueryRequest{
		SQL: sql, Project: s.settings.BillingProject, MaxInlineRows: firstPageRows,
	}))
}

// Preview shows the first rows of a table (Storage Read API, no query cost).
func (s *Shell) Preview(info app.ObjectInfo) {
	s.Results.Show(s.Runner.Preview(info.ObjectRef()))
}

// Bottom-pane tabs.
const (
	TabResults = iota
	TabJSON
	TabPlan
	TabGraph
	TabJob
	TabHistory
)

// bottomPane stacks the Results, Job information and History tabs under a
// segmented switcher.
func (s *Shell) bottomPane() objc.ID {
	s.tabs = appkit.NewSegmented([]string{"Results", "JSON", "Execution details", "Execution graph", "Job information", "History"}, s.SelectTab)
	s.tabViews = []objc.ID{s.Results.View, s.JSON.View, s.Plan.View, s.Graph.View, s.Job.View, s.History.View}

	v := appkit.NewView()
	appkit.UseAutoLayout(s.tabs.View)
	objc.Msg(v, "addSubview:", s.tabs.View)

	content := appkit.NewView()
	appkit.UseAutoLayout(content)
	objc.Msg(v, "addSubview:", content)

	for _, tv := range s.tabViews {
		appkit.AddFilling(content, tv, 0)
	}

	appkit.Activate(
		appkit.Equal(appkit.Anchor(s.tabs.View, "topAnchor"), appkit.Anchor(v, "topAnchor"), 6),
		appkit.Equal(appkit.Anchor(s.tabs.View, "leadingAnchor"), appkit.Anchor(v, "leadingAnchor"), 10),
		appkit.Equal(appkit.Anchor(content, "topAnchor"), appkit.Anchor(s.tabs.View, "bottomAnchor"), 2),
		appkit.Equal(appkit.Anchor(content, "leadingAnchor"), appkit.Anchor(v, "leadingAnchor"), 0),
		appkit.Equal(appkit.Anchor(content, "trailingAnchor"), appkit.Anchor(v, "trailingAnchor"), 0),
		appkit.Equal(appkit.Anchor(content, "bottomAnchor"), appkit.Anchor(v, "bottomAnchor"), 0),
	)

	s.SelectTab(TabResults)

	return v
}

// SelectTab switches the bottom pane.
func (s *Shell) SelectTab(i int) {
	s.tabs.Select(i)

	for j, tv := range s.tabViews {
		appkit.SetHidden(tv, j != i)
	}

	if i == TabHistory {
		s.History.Shown()
	}
}

// onRunUpdate routes runner updates to the panes.
func (s *Shell) onRunUpdate(r *app.Run) {
	s.Results.Update(r)

	if r != s.Results.Current() {
		return
	}

	snap := r.Snapshot()
	s.JSON.Update(snap)

	pl := plan.FromJob(snap.JobDetails)
	s.Plan.SetPlan(pl)
	s.Graph.SetPlan(pl)

	switch snap.State {
	case app.RunDone, app.RunFailed, app.RunCancelled:
		if snap.Job != nil {
			s.Job.ShowJob(*snap.Job)
		} else {
			s.Job.Clear("This run has no BigQuery job (previews read tables directly).")
		}

		s.History.RefreshIfShown()
	case app.RunRunning, app.RunFetching:
	}
}

// ShowStage shows a plan stage in the inspector.
func (s *Shell) ShowStage(pl *plan.Plan, id int64) {
	st, ok := pl.Stage(id)
	if !ok {
		return
	}

	s.Inspector.ShowSections(st.Name, "plan stage · "+strings.ToLower(st.Status), app.StageSections(pl, st))
}

// CancelJob asks BigQuery to cancel a job from the history.
func (s *Shell) CancelJob(ref bq.JobRef) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), projectsTimeout)
		defer cancel()

		err := s.opts.Jobs.CancelJob(ctx, ref)

		objc.Main(func() {
			if err != nil {
				s.status("Cancel failed: " + err.Error())
			}

			s.History.Reload()
		})
	}()
}

// Stop cancels a running export, else the current run.
func (s *Shell) Stop() {
	if s.exportCancel != nil {
		s.exportCancel()

		return
	}

	if r := s.Results.Current(); r != nil {
		r.Cancel()
	}
}

// Status returns the results-pane status text. Main thread only.
func (s *Shell) Status() string { return s.Results.StatusText() }

func (s *Shell) onCost(summary string) {
	if summary == "" {
		summary = "—"
	}

	appkit.SetString(s.Cost, summary)
}

func (s *Shell) status(msg string) { s.Results.Notice(msg) }

// ResolveBillingProject fills in the billing project from gcloud/env when
// none is saved yet (runs gcloud off the main thread).
func (s *Shell) ResolveBillingProject() {
	if s.settings.BillingProject != "" || !s.opts.Live {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), projectsTimeout)
		defer cancel()

		if p := bq.DefaultProject(ctx); p != "" {
			objc.Main(func() {
				if s.settings.BillingProject == "" {
					s.SetBillingProject(p)
				}
			})
		}
	}()
}

const sampleSQL = `-- Top words in Shakespeare's works
SELECT
  word,
  SUM(word_count) AS total
FROM ` + "`bigquery-public-data.samples.shakespeare`" + `
GROUP BY word
ORDER BY total DESC
LIMIT 100;
`
