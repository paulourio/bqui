//go:build darwin

package ui

import (
	"path/filepath"
	"strings"

	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/store"
)

// SavedPane lists the local saved-queries folder (.sql files).
type SavedPane struct {
	View    objc.ID
	Outline *appkit.Outline

	s       *Shell
	all     []store.SavedQuery
	shown   []store.SavedQuery
	filter  string
	loadErr error
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

// Reload rereads the folder (it is small; .sql headers only).
func (p *SavedPane) Reload() {
	p.all, p.loadErr = p.s.Workspace.SavedQueries()
	p.apply()
}

// Queries returns the listed (filtered) queries (smoke checks).
func (p *SavedPane) Queries() []store.SavedQuery { return p.shown }

func (p *SavedPane) apply() {
	p.shown = p.shown[:0]

	for _, q := range p.all {
		if p.filter == "" || strings.Contains(strings.ToLower(q.Title+" "+p.rel(q.Path)), p.filter) {
			p.shown = append(p.shown, q)
		}
	}

	p.Outline.Reload(0)
}

func (p *SavedPane) rel(path string) string {
	if r, err := filepath.Rel(p.s.Workspace.QueriesDir(), path); err == nil {
		return r
	}

	return path
}

func (p *SavedPane) query(id uint64) (store.SavedQuery, bool) {
	if id == 0 || id > uint64(len(p.shown)) {
		return store.SavedQuery{}, false
	}

	return p.shown[id-1], true
}

func (p *SavedPane) open(id uint64) {
	if q, ok := p.query(id); ok {
		p.s.OpenQuery(q.Path)
	}
}

func (p *SavedPane) menu(id uint64) []appkit.MenuItem {
	refresh := appkit.MenuItem{Title: "Refresh", Run: p.Reload}
	folder := appkit.MenuItem{Title: "Show Folder in Finder", Run: func() { appkit.RevealInFinder(p.s.Workspace.QueriesDir()) }}

	q, ok := p.query(id)
	if !ok {
		return []appkit.MenuItem{refresh, folder}
	}

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

type savedSource struct{ p *SavedPane }

func (s savedSource) Children(parent uint64) []uint64 {
	if parent != 0 {
		return nil
	}

	if len(s.p.shown) == 0 {
		return []uint64{placeholderID}
	}

	ids := make([]uint64, len(s.p.shown))
	for i := range ids {
		ids[i] = uint64(i) + 1
	}

	return ids
}

func (savedSource) Expandable(uint64) bool { return false }

// placeholderID is the "No saved queries" row.
const placeholderID = ^uint64(0)

func (s savedSource) Cell(id uint64) appkit.OutlineCell {
	if id == placeholderID {
		msg := "No saved queries yet — File ▸ Save As…"

		switch {
		case s.p.loadErr != nil:
			msg = "✕ " + s.p.loadErr.Error()
		case s.p.filter != "":
			msg = "No matches"
		}

		return appkit.OutlineCell{Title: msg, Secondary: true}
	}

	q, ok := s.p.query(id)
	if !ok {
		return appkit.OutlineCell{}
	}

	tip := s.p.rel(q.Path)
	if q.Project != "" {
		tip += " · " + q.Project
	}

	return appkit.OutlineCell{Title: q.Title, Symbol: "doc.text", ToolTip: tip}
}
