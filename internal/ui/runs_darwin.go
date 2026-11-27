//go:build darwin

package ui

import (
	"context"
	"strings"
	"time"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/store"
)

const runsTimeout = 5 * time.Second

// RunsPane lists the runs of the open query file, newest first.
type RunsPane struct {
	View  objc.ID
	Table *appkit.Table

	s    *Shell
	runs []store.Run
	note objc.ID
	gen  int
}

func newRunsPane(s *Shell) *RunsPane {
	p := &RunsPane{s: s}

	p.Table = appkit.NewTable(runsSource{p}, appkit.TableOptions{
		Menu:          p.menu,
		OnDoubleClick: p.openResults,
		Copy: func(rows []int) string {
			var b strings.Builder
			for _, r := range rows {
				b.WriteString(p.runs[r].SQL + "\n")
			}

			return b.String()
		},
	})
	p.Table.SetColumns([]appkit.TableColumn{
		{ID: "state", Title: "", Width: 24, MinWidth: 20},
		{ID: "started", Title: "Started", Width: 150},
		{ID: "duration", Title: "Duration", Width: 80},
		{ID: "processed", Title: "Processed", Width: 90},
		{ID: "rows", Title: "Rows", Width: 80},
		{ID: "query", Title: "Query", Width: 420},
		{ID: "job", Title: "Job ID", Width: 260},
	})

	p.note = appkit.SecondaryLabel("Runs of this query appear here.")
	p.View = appkit.NewView()
	appkit.AddFilling(p.View, p.Table.Scroll, 0)
	appkit.AddCenteredWrapping(p.View, p.note, 16)

	return p
}

// Reload loads the open query's runs (off the main thread).
func (p *RunsPane) Reload() {
	p.gen++
	gen := p.gen

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), runsTimeout)
		defer cancel()

		runs, _ := p.s.Workspace.FileRuns(ctx)

		objc.Main(func() {
			if gen != p.gen {
				return
			}

			p.runs = runs
			p.Table.Reload()
			appkit.SetHidden(p.note, len(runs) > 0)
			p.s.setRunsCount(len(runs))

			if len(runs) > 0 {
				p.s.rememberLastRun(p.s.Workspace.Tab().File, runs[0])
			}
		})
	}()
}

// Runs returns the listed runs (smoke checks).
func (p *RunsPane) Runs() []store.Run { return p.runs }

// NoteView returns the placeholder note label (smoke checks).
func (p *RunsPane) NoteView() objc.ID { return p.note }

func (p *RunsPane) run(i int) (store.Run, bool) {
	if i < 0 || i >= len(p.runs) {
		return store.Run{}, false
	}

	return p.runs[i], true
}

func (p *RunsPane) openResults(i int) {
	r, ok := p.run(i)
	if !ok || r.JobID == "" {
		return
	}

	p.s.SelectTab(TabResults)
	p.s.showRun(p.s.Runner.OpenJob(bq.JobRef{Project: r.Project, JobID: r.JobID, Location: r.Location}))
}

func (p *RunsPane) menu(i int, _ string) []appkit.MenuItem {
	r, ok := p.run(i)
	if !ok {
		return nil
	}

	items := []appkit.MenuItem{
		{Title: "Insert This SQL into the Editor", Run: func() { p.s.Editor.AppendText(r.SQL) }},
		{Title: "Run Again", Run: func() { p.s.SelectTab(TabResults); p.s.RunSQL(r.SQL) }},
		{Title: "Copy SQL", Run: func() { appkit.CopyToPasteboard(r.SQL) }},
	}

	if r.JobID == "" {
		return items
	}

	row := app.JobRow{Ref: bq.JobRef{Project: r.Project, JobID: r.JobID, Location: r.Location}}

	return append(items, appkit.MenuItem{},
		appkit.MenuItem{Title: "Show Results", Run: func() { p.openResults(i) }},
		appkit.MenuItem{Title: "Show Details", Run: func() { p.s.Inspector.ShowJob(row.Ref) }},
		appkit.MenuItem{Title: "Copy Job ID", Run: func() { appkit.CopyToPasteboard(r.JobID) }},
		appkit.MenuItem{Title: "Open in BigQuery Console", Run: func() { appkit.OpenURL(jobConsoleURL(row)) }},
	)
}

type runsSource struct{ p *RunsPane }

func (s runsSource) NumberOfRows() int { return len(s.p.runs) }

func (s runsSource) RowSeparator(int) bool { return true }

func (s runsSource) CellText(i int, column string) appkit.CellText {
	r, ok := s.p.run(i)
	if !ok {
		return appkit.CellText{}
	}

	switch column {
	case "state":
		state := r.State
		if state == "failed" || state == "cancelled" {
			state = "error"
		}

		return appkit.CellText{Text: stateSymbol(state), Secondary: state != "error", ToolTip: r.Error}
	case "started":
		return appkit.CellText{Text: r.Started.Local().Format(time.DateTime)}
	case "duration":
		return appkit.CellText{Text: seconds(r.Elapsed), Right: true}
	case "processed":
		if r.Bytes == 0 {
			return appkit.CellText{}
		}

		return appkit.CellText{Text: app.Bytes(r.Bytes), Right: true}
	case "rows":
		return appkit.CellText{Text: app.CountInt(r.Rows), Right: true}
	case "query":
		return appkit.CellText{Text: strings.Join(strings.Fields(r.SQL), " "), ToolTip: r.SQL}
	case "job":
		return appkit.CellText{Text: r.JobID, Secondary: true}
	}

	return appkit.CellText{}
}

// setRunsCount shows the count in the bottom tab title ("Runs (12)").
func (s *Shell) setRunsCount(n int) {
	s.runsCount = n
	s.fitBottomTabs(true)
}

// rememberLastRun keeps a file's latest run for its tree tooltip.
func (s *Shell) rememberLastRun(path string, r store.Run) {
	if s.lastRuns == nil {
		s.lastRuns = map[string]store.Run{}
	}

	s.lastRuns[path] = r
}

// TabTitle returns a bottom tab's title (smoke checks).
func (s *Shell) TabTitle(i int) string {
	return objc.GoString(objc.Msg(s.tabs.View, "labelForSegment:", int64(i)))
}
