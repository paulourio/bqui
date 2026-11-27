//go:build darwin

package appkit

import (
	"reflect"
	"strings"
	"testing"

	"github.com/paulourio/bqui/internal/objc"
)

type stubTableSrc struct {
	cols map[string][]CellText
	rows int
}

func (s *stubTableSrc) NumberOfRows() int { return s.rows }

func (s *stubTableSrc) CellText(row int, column string) CellText {
	if vals, ok := s.cols[column]; ok && row >= 0 && row < len(vals) {
		return vals[row]
	}

	return CellText{}
}

func (*stubTableSrc) RowSeparator(int) bool { return true }

func TestTableWideColumnsCellsAndSelection(t *testing.T) {
	onMain(t, func() {
		objc.Msg(objc.Cls("NSApplication"), "sharedApplication")

		longJSON := `{"nested":{"items":[` + strings.Repeat(`"abcdefghij",`, 35) + `"end"]}}`
		if w := MeasureGridTextWidth(longJSON, true); w <= 2400 {
			t.Fatalf("expected longJSON mono width > 2400, got %v", w)
		}

		color := RGB(0.1, 0.5, 0.8, 1.0)
		src := &stubTableSrc{
			rows: 3,
			cols: map[string][]CellText{
				"row": {
					{Text: "1", Secondary: true, Right: true},
					{Text: "2", Secondary: true, Right: true},
					{Text: "3", Secondary: true, Right: true},
				},
				"json": {
					{
						Text: longJSON,
						Mono: true,
						Spans: []Span{
							{Text: longJSON[:20], Color: color},
							{Text: longJSON[20:]},
						},
					},
					{Text: "plain\nrow", Mono: false},
					{Text: "short", Mono: true},
				},
			},
		}

		tbl := NewTable(src, TableOptions{})
		win := NewWindow("table-test", MakeRect(0, 0, 800, 300))
		defer objc.Msg(win, "close")

		AddFilling(objc.Msg(win, "contentView"), tbl.Scroll, 0)

		// Rebuild columns multiple times to exercise SetColumns memory management.
		tbl.SetColumns([]TableColumn{{ID: "tmp", Title: "Tmp", Width: 100}})
		tbl.SetColumns([]TableColumn{
			{ID: "row", Title: "Row", Width: 56, MinWidth: 32},
			{ID: "json", Title: "JSON", Width: 12000, MinWidth: 200},
		})
		tbl.Reload()
		objc.Msg(objc.Msg(win, "contentView"), "layoutSubtreeIfNeeded")

		colIdx := objc.Send[int64](tbl.View, "columnWithIdentifier:", objc.String("json"))
		if colIdx < 0 {
			t.Fatal("missing json column")
		}

		col := objc.Msg(objc.Msg(tbl.View, "tableColumns"), "objectAtIndex:", uint64(colIdx))
		if gotW := objc.Send[float64](col, "width"); gotW != 12000 {
			t.Fatalf("json column width = %v, want 12000 (not clamped)", gotW)
		}

		// Verify live NSTableCellView rendering for >2400px attributed cell and plain cell.
		cv0 := objc.Msg(tbl.View, "viewAtColumn:row:makeIfNecessary:", colIdx, int64(0), true)
		if cv0 == objc.Nil {
			t.Fatal("row 0 cell view is nil")
		}

		tf0 := objc.Msg(cv0, "textField")
		if got := objc.GoString(objc.Msg(objc.Msg(tf0, "attributedStringValue"), "string")); got != longJSON {
			t.Errorf("row 0 attributedStringValue mismatch: len=%d want=%d", len(got), len(longJSON))
		}

		if !objc.Send[bool](tf0, "usesSingleLineMode") || objc.Send[int64](tf0, "maximumNumberOfLines") != 1 {
			t.Error("grid cell should enforce single-line mode with max 1 line")
		}

		if got := objc.Send[int64](tf0, "alignment"); got != TextAlignmentLeft {
			t.Errorf("left-aligned json cell alignment = %d, want %d", got, TextAlignmentLeft)
		}

		rowColIdx := objc.Send[int64](tbl.View, "columnWithIdentifier:", objc.String("row"))
		if rowColIdx < 0 {
			t.Fatal("missing row column")
		}

		cvRow := objc.Msg(tbl.View, "viewAtColumn:row:makeIfNecessary:", rowColIdx, int64(0), true)
		if cvRow == objc.Nil {
			t.Fatal("row gutter cell view is nil")
		}

		rowTF := objc.Msg(cvRow, "textField")
		if got := objc.Send[int64](rowTF, "alignment"); got != TextAlignmentRight {
			t.Errorf("right-aligned row cell alignment = %d, want %d", got, TextAlignmentRight)
		}

		cv1 := objc.Msg(tbl.View, "viewAtColumn:row:makeIfNecessary:", colIdx, int64(1), true)
		if cv1 == objc.Nil {
			t.Fatal("row 1 cell view is nil")
		}

		tf1 := objc.Msg(cv1, "textField")
		if got := StringValue(tf1); got != "plain\nrow" {
			t.Errorf("row 1 stringValue = %q, want %q", got, "plain\nrow")
		}

		if got := objc.Send[int64](tf1, "alignment"); got != TextAlignmentLeft {
			t.Errorf("left-aligned row 1 cell alignment = %d, want %d", got, TextAlignmentLeft)
		}

		if info := TableRenderedCell(tbl, 0, "json"); !info.Found || info.Text != longJSON || info.ColWidth != 12000 {
			t.Errorf("TableRenderedCell(0, json) = %+v", info)
		}

		tbl.SetColumnWidth("json", 15000)

		if gotW := objc.Send[float64](col, "width"); gotW != 15000 {
			t.Errorf("SetColumnWidth json width = %v, want 15000", gotW)
		}

		tbl.SelectRows(1, 2)

		if got := tbl.SelectedRows(); !reflect.DeepEqual(got, []int{1, 2}) {
			t.Errorf("SelectedRows = %v, want [1 2]", got)
		}
	})
}
