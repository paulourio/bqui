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
	jsonLineRunes     = 131072   // safety cap on single-line AppKit cell rendering
	jsonScanLimit     = 2000     // rows scanned on initial/streaming load for peak width
	copyAllCap        = 16 << 20 // Copy All as JSON stops here (use export)
	jsonMinWidth      = 600.0
	jsonColMinW       = 200.0
	monoMaxCharFactor = 2.0 // conservative upper-bound px/byte factor for lazy resize check
)

// JSONPane shows one compact JSON object per result row, encoded on demand
// from the grid's chunk cache; the whole result is never materialized.
type JSONPane struct {
	View  objc.ID
	Table *appkit.Table

	s            *Shell
	grid         *results.Grid
	rows         int64
	width        float64
	appliedWidth float64
	fontSize     float64
	note         objc.ID
	// colors by JSON token kind (theme); nil disables syntax coloring.
	colors map[results.JSONKind]objc.ID
}

func newJSONPane(s *Shell) *JSONPane {
	p := &JSONPane{
		s:            s,
		width:        jsonMinWidth,
		appliedWidth: jsonMinWidth,
		fontSize:     appkit.GridFontSize(),
	}
	p.Table = appkit.NewTable(jsonSource{p}, appkit.TableOptions{
		Copy: p.copyRows,
		Menu: p.menu,
		OnSelect: func(rows []int) {
			if len(rows) == 1 {
				p.inspect(rows[0])
			}
		},
	})
	p.Table.SetColumns(p.columns(jsonMinWidth))
	p.note = appkit.SecondaryLabel("Run a query to see its rows as JSON.")

	p.View = appkit.NewView()
	appkit.AddFilling(p.View, p.Table.Scroll, 0)
	appkit.AddCenteredWrapping(p.View, p.note, 16)
	appkit.SetHidden(p.Table.Scroll, true)

	return p
}

func (p *JSONPane) columns(jsonW float64) []appkit.TableColumn {
	return []appkit.TableColumn{
		{ID: gutterID, Title: "Row", Width: gutterWidth, MinWidth: 32},
		{ID: "json", Title: "JSON", Width: jsonW, MinWidth: jsonColMinW, MaxWidth: max(jsonW, 100_000.0)},
	}
}

// Update follows the current run's grid (main thread).
func (p *JSONPane) Update(snap app.RunSnapshot) {
	if snap.Grid != p.grid {
		p.grid, p.rows, p.width, p.appliedWidth = snap.Grid, 0, jsonMinWidth, jsonMinWidth
		p.fontSize = appkit.GridFontSize()
		appkit.SetHidden(p.Table.Scroll, p.grid == nil)
		appkit.SetHidden(p.note, p.grid != nil)

		if p.grid == nil {
			p.Table.SetColumns(p.columns(jsonMinWidth))
			p.Table.Reload()

			return
		}

		rows, _, _ := p.grid.Size()
		p.rows = rows

		if rows > 0 {
			p.width = p.sampleWidth(0, min(rows, jsonScanLimit))
			p.appliedWidth = p.width
		}

		p.Table.SetColumns(p.columns(p.width))
		p.Table.Reload()
		p.Table.ScrollToTop()

		return
	}

	if p.grid == nil {
		return
	}

	p.checkFontSize(true)

	if rows, _, _ := p.grid.Size(); rows != p.rows {
		prev := p.rows
		first := prev == 0
		p.rows = rows

		if prev < jsonScanLimit {
			if w := p.sampleWidth(prev, min(rows, jsonScanLimit)); w > p.width {
				p.width = w
				p.appliedWidth = w
				p.Table.SetColumnWidth("json", p.width)
			}
		}

		p.Table.NoteRowsChanged()

		if first {
			p.Table.ScrollToTop()
		}
	}
}

func (p *JSONPane) sampleWidth(from, to int64) float64 {
	w := jsonMinWidth
	directEnd := min(to, sampleLines)

	for i := from; i < directEnd; i++ {
		w = max(w, appkit.MeasureGridTextWidth(p.renderedLine(i), true)+colPadding)
	}

	if to <= directEnd {
		return w
	}

	var bestByteText, bestRuneText string

	var bestBytes, bestRunes int

	for i := max(from, directEnd); i < to; i++ {
		text := p.renderedLine(i)
		if len(text) > bestBytes {
			bestBytes = len(text)
			bestByteText = text
		}

		if runes := utf8.RuneCountInString(text); runes > bestRunes {
			bestRunes = runes
			bestRuneText = text
		}
	}

	if bestByteText != "" {
		w = max(w, appkit.MeasureGridTextWidth(bestByteText, true)+colPadding)
	}

	if bestRuneText != "" && bestRuneText != bestByteText {
		w = max(w, appkit.MeasureGridTextWidth(bestRuneText, true)+colPadding)
	}

	return w
}

func (p *JSONPane) checkFontSize(sync bool) {
	fs := appkit.GridFontSize()
	if fs <= 0 || fs == p.fontSize {
		return
	}

	p.fontSize = fs
	if p.grid == nil {
		return
	}

	w := jsonMinWidth
	if p.rows > 0 {
		w = p.sampleWidth(0, min(p.rows, jsonScanLimit))
	}

	p.width = w
	if sync {
		p.appliedWidth = w
		p.Table.SetColumnWidth("json", w)

		return
	}

	grid := p.grid
	objc.Main(func() {
		if p.grid == grid && p.fontSize == fs && p.width != p.appliedWidth {
			p.appliedWidth = p.width
			p.Table.SetColumnWidth("json", p.width)
		}
	})
}

func (p *JSONPane) maybeWiden(text string) {
	if text == "" || p.grid == nil {
		return
	}

	fs := p.fontSize
	if fs <= 0 {
		fs = appkit.GridFontSize()
	}

	if float64(len(text))*max(fs, 12.0)*monoMaxCharFactor+colPadding <= p.width {
		return
	}

	if w := appkit.MeasureGridTextWidth(text, true) + colPadding; w > p.width {
		p.width = w
		grid := p.grid
		objc.Main(func() {
			if p.grid == grid && p.width > p.appliedWidth {
				p.appliedWidth = p.width
				p.Table.SetColumnWidth("json", p.width)
			}
		})
	}
}

func (p *JSONPane) renderedLine(i int64) string {
	text := string(p.rowJSON(i))
	if len(text) <= jsonLineRunes || utf8.RuneCountInString(text) <= jsonLineRunes {
		return text
	}

	n := 0
	for idx := range text {
		if n == jsonLineRunes {
			return text[:idx] + " …"
		}

		n++
	}

	return text
}

// Rows reports how many rows the JSON view lists (smoke checks).
func (p *JSONPane) Rows() int64 { return p.rows }

// Line returns the JSON text of a row as displayed (smoke checks).
func (p *JSONPane) Line(i int) string { return jsonSource{p}.CellText(i, "json").Text }

// Cell returns a rendered JSON line with its colored spans (smoke checks).
func (p *JSONPane) Cell(i int) appkit.CellText { return jsonSource{p}.CellText(i, "json") }

// KeyColor is the theme color for object keys (smoke checks).
func (p *JSONPane) KeyColor() objc.ID { return p.colors[results.JSONKey] }

// NoteView returns the placeholder note label (smoke checks).
func (p *JSONPane) NoteView() objc.ID { return p.note }

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

func (s jsonSource) NumberOfRows() int {
	s.p.checkFontSize(false)

	return int(s.p.rows)
}

func (s jsonSource) RowSeparator(int) bool { return true }

func (s jsonSource) CellText(i int, column string) appkit.CellText {
	if s.p.grid == nil {
		return appkit.CellText{}
	}

	if column == gutterID {
		return appkit.CellText{Text: strconv.Itoa(i + 1), Secondary: true, Right: true}
	}

	// Near the end of the loaded rows, fetch the next window (panes built
	// without a shell, as in tests, have nothing to ask).
	if sh := s.p.s; sh != nil && sh.Results != nil && int64(i) >= s.p.rows-jsonFetchAhead {
		sh.Results.requestMore()
	}

	text := s.p.renderedLine(int64(i))
	s.p.maybeWiden(text)

	c := appkit.CellText{Text: text, Mono: true}

	if colors := s.p.colors; colors != nil {
		for _, sp := range results.JSONSpans(text) {
			c.Spans = append(c.Spans, appkit.Span{Text: text[sp.Start:sp.End], Color: colors[sp.Kind]})
		}
	}

	return c
}

const jsonFetchAhead = 100
