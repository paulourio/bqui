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
	"github.com/paulourio/bqui/internal/theme"
)

// AppName is the user-visible application name.
const AppName = "bqui"

const (
	defaultWidth                   = 1280
	defaultHeight                  = 800
	windowMinWidth                 = 900
	windowMinHeight                = 520
	sidebarMin                     = 172
	sidebarMax                     = 480
	contentMin                     = 420
	inspectorMin                   = 300
	inspectorMax                   = 720
	inspectorWidth                 = 440
	editorMin                      = 120
	resultsMin                     = 120
	resultsHolding                 = 240 // results pane gives space before the editor
	editorHolding                  = 260
	windowAutosaveID               = "BQUIMainWindow"
	editorShare                    = 0.55
	projectsTimeout                = 30 * time.Second
	projectPickerW                 = 80
	projectPickerMinW              = 64
	bottomTabFontSize              = 11.0
	bottomTabPadding               = 12.0
	bottomTabMinWidth              = 36.0
	bottomTabCompactPad            = 18.0
	bottomTabCompactMinW           = 32.0
	controlSizeSmall        uint64 = 1
	lineBreakTruncatingTail uint64 = 4
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
	// Workspace persists the editor text, saved queries and run history
	// (nil: an in-memory workspace).
	Workspace *app.Workspace
	// ThemesDir holds user themes (TOML).
	ThemesDir string
}

// Shell is the main window and its panes.
type Shell struct {
	App          objc.ID
	Window       objc.ID
	Split        objc.ID // outer NSSplitViewController (sidebar | content | inspector)
	Explorer     *ExplorerPane
	Inspector    *InspectorPane
	Editor       *EditorPane
	Cost         objc.ID // toolbar cost estimate label
	Results      *ResultsPane
	JSON         *JSONPane
	Plan         *PlanPane
	Graph        *GraphPane
	Job          *JobPane
	History      *HistoryPane
	Workbooks    *WorkbooksPane
	Runs         *RunsPane
	Runner       *app.Runner
	Workspace    *app.Workspace
	tabs         *appkit.Segmented
	tabScroll    objc.ID
	tabResizeObs objc.ID
	tabViews     []objc.ID
	runsCount    int
	tabsCompact  bool
	fittingTabs  bool
	sideTabs     *appkit.Segmented
	sideViews    []objc.ID
	recorded     map[*app.Run]bool
	runFiles     map[*app.Run]string  // file ID a run started from
	lastRuns     map[string]store.Run // query path → latest run (tooltips)

	Themes      []*theme.Theme
	Theme       *theme.Theme
	themeErrs   []error
	themeColors []objc.ID
	prefs       *prefsWindow
	readOnly    bool // editor read-only (job document)
	revealGen   int

	exportCancel func()
	lastExport   app.ExportProgress

	opts     Options
	settings *store.Settings
	billing  atomic.Pointer[string] // readable from any goroutine
	projects objc.ID                // toolbar project picker (NSPopUpButton)
	all      []string

	projectsLoading bool
	projectsErr     error
}

// NewShell builds the application menu, main window, toolbar and panes.
func NewShell(opts Options) (*Shell, error) {
	if opts.Settings == nil {
		opts.Settings = &store.Settings{PinnedProjects: []string{store.DefaultPinned}}
	}

	// The public datasets project is browsable but cannot bill jobs.
	if opts.Settings.BillingProject == store.DefaultPinned {
		opts.Settings.BillingProject = ""
	}

	s := &Shell{App: appkit.SharedApplication(), opts: opts, settings: opts.Settings, Workspace: opts.Workspace}
	s.billing.Store(&opts.Settings.BillingProject)

	if s.Workspace == nil {
		w, err := app.OpenWorkspace(app.WorkspaceOptions{InitialSQL: SampleSQL})
		if err != nil {
			return nil, err
		}

		s.Workspace = w
	}

	s.loadThemes()
	appkit.SetMainMenu(s.App, s.menus())

	ed, err := NewEditorPane(EditorOptions{
		LSP:         s.lspOptions(),
		Inspectable: opts.Inspectable,
		OnRun:       s.onRun,
		OnCost:      s.onCost,
		OnChange:    s.onEdited,
		ObjectAt:    s.objectAt,
		OnOpenObject: func(ref string) {
			s.RevealObject(ref)
		},
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
	s.Runs = newRunsPane(s)
	s.Runner = app.NewRunner(app.RunnerOptions{
		Backend:  opts.Queries,
		Metadata: opts.Backend,
		Jobs:     opts.Jobs,
		Storage:  opts.Storage,
		Post:     objc.Main,
		OnUpdate: s.onRunUpdate,
		SpillDir: opts.SpillDir,
	})
	s.Runner.SetMaxSpillBytes(s.settings.Prefs.MaxSpillBytes)

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
		{Kind: appkit.SplitSidebar, Controller: appkit.ViewController(s.sidebar()), MinThickness: sidebarMin, MaxThickness: sidebarMax},
		{Kind: appkit.SplitContent, Controller: content, MinThickness: contentMin},
		{Kind: appkit.SplitInspector, Controller: appkit.ViewController(s.Inspector.View), MinThickness: inspectorMin, MaxThickness: inspectorMax},
	})

	s.Window = appkit.NewWindow(AppName, appkit.MakeRect(0, 0, defaultWidth, defaultHeight))
	objc.Msg(s.Window, "setContentViewController:", s.Split)
	objc.Msg(s.Window, "setContentSize:", appkit.Size{Width: defaultWidth, Height: defaultHeight})
	appkit.SetContentMinSize(s.Window, windowMinWidth, windowMinHeight)
	objc.Msg(s.Window, "center")
	appkit.SetToolbar(s.Window, "BQUIMainToolbar", s.toolbarItems())
	s.restoreWorkspace()
	s.Runs.Reload()
	s.ApplyTheme(s.settings.Theme)
	s.rebuildMenus() // theme check marks
	s.watchAppearance()

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
	s.Explorer.Model.SetStarred(s.settings.Starred)

	if s.settings.StarredOnly {
		s.Explorer.SetStarredOnly(true)
	}
	s.LoadProjects()

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
	o.DefaultDataset = s.settings.Prefs.DefaultDataset

	if s.settings.Prefs.CostWarningUSD > 0 {
		o.CostWarningThresholdUSD = s.settings.Prefs.CostWarningUSD
	}

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
		s.Explorer.Model.SetStarred(s.settings.Starred)

		if s.settings.StarredOnly {
			s.Explorer.SetStarredOnly(true)
		}
	}

	s.Editor.ConfigureLSP(s.lspOptions())
	s.rebuildProjectPicker()

	if s.Workbooks != nil && s.sideTabs.Selected() == SidebarWorkbooks {
		s.Workbooks.ReloadJobs()
	}
	s.saveSettings()
}

// UnpinProject removes a project from the explorer.
func (s *Shell) UnpinProject(project string) {
	if s.settings.Unpin(project) {
		s.Explorer.Model.SetProjects(s.settings.PinnedProjects)
		s.Explorer.Model.SetStarred(s.settings.Starred)

		if s.settings.StarredOnly {
			s.Explorer.SetStarredOnly(true)
		}
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

// Project picker item tags (0 = a project).
const (
	pickEnterID = 1
	pickRefresh = 2
	pickNone    = 3
)

// LoadProjects lists the user's projects in the background for the picker.
// Failures are explained in the results pane and offered for retry; a
// project ID can always be typed in.
func (s *Shell) LoadProjects() {
	s.projectsLoading, s.projectsErr = true, nil
	s.rebuildProjectPicker()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), projectsTimeout)
		defer cancel()

		ps, err := s.opts.Backend.ListProjects(ctx)

		ids := make([]string, 0, len(ps))
		for _, p := range ps {
			ids = append(ids, p.ID)
		}

		slices.Sort(ids)

		objc.Main(func() {
			s.projectsLoading = false

			if err != nil {
				fmt.Fprintln(os.Stderr, "bqui: list projects:", err)
				s.projectsErr = err
				s.Results.ShowProblem(app.Explain(err))
			} else {
				s.all = ids
			}

			s.rebuildProjectPicker()
		})
	}()
}

// ProjectChoices are the billing projects offered: listed projects plus
// pinned ones the user chose, never the public datasets project.
func (s *Shell) ProjectChoices() []string {
	out := slices.Clone(s.all)

	for _, p := range s.settings.PinnedProjects {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}

	out = slices.DeleteFunc(out, func(p string) bool { return p == store.DefaultPinned })
	slices.Sort(out)

	return out
}

// rebuildProjectPicker lists the user's projects; the selection is the
// billing project.
func (s *Shell) rebuildProjectPicker() {
	if s.projects == objc.Nil {
		return
	}

	objc.Msg(s.projects, "removeAllItems")
	menu := objc.Msg(s.projects, "menu")

	add := func(title string, enabled bool, tag int64, tip string) {
		item := objc.Msg(menu, "addItemWithTitle:action:keyEquivalent:", objc.String(title), objc.SEL(0), objc.String(""))
		objc.Msg(item, "setEnabled:", enabled)
		objc.Msg(item, "setTag:", tag)

		if tip != "" {
			objc.Msg(item, "setToolTip:", objc.String(tip))
		}
	}

	if s.settings.BillingProject == "" {
		add("Choose billing project…", true, pickNone, "")
	}

	choices := s.ProjectChoices()
	for _, p := range choices {
		add(p, true, 0, "")
	}

	switch {
	case s.projectsLoading:
		add("Loading projects…", false, pickNone, "")
	case s.projectsErr != nil:
		pr := app.Explain(s.projectsErr)
		add("⚠︎ "+pr.Title, false, pickNone, pr.Detail)
	case len(choices) == 0:
		add("No projects found", false, pickNone, "")
	}

	objc.Msg(menu, "addItem:", objc.Msg(objc.Cls("NSMenuItem"), "separatorItem"))
	add("Enter Project ID…", true, pickEnterID, "Use any project you can run BigQuery jobs in")
	add("Refresh Project List", true, pickRefresh, "")

	if s.settings.BillingProject != "" {
		objc.Msg(s.projects, "selectItemWithTitle:", objc.String(s.settings.BillingProject))
	} else {
		objc.Msg(s.projects, "selectItemAtIndex:", int64(0))
	}
}

func (s *Shell) onProjectPicked() {
	item := objc.Msg(s.projects, "selectedItem")
	tag := objc.Send[int64](item, "tag")
	title := objc.GoString(objc.Msg(item, "title"))

	switch tag {
	case 0:
		s.SetBillingProject(title)
	case pickEnterID:
		s.rebuildProjectPicker() // restore the selection while the prompt is up

		if id, ok := appkit.Prompt("Billing Project", "Enter the ID of a project you can run BigQuery jobs in (for example my-project-123).", s.settings.BillingProject); ok {
			if id = strings.TrimSpace(id); id != "" {
				s.SetBillingProject(id)
			}
		}
	case pickRefresh:
		s.LoadProjects()
	default:
		s.rebuildProjectPicker()
	}
}

func (s *Shell) menus() []appkit.Menu {
	cmd, shift, ctrl, opt := appkit.ModCommand, appkit.ModShift, appkit.ModControl, appkit.ModOption

	return []appkit.Menu{
		{Role: appkit.MenuApp, Title: AppName, Items: []appkit.MenuItem{
			{Title: "About " + AppName, Action: "orderFrontStandardAboutPanel:"},
			{},
			{Title: "Settings…", Key: ",", Run: s.ShowPreferences},
			{},
			{Title: "Hide " + AppName, Action: "hide:", Key: "h"},
			{Title: "Hide Others", Action: "hideOtherApplications:", Key: "h", Mods: cmd | opt},
			{Title: "Show All", Action: "unhideAllApplications:"},
			{},
			{Title: "Quit " + AppName, Action: "terminate:", Key: "q"},
		}},
		{Title: "File", Items: []appkit.MenuItem{
			{Title: "New Query", Key: "n", Run: s.NewQuery},
			{Title: "Open…", Key: "o", Run: s.ChooseQuery},
			{},
			{Title: "Save", Key: "s", Run: func() { s.SaveQuery() }},
			{Title: "Save As…", Key: "s", Mods: cmd | shift, Run: func() { s.SaveQueryAs() }},
			{Title: "Save Job SQL to Workbook", Run: func() { s.SaveJobDocToWorkbook() }},
			{Title: "New Workbook", Key: "n", Mods: cmd | shift, Run: func() { s.NewWorkbook() }},
			{Title: "Show Workbooks", Key: "1", Mods: cmd | opt, Run: func() { s.SelectSidebar(SidebarWorkbooks) }},
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
			{Title: "Run History", Key: "y", Run: s.RunHistory},
			{},
			s.themeMenu(),
			{},
			{Title: "Enter Full Screen", Action: "toggleFullScreen:", Key: "f", Mods: cmd | ctrl},
		}},
		{Title: "Query", Items: []appkit.MenuItem{
			{Title: "Run", Key: "\r", Run: func() { s.Editor.RequestRun(false) }},
			{Title: "Run Statement at Cursor", Key: "\r", Mods: cmd | shift, Run: func() { s.Editor.RequestRun(true) }},
			{Title: "Stop", Key: ".", Run: s.Stop},
			{},
			{Title: "Format", Key: "f", Mods: cmd | shift, Run: func() { s.Editor.Format() }},
			{Title: "Go to Definition", Run: func() { s.Editor.GoToDefinition() }},
			{},
			{Title: "BigQuery Session Mode", Run: s.ToggleSessionMode, Checked: s.sessionOn},
			{Title: "End Session", Run: s.EndSession},
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
	appkit.UseAutoLayout(s.projects)
	appkit.SetCompressionResistance(s.projects, appkit.OrientHorizontal, appkit.PriorityDefaultLow)
	appkit.SetHuggingPriority(s.projects, appkit.OrientHorizontal, appkit.PriorityDefaultLow)
	appkit.Activate(
		appkit.GreaterOrEqualConst(appkit.Anchor(s.projects, "widthAnchor"), projectPickerMinW),
		appkit.LessOrEqualConst(appkit.Anchor(s.projects, "widthAnchor"), projectPickerW),
	)

	if cell := objc.Msg(s.projects, "cell"); cell != objc.Nil {
		objc.Msg(cell, "setLineBreakMode:", lineBreakTruncatingTail)
	}

	objc.Msg(s.projects, "setToolTip:", objc.String("Billing project for queries (also bqls' default project)"))
	objc.Msg(s.projects, "setAutoenablesItems:", false)
	objc.Msg(s.projects, "setTarget:", objc.NewTarget(func(objc.ID) { s.onProjectPicked() }))
	objc.Msg(s.projects, "setAction:", objc.Sel(objc.ActionSelector))

	cost := appkit.SecondaryLabel("—")
	appkit.SetCompressionResistance(cost, appkit.OrientHorizontal, appkit.PriorityDefaultLow)
	objc.Msg(cost, "setToolTip:", objc.String("Estimated bytes processed (bqls dry run)"))
	s.Cost = cost

	return []*appkit.ToolbarItem{
		{Kind: appkit.ToolbarToggleSidebar},
		{Kind: appkit.ToolbarSidebarSeparator},
		{ID: "project", Kind: appkit.ToolbarCustomView, Label: "Project", View: s.projects, VisibilityPriority: appkit.ToolbarVisibilityHigh},
		{Kind: appkit.ToolbarFlexibleSpace},
		{ID: "run", Kind: appkit.ToolbarButton, Label: "Run", Symbol: "play.fill", ToolTip: "Run (⌘↩)", VisibilityPriority: appkit.ToolbarVisibilityHigh, Run: func() { s.Editor.RequestRun(false) }},
		{ID: "stop", Kind: appkit.ToolbarButton, Label: "Stop", Symbol: "stop.fill", ToolTip: "Stop (⌘.)", VisibilityPriority: appkit.ToolbarVisibilityHigh, Run: s.Stop},
		{ID: "format", Kind: appkit.ToolbarButton, Label: "Format", Symbol: "text.alignleft", ToolTip: "Format SQL (⇧⌘F)", VisibilityPriority: appkit.ToolbarVisibilityHigh, Run: func() { s.Editor.Format() }},
		{ID: "cost", Kind: appkit.ToolbarCustomView, Label: "Estimate", View: cost, VisibilityPriority: appkit.ToolbarVisibilityLow},
		{Kind: appkit.ToolbarInspectorSeparator},
		{Kind: appkit.ToolbarToggleInspector},
	}
}

// onRun receives the SQL the editor resolved (selection, statement at the
// cursor, or whole script) and runs it in the billing project.
func (s *Shell) onRun(req RunRequest) {
	s.RunSQL(req.SQL)
}

// RunHistory shows this Mac's run history in the History tab.
func (s *Shell) RunHistory() {
	s.History.scope.Select(historyScopeLocal)
	s.SelectTab(TabHistory)
	s.History.Reload()
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

	// Always create a job (like the console): without one BigQuery returns
	// no query plan, and the run has no job ID for history.
	req := bq.QueryRequest{SQL: sql, Project: s.settings.BillingProject, MaxInlineRows: firstPageRows, RequireJob: true}
	app.ApplyPreferences(s.settings.Prefs, &req)
	s.Workspace.Prepare(&req)

	run := s.Runner.Start(req)
	if id := s.Workspace.FileID(); id != "" {
		if s.runFiles == nil {
			s.runFiles = map[*app.Run]string{}
		}

		s.runFiles[run] = id
	}

	s.showRun(run)
}

// Preview shows the first rows of a table (Storage Read API, no query cost).
func (s *Shell) Preview(info app.ObjectInfo) {
	s.showRun(s.Runner.Preview(info.ObjectRef()))
}

func (s *Shell) showRun(r *app.Run) {
	s.Results.Show(r)
	s.JSON.Update(r.Snapshot())
}

// ShowRun displays r in both the Results and JSON panes (smoke checks).
func (s *Shell) ShowRun(r *app.Run) { s.showRun(r) }

// Tabs returns the bottom tab segmented control (smoke checks).
func (s *Shell) Tabs() *appkit.Segmented { return s.tabs }

// SideTabs returns the sidebar segmented control (smoke checks).
func (s *Shell) SideTabs() *appkit.Segmented { return s.sideTabs }

// Bottom-pane tabs.
const (
	TabResults = iota
	TabRuns
	TabJSON
	TabPlan
	TabGraph
	TabJob
	TabHistory
)

// bottomPane stacks the Results, Job information and History tabs under a
// segmented switcher.
func (s *Shell) bottomPane() objc.ID {
	s.tabs = appkit.NewSegmented([]string{"Results", "Runs", "JSON", "Execution details", "Execution graph", "Job information", "History"}, s.SelectTab)
	objc.Msg(s.tabs.View, "setControlSize:", controlSizeSmall)
	s.tabs.SetFont(bottomTabFontSize)
	s.tabs.FitWidths(bottomTabPadding, bottomTabMinWidth)
	objc.Msg(s.tabs.View, "setAutoresizingMask:", uint64(0))
	s.tabViews = []objc.ID{s.Results.View, s.Runs.View, s.JSON.View, s.Plan.View, s.Graph.View, s.Job.View, s.History.View}

	tabScroll := objc.New("NSScrollView")
	objc.Msg(tabScroll, "setBorderType:", uint64(0))
	objc.Msg(tabScroll, "setDrawsBackground:", false)
	objc.Msg(tabScroll, "setHasVerticalScroller:", false)
	objc.Msg(tabScroll, "setHasHorizontalScroller:", false)
	objc.Msg(tabScroll, "setDocumentView:", s.tabs.View)
	s.tabScroll = tabScroll

	objc.Msg(tabScroll, "setPostsFrameChangedNotifications:", true)
	s.tabResizeObs = objc.NewTarget(func(objc.ID) { s.fitBottomTabs(false) })
	objc.Msg(objc.Msg(objc.Cls("NSNotificationCenter"), "defaultCenter"),
		"addObserver:selector:name:object:",
		s.tabResizeObs, objc.Sel(objc.ActionSelector), objc.String("NSViewFrameDidChangeNotification"), tabScroll)

	tabHeight := appkit.ViewBounds(s.tabs.View).Size.Height
	if tabHeight <= 0 {
		tabHeight = objc.Send[appkit.Size](s.tabs.View, "fittingSize").Height
	}

	v := appkit.NewView()
	appkit.UseAutoLayout(tabScroll)
	objc.Msg(v, "addSubview:", tabScroll)

	content := appkit.NewView()
	appkit.UseAutoLayout(content)
	objc.Msg(v, "addSubview:", content)

	for _, tv := range s.tabViews {
		appkit.AddFilling(content, tv, 0)
	}

	appkit.Activate(
		appkit.Equal(appkit.Anchor(tabScroll, "topAnchor"), appkit.Anchor(v, "topAnchor"), 6),
		appkit.Equal(appkit.Anchor(tabScroll, "leadingAnchor"), appkit.Anchor(v, "leadingAnchor"), 10),
		appkit.Equal(appkit.Anchor(tabScroll, "trailingAnchor"), appkit.Anchor(v, "trailingAnchor"), -10),
		appkit.EqualConst(appkit.Anchor(tabScroll, "heightAnchor"), tabHeight),
		appkit.Equal(appkit.Anchor(content, "topAnchor"), appkit.Anchor(tabScroll, "bottomAnchor"), 2),
		appkit.Equal(appkit.Anchor(content, "leadingAnchor"), appkit.Anchor(v, "leadingAnchor"), 0),
		appkit.Equal(appkit.Anchor(content, "trailingAnchor"), appkit.Anchor(v, "trailingAnchor"), 0),
		appkit.Equal(appkit.Anchor(content, "bottomAnchor"), appkit.Anchor(v, "bottomAnchor"), 0),
	)

	s.SelectTab(TabResults)

	return v
}

func (s *Shell) fitBottomTabs(force bool) {
	if s.tabs == nil || s.tabs.View == objc.Nil || s.fittingTabs {
		return
	}

	s.fittingTabs = true
	defer func() { s.fittingTabs = false }()

	runsTitle := "Runs"
	if s.runsCount > 0 {
		runsTitle += " (" + app.CountInt(int64(s.runsCount)) + ")"
	}

	full := []string{"Results", runsTitle, "JSON", "Execution details", "Execution graph", "Job information", "History"}
	compactLabels := []string{"Results", runsTitle, "JSON", "Plan", "Graph", "Job", "History"}

	var fullSum float64
	for _, w := range s.tabs.MeasureWidths(full, bottomTabPadding, bottomTabMinWidth) {
		fullSum += w
	}

	clipW := appkit.ViewBounds(objc.Msg(s.tabs.View, "superview")).Size.Width
	compact := clipW > 0 && fullSum > clipW+0.5

	if !force && compact == s.tabsCompact {
		return
	}

	s.tabsCompact = compact
	labels, pad, minW := full, bottomTabPadding, bottomTabMinWidth
	if compact {
		labels, pad, minW = compactLabels, bottomTabCompactPad, bottomTabCompactMinW
	}

	for i, lbl := range labels {
		s.tabs.SetSegmentLabel(i, lbl)
	}

	s.tabs.FitWidths(pad, minW)
}

// SelectTab switches the bottom pane.
func (s *Shell) SelectTab(i int) {
	s.tabs.Select(i)
	s.scrollTabToVisible(i)

	for j, tv := range s.tabViews {
		appkit.SetHidden(tv, j != i)
	}

	if i == TabHistory {
		s.History.Shown()
	}
}

func (s *Shell) scrollTabToVisible(i int) {
	if s.tabs == nil || s.tabs.View == objc.Nil {
		return
	}

	n := s.tabs.SegmentCount()
	if i < 0 || i >= n {
		return
	}

	b := appkit.ViewBounds(s.tabs.View)
	if b.Size.Width <= 0 || b.Size.Height <= 0 {
		return
	}

	var x float64
	for j := range i {
		x += s.tabs.SegmentWidth(j)
	}

	w := s.tabs.SegmentWidth(i)
	if w <= 0 {
		return
	}

	if i == n-1 && b.Size.Width-x > w {
		w = b.Size.Width - x
	}

	objc.Send[bool](s.tabs.View, "scrollRectToVisible:", appkit.MakeRect(b.Origin.X+x, b.Origin.Y, w, b.Size.Height))
}

// onRunUpdate routes runner updates to the panes.
func (s *Shell) onRunUpdate(r *app.Run) {
	s.Results.Update(r)
	s.Inspector.PreviewPane().Update(r)

	snap := r.Snapshot()

	if snap.State == app.RunDone || snap.State == app.RunFailed || snap.State == app.RunCancelled {
		s.recordFinished(r, snap)
	}

	if r != s.Results.Current() {
		return
	}

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
	objc.Msg(s.Cost, "sizeToFit")
	objc.Msg(s.Cost, "invalidateIntrinsicContentSize")
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

// SampleSQL fills a brand-new workspace.
const SampleSQL = `-- Top words in Shakespeare's works
SELECT
  word,
  SUM(word_count) AS total
FROM ` + "`bigquery-public-data.samples.shakespeare`" + `
GROUP BY word
ORDER BY total DESC
LIMIT 100;
`
