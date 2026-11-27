//go:build darwin

package ui

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/objc"
)

const (
	historyScopeMine  = 0
	historyScopeLocal = 2
	historyLoadAhead  = 10 // rows from the end that trigger the next page
	historyInset      = 8.0
	historySearchW    = 220.0

	localRefreshDelay  = 150 * time.Millisecond
	localSearchTimeout = 5 * time.Second
)

// HistoryPane lists the project's jobs (personal or project-wide).
type HistoryPane struct {
	View  objc.ID
	Table *appkit.Table
	Model *app.History

	s      *Shell
	scope  *appkit.Segmented
	count  objc.ID
	shown  bool
	asking bool

	filter   string
	filtered []app.JobRow // BigQuery rows matching filter
	local    []app.JobRow // local runs matching filter (FTS)
	localErr error
	localGen int
}

// rows returns what the table shows for the selected scope and filter.
func (p *HistoryPane) rows() []app.JobRow {
	switch {
	case p.Local():
		return p.local
	case p.filter != "":
		return p.filtered
	}

	return p.Model.Rows()
}

// Rows returns the listed rows (smoke checks).
func (p *HistoryPane) Rows() []app.JobRow { return p.rows() }

// Local reports whether the pane shows this Mac's run history.
func (p *HistoryPane) Local() bool { return p.scope != nil && p.scope.Selected() == historyScopeLocal }

// SetFilter filters jobs (substring) or searches local runs (full text).
func (p *HistoryPane) SetFilter(q string) {
	p.filter = q

	if p.Local() {
		p.loadLocal()

		return
	}

	p.changed()
}

// LocalChanged refreshes local runs after a run is recorded.
func (p *HistoryPane) LocalChanged() {
	if p.shown && p.Local() {
		// Recording is asynchronous; give it a moment before searching.
		time.AfterFunc(localRefreshDelay, func() { objc.Main(p.loadLocal) })
	}
}

func (p *HistoryPane) loadLocal() {
	p.localGen++
	gen, q := p.localGen, p.filter

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), localSearchTimeout)
		defer cancel()

		runs, err := p.s.Workspace.SearchRuns(ctx, q)

		objc.Main(func() {
			if gen != p.localGen {
				return // a newer search superseded this one
			}

			p.local, p.localErr = app.JobRowsFromRuns(runs), err
			p.changed()
		})
	}()
}

func newHistoryPane(s *Shell, be app.JobsBackend) *HistoryPane {
	p := &HistoryPane{s: s}
	p.Model = app.NewHistory(be, objc.Main, p.changed)

	p.Table = appkit.NewTable(historySource{p}, appkit.TableOptions{
		Menu:          p.menu,
		OnDoubleClick: func(row int) { p.openInEditor(row) },
		OnSelect: func(rows []int) {
			if len(rows) == 1 && p.rows()[rows[0]].Ref.JobID != "" {
				p.s.Inspector.ShowJob(p.rows()[rows[0]].Ref)
			}
		},
		Copy: func(rows []int) string {
			var b strings.Builder
			for _, r := range rows {
				b.WriteString(p.rows()[r].Ref.JobID + "\n")
			}

			return b.String()
		},
	})
	p.Table.SetColumns([]appkit.TableColumn{
		{ID: "state", Title: "", Width: 24, MinWidth: 20},
		{ID: "created", Title: "Created", Width: 150},
		{ID: "duration", Title: "Duration", Width: 80},
		{ID: "processed", Title: "Processed", Width: 90},
		{ID: "statement", Title: "Statement", Width: 90},
		{ID: "user", Title: "User", Width: 160},
		{ID: "query", Title: "Query", Width: 420},
		{ID: "job", Title: "Job ID", Width: 260},
	})

	p.scope = appkit.NewSegmented([]string{"My jobs", "Project jobs", "This Mac"}, func(int) { p.Reload() })
	p.count = appkit.SecondaryLabel("")
	search := appkit.NewSearchField("Search SQL or job ID", p.SetFilter)

	refresh := objc.Msg(objc.Cls("NSButton"), "buttonWithImage:target:action:",
		appkit.SymbolImage("arrow.clockwise", "Refresh"), objc.NewTarget(func(objc.ID) { p.Reload() }), objc.Sel(objc.ActionSelector))
	objc.Msg(refresh, "setBordered:", false)
	objc.Msg(refresh, "setToolTip:", objc.String("Refresh job history"))

	p.View = appkit.NewView()

	for _, v := range []objc.ID{p.scope.View, refresh, p.count, search, p.Table.Scroll} {
		appkit.UseAutoLayout(v)
		objc.Msg(p.View, "addSubview:", v)
	}

	top, lead, trail := appkit.Anchor(p.View, "topAnchor"), appkit.Anchor(p.View, "leadingAnchor"), appkit.Anchor(p.View, "trailingAnchor")
	appkit.Activate(
		appkit.Equal(appkit.Anchor(p.scope.View, "topAnchor"), top, historyInset/2),
		appkit.Equal(appkit.Anchor(p.scope.View, "leadingAnchor"), lead, historyInset),
		appkit.Equal(appkit.Anchor(refresh, "centerYAnchor"), appkit.Anchor(p.scope.View, "centerYAnchor"), 0),
		appkit.Equal(appkit.Anchor(refresh, "leadingAnchor"), appkit.Anchor(p.scope.View, "trailingAnchor"), historyInset),
		appkit.Equal(appkit.Anchor(p.count, "centerYAnchor"), appkit.Anchor(p.scope.View, "centerYAnchor"), 0),
		appkit.Equal(appkit.Anchor(p.count, "leadingAnchor"), appkit.Anchor(refresh, "trailingAnchor"), historyInset),
		appkit.Equal(appkit.Anchor(search, "centerYAnchor"), appkit.Anchor(p.scope.View, "centerYAnchor"), 0),
		appkit.Equal(appkit.Anchor(search, "trailingAnchor"), trail, -historyInset),
		appkit.EqualConst(appkit.Anchor(search, "widthAnchor"), historySearchW),
		appkit.Equal(appkit.Anchor(p.Table.Scroll, "topAnchor"), appkit.Anchor(p.scope.View, "bottomAnchor"), historyInset/2),
		appkit.Equal(appkit.Anchor(p.Table.Scroll, "leadingAnchor"), lead, 0),
		appkit.Equal(appkit.Anchor(p.Table.Scroll, "trailingAnchor"), trail, 0),
		appkit.Equal(appkit.Anchor(p.Table.Scroll, "bottomAnchor"), appkit.Anchor(p.View, "bottomAnchor"), 0),
	)

	return p
}

// SetScope selects My jobs (0), Project jobs (1) or This Mac (2) and reloads.
func (p *HistoryPane) SetScope(i int) {
	p.scope.Select(i)
	p.Reload()
}

// Shown loads the history the first time the tab is opened.
func (p *HistoryPane) Shown() {
	if !p.shown {
		p.shown = true
		p.Reload()
	}
}

// Reload refetches for the billing project and the selected scope.
func (p *HistoryPane) Reload() {
	if p.Local() {
		p.loadLocal()

		return
	}

	p.Model.Reload(p.s.BillingProject(), p.scope.Selected() != historyScopeMine)
}

// RefreshIfShown reloads after a run finishes, if the tab was ever opened.
func (p *HistoryPane) RefreshIfShown() {
	if p.shown && !p.Local() {
		p.Reload()
	}
}

func (p *HistoryPane) changed() {
	p.asking = false

	if !p.Local() && p.filter != "" {
		p.filtered = app.FilterJobRows(p.Model.Rows(), p.filter)
	}

	p.Table.Reload()

	n := len(p.rows())

	switch {
	case p.Local() && p.localErr != nil:
		appkit.SetString(p.count, "✕ "+p.localErr.Error())
	case p.Local():
		appkit.SetString(p.count, app.Count(uint64(n))+" local runs")
	case p.Model.Err() != nil:
		appkit.SetString(p.count, "✕ "+p.Model.Err().Error())
	case p.Model.Loading() && n == 0:
		appkit.SetString(p.count, "Loading…")
	default:
		more := ""
		if p.Model.HasMore() {
			more = "+"
		}

		appkit.SetString(p.count, app.Count(uint64(n))+more+" jobs")
	}
}

func (p *HistoryPane) row(i int) (app.JobRow, bool) {
	rows := p.rows()
	if i < 0 || i >= len(rows) {
		return app.JobRow{}, false
	}

	return rows[i], true
}

func (p *HistoryPane) openInEditor(i int) {
	if r, ok := p.row(i); ok && r.SQL != "" {
		p.s.Editor.AppendText(r.SQL)
	}
}

func (p *HistoryPane) menu(i int, _ string) []appkit.MenuItem {
	r, ok := p.row(i)
	if !ok {
		return nil
	}

	items := []appkit.MenuItem{
		{Title: "Open Query in Editor", Run: func() { p.openInEditor(i) }},
		{Title: "Run Again", Run: func() { p.s.SelectTab(TabResults); p.s.RunSQL(r.SQL) }},
	}

	if r.Ref.JobID == "" {
		return append(items, appkit.MenuItem{}, appkit.MenuItem{Title: "Copy Query", Run: func() { appkit.CopyToPasteboard(r.SQL) }})
	}

	items = append(items,
		appkit.MenuItem{Title: "Show Results", Run: func() { p.s.SelectTab(TabResults); p.s.Results.Show(p.s.Runner.OpenJob(r.Ref)) }},
		appkit.MenuItem{Title: "Show Details", Run: func() { p.s.Inspector.ShowJob(r.Ref) }},
	)

	if r.State == "running" || r.State == "pending" {
		items = append(items, appkit.MenuItem{Title: "Cancel Job", Run: func() { p.s.CancelJob(r.Ref) }})
	}

	return append(items, appkit.MenuItem{},
		appkit.MenuItem{Title: "Copy Job ID", Run: func() { appkit.CopyToPasteboard(r.Ref.JobID) }},
		appkit.MenuItem{Title: "Open in BigQuery Console", Run: func() { appkit.OpenURL(jobConsoleURL(r)) }},
	)
}

func jobConsoleURL(r app.JobRow) string {
	q := url.Values{"project": {r.Ref.Project}, "j": {"bq:" + r.Ref.Location + ":" + r.Ref.JobID}, "page": {"queryresults"}}

	return "https://console.cloud.google.com/bigquery?" + q.Encode()
}

type historySource struct{ p *HistoryPane }

func (s historySource) NumberOfRows() int { return len(s.p.rows()) }

func (s historySource) RowSeparator(int) bool { return true }

func (s historySource) CellText(i int, column string) appkit.CellText {
	r, ok := s.p.row(i)
	if !ok {
		return appkit.CellText{}
	}

	// Load the next page as the user nears the end (outside this callback).
	if !s.p.Local() && i >= len(s.p.rows())-historyLoadAhead && s.p.Model.HasMore() && !s.p.asking {
		s.p.asking = true

		objc.Main(s.p.Model.LoadMore)
	}

	switch column {
	case "state":
		return appkit.CellText{Text: stateSymbol(r.State), Secondary: r.State != "error", ToolTip: r.Error}
	case "created":
		return appkit.CellText{Text: r.Created.Local().Format(time.DateTime)}
	case "duration":
		if r.Duration == 0 {
			return appkit.CellText{}
		}

		return appkit.CellText{Text: seconds(r.Duration), Right: true}
	case "processed":
		if r.CacheHit {
			return appkit.CellText{Text: "cached", Secondary: true, Right: true}
		}

		return appkit.CellText{Text: app.Bytes(r.Processed), Right: true}
	case "statement":
		return appkit.CellText{Text: r.Statement, Secondary: true}
	case "user":
		return appkit.CellText{Text: r.User, Secondary: true}
	case "query":
		return appkit.CellText{Text: r.Summary(), ToolTip: r.SQL}
	case "job":
		return appkit.CellText{Text: r.Ref.JobID, Secondary: true}
	}

	return appkit.CellText{}
}

func stateSymbol(state string) string {
	switch state {
	case "done":
		return "✓"
	case "error":
		return "✕"
	case "running":
		return "…"
	case "pending":
		return "◷"
	}

	return state
}
