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
	"time"

	"github.com/paulourio/bqlang/bqconfig"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/objc"
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
	Results   objc.ID // results placeholder label

	opts     Options
	settings *store.Settings
	projects objc.ID // toolbar project picker (NSPopUpButton)
	all      []string
}

// NewShell builds the application menu, main window, toolbar and panes.
func NewShell(opts Options) (*Shell, error) {
	if opts.Settings == nil {
		opts.Settings = &store.Settings{PinnedProjects: []string{store.DefaultPinned}}
	}

	s := &Shell{App: appkit.SharedApplication(), opts: opts, settings: opts.Settings}

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
	s.Explorer = newExplorerPane(s, opts.Backend)

	s.Results = appkit.SecondaryLabel("Run a query with ⌘↩ to see results here.")

	autosave := func(name string) string {
		if opts.Deterministic {
			return ""
		}

		return name
	}

	resultsView := appkit.NewView()
	appkit.AddCentered(resultsView, s.Results)

	content := appkit.SplitViewController(false, autosave("BQUIEditorResults"), []appkit.SplitItem{
		{Controller: appkit.ViewController(s.Editor.Web.View), MinThickness: editorMin, HoldingWeight: editorHolding},
		{Controller: appkit.ViewController(resultsView), MinThickness: resultsMin, HoldingWeight: resultsHolding},
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

// BillingProject returns the project jobs run in.
func (s *Shell) BillingProject() string { return s.settings.BillingProject }

// SetBillingProject switches the billing project, pins it, retargets bqls
// and persists the choice.
func (s *Shell) SetBillingProject(project string) {
	if project == "" {
		return
	}

	s.settings.BillingProject = project
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
			{Title: "Stop", Key: ".", Run: func() { s.status("Nothing is running.") }},
			{},
			{Title: "Format", Key: "f", Mods: cmd | shift, Run: func() { s.Editor.Format() }},
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
		{ID: "stop", Kind: appkit.ToolbarButton, Label: "Stop", Symbol: "stop.fill", ToolTip: "Stop (⌘.)", Run: func() { s.status("Nothing is running.") }},
		{ID: "format", Kind: appkit.ToolbarButton, Label: "Format", Symbol: "text.alignleft", ToolTip: "Format SQL (⇧⌘F)", Run: func() { s.Editor.Format() }},
		{ID: "cost", Kind: appkit.ToolbarCustomView, Label: "Estimate", View: cost},
		{ID: "search", Kind: appkit.ToolbarSearch, Label: "Search"},
		{Kind: appkit.ToolbarInspectorSeparator},
		{Kind: appkit.ToolbarToggleInspector},
	}
}

// onRun receives the SQL the editor resolved (selection, statement at the
// cursor, or whole script). Execution and the results grid arrive in M5.
func (s *Shell) onRun(req RunRequest) {
	sql := strings.TrimSpace(req.SQL)
	if sql == "" {
		s.status("Nothing to run.")

		return
	}

	what := "script"

	switch {
	case req.Selection != "":
		what = "selection"
	case req.Kind != "":
		what = req.Kind + " statement"
	}

	first, _, _ := strings.Cut(sql, "\n")
	s.status(fmt.Sprintf("Would run %s (%d chars): %s", what, len(sql), first))
}

// Status returns the results-pane status text. Main thread only.
func (s *Shell) Status() string {
	return objc.GoString(objc.Msg(s.Results, "stringValue"))
}

func (s *Shell) onCost(summary string) {
	if summary == "" {
		summary = "—"
	}

	appkit.SetString(s.Cost, summary)
}

func (s *Shell) status(msg string) {
	appkit.SetString(s.Results, msg)
}

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
