//go:build darwin

package ui

import (
	"net/url"
	"time"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/objc"
)

const sidebarInset = 8.0

// ExplorerPane is the sidebar: a filter field over the lazy dataset tree.
type ExplorerPane struct {
	Model   *app.Explorer
	Outline *appkit.Outline
	View    objc.ID

	s      *Shell
	filter bool
	star   objc.ID // starred-only toggle button

	// LastReload / MaxReload measure main-thread time spent reloading the
	// outline after a load (smoke checks keep this bounded).
	LastReload time.Duration
	MaxReload  time.Duration
}

func newExplorerPane(s *Shell, be app.Backend) *ExplorerPane {
	p := &ExplorerPane{s: s}
	p.Model = app.NewExplorer(be, objc.Main, p.changed)

	p.Outline = appkit.NewOutline(explorerSource{p}, appkit.OutlineOptions{
		OnSelect: p.onSelect,
		// Expanding reloads the outline; never do that inside its own
		// delegate callback (AppKit warns about reentrancy), so defer it.
		OnExpand:      func(id uint64) { objc.Main(func() { p.Model.Expand(id) }) },
		OnDoubleClick: p.onDoubleClick,
		Menu:          p.menu,
	})

	search := appkit.NewSearchField("Filter", p.SetFilter)

	p.star = objc.Msg(objc.Cls("NSButton"), "buttonWithImage:target:action:",
		appkit.SymbolImage("star", "Starred only"), objc.NewTarget(func(objc.ID) { p.SetStarredOnly(!p.Model.StarredOnly()) }),
		objc.Sel(objc.ActionSelector))
	objc.Msg(p.star, "setBordered:", false)
	objc.Msg(p.star, "setToolTip:", objc.String("Show only starred datasets"))

	p.View = appkit.NewView()
	appkit.UseAutoLayout(search)
	appkit.UseAutoLayout(p.star)
	appkit.UseAutoLayout(p.Outline.Scroll)
	objc.Msg(p.View, "addSubview:", search)
	objc.Msg(p.View, "addSubview:", p.star)
	objc.Msg(p.View, "addSubview:", p.Outline.Scroll)

	safeTop := objc.Msg(objc.Msg(p.View, "safeAreaLayoutGuide"), "topAnchor")
	appkit.Activate(
		appkit.Equal(appkit.Anchor(search, "topAnchor"), safeTop, sidebarInset),
		appkit.Equal(appkit.Anchor(search, "leadingAnchor"), appkit.Anchor(p.View, "leadingAnchor"), sidebarInset),
		appkit.Equal(appkit.Anchor(p.star, "trailingAnchor"), appkit.Anchor(p.View, "trailingAnchor"), -sidebarInset),
		appkit.Equal(appkit.Anchor(p.star, "centerYAnchor"), appkit.Anchor(search, "centerYAnchor"), 0),
		appkit.Equal(appkit.Anchor(search, "trailingAnchor"), appkit.Anchor(p.star, "leadingAnchor"), -sidebarInset/2),
		appkit.Equal(appkit.Anchor(p.Outline.Scroll, "topAnchor"), appkit.Anchor(search, "bottomAnchor"), sidebarInset/2),
		appkit.Equal(appkit.Anchor(p.Outline.Scroll, "leadingAnchor"), appkit.Anchor(p.View, "leadingAnchor"), 0),
		appkit.Equal(appkit.Anchor(p.Outline.Scroll, "trailingAnchor"), appkit.Anchor(p.View, "trailingAnchor"), 0),
		appkit.Equal(appkit.Anchor(p.Outline.Scroll, "bottomAnchor"), appkit.Anchor(p.View, "bottomAnchor"), 0),
	)

	return p
}

// ToggleStar stars or unstars a dataset and saves the choice.
func (p *ExplorerPane) ToggleStar(info app.ObjectInfo) {
	p.s.settings.ToggleStar(info.Project, info.Dataset)
	p.s.saveSettings()
	p.Model.SetStarred(p.s.settings.Starred)
}

// SetStarredOnly shows only starred datasets (saved across launches) and
// loads the projects that hold them.
func (p *ExplorerPane) SetStarredOnly(on bool) {
	p.s.settings.StarredOnly = on
	p.s.saveSettings()
	p.Model.SetStarredOnly(on)

	symbol := "star"
	if on {
		symbol = "star.fill"
		objc.Msg(p.star, "setContentTintColor:", appkit.SystemColor("systemYellowColor"))

		for _, id := range p.Model.StarredProjects() {
			p.Model.Expand(id)
			p.Outline.Expand(id, false)
		}
	} else {
		objc.Msg(p.star, "setContentTintColor:", objc.Nil)
	}

	objc.Msg(p.star, "setImage:", appkit.SymbolImage(symbol, "Starred only"))
}

// SetFilter filters the tree (the search field calls this as the user types).
func (p *ExplorerPane) SetFilter(q string) {
	p.filter = q != ""
	p.Model.SetFilter(q)
}

// changed runs on the main thread after the model changed under id.
func (p *ExplorerPane) changed(id uint64) {
	start := time.Now()

	p.Outline.Reload(id)

	if p.filter {
		p.expandLoaded(app.RootID)
	}

	p.LastReload = time.Since(start)
	p.MaxReload = max(p.MaxReload, p.LastReload)
}

// expandLoaded expands every already-loaded container so filter matches are
// visible, without triggering new loads.
func (p *ExplorerPane) expandLoaded(id uint64) {
	for _, k := range p.Model.Children(id) {
		if p.Model.Loaded(k) {
			p.Outline.Expand(k, false)
			p.expandLoaded(k)
		}
	}
}

func (p *ExplorerPane) onSelect(id uint64) {
	info, ok := p.Model.Info(id)
	if !ok {
		return
	}

	p.s.Inspector.Show(info)
}

func (p *ExplorerPane) onDoubleClick(id uint64) {
	info, ok := p.Model.Info(id)
	if !ok {
		return
	}

	switch info.Kind {
	case app.KindProject, app.KindDataset:
		if p.Outline.IsExpanded(id) {
			objc.Msg(p.Outline.View, "collapseItem:", objc.Number(id))
		} else {
			p.Outline.Expand(id, false)
		}
	case app.KindTable, app.KindView, app.KindMaterializedView, app.KindExternalTable,
		app.KindSnapshot, app.KindShardGroup, app.KindRoutine:
		p.s.Editor.InsertText(info.SQLName())
	case app.KindModel, app.KindRoot, app.KindSection, app.KindPlaceholder:
	}
}

func (p *ExplorerPane) menu(id uint64) []appkit.MenuItem {
	info, ok := p.Model.Info(id)
	if !ok {
		return nil
	}

	copyID := appkit.MenuItem{Title: "Copy ID", Run: func() { appkit.CopyToPasteboard(info.FullID()) }}
	copySQL := appkit.MenuItem{Title: "Copy SQL Reference", Run: func() { appkit.CopyToPasteboard(info.SQLName()) }}
	console := appkit.MenuItem{Title: "Open in BigQuery Console", Run: func() { appkit.OpenURL(consoleURL(info)) }}
	refresh := appkit.MenuItem{Title: "Refresh", Run: func() { p.Model.Refresh(id); p.Outline.Expand(id, false) }}

	switch info.Kind {
	case app.KindProject:
		return []appkit.MenuItem{
			refresh,
			{},
			{Title: "Use as Billing Project", Run: func() { p.s.SetBillingProject(info.Project) }},
			{Title: "Unpin Project", Run: func() { p.s.UnpinProject(info.Project) }},
			{},
			copyID, console,
		}
	case app.KindDataset:
		star := "Add to Starred"
		if p.Model.IsStarred(id) {
			star = "Remove from Starred"
		}

		return []appkit.MenuItem{
			{Title: star, Run: func() { p.ToggleStar(info) }},
			{},
			refresh,
			{},
			copyID, copySQL, console,
		}
	case app.KindTable, app.KindView, app.KindMaterializedView, app.KindExternalTable,
		app.KindSnapshot, app.KindShardGroup:
		items := []appkit.MenuItem{
			{Title: "Query", Run: func() { p.s.Editor.AppendText(selectStar(info)) }},
			{Title: "Insert Reference", Run: func() { p.s.Editor.InsertText(info.SQLName()) }},
		}

		// Storage Read API previews cover native tables, snapshots and shards.
		if info.Kind == app.KindTable || info.Kind == app.KindSnapshot || info.Kind == app.KindShardGroup {
			items = append(items, appkit.MenuItem{Title: "Preview", Run: func() { p.s.Preview(info) }})

			if p.s.opts.Storage != nil {
				items = append(items, p.s.exportMenu("Export Table", tableExportSource(info)))
			}
		}

		return append(items, appkit.MenuItem{}, copyID, copySQL, console)
	case app.KindRoutine:
		return []appkit.MenuItem{
			{Title: "Insert Reference", Run: func() { p.s.Editor.InsertText(info.SQLName()) }},
			{},
			copyID, copySQL, console,
		}
	case app.KindModel:
		return []appkit.MenuItem{copyID, copySQL, console}
	case app.KindRoot, app.KindSection, app.KindPlaceholder:
	}

	return nil
}

// selectStar is the console's "Query" template; SELECT * is what the
// console generates too, and the user edits it before running.
func selectStar(info app.ObjectInfo) string {
	sql := "SELECT *\nFROM " + info.SQLName() //nolint:unqueryvet // template for the user to edit
	if info.Kind == app.KindShardGroup && len(info.Shards) > 0 {
		newest := info.Shards[0][len(info.Name):]
		sql += "\nWHERE _TABLE_SUFFIX = '" + newest + "'"
	}

	return sql + "\nLIMIT 1000;"
}

// consoleURL links to the object in the BigQuery console.
func consoleURL(info app.ObjectInfo) string {
	q := url.Values{"project": {info.Project}, "p": {info.Project}}

	switch info.Kind {
	case app.KindProject:
		q.Set("page", "project")
	case app.KindDataset:
		q.Set("d", info.Dataset)
		q.Set("page", "dataset")
	case app.KindRoutine:
		q.Set("d", info.Dataset)
		q.Set("r", info.Name)
		q.Set("page", "routine")
	case app.KindModel:
		q.Set("d", info.Dataset)
		q.Set("m", info.Name)
		q.Set("page", "model")
	default:
		q.Set("d", info.Dataset)
		q.Set("t", info.ObjectRef().Table)
		q.Set("page", "table")
	}

	return "https://console.cloud.google.com/bigquery?" + q.Encode()
}

// explorerSource adapts app.Explorer to appkit.OutlineSource.
type explorerSource struct{ p *ExplorerPane }

func (s explorerSource) Children(parent uint64) []uint64 { return s.p.Model.Children(parent) }

func (s explorerSource) Expandable(item uint64) bool { return s.p.Model.Expandable(item) }

func (s explorerSource) Cell(item uint64) appkit.OutlineCell {
	n, _ := s.p.Model.Tree().Node(item)

	symbol := kindSymbol(n.Kind)
	if s.p.Model.IsStarred(item) {
		symbol = "star.fill"
	}

	return appkit.OutlineCell{
		Title:     n.Title,
		Symbol:    symbol,
		ToolTip:   n.Detail,
		Secondary: n.Kind == app.KindPlaceholder,
	}
}

func kindSymbol(k app.NodeKind) string {
	switch k {
	case app.KindProject:
		return "shippingbox"
	case app.KindDataset:
		return "cylinder.split.1x2"
	case app.KindTable:
		return "tablecells"
	case app.KindShardGroup:
		return "square.stack.3d.up"
	case app.KindView:
		return "eye"
	case app.KindMaterializedView:
		return "eye.square"
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
