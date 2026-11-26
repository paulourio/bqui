//go:build darwin

// Package ui builds bqui's AppKit interface on top of internal/appkit and the
// UI-agnostic models in internal/app.
package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/paulourio/bqlang/bqconfig"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/objc"
)

// AppName is the user-visible application name.
const AppName = "bqui"

const (
	defaultWidth     = 1280
	defaultHeight    = 800
	sidebarMin       = 220
	sidebarMax       = 420
	inspectorMin     = 240
	inspectorMax     = 400
	editorMin        = 120
	resultsMin       = 120
	resultsHolding   = 240 // results pane gives space before the editor
	editorHolding    = 260
	windowAutosaveID = "BQUIMainWindow"
	editorShare      = 0.55
)

// Options configures the shell.
type Options struct {
	// Deterministic disables frame/split autosave so screenshots are stable.
	Deterministic bool
	// Inspectable enables the Safari Web Inspector for the editor.
	Inspectable bool
}

// Shell is the main window and its panes.
type Shell struct {
	App     objc.ID
	Window  objc.ID
	Split   objc.ID // outer NSSplitViewController (sidebar | content | inspector)
	Sidebar *appkit.Outline
	Editor  *EditorPane
	Cost    objc.ID // toolbar cost estimate label
	Results objc.ID // results placeholder label
	Detail  objc.ID // inspector label

	tree *app.Tree
}

// NewShell builds the application menu, main window, toolbar and panes.
func NewShell(opts Options) (*Shell, error) {
	s := &Shell{App: appkit.SharedApplication(), tree: demoTree()}

	appkit.SetMainMenu(s.App, s.menus())

	s.Sidebar = appkit.NewOutline(treeSource{s.tree}, s.onSelect)

	ed, err := NewEditorPane(EditorOptions{
		LSP:         lspOptions(),
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

	s.Results = appkit.Label("Run a query with ⌘↩ to see results here.", 0)
	objc.Msg(s.Results, "setTextColor:", objc.Msg(objc.Cls("NSColor"), "secondaryLabelColor"))
	s.Detail = appkit.Label("No selection", 0)
	objc.Msg(s.Detail, "setTextColor:", objc.Msg(objc.Cls("NSColor"), "secondaryLabelColor"))

	autosave := func(name string) string {
		if opts.Deterministic {
			return ""
		}

		return name
	}

	resultsView := appkit.NewView()
	appkit.AddCentered(resultsView, s.Results)

	inspectorView := appkit.NewView()
	appkit.AddCentered(inspectorView, s.Detail)

	content := appkit.SplitViewController(false, autosave("BQUIEditorResults"), []appkit.SplitItem{
		{Controller: appkit.ViewController(s.Editor.Web.View), MinThickness: editorMin, HoldingWeight: editorHolding},
		{Controller: appkit.ViewController(resultsView), MinThickness: resultsMin, HoldingWeight: resultsHolding},
	})

	s.Split = appkit.SplitViewController(true, autosave("BQUIMainSplit"), []appkit.SplitItem{
		{Kind: appkit.SplitSidebar, Controller: appkit.ViewController(s.Sidebar.Scroll), MinThickness: sidebarMin, MaxThickness: sidebarMax},
		{Kind: appkit.SplitContent, Controller: content},
		{Kind: appkit.SplitInspector, Controller: appkit.ViewController(inspectorView), MinThickness: inspectorMin, MaxThickness: inspectorMax},
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

	if !opts.Deterministic {
		objc.Send[bool](s.Window, "setFrameAutosaveName:", objc.String(windowAutosaveID))
	}

	s.Sidebar.Reload(0)
	s.Sidebar.Expand(0, true)

	objc.Msg(s.Window, "makeKeyAndOrderFront:", objc.Nil)
	objc.Msg(s.Window, "makeFirstResponder:", s.Editor.Web.View)

	return s, nil
}

// lspOptions configures the embedded bqls. Live BigQuery metadata and dry
// runs use bqlang's ADC/gcloud credentials; without them bqls degrades to
// offline analysis.
func lspOptions() *bqconfig.LSPOptions {
	o := bqconfig.DefaultLSPOptions()
	o.DefaultProject = os.Getenv("GOOGLE_CLOUD_PROJECT")
	o.BigQueryAPIEnabled = o.DefaultProject != ""

	return o
}

// Tree exposes the explorer model (for tests and smoke checks).
func (s *Shell) Tree() *app.Tree { return s.tree }

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
			{Title: "Find…", Action: "performTextFinderAction:", Key: "f", Tag: textFinderShowFind},
			{Title: "Find and Replace…", Action: "performTextFinderAction:", Key: "f", Mods: cmd | opt, Tag: textFinderShowReplace},
			{Title: "Find Next", Action: "performTextFinderAction:", Key: "g", Tag: textFinderNext},
			{Title: "Find Previous", Action: "performTextFinderAction:", Key: "g", Mods: cmd | shift, Tag: textFinderPrevious},
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

// NSTextFinderAction tags.
const (
	textFinderShowFind    = 1
	textFinderNext        = 2
	textFinderPrevious    = 3
	textFinderShowReplace = 12
)

func (s *Shell) toolbarItems() []*appkit.ToolbarItem {
	projects := objc.Msg(objc.Msg(objc.Cls("NSPopUpButton"), "alloc"), "initWithFrame:pullsDown:", appkit.MakeRect(0, 0, 220, 24), false)
	objc.Msg(projects, "addItemsWithTitles:", objc.Array(objc.String("bigquery-public-data"), objc.String("my-sandbox-project")))
	objc.Msg(projects, "setToolTip:", objc.String("Billing project for queries"))

	cost := appkit.Label("—", 0)
	objc.Msg(cost, "setTextColor:", objc.Msg(objc.Cls("NSColor"), "secondaryLabelColor"))
	objc.Msg(cost, "setToolTip:", objc.String("Estimated bytes processed (bqls dry run)"))
	s.Cost = cost

	return []*appkit.ToolbarItem{
		{Kind: appkit.ToolbarToggleSidebar},
		{Kind: appkit.ToolbarSidebarSeparator},
		{ID: "project", Kind: appkit.ToolbarCustomView, Label: "Project", View: projects},
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

func (s *Shell) onSelect(id uint64) {
	n, ok := s.tree.Node(id)
	if !ok {
		appkit.SetString(s.Detail, "No selection")

		return
	}

	appkit.SetString(s.Detail, fmt.Sprintf("%s\n%s", strings.Join(s.tree.Path(id), "."), n.Kind))
}

// treeSource adapts app.Tree to appkit.OutlineSource.
type treeSource struct{ t *app.Tree }

func (ts treeSource) Children(parent uint64) []uint64 { return ts.t.Children(parent) }

func (ts treeSource) Expandable(item uint64) bool {
	n, ok := ts.t.Node(item)

	return ok && n.Kind.Container() && len(n.Children) > 0
}

func (ts treeSource) Cell(item uint64) appkit.OutlineCell {
	n, _ := ts.t.Node(item)

	return appkit.OutlineCell{Title: n.Title, Symbol: kindSymbol(n.Kind), ToolTip: n.Detail, Group: n.Kind == app.KindSection}
}

func kindSymbol(k app.NodeKind) string {
	switch k {
	case app.KindProject:
		return "shippingbox"
	case app.KindDataset:
		return "cylinder.split.1x2"
	case app.KindTable:
		return "tablecells"
	case app.KindView, app.KindMaterializedView:
		return "eye"
	case app.KindExternalTable:
		return "externaldrive.connected.to.line.below"
	case app.KindSnapshot:
		return "camera"
	case app.KindRoutine:
		return "function"
	case app.KindModel:
		return "brain"
	case app.KindRoot, app.KindSection, app.KindPlaceholder:
	}

	return ""
}

// demoTree is static placeholder content until the explorer is wired to
// BigQuery in M3.
func demoTree() *app.Tree {
	t := app.NewTree()
	sec := t.Add(app.RootID, app.KindSection, "Explorer", "")
	p := t.Add(sec, app.KindProject, "bigquery-public-data", "")
	samples := t.Add(p, app.KindDataset, "samples", "US")
	t.Add(samples, app.KindTable, "github_nested", "")
	t.Add(samples, app.KindTable, "shakespeare", "")
	t.Add(samples, app.KindTable, "wikipedia", "")
	ga := t.Add(p, app.KindDataset, "google_analytics_sample", "US")
	t.Add(ga, app.KindTable, "ga_sessions_20170801", "")
	t.Add(p, app.KindDataset, "usa_names", "US")

	saved := t.Add(app.RootID, app.KindSection, "Saved Queries", "")
	t.Add(saved, app.KindPlaceholder, "No saved queries yet", "")

	return t
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
