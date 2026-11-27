//go:build darwin

package ui

import (
	"strconv"
	"unicode/utf8"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/results"
)

const (
	jsonLineRunes = 4096     // characters shown per row
	copyAllCap    = 16 << 20 // Copy All as JSON stops here (use export)
)

// JSONPane shows one compact JSON object per result row, encoded on demand
// from the grid's chunk cache; the whole result is never materialized.
type JSONPane struct {
	View  objc.ID
	Table *appkit.Table

	s    *Shell
	grid *results.Grid
	rows int64
	note objc.ID
	// colors by JSON token kind (theme); nil disables syntax coloring.
	colors map[results.JSONKind]objc.ID
}

func newJSONPane(s *Shell) *JSONPane {
	p := &JSONPane{s: s}
	p.Table = appkit.NewTable(jsonSource{p}, appkit.TableOptions{
		Copy: p.copyRows,
		Menu: p.menu,
		OnSelect: func(rows []int) {
			if len(rows) == 1 {
				p.inspect(rows[0])
			}
		},
	})
	p.Table.SetColumns([]appkit.TableColumn{
		{ID: gutterID, Title: "Row", Width: gutterWidth, MinWidth: 32},
		{ID: "json", Title: "JSON", Width: 2400, MinWidth: 200},
	})
	p.note = appkit.SecondaryLabel("Run a query to see its rows as JSON.")

	p.View = appkit.NewView()
	appkit.AddFilling(p.View, p.Table.Scroll, 0)
	appkit.AddCentered(p.View, p.note)
	appkit.SetHidden(p.Table.Scroll, true)

	return p
}

// Update follows the current run's grid (main thread).
func (p *JSONPane) Update(snap app.RunSnapshot) {
	if snap.Grid != p.grid {
		p.grid, p.rows = snap.Grid, 0
		p.Table.Reload()
		appkit.SetHidden(p.Table.Scroll, p.grid == nil)
		appkit.SetHidden(p.note, p.grid != nil)
	}

	if p.grid == nil {
		return
	}

	if rows, _, _ := p.grid.Size(); rows != p.rows {
		p.rows = rows
		p.Table.NoteRowsChanged()
	}
}

// Rows reports how many rows the JSON view lists (smoke checks).
func (p *JSONPane) Rows() int64 { return p.rows }

// Line returns the JSON text of a row as displayed (smoke checks).
func (p *JSONPane) Line(i int) string { return jsonSource{p}.CellText(i, "json").Text }

// Cell returns a rendered JSON line with its colored spans (smoke checks).
func (p *JSONPane) Cell(i int) appkit.CellText { return jsonSource{p}.CellText(i, "json") }

// KeyColor is the theme color for object keys (smoke checks).
func (p *JSONPane) KeyColor() objc.ID { return p.colors[results.JSONKey] }

func (p *JSONPane) rowJSON(i int64) []byte {
	row, err := p.grid.Row(i)
	if err != nil {
		return nil
	}

	return results.AppendRowJSON(nil, p.grid.Fields(), row)
}

func (p *JSONPane) inspect(i int) {
	if p.grid == nil {
		return
	}

	if row, err := p.grid.Row(int64(i)); err == nil {
		p.s.Inspector.ShowRow(p.grid.Fields(), row, int64(i), "")
	}
}

func (p *JSONPane) copyRows(rows []int) string {
	var b []byte

	for _, i := range rows {
		b = append(append(b, p.rowJSON(int64(i))...), '\n')
	}

	return string(b)
}

// copyAll copies every row as JSON lines, up to copyAllCap.
func (p *JSONPane) copyAll() {
	if p.grid == nil {
		return
	}

	var b []byte

	for i := range p.rows {
		b = append(append(b, p.rowJSON(i)...), '\n')

		if len(b) > copyAllCap {
			p.s.status("Copy All stopped at " + app.Count(uint64(i+1)) + " rows (16 MiB); use Export for the full result.")
			appkit.CopyToPasteboard(string(b))

			return
		}
	}

	appkit.CopyToPasteboard(string(b))
}

func (p *JSONPane) menu(row int, _ string) []appkit.MenuItem {
	if p.grid == nil {
		return nil
	}

	items := []appkit.MenuItem{{Title: "Copy All as JSON", Run: p.copyAll}}

	if row >= 0 {
		items = append([]appkit.MenuItem{
			{Title: "Copy Row", Run: func() { appkit.CopyToPasteboard(string(p.rowJSON(int64(row)))) }},
			{Title: "Inspect Row", Run: func() { p.inspect(row) }},
			{},
		}, items...)
	}

	return items
}

type jsonSource struct{ p *JSONPane }

func (s jsonSource) NumberOfRows() int { return int(s.p.rows) }

func (s jsonSource) RowSeparator(int) bool { return true }

func (s jsonSource) CellText(i int, column string) appkit.CellText {
	if s.p.grid == nil {
		return appkit.CellText{}
	}

	if column == gutterID {
		return appkit.CellText{Text: strconv.Itoa(i + 1), Secondary: true, Right: true}
	}

	text := string(s.p.rowJSON(int64(i)))
	if utf8.RuneCountInString(text) > jsonLineRunes {
		text = string([]rune(text)[:jsonLineRunes]) + " …"
	}

	c := appkit.CellText{Text: text, Mono: true}

	if colors := s.p.colors; colors != nil {
		for _, sp := range results.JSONSpans(text) {
			c.Spans = append(c.Spans, appkit.Span{Text: text[sp.Start:sp.End], Color: colors[sp.Kind]})
		}
	}

	return c
}
