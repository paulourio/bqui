//go:build darwin

package ui

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/results"
)

const (
	gutterID            = "__row"
	gutterWidth         = 56.0
	colMinWidth         = 48.0
	colMaxWidth         = 360.0
	colPadding          = 18.0
	sampleLines         = 200
	statusInset         = 10.0
	tickInterval        = 200 * time.Millisecond
	maxCopyLines        = 100_000
	colPrefix           = "c"
	statusFontSize      = 11.0
	defaultGridFontSize = 12.0
)

// ResultsPane shows the current run: a status line over the result grid.
type ResultsPane struct {
	View  objc.ID
	Table *appkit.Table

	// Inspect receives a double-clicked row and the clicked value's path.
	Inspect func(fields []results.Field, row []any, index int64, path string)
	// ExportMenu builds the Export submenu for the context menu.
	ExportMenu func() appkit.MenuItem
	// Banner explains failures (red box under the status line).
	Banner    *appkit.Banner
	bannerRun *app.Run // run whose failure the banner showed
	// NullColor and NumberColor come from the theme (nil = system colors).
	NullColor, NumberColor objc.ID

	status, empty objc.ID

	run      *app.Run
	snap     app.RunSnapshot
	grid     *results.Grid
	cols     []results.Column
	lines    int64
	fontSize float64

	ticking bool
	asking  bool // a FetchMore is queued
}

func newResultsPane() *ResultsPane {
	p := &ResultsPane{fontSize: appkit.GridFontSize()}
	src := &gridSource{p: p}

	p.Table = appkit.NewTable(src, appkit.TableOptions{Copy: p.copyLines, Menu: p.menu, OnDoubleClick: p.inspectLine})
	p.status = appkit.WrappingLabel("", statusFontSize)
	objc.Msg(p.status, "setTextColor:", objc.Msg(objc.Cls("NSColor"), "secondaryLabelColor"))
	p.empty = appkit.SecondaryLabel("Run a query with ⌘↩ to see results here.")

	p.Banner = appkit.NewBanner()

	// Status line, error banner (hidden unless something failed), grid.
	// The stack drops hidden views from the layout.
	top := appkit.VStack(6, statusInset, p.status, p.Banner.View)
	p.View = appkit.NewView()

	for _, v := range []objc.ID{top, p.Table.Scroll} {
		appkit.UseAutoLayout(v)
		objc.Msg(p.View, "addSubview:", v)
	}

	appkit.AddCenteredWrapping(p.View, p.empty, 16)

	lead, trail := appkit.Anchor(p.View, "leadingAnchor"), appkit.Anchor(p.View, "trailingAnchor")
	appkit.Activate(
		appkit.Equal(appkit.Anchor(top, "topAnchor"), appkit.Anchor(p.View, "topAnchor"), 6),
		appkit.Equal(appkit.Anchor(top, "leadingAnchor"), lead, 0),
		appkit.Equal(appkit.Anchor(top, "trailingAnchor"), trail, 0),
		appkit.Equal(appkit.Anchor(p.Table.Scroll, "topAnchor"), appkit.Anchor(top, "bottomAnchor"), 6),
		appkit.Equal(appkit.Anchor(p.Table.Scroll, "leadingAnchor"), lead, 0),
		appkit.Equal(appkit.Anchor(p.Table.Scroll, "trailingAnchor"), trail, 0),
		appkit.Equal(appkit.Anchor(p.Table.Scroll, "bottomAnchor"), appkit.Anchor(p.View, "bottomAnchor"), 0),
	)

	appkit.SetHidden(p.Table.Scroll, true)

	return p
}

// Show switches to a new run. The previous run is cancelled and its spill
// file released off the main thread.
func (p *ResultsPane) Show(r *app.Run) {
	if old := p.run; old != nil && old != r {
		go old.Close()
	}

	p.run, p.grid, p.cols, p.lines, p.fontSize = r, nil, nil, 0, appkit.GridFontSize()
	p.Table.SetColumns(nil)
	p.Table.Reload()
	appkit.SetHidden(p.Table.Scroll, true)
	appkit.SetHidden(p.empty, true)
	p.Banner.Hide()
	p.Update(r)
}

// Notice shows a message in the status line without a run. Messages
// starting with "✕ " are errors: they also open the error banner.
func (p *ResultsPane) Notice(msg string) {
	if detail, isErr := strings.CutPrefix(msg, "✕ "); isErr {
		p.ShowProblem(app.Explain(errors.New(detail)))

		return
	}

	p.Banner.Hide()
	appkit.SetHidden(p.status, false)
	appkit.SetString(p.status, msg)
	objc.Msg(p.status, "setTextColor:", objc.Msg(objc.Cls("NSColor"), "secondaryLabelColor"))
}

// ShowProblem shows an explained error: a short red status line and the
// banner with details and what to do.
func (p *ResultsPane) ShowProblem(pr app.Problem) {
	// The banner says it all; the status line keeps the text for checks.
	appkit.SetString(p.status, "✕ "+pr.Title)
	appkit.SetHidden(p.status, true)
	appkit.SetHidden(p.empty, true)
	p.Banner.ShowStyled(pr.Title, pr.Detail, pr.Hint, pr.Warning)
}

// Reset stops and forgets the current run (the pane shows its empty text).
func (p *ResultsPane) Reset() {
	if old := p.run; old != nil {
		go old.Close()
	}

	p.run, p.grid, p.cols, p.lines = nil, nil, nil, 0
	p.Table.SetColumns(nil)
	p.Table.Reload()
	appkit.SetHidden(p.Table.Scroll, true)
	p.Banner.Hide()
	appkit.SetString(p.status, "")
	appkit.SetHidden(p.empty, false)
}

// SetEmptyText changes the text shown before anything runs.
func (p *ResultsPane) SetEmptyText(text string) { appkit.SetString(p.empty, text) }

// Current returns the run being shown (nil before the first run).
func (p *ResultsPane) Current() *app.Run { return p.run }

// StatusText returns the status line (smoke checks).
func (p *ResultsPane) StatusText() string { return objc.GoString(objc.Msg(p.status, "stringValue")) }

// StatusView returns the status label (smoke checks).
func (p *ResultsPane) StatusView() objc.ID { return p.status }

// EmptyView returns the empty placeholder label (smoke checks).
func (p *ResultsPane) EmptyView() objc.ID { return p.empty }

// Update refreshes the pane for r (main thread; stale runs are ignored).
func (p *ResultsPane) Update(r *app.Run) {
	if r != p.run {
		return
	}

	p.snap = r.Snapshot()

	gridAdded := p.snap.Grid != nil && p.grid == nil
	if gridAdded {
		p.grid = p.snap.Grid
		p.cols = p.grid.Columns()
		p.fontSize = appkit.GridFontSize()
		appkit.SetHidden(p.Table.Scroll, false)
	}

	if p.grid != nil {
		if !gridAdded {
			p.checkFontSize(true)
		}

		_, lines, _ := p.grid.Size()
		switch {
		case gridAdded:
			p.lines = lines
			p.Table.SetColumns(p.columns())
			p.Table.Reload()
			p.Table.ScrollToTop()
		case lines != p.lines:
			first := p.lines == 0
			p.lines = lines

			if first {
				p.Table.SetColumns(p.columns()) // widths sampled from real data
				p.Table.Reload()
				p.Table.ScrollToTop()
			} else {
				p.Table.NoteRowsChanged()
			}
		}
	}

	p.setStatus()
	p.tick()
}

// tick keeps the elapsed time moving while the run is active.
func (p *ResultsPane) tick() {
	active := p.snap.State == app.RunRunning || p.snap.State == app.RunFetching
	if !active || p.ticking {
		return
	}

	p.ticking = true
	r := p.run

	time.AfterFunc(tickInterval, func() {
		objc.Main(func() {
			p.ticking = false

			if r == p.run {
				p.Update(r)
			}
		})
	})
}

func (p *ResultsPane) setStatus() {
	s := p.snap
	color := "secondaryLabelColor"

	var text string

	switch s.State {
	case app.RunRunning:
		text = "Running… " + seconds(s.Elapsed)
		if s.Stages > 0 {
			text += fmt.Sprintf(" · %d/%d stages", s.StagesDone, s.Stages)
		}

		if s.Job != nil {
			text += " · " + s.Job.JobID
		}
	case app.RunFetching:
		rows, _, _ := p.grid.Size()
		text = fmt.Sprintf("Loading results… %s rows · %s", app.Count(uint64(rows)), seconds(s.Elapsed)) //nolint:gosec // non-negative
	case app.RunDone:
		text = p.doneText()
	case app.RunFailed:
		// Once per run: later updates must not cover a newer notice.
		if p.bannerRun != p.run {
			p.bannerRun = p.run
			pr := app.Explain(s.Err)
			p.Banner.ShowStyled(pr.Title, pr.Detail, pr.Hint, pr.Warning)
		}

		text = "Failed after " + seconds(s.Elapsed)
	case app.RunCancelled:
		text = "Cancelled after " + seconds(s.Elapsed)
	}

	appkit.SetHidden(p.status, false)
	appkit.SetString(p.status, text)
	objc.Msg(p.status, "setTextColor:", objc.Msg(objc.Cls("NSColor"), color))
}

func (p *ResultsPane) doneText() string {
	s := p.snap
	parts := []string{}

	if s.Kind == "preview" {
		parts = append(parts, s.Title)
	}

	if p.grid != nil {
		parts = append(parts, p.rowsText()...)
	} else if res := s.Result; res != nil {
		switch {
		case res.DMLAffected > 0:
			parts = append(parts, fmt.Sprintf("✓ %s rows affected", app.Count(uint64(res.DMLAffected))))
		case res.Statement != "":
			parts = append(parts, "✓ "+res.Statement+" completed")
		default:
			parts = append(parts, "✓ completed")
		}
	}

	if res := s.Result; res != nil {
		if res.BytesProc > 0 {
			parts = append(parts, noBreak(app.Bytes(res.BytesProc))+"\u00a0read")
		}

		if res.BytesBilled > 0 {
			parts = append(parts, noBreak(app.Bytes(res.BytesBilled))+"\u00a0billed")
		}

		if res.CacheHit {
			parts = append(parts, "cached")
		}
	}

	if s.SlotMs > 0 {
		parts = append(parts, "slot\u00a0"+seconds(time.Duration(s.SlotMs)*time.Millisecond))
	}

	parts = append(parts, seconds(s.Elapsed))

	if s.Source == "storage" {
		parts = append(parts, "via Storage Read API")
	}

	return strings.Join(parts, " · ")
}

func noBreak(s string) string { return strings.ReplaceAll(s, " ", "\u00a0") }

func seconds(d time.Duration) string {
	if d < time.Second {
		return strconv.FormatInt(d.Milliseconds(), 10) + "\u00a0ms"
	}

	return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "\u00a0s"
}

// columns builds table columns: a row-number gutter, then one per leaf with
// a width sampled from the first lines.
func (p *ResultsPane) columns() []appkit.TableColumn {
	fs := p.fontSize
	if fs <= 0 {
		fs = appkit.GridFontSize()
		p.fontSize = fs
	}

	scale := fs / defaultGridFontSize
	maxW := max(colMaxWidth, colMaxWidth*scale)
	gutterW := max(gutterWidth, gutterWidth*scale)

	out := []appkit.TableColumn{{ID: gutterID, Title: "Row", Width: gutterW, MinWidth: 32}}

	sample := min(p.lines, sampleLines)

	// Auto-fit: the widest of the header and the sampled cells, measured in
	// the fonts they render with (proportional, so not by character count).
	for i, c := range p.cols {
		w := appkit.TextWidth(c.Name, appkit.FontHeader) + colPadding

		for line := range sample {
			if cell, _, err := p.grid.Cell(line, i); err == nil && cell.Kind == results.Value {
				w = max(w, appkit.TextWidth(results.FormatCell(c.Type, cell.V), appkit.FontRegular)+colPadding)
			}
		}

		out = append(out, appkit.TableColumn{
			ID: colPrefix + strconv.Itoa(i), Title: c.Name, ToolTip: c.Name + " · " + c.Type,
			Width: min(max(w, colMinWidth), maxW), MinWidth: colMinWidth,
		})
	}

	return out
}

func (p *ResultsPane) checkFontSize(sync bool) {
	fs := appkit.GridFontSize()
	if fs <= 0 || fs == p.fontSize {
		return
	}

	p.fontSize = fs
	if p.grid == nil {
		return
	}

	if sync {
		p.applyColumnWidths()

		return
	}

	grid := p.grid
	objc.Main(func() {
		if p.grid == grid && p.fontSize == fs {
			p.applyColumnWidths()
		}
	})
}

func (p *ResultsPane) applyColumnWidths() {
	for _, col := range p.columns() {
		p.Table.SetColumnWidth(col.ID, col.Width)
	}
}

func columnIndex(id string) (int, bool) {
	if !strings.HasPrefix(id, colPrefix) {
		return 0, false
	}

	i, err := strconv.Atoi(id[len(colPrefix):])

	return i, err == nil
}

// gridSource adapts the current grid to appkit.TableSource.
type gridSource struct{ p *ResultsPane }

func (s *gridSource) NumberOfRows() int {
	s.p.checkFontSize(false)

	return int(s.p.lines)
}

func (s *gridSource) CellText(row int, column string) appkit.CellText {
	g := s.p.grid
	if g == nil {
		return appkit.CellText{}
	}

	if column == gutterID {
		pos, err := g.Locate(int64(row))
		if err != nil || !pos.First() {
			return appkit.CellText{}
		}

		return appkit.CellText{Text: app.Count(uint64(pos.Row + 1)), Secondary: true, Right: true} //nolint:gosec // non-negative
	}

	i, ok := columnIndex(column)
	if !ok || i >= len(s.p.cols) {
		return appkit.CellText{}
	}

	if i == 0 {
		s.p.nearEnd(row)
	}

	cell, _, err := g.Cell(int64(row), i)
	if err != nil {
		return appkit.CellText{}
	}

	switch cell.Kind {
	case results.Null:
		return appkit.CellText{Text: "null", Secondary: true, Italic: true, Color: s.p.NullColor}
	case results.Value:
		c := appkit.CellText{Text: results.FormatCell(s.p.cols[i].Type, cell.V), Right: s.p.cols[i].Numeric()}
		if c.Right {
			c.Color = s.p.NumberColor
		}

		return c
	case results.Blank:
	}

	return appkit.CellText{}
}

func (s *gridSource) RowSeparator(row int) bool {
	if s.p.grid == nil {
		return false
	}

	pos, err := s.p.grid.Locate(int64(row))

	return err == nil && pos.Last()
}

// inspectLine opens the value inspector on the double-clicked cell.
func (p *ResultsPane) inspectLine(line int) {
	if p.grid == nil || p.Inspect == nil {
		return
	}

	pos, err := p.grid.Locate(int64(line))
	if err != nil {
		return
	}

	row, err := p.grid.Row(pos.Row)
	if err != nil {
		return
	}

	path := ""
	if i, ok := columnIndex(p.Table.ClickedColumn()); ok && i < len(p.cols) {
		path = results.ColumnPath(p.grid.Fields(), row, p.cols[i], pos.Sub)
	}

	p.Inspect(p.grid.Fields(), row, pos.Row, path)
}

// InspectCell opens the value inspector for (line, column index) (smoke).
func (p *ResultsPane) InspectCell(line, col int) {
	if p.grid == nil || p.Inspect == nil {
		return
	}

	pos, err := p.grid.Locate(int64(line))
	if err != nil {
		return
	}

	if row, err := p.grid.Row(pos.Row); err == nil {
		p.Inspect(p.grid.Fields(), row, pos.Row, results.ColumnPath(p.grid.Fields(), row, p.cols[col], pos.Sub))
	}
}

// copyLines renders selected visual lines as TSV, exactly as displayed.
func (p *ResultsPane) copyLines(lines []int) string {
	if p.grid == nil || len(lines) == 0 {
		return ""
	}

	if len(lines) > maxCopyLines {
		lines = lines[:maxCopyLines]
	}

	var b strings.Builder

	for i, c := range p.cols {
		if i > 0 {
			b.WriteByte('\t')
		}

		b.WriteString(c.Name)
	}

	b.WriteByte('\n')

	for _, line := range lines {
		for i, c := range p.cols {
			if i > 0 {
				b.WriteByte('\t')
			}

			if cell, _, err := p.grid.Cell(int64(line), i); err == nil {
				switch cell.Kind {
				case results.Value:
					b.WriteString(results.FormatCell(c.Type, cell.V))
				case results.Null:
					b.WriteString("null")
				case results.Blank:
				}
			}
		}

		b.WriteByte('\n')
	}

	return b.String()
}

// copyJSON renders the logical rows behind the selected lines as JSON lines.
func (p *ResultsPane) copyJSON(lines []int) string {
	if p.grid == nil {
		return ""
	}

	var (
		b    []byte
		seen = make(map[int64]bool)
	)

	for _, line := range lines {
		pos, err := p.grid.Locate(int64(line))
		if err != nil || seen[pos.Row] {
			continue
		}

		seen[pos.Row] = true

		if row, err := p.grid.Row(pos.Row); err == nil {
			b = results.AppendRowJSON(b, p.grid.Fields(), row)
			b = append(b, '\n')
		}
	}

	return string(b)
}

func (p *ResultsPane) menu(row int, column string) []appkit.MenuItem {
	if p.grid == nil || row < 0 {
		return nil
	}

	lines := p.Table.SelectedRows()
	if len(lines) == 0 || !slices.Contains(lines, row) {
		lines = []int{row}
	}

	items := []appkit.MenuItem{
		{Title: "Auto-fit Column Widths", Run: p.AutoFit},
		{},
		{Title: "Copy", Run: func() { appkit.CopyToPasteboard(p.copyLines(lines)) }},
		{Title: "Copy Rows as JSON", Run: func() { appkit.CopyToPasteboard(p.copyJSON(lines)) }},
	}

	if i, ok := columnIndex(column); ok && i < len(p.cols) {
		if cell, _, err := p.grid.Cell(int64(row), i); err == nil && cell.Kind == results.Value {
			value := results.FormatValue(p.cols[i].Type, cell.V)
			items = append(items, appkit.MenuItem{}, appkit.MenuItem{
				Title: "Copy Cell Value", Run: func() { appkit.CopyToPasteboard(value) },
			})
		}
	}

	if p.ExportMenu != nil {
		items = append(items, appkit.MenuItem{}, p.ExportMenu())
	}

	return items
}

// fetchAhead is how close (in lines) to the end of the loaded rows the grid
// asks for the next window.
const fetchAhead = 300

// nearEnd asks the run for more rows when the table draws a line close to
// the end of what is loaded (outside the drawing callback).
func (p *ResultsPane) nearEnd(line int) {
	if int64(line) >= p.lines-fetchAhead {
		p.requestMore()
	}
}

// requestMore asks the run for the next window (once at a time).
func (p *ResultsPane) requestMore() {
	if p.asking || p.run == nil || !p.snap.More || p.snap.LoadingMore {
		return
	}

	p.asking = true
	run := p.run

	objc.Main(func() {
		p.asking = false
		run.FetchMore(app.DefaultFetchWindow)
	})
}

// AutoFit re-sizes the columns to the header and the visible/sampled rows.
func (p *ResultsPane) AutoFit() {
	if p.grid != nil {
		p.Table.SetColumns(p.columns())
		p.Table.Reload()
	}
}

// LoadedRows returns loaded rows and whether more can be fetched (smoke).
func (p *ResultsPane) LoadedRows() (rows int64, more bool) {
	if p.grid == nil {
		return 0, false
	}

	rows, _, _ = p.grid.Size()

	return rows, p.snap.More
}

// ColumnWidth returns a result column's width (smoke checks).
func (p *ResultsPane) ColumnWidth(i int) float64 {
	col := objc.Msg(p.Table.View, "tableColumnWithIdentifier:", objc.String(colPrefix+strconv.Itoa(i)))
	if col == objc.Nil {
		return 0
	}

	return objc.Send[float64](col, "width")
}

// rowsText describes the loaded rows (and whether more can be fetched).
func (p *ResultsPane) rowsText() []string {
	s := p.snap

	var parts []string

	rows, _, _ := p.grid.Size()

	switch {
	case s.More || s.LoadingMore:
		text := "✓ " + app.CountInt(rows)
		if s.TotalRows > rows {
			text += " of " + app.CountInt(s.TotalRows)
		}

		text += " rows loaded"
		if s.LoadingMore {
			text += " · loading more…"
		} else {
			text += " · scroll for more"
		}

		parts = append(parts, text)
	default:
		parts = append(parts, "✓ "+app.Count(uint64(rows))+" rows") //nolint:gosec // non-negative
	}

	if s.Truncated {
		parts = append(parts, "results truncated")
	}

	if s.FetchErr != nil {
		parts = append(parts, "✕ loading more failed: "+app.Explain(s.FetchErr).Title)
	}

	return parts
}
