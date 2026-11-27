//go:build darwin

package ui

import (
	"path/filepath"
	"strings"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/dataform"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/store"
)

// Saved pane outline item IDs: two section headers, then local queries
// (1..n) and Studio queries (studioBase+1..).
const (
	localGroupID  = uint64(1) << 62
	studioGroupID = localGroupID + 1
	localEmptyID  = localGroupID + 2
	studioEmptyID = localGroupID + 3
	studioBase    = uint64(1) << 40
)

// SavedPane lists local saved queries (.sql files) and BigQuery Studio
// saved queries.
type SavedPane struct {
	View    objc.ID
	Outline *appkit.Outline

	s       *Shell
	all     []store.SavedQuery
	shown   []store.SavedQuery
	filter  string
	loadErr error

	studio        []dataform.SavedQuery
	studioShown   []dataform.SavedQuery
	studioErr     error
	studioLoading bool
	studioGen     int
}

func newSavedPane(s *Shell) *SavedPane {
	p := &SavedPane{s: s}

	p.Outline = appkit.NewOutline(savedSource{p}, appkit.OutlineOptions{
		OnDoubleClick: func(id uint64) { objc.Main(func() { p.open(id) }) },
		Menu:          p.menu,
	})

	search := appkit.NewSearchField("Filter saved queries", func(q string) {
		p.filter = strings.ToLower(strings.TrimSpace(q))
		p.apply()
	})

	p.View = appkit.NewView()

	for _, v := range []objc.ID{search, p.Outline.Scroll} {
		appkit.UseAutoLayout(v)
		objc.Msg(p.View, "addSubview:", v)
	}

	appkit.Activate(
		appkit.Equal(appkit.Anchor(search, "topAnchor"), appkit.Anchor(p.View, "topAnchor"), sidebarInset),
		appkit.Equal(appkit.Anchor(search, "leadingAnchor"), appkit.Anchor(p.View, "leadingAnchor"), sidebarInset),
		appkit.Equal(appkit.Anchor(search, "trailingAnchor"), appkit.Anchor(p.View, "trailingAnchor"), -sidebarInset),
		appkit.Equal(appkit.Anchor(p.Outline.Scroll, "topAnchor"), appkit.Anchor(search, "bottomAnchor"), sidebarInset/2),
		appkit.Equal(appkit.Anchor(p.Outline.Scroll, "leadingAnchor"), appkit.Anchor(p.View, "leadingAnchor"), 0),
		appkit.Equal(appkit.Anchor(p.Outline.Scroll, "trailingAnchor"), appkit.Anchor(p.View, "trailingAnchor"), 0),
		appkit.Equal(appkit.Anchor(p.Outline.Scroll, "bottomAnchor"), appkit.Anchor(p.View, "bottomAnchor"), 0),
	)

	return p
}

// Reload rereads the local folder and refetches BigQuery Studio queries.
func (p *SavedPane) Reload() {
	p.all, p.loadErr = p.s.Workspace.SavedQueries()
	p.apply()
	p.ReloadStudio()
}

// ReloadStudio refetches the BigQuery Studio list for the billing project.
func (p *SavedPane) ReloadStudio() {
	sb, project := p.s.opts.Studio, p.s.BillingProject()
	if sb == nil || project == "" {
		return
	}

	p.studioGen++
	gen, region := p.studioGen, p.s.settings.Prefs.Region()
	p.studioLoading = true
	p.apply()

	go func() {
		ctx, cancel := app.StudioContext()
		defer cancel()

		list, err := sb.List(ctx, project, region)

		objc.Main(func() {
			if gen != p.studioGen {
				return
			}

			p.studio, p.studioErr, p.studioLoading = list, err, false
			p.apply()
		})
	}()
}

// Queries returns the listed (filtered) local queries (smoke checks).
func (p *SavedPane) Queries() []store.SavedQuery { return p.shown }

// StudioQueries returns the listed BigQuery Studio queries (smoke checks).
func (p *SavedPane) StudioQueries() []dataform.SavedQuery { return p.studioShown }

// StudioLoading reports a Studio fetch in flight (smoke checks).
func (p *SavedPane) StudioLoading() bool { return p.studioLoading }

func (p *SavedPane) apply() {
	p.shown = p.shown[:0]

	for _, q := range p.all {
		if p.matches(q.Title + " " + p.rel(q.Path)) {
			p.shown = append(p.shown, q)
		}
	}

	p.studioShown = p.studioShown[:0]

	for _, q := range p.studio {
		if p.matches(q.Title) {
			p.studioShown = append(p.studioShown, q)
		}
	}

	p.Outline.Reload(0)
	p.Outline.Expand(localGroupID, false)
	p.Outline.Expand(studioGroupID, false)
}

func (p *SavedPane) matches(text string) bool {
	return p.filter == "" || strings.Contains(strings.ToLower(text), p.filter)
}

func (p *SavedPane) rel(path string) string {
	if r, err := filepath.Rel(p.s.Workspace.QueriesDir(), path); err == nil {
		return r
	}

	return path
}

func (p *SavedPane) local(id uint64) (store.SavedQuery, bool) {
	if id == 0 || id >= studioBase || id > uint64(len(p.shown)) {
		return store.SavedQuery{}, false
	}

	return p.shown[id-1], true
}

func (p *SavedPane) remote(id uint64) (dataform.SavedQuery, bool) {
	if id <= studioBase || id >= localGroupID || id-studioBase > uint64(len(p.studioShown)) {
		return dataform.SavedQuery{}, false
	}

	return p.studioShown[id-studioBase-1], true
}

func (p *SavedPane) open(id uint64) {
	if q, ok := p.local(id); ok {
		p.s.OpenQuery(q.Path)
	}

	if q, ok := p.remote(id); ok {
		p.s.OpenStudioQuery(q)
	}
}

func (p *SavedPane) menu(id uint64) []appkit.MenuItem {
	refresh := appkit.MenuItem{Title: "Refresh", Run: p.Reload}

	if q, ok := p.local(id); ok {
		return []appkit.MenuItem{
			{Title: "Open", Run: func() { p.s.OpenQuery(q.Path) }},
			{Title: "Run", Run: func() {
				if _, body, err := store.ReadQuery(q.Path); err == nil {
					p.s.RunSQL(body)
				}
			}},
			{},
			{Title: "Show in Finder", Run: func() { appkit.RevealInFinder(q.Path) }},
			{Title: "Copy Path", Run: func() { appkit.CopyToPasteboard(q.Path) }},
			{},
			refresh,
		}
	}

	if q, ok := p.remote(id); ok {
		return []appkit.MenuItem{
			{Title: "Open", Run: func() { p.s.OpenStudioQuery(q) }},
			{},
			{Title: "Rename…", Run: func() { p.s.RenameStudioQuery(q) }},
			{Title: "Delete…", Run: func() { p.s.DeleteStudioQuery(q) }},
			{},
			{Title: "Copy Resource Name", Run: func() { appkit.CopyToPasteboard(q.Name) }},
			refresh,
		}
	}

	return []appkit.MenuItem{
		refresh,
		{Title: "Show Folder in Finder", Run: func() { appkit.RevealInFinder(p.s.Workspace.QueriesDir()) }},
	}
}

type savedSource struct{ p *SavedPane }

func (s savedSource) Children(parent uint64) []uint64 {
	switch parent {
	case 0:
		if s.p.s.opts.Studio == nil {
			return []uint64{localGroupID}
		}

		return []uint64{localGroupID, studioGroupID}
	case localGroupID:
		if len(s.p.shown) == 0 {
			return []uint64{localEmptyID}
		}

		return seq(1, len(s.p.shown))
	case studioGroupID:
		if len(s.p.studioShown) == 0 {
			return []uint64{studioEmptyID}
		}

		return seq(studioBase+1, len(s.p.studioShown))
	}

	return nil
}

func seq(first uint64, n int) []uint64 {
	ids := make([]uint64, n)
	for i := range ids {
		ids[i] = first + uint64(i)
	}

	return ids
}

func (savedSource) Expandable(id uint64) bool { return id == localGroupID || id == studioGroupID }

func (s savedSource) Cell(id uint64) appkit.OutlineCell {
	p := s.p

	switch id {
	case localGroupID:
		return appkit.OutlineCell{Title: "On this Mac", Group: true}
	case studioGroupID:
		return appkit.OutlineCell{Title: "BigQuery Studio", Group: true, ToolTip: p.s.BillingProject() + " · " + p.s.settings.Prefs.Region()}
	case localEmptyID:
		return appkit.OutlineCell{Title: emptyText(p.loadErr, p.filter, "No saved queries yet — File ▸ Save As…"), Secondary: true}
	case studioEmptyID:
		msg := emptyText(p.studioErr, p.filter, "No saved queries in "+p.s.settings.Prefs.Region())
		if p.studioLoading {
			msg = "Loading…"
		}

		return appkit.OutlineCell{Title: msg, Secondary: true, ToolTip: msg}
	}

	if q, ok := p.local(id); ok {
		tip := p.rel(q.Path)
		if q.Project != "" {
			tip += " · " + q.Project
		}

		symbol := "doc.text"
		if q.LinkID != "" {
			symbol = "link"
			tip += " · linked to BigQuery Studio"
		}

		return appkit.OutlineCell{Title: q.Title, Symbol: symbol, ToolTip: tip}
	}

	if q, ok := p.remote(id); ok {
		return appkit.OutlineCell{Title: q.Title, Symbol: "cloud", ToolTip: "Modified " + q.Modified.Local().Format("2006-01-02 15:04")}
	}

	return appkit.OutlineCell{}
}

func emptyText(err error, filter, empty string) string {
	switch {
	case err != nil:
		return "✕ " + err.Error()
	case filter != "":
		return "No matches"
	}

	return empty
}
