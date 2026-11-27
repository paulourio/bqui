//go:build darwin

package ui

import (
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/dataform"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/store"
)

// Workbooks tree item IDs. Workbooks and queries get stable IDs per path
// (so selection and expansion survive reloads); fixed IDs mark the
// BigQuery Studio section.
const (
	studioGroupID = uint64(1) << 54
	studioEmptyID = studioGroupID + 1
	studioBase    = uint64(1) << 40
)

type wbNode struct {
	workbook *store.Workbook
	query    *store.Query
}

// WorkbooksPane is the Library › Workbook › Query tree, plus BigQuery
// Studio saved queries.
type WorkbooksPane struct {
	View    objc.ID
	Outline *appkit.Outline

	s         *Shell
	filter    string
	selecting bool // programmatic selection (no switch)

	ids     map[string]uint64 // path → id
	nodes   map[uint64]wbNode
	next    uint64
	roots   []uint64
	kids    map[uint64][]uint64
	loadErr error

	studio        []dataform.SavedQuery
	studioShown   []dataform.SavedQuery
	studioErr     error
	studioLoading bool
	studioGen     int
}

func newWorkbooksPane(s *Shell) *WorkbooksPane {
	p := &WorkbooksPane{s: s, ids: map[string]uint64{}, nodes: map[uint64]wbNode{}, kids: map[uint64][]uint64{}}

	p.Outline = appkit.NewOutline(workbooksSource{p}, appkit.OutlineOptions{
		OnSelect:      p.onSelect,
		OnDoubleClick: func(id uint64) { objc.Main(func() { p.open(id) }) },
		Menu:          p.menu,
	})

	search := appkit.NewSearchField("Filter", func(q string) {
		p.filter = strings.ToLower(strings.TrimSpace(q))
		p.rebuild()
	})

	newWorkbook := symbolButton("folder.badge.plus", "New Workbook (⇧⌘N)", s.NewWorkbook)
	newQuery := symbolButton("square.and.pencil", "New Query (⌘N)", s.NewQuery)

	p.View = appkit.NewView()

	for _, v := range []objc.ID{search, newWorkbook, newQuery, p.Outline.Scroll} {
		appkit.UseAutoLayout(v)
		objc.Msg(p.View, "addSubview:", v)
	}

	lead, trail := appkit.Anchor(p.View, "leadingAnchor"), appkit.Anchor(p.View, "trailingAnchor")
	appkit.Activate(
		appkit.Equal(appkit.Anchor(search, "topAnchor"), appkit.Anchor(p.View, "topAnchor"), sidebarInset),
		appkit.Equal(appkit.Anchor(search, "leadingAnchor"), lead, sidebarInset),
		appkit.Equal(appkit.Anchor(search, "trailingAnchor"), appkit.Anchor(newWorkbook, "leadingAnchor"), -sidebarInset/2),
		appkit.Equal(appkit.Anchor(newWorkbook, "centerYAnchor"), appkit.Anchor(search, "centerYAnchor"), 0),
		appkit.Equal(appkit.Anchor(newWorkbook, "trailingAnchor"), appkit.Anchor(newQuery, "leadingAnchor"), -sidebarInset/2),
		appkit.Equal(appkit.Anchor(newQuery, "centerYAnchor"), appkit.Anchor(search, "centerYAnchor"), 0),
		appkit.Equal(appkit.Anchor(newQuery, "trailingAnchor"), trail, -sidebarInset),
		appkit.Equal(appkit.Anchor(p.Outline.Scroll, "topAnchor"), appkit.Anchor(search, "bottomAnchor"), sidebarInset/2),
		appkit.Equal(appkit.Anchor(p.Outline.Scroll, "leadingAnchor"), lead, 0),
		appkit.Equal(appkit.Anchor(p.Outline.Scroll, "trailingAnchor"), trail, 0),
		appkit.Equal(appkit.Anchor(p.Outline.Scroll, "bottomAnchor"), appkit.Anchor(p.View, "bottomAnchor"), 0),
	)

	return p
}

func symbolButton(symbol, tip string, fn func()) objc.ID {
	b := objc.Msg(objc.Cls("NSButton"), "buttonWithImage:target:action:",
		appkit.SymbolImage(symbol, tip), objc.NewTarget(func(objc.ID) { fn() }), objc.Sel(objc.ActionSelector))
	objc.Msg(b, "setBordered:", false)
	objc.Msg(b, "setToolTip:", objc.String(tip))

	return b
}

func (p *WorkbooksPane) id(path string) uint64 {
	if id, ok := p.ids[path]; ok {
		return id
	}

	p.next++
	p.ids[path] = p.next

	return p.next
}

// Reload rereads the library and refetches BigQuery Studio queries.
func (p *WorkbooksPane) Reload() {
	p.rebuild()
	p.ReloadStudio()
}

// rebuild rereads the library from disk (it is small: folder listings).
func (p *WorkbooksPane) rebuild() {
	p.roots, p.kids, p.nodes, p.loadErr = nil, map[uint64][]uint64{}, map[uint64]wbNode{}, nil

	if lib := p.s.Workspace.Library(); lib != nil {
		p.loadErr = p.addLibrary(lib)
	}

	p.studioShown = p.studioShown[:0]

	for _, q := range p.studio {
		if p.matches(q.Title) {
			p.studioShown = append(p.studioShown, q)
		}
	}

	p.Outline.Reload(0)

	for _, id := range p.roots {
		p.Outline.Expand(id, false)
	}

	p.Outline.Expand(studioGroupID, false)
	p.selectCurrent()
}

func (p *WorkbooksPane) addLibrary(lib *store.Library) error {
	wbs, err := lib.Workbooks()
	if err != nil {
		return err
	}

	for _, w := range wbs {
		qs, err := lib.Queries(w.Name)
		if err != nil {
			return err
		}

		wid := p.id(w.Path)
		wbMatch := p.matches(w.Name)

		for _, q := range qs {
			if !wbMatch && !p.matches(q.Name) {
				continue
			}

			qid := p.id(q.Path)
			p.nodes[qid] = wbNode{query: &q}
			p.kids[wid] = append(p.kids[wid], qid)
		}

		if wbMatch || len(p.kids[wid]) > 0 {
			p.nodes[wid] = wbNode{workbook: &w}
			p.roots = append(p.roots, wid)
		}
	}

	return nil
}

func (p *WorkbooksPane) matches(text string) bool {
	return p.filter == "" || strings.Contains(strings.ToLower(text), p.filter)
}

// selectCurrent highlights the open query (no switch: it is already open).
func (p *WorkbooksPane) selectCurrent() {
	if id, ok := p.ids[p.s.Workspace.Tab().File]; ok {
		if _, shown := p.nodes[id]; shown {
			p.selecting = true
			p.Outline.Select(id)
			p.selecting = false
		}
	}
}

// ReloadStudio refetches the BigQuery Studio list for the billing project.
func (p *WorkbooksPane) ReloadStudio() {
	sb, project := p.s.opts.Studio, p.s.BillingProject()
	if sb == nil || project == "" {
		return
	}

	p.studioGen++
	gen, region := p.studioGen, p.s.settings.Prefs.Region()
	p.studioLoading = true
	p.rebuild()

	go func() {
		ctx, cancel := app.StudioContext()
		defer cancel()

		list, err := sb.List(ctx, project, region)

		objc.Main(func() {
			if gen != p.studioGen {
				return
			}

			p.studio, p.studioErr, p.studioLoading = list, err, false
			p.rebuild()
		})
	}()
}

// Workbooks returns the listed workbooks; Queries a workbook's queries
// (smoke checks).
func (p *WorkbooksPane) Workbooks() []string {
	var out []string
	for _, id := range p.roots {
		out = append(out, p.nodes[id].workbook.Name)
	}

	return out
}

// Queries lists the shown query names of a workbook (smoke checks).
func (p *WorkbooksPane) Queries(workbook string) []string {
	var out []string

	for _, id := range p.roots {
		if p.nodes[id].workbook.Name != workbook {
			continue
		}

		for _, q := range p.kids[id] {
			out = append(out, p.nodes[q].query.Name)
		}
	}

	return out
}

// SelectedQuery returns the query selected in the tree (smoke checks).
func (p *WorkbooksPane) SelectedQuery() (store.Query, bool) {
	n, ok := p.nodes[p.Outline.Selected()]
	if !ok || n.query == nil {
		return store.Query{}, false
	}

	return *n.query, true
}

// SelectedWorkbook is the workbook of the selection, or of the open query.
func (p *WorkbooksPane) SelectedWorkbook() string {
	if n, ok := p.nodes[p.Outline.Selected()]; ok {
		if n.workbook != nil {
			return n.workbook.Name
		}

		if n.query != nil {
			return n.query.Workbook
		}
	}

	if q, ok := p.s.Workspace.CurrentQuery(); ok {
		return q.Workbook
	}

	if len(p.roots) > 0 {
		return p.nodes[p.roots[0]].workbook.Name
	}

	return ""
}

// StudioQueries returns the listed BigQuery Studio queries (smoke checks).
func (p *WorkbooksPane) StudioQueries() []dataform.SavedQuery { return p.studioShown }

// StudioLoading reports a Studio fetch in flight (smoke checks).
func (p *WorkbooksPane) StudioLoading() bool { return p.studioLoading }

// StudioExpanded reports whether the BigQuery Studio group is expanded (smoke checks).
func (p *WorkbooksPane) StudioExpanded() bool { return p.Outline.IsExpanded(studioGroupID) }

func (p *WorkbooksPane) remote(id uint64) (dataform.SavedQuery, bool) {
	if id <= studioBase || id >= studioGroupID || id-studioBase > uint64(len(p.studioShown)) {
		return dataform.SavedQuery{}, false
	}

	return p.studioShown[id-studioBase-1], true
}

// onSelect opens a query when it is selected (one click, like a file
// browser); selecting the open query again does nothing.
func (p *WorkbooksPane) onSelect(id uint64) {
	if p.selecting {
		return
	}

	if n, ok := p.nodes[id]; ok && n.query != nil && n.query.Path != p.s.Workspace.Tab().File {
		p.s.OpenLibraryQuery(n.query.Path)
	}
}

func (p *WorkbooksPane) open(id uint64) {
	if q, ok := p.remote(id); ok {
		p.s.OpenStudioQuery(q)
	}
}

func (p *WorkbooksPane) menu(id uint64) []appkit.MenuItem {
	lib := p.s.Workspace.Library()
	newWorkbook := appkit.MenuItem{Title: "New Workbook", Run: p.s.NewWorkbook}
	refresh := appkit.MenuItem{Title: "Refresh", Run: p.Reload}

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

	n, ok := p.nodes[id]

	switch {
	case ok && n.workbook != nil:
		w := *n.workbook

		return []appkit.MenuItem{
			{Title: "New Query", Run: func() { p.s.NewQueryIn(w.Name) }},
			{},
			{Title: "Rename…", Run: func() { p.s.RenameWorkbook(w) }},
			{Title: "Show in Finder", Run: func() { appkit.RevealInFinder(w.Path) }},
			{},
			{Title: "Move to Trash…", Run: func() { p.s.TrashWorkbook(w) }},
			{},
			newWorkbook, refresh,
		}
	case ok && n.query != nil:
		q := *n.query

		var moves []appkit.MenuItem

		if wbs, err := lib.Workbooks(); err == nil {
			for _, w := range wbs {
				if w.Name != q.Workbook {
					to := w.Name
					moves = append(moves, appkit.MenuItem{Title: to, Run: func() { p.s.MoveQuery(q, to, q.Name) }})
				}
			}
		}

		items := []appkit.MenuItem{
			{Title: "Open", Run: func() { p.s.OpenLibraryQuery(q.Path) }},
			{Title: "Run", Run: func() {
				if _, body, err := store.ReadQuery(q.Path); err == nil {
					p.s.RunSQL(body)
				}
			}},
			{},
			{Title: "Rename…", Run: func() { p.s.RenameQuery(q) }},
			{Title: "Duplicate", Run: func() { p.s.DuplicateQuery(q) }},
		}

		if len(moves) > 0 {
			items = append(items, appkit.MenuItem{Title: "Move to", Submenu: &appkit.Menu{Title: "Move to", Items: moves}})
		}

		return append(items,
			appkit.MenuItem{},
			appkit.MenuItem{Title: "Show in Finder", Run: func() { appkit.RevealInFinder(q.Path) }},
			appkit.MenuItem{Title: "Copy Path", Run: func() { appkit.CopyToPasteboard(q.Path) }},
			appkit.MenuItem{},
			appkit.MenuItem{Title: "Move to Trash", Run: func() { p.s.TrashQuery(q) }},
		)
	}

	return []appkit.MenuItem{
		newWorkbook, refresh,
		{Title: "Show Library in Finder", Run: func() {
			if lib != nil {
				appkit.RevealInFinder(lib.Root)
			}
		}},
	}
}

type workbooksSource struct{ p *WorkbooksPane }

func (s workbooksSource) Children(parent uint64) []uint64 {
	p := s.p

	switch parent {
	case 0:
		out := append([]uint64(nil), p.roots...)
		if p.s.opts.Studio != nil {
			out = append(out, studioGroupID)
		}

		return out
	case studioGroupID:
		if len(p.studioShown) == 0 {
			return []uint64{studioEmptyID}
		}

		ids := make([]uint64, len(p.studioShown))
		for i := range ids {
			ids[i] = studioBase + 1 + uint64(i)
		}

		return ids
	}

	return p.kids[parent]
}

func (s workbooksSource) Expandable(id uint64) bool {
	if id == studioGroupID {
		return true
	}

	n, ok := s.p.nodes[id]

	return ok && n.workbook != nil
}

func (s workbooksSource) Cell(id uint64) appkit.OutlineCell {
	p := s.p

	switch id {
	case studioGroupID:
		return appkit.OutlineCell{Title: "BigQuery Studio", Group: true, ToolTip: p.s.BillingProject() + " · " + p.s.settings.Prefs.Region()}
	case studioEmptyID:
		msg := emptyText(p.studioErr, p.filter, "No saved queries in "+p.s.settings.Prefs.Region())
		if p.studioLoading {
			msg = "Loading…"
		}

		return appkit.OutlineCell{Title: msg, Secondary: true, ToolTip: msg}
	}

	if q, ok := p.remote(id); ok {
		return appkit.OutlineCell{Title: q.Title, Symbol: "cloud", ToolTip: "Modified " + q.Modified.Local().Format("2006-01-02 15:04")}
	}

	n, ok := p.nodes[id]

	switch {
	case ok && n.workbook != nil:
		return appkit.OutlineCell{Title: n.workbook.Name, Symbol: "folder", ToolTip: n.workbook.Path}
	case ok && n.query != nil:
		return appkit.OutlineCell{Title: n.query.Name, Symbol: "doc.text", ToolTip: p.s.queryTip(*n.query)}
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

// queryTip describes a query file for its tooltip (path and last run).
func (s *Shell) queryTip(q store.Query) string {
	tip := q.Workbook + "/" + filepath.Base(q.Path)

	if !q.Modified.IsZero() {
		tip += "\nEdited " + q.Modified.Local().Format("2006-01-02 15:04")
	}

	if last, ok := s.lastRuns[q.Path]; ok {
		tip += "\nLast run " + last.Started.Local().Format("2006-01-02 15:04") + " · " + last.State
	}

	return tip
}

// --- Shell actions ---------------------------------------------------------

// OpenLibraryQuery switches the editor to a library query (the current one
// is saved first).
func (s *Shell) OpenLibraryQuery(path string) {
	t := s.Workspace.Tab()
	if t.File == "" && t.Remote != "" && !s.keepChanges() {
		return
	}

	sql, err := s.Workspace.Switch(path)
	if err != nil {
		s.status("✕ " + err.Error())

		return
	}

	s.Editor.SetTextAt(sql, 0, 0)
	s.updateTitle()
	s.Workbooks.selectCurrent()
	s.Runs.Reload()
}

// NewQuery creates Untitled-N in the selected workbook (⌘N).
func (s *Shell) NewQuery() { s.NewQueryIn(s.Workbooks.SelectedWorkbook()) }

// NewQueryIn creates Untitled-N in a workbook and opens it.
func (s *Shell) NewQueryIn(workbook string) {
	if s.Workspace.Library() == nil || workbook == "" {
		s.NewWorkbook()

		return
	}

	if _, err := s.Workspace.NewQuery(workbook); err != nil {
		s.status("✕ " + err.Error())

		return
	}

	s.Editor.SetTextAt("", 0, 0)
	s.updateTitle()
	s.SelectSidebar(SidebarWorkbooks)
	s.Workbooks.rebuild()
	s.Runs.Reload()
	s.Editor.Focus()
}

// NewWorkbook creates workbook-YYYY-MM-DD with its first query (⇧⌘N).
func (s *Shell) NewWorkbook() {
	lib := s.Workspace.Library()
	if lib == nil {
		return
	}

	w, err := lib.CreateWorkbook("", time.Now())
	if err != nil {
		s.status("✕ " + err.Error())

		return
	}

	s.NewQueryIn(w.Name)
}

// RenameWorkbook asks for a new workbook name.
func (s *Shell) RenameWorkbook(w store.Workbook) {
	name, ok := appkit.Prompt("Rename Workbook", "", w.Name)
	if ok {
		s.RenameWorkbookTo(w, name)
	}
}

// RenameWorkbookTo renames a workbook folder, following the open query.
func (s *Shell) RenameWorkbookTo(w store.Workbook, name string) {
	name = strings.TrimSpace(name)

	nw, err := s.Workspace.Library().RenameWorkbook(w.Name, name)
	if err != nil {
		s.status("✕ " + renameError(err))

		return
	}

	if q, ok := s.Workspace.CurrentQuery(); ok && q.Workbook == w.Name {
		s.Workspace.Relink(q, store.Query{Name: q.Name, Workbook: nw.Name, Path: filepath.Join(nw.Path, filepath.Base(q.Path))})
	}

	s.afterLibraryChange()
}

// RenameQuery asks for a new query name (without .sql).
func (s *Shell) RenameQuery(q store.Query) {
	name, ok := appkit.Prompt("Rename Query", "", q.Name)
	if ok {
		s.MoveQuery(q, q.Workbook, name)
	}
}

// MoveQuery renames and/or moves a query, following it if it is open.
func (s *Shell) MoveQuery(q store.Query, workbook, name string) {
	if s.Workspace.Tab().File == q.Path && s.Workspace.Dirty() {
		_ = s.Workspace.Save()
	}

	nq, err := s.Workspace.Library().MoveQuery(q, workbook, name)
	if err != nil {
		s.status("✕ " + renameError(err))

		return
	}

	s.Workspace.Relink(q, nq)
	s.afterLibraryChange()
}

// DuplicateQuery copies a query next to itself.
func (s *Shell) DuplicateQuery(q store.Query) {
	if _, err := s.Workspace.Library().DuplicateQuery(q); err != nil {
		s.status("✕ " + err.Error())

		return
	}

	s.afterLibraryChange()
}

// TrashQuery moves a query to the Trash; the open query is replaced by
// another one in its workbook (or a new one).
func (s *Shell) TrashQuery(q store.Query) {
	if err := appkit.Trash(q.Path); err != nil {
		s.status("✕ " + err.Error())

		return
	}

	if s.Workspace.Tab().File == q.Path {
		s.openAfterRemoval(q.Workbook)
	}

	s.status("Moved “" + q.Name + "” to the Trash.")
	s.afterLibraryChange()
}

// TrashWorkbook moves a workbook folder to the Trash (after confirming).
func (s *Shell) TrashWorkbook(w store.Workbook) {
	qs, _ := s.Workspace.Library().Queries(w.Name)
	if !s.opts.Deterministic && appkit.Confirm("Move “"+w.Name+"” to the Trash?",
		app.Count(uint64(len(qs)))+" queries go with it. You can restore it from the Trash.", "Move to Trash", "Cancel") != appkit.AlertFirst {
		return
	}

	if err := appkit.Trash(w.Path); err != nil {
		s.status("✕ " + err.Error())

		return
	}

	if q, ok := s.Workspace.CurrentQuery(); ok && q.Workbook == w.Name || s.Workspace.Tab().File == "" {
		s.openAfterRemoval("")
	}

	s.afterLibraryChange()
}

// openAfterRemoval opens the first query of a workbook (or anywhere), or
// creates one, after the open query was removed.
func (s *Shell) openAfterRemoval(workbook string) {
	lib := s.Workspace.Library()

	wbs, _ := lib.Workbooks()
	for _, w := range wbs {
		if workbook != "" && w.Name != workbook {
			continue
		}

		if qs, _ := lib.Queries(w.Name); len(qs) > 0 {
			s.openWithoutSaving(qs[0].Path)

			return
		}
	}

	if workbook != "" {
		s.NewQueryIn(workbook)

		return
	}

	if len(wbs) > 0 {
		s.NewQueryIn(wbs[0].Name)

		return
	}

	s.NewWorkbook()
}

// openWithoutSaving opens a query when the open file no longer exists.
func (s *Shell) openWithoutSaving(path string) {
	sql, err := s.Workspace.Open(path)
	if err != nil {
		s.status("✕ " + err.Error())

		return
	}

	s.Editor.SetTextAt(sql, 0, 0)
	s.updateTitle()
	s.Runs.Reload()
}

func (s *Shell) afterLibraryChange() {
	s.updateTitle()
	s.Workbooks.rebuild()
}

func renameError(err error) string {
	switch {
	case errors.Is(err, store.ErrExists):
		return "An item with that name already exists."
	case errors.Is(err, store.ErrBadName):
		return "Names can't be empty, start with a dot, or contain / or :."
	}

	return err.Error()
}
