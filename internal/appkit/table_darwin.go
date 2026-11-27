//go:build darwin

package appkit

import (
	"github.com/paulourio/bqui/internal/objc"
)

// TableSource feeds a Table from Go. Rows are visual lines.
type TableSource interface {
	NumberOfRows() int
	CellText(row int, column string) CellText
	// RowSeparator reports whether a line ends a logical row (border below).
	RowSeparator(row int) bool
}

// TableColumn describes one column of a Table.
type TableColumn struct {
	ID       string
	Title    string
	ToolTip  string
	Width    float64
	MinWidth float64
}

// TableOptions configures a Table.
type TableOptions struct {
	// Copy returns the text for ⌘C / Edit ▸ Copy given the selected rows.
	Copy func(rows []int) string
	// Menu builds the context menu for the clicked row and column (-1 if none).
	Menu      func(row int, column string) []MenuItem
	RowHeight float64
	// OnDoubleClick receives the double-clicked row.
	OnDoubleClick func(row int)
	// OnSelect receives the selected rows after the selection changes.
	OnSelect func(rows []int)
}

// Table is a view-based NSTableView for large virtual grids.
type Table struct {
	Scroll objc.ID
	View   objc.ID

	src     TableSource
	opts    TableOptions
	targets []objc.ID
}

const (
	rowViewID                 = "BQUIGridRow"
	defaultRowHeight          = 22.0
	tableStylePlain   int64   = 4
	gridVerticalLines uint64  = 1
	boxCustom         uint64  = 4
	noTitle           uint64  = 0
	autoresizeWidth   uint64  = 1 << 1
	autoresizeMinY    uint64  = 1 << 3
	autoresizeMaxY    uint64  = 1 << 5
	columnNoAutoSize  uint64  = 0
	textAlignRight    int64   = 2
	italicFontMask    uint64  = 1
	gridFontSize      float64 = 12
)

var (
	tables          objc.Registry[*Table]
	tableByView     objc.Registry[*Table]
	rowSeparators   objc.Registry[objc.ID] // row view → its separator (NSView.tag is read-only)
	tableController = objc.LazyClass("BQUITableController", "NSObject",
		[]string{"NSTableViewDataSource", "NSTableViewDelegate", "NSMenuDelegate"}, tableMethods)
	gridTableClass = objc.LazyClass("BQUIGridTableView", "NSTableView", nil, func() []objc.Method {
		return []objc.Method{
			{Sel: "copy:", Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
				if t, ok := tableByView.Get(self); ok && t.opts.Copy != nil {
					if s := t.opts.Copy(t.SelectedRows()); s != "" {
						CopyToPasteboard(s)
					}
				}
			}},
			{Sel: "validateMenuItem:", Fn: func(self objc.ID, _ objc.SEL, item objc.ID) bool {
				if objc.Msg(item, "action") == objc.ID(objc.Sel("copy:")) {
					return objc.Send[int64](self, "numberOfSelectedRows") > 0
				}

				return true
			}},
		}
	})

	fontsOnce                         bool
	fontRegular, fontItalic, fontMono objc.ID
)

func gridFonts() (regular, italic, mono objc.ID) {
	if !fontsOnce {
		fontsOnce = true
		fontRegular = objc.Retain(objc.Msg(objc.Cls("NSFont"), "monospacedDigitSystemFontOfSize:weight:", gridFontSize, 0.0))
		fm := objc.Msg(objc.Cls("NSFontManager"), "sharedFontManager")
		fontItalic = objc.Retain(objc.Msg(fm, "convertFont:toHaveTrait:", fontRegular, italicFontMask))
		fontMono = objc.Retain(objc.Msg(objc.Cls("NSFont"), "monospacedSystemFontOfSize:weight:", monoFontSize, 0.0))
	}

	return fontRegular, fontItalic, fontMono
}

func tableMethods() []objc.Method {
	get := tables.Get

	return []objc.Method{
		{Sel: "numberOfRowsInTableView:", Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) int64 {
			if t, ok := get(self); ok {
				return int64(t.src.NumberOfRows())
			}

			return 0
		}},
		{Sel: "tableView:viewForTableColumn:row:", Fn: func(self objc.ID, _ objc.SEL, tv, col objc.ID, row int64) objc.ID {
			t, ok := get(self)
			if !ok || col == objc.Nil {
				return objc.Nil
			}

			c := t.src.CellText(int(row), objc.GoString(objc.Msg(col, "identifier")))

			return gridCell(tv, c)
		}},
		{Sel: "tableView:rowViewForRow:", Fn: func(self objc.ID, _ objc.SEL, tv objc.ID, row int64) objc.ID {
			t, ok := get(self)
			if !ok {
				return objc.Nil
			}

			return t.rowView(tv, int(row))
		}},
		{Sel: "menuNeedsUpdate:", Fn: func(self objc.ID, _ objc.SEL, menu objc.ID) {
			if t, ok := get(self); ok {
				t.rebuildMenu(menu)
			}
		}},
		{Sel: "tableViewSelectionDidChange:", Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
			if t, ok := get(self); ok && t.opts.OnSelect != nil {
				t.opts.OnSelect(t.SelectedRows())
			}
		}},
	}
}

// NewTable creates an empty grid table.
func NewTable(src TableSource, opts TableOptions) *Table {
	t := &Table{src: src, opts: opts}
	t.View = objc.Msg(objc.Msg(gridTableClass(), "alloc"), "init")
	tableByView.Put(t.View, t)

	h := opts.RowHeight
	if h <= 0 {
		h = defaultRowHeight
	}

	objc.Msg(t.View, "setStyle:", tableStylePlain)
	objc.Msg(t.View, "setRowHeight:", h)
	objc.Msg(t.View, "setGridStyleMask:", gridVerticalLines)
	objc.Msg(t.View, "setUsesAlternatingRowBackgroundColors:", false)
	objc.Msg(t.View, "setAllowsMultipleSelection:", true)
	objc.Msg(t.View, "setAllowsColumnReordering:", true)
	objc.Msg(t.View, "setAllowsColumnResizing:", true)
	objc.Msg(t.View, "setColumnAutoresizingStyle:", columnNoAutoSize)

	ctl := objc.Msg(objc.Msg(tableController(), "alloc"), "init")
	tables.Put(ctl, t)
	objc.Msg(t.View, "setDataSource:", ctl)
	objc.Msg(t.View, "setDelegate:", ctl)

	if opts.Menu != nil {
		menu := newMenu("")
		objc.Msg(menu, "setDelegate:", ctl)
		objc.Msg(t.View, "setMenu:", menu)
	}

	if opts.OnDoubleClick != nil {
		fn := opts.OnDoubleClick
		objc.Msg(t.View, "setTarget:", objc.NewTarget(func(objc.ID) {
			if row := objc.Send[int64](t.View, "clickedRow"); row >= 0 {
				fn(int(row))
			}
		}))
		objc.Msg(t.View, "setDoubleAction:", objc.Sel(objc.ActionSelector))
	}

	t.Scroll = objc.New("NSScrollView")
	objc.Msg(t.Scroll, "setDocumentView:", t.View)
	objc.Msg(t.Scroll, "setHasVerticalScroller:", true)
	objc.Msg(t.Scroll, "setHasHorizontalScroller:", true)
	objc.Msg(t.Scroll, "setAutohidesScrollers:", true)

	return t
}

// SetColumns replaces all columns.
func (t *Table) SetColumns(cols []TableColumn) {
	for _, c := range objc.ArrayItems(objc.Msg(objc.Msg(t.View, "tableColumns"), "copy")) {
		objc.Msg(t.View, "removeTableColumn:", c)
	}

	for _, c := range cols {
		col := objc.Msg(objc.Msg(objc.Cls("NSTableColumn"), "alloc"), "initWithIdentifier:", objc.String(c.ID))
		objc.Msg(objc.Msg(col, "headerCell"), "setStringValue:", objc.String(c.Title))

		if c.ToolTip != "" {
			objc.Msg(col, "setHeaderToolTip:", objc.String(c.ToolTip))
		}

		if c.Width > 0 {
			objc.Msg(col, "setWidth:", c.Width)
		}

		if c.MinWidth > 0 {
			objc.Msg(col, "setMinWidth:", c.MinWidth)
		}

		objc.Msg(t.View, "addTableColumn:", col)
		objc.Release(col)
	}
}

// Reload reloads everything.
func (t *Table) Reload() { objc.Msg(t.View, "reloadData") }

// NoteRowsChanged tells the table the row count changed (keeps scrolling).
func (t *Table) NoteRowsChanged() { objc.Msg(t.View, "noteNumberOfRowsChanged") }

// Rows returns the number of rows the table currently shows.
func (t *Table) Rows() int { return int(objc.Send[int64](t.View, "numberOfRows")) }

// ScrollToTop scrolls to the first row.
func (t *Table) ScrollToTop() {
	if t.Rows() > 0 {
		objc.Msg(t.View, "scrollRowToVisible:", int64(0))
	}
}

// ClickedColumn returns the identifier of the last clicked column ("" if none).
func (t *Table) ClickedColumn() string {
	i := objc.Send[int64](t.View, "clickedColumn")
	if i < 0 {
		return ""
	}

	c := objc.Msg(objc.Msg(t.View, "tableColumns"), "objectAtIndex:", uint64(i))

	return objc.GoString(objc.Msg(c, "identifier"))
}

// ScrollToColumn scrolls horizontally so the column is visible.
func (t *Table) ScrollToColumn(id string) {
	if i := objc.Send[int64](t.View, "columnWithIdentifier:", objc.String(id)); i >= 0 {
		objc.Msg(t.View, "scrollColumnToVisible:", i)
	}
}

// SelectedRows returns the selected row indexes, ascending.
func (t *Table) SelectedRows() []int {
	set := objc.Msg(t.View, "selectedRowIndexes")

	var out []int

	for i := objc.Send[uint64](set, "firstIndex"); i != nsNotFound; i = objc.Send[uint64](set, "indexGreaterThanIndex:", i) {
		out = append(out, int(i)) //nolint:gosec // row indexes fit in int
	}

	return out
}

// SelectRows selects rows [from, to].
func (t *Table) SelectRows(from, to int) {
	if from < 0 || to < from {
		return
	}

	set := objc.Msg(objc.Cls("NSIndexSet"), "indexSetWithIndexesInRange:",
		nsRange{Location: uint64(from), Length: uint64(to - from + 1)}) //nolint:gosec // checked non-negative
	objc.Msg(t.View, "selectRowIndexes:byExtendingSelection:", set, false)
}

// SeparatorGeometry reports, for the row view currently displaying row, the
// separator's frame and whether it is visible (smoke checks).
func (t *Table) SeparatorGeometry(row int) (rowView, separator Rect, visible bool) {
	rv := objc.Msg(t.View, "rowViewAtRow:makeIfNecessary:", int64(row), false)
	if rv == objc.Nil {
		return Rect{}, Rect{}, false
	}

	sep, ok := rowSeparators.Get(rv)
	if !ok {
		return objc.Send[Rect](rv, "bounds"), Rect{}, false
	}

	return objc.Send[Rect](rv, "bounds"), objc.Send[Rect](sep, "frame"), !objc.Send[bool](sep, "isHidden")
}

// VisibleText returns the cell text at (row, column) as the table shows it
// (smoke checks).
func (t *Table) VisibleText(row int, column string) CellText { return t.src.CellText(row, column) }

const nsNotFound = uint64(1<<63 - 1)

type nsRange struct {
	Location uint64
	Length   uint64
}

func (t *Table) rowView(tv objc.ID, row int) objc.ID {
	rv := objc.Msg(tv, "makeViewWithIdentifier:owner:", objc.String(rowViewID), objc.Nil)
	if rv == objc.Nil {
		// Create the row view at its final height so autoresizing never
		// moves the separator, and put the separator on the bottom edge in
		// the view's own coordinate system.
		const width = 100.0

		h := objc.Send[float64](tv, "rowHeight")
		rv = objc.Msg(objc.Msg(objc.Cls("NSTableRowView"), "alloc"), "initWithFrame:", MakeRect(0, 0, width, h))
		SetIdentifier(rv, rowViewID)

		y, mask := 0.0, autoresizeWidth|autoresizeMaxY
		if objc.Send[bool](rv, "isFlipped") {
			y, mask = h-1, autoresizeWidth|autoresizeMinY
		}

		// A custom box keeps its exact 1pt frame (the separator box style
		// forces 4pt and centers its line) and its dynamic fill color follows
		// light/dark appearance.
		sep := objc.Msg(objc.Msg(objc.Cls("NSBox"), "alloc"), "initWithFrame:", MakeRect(0, y, width, 1))
		objc.Msg(sep, "setBoxType:", boxCustom)
		objc.Msg(sep, "setTitlePosition:", noTitle)
		objc.Msg(sep, "setBorderWidth:", 0.0)
		objc.Msg(sep, "setFillColor:", objc.Msg(objc.Cls("NSColor"), "separatorColor"))
		objc.Msg(sep, "setFrame:", MakeRect(0, y, width, 1))
		objc.Msg(sep, "setAutoresizingMask:", mask)
		objc.Msg(rv, "addSubview:", sep)
		objc.Release(sep)
		rowSeparators.Put(rv, sep)
	}

	if sep, ok := rowSeparators.Get(rv); ok {
		objc.Msg(sep, "setHidden:", !t.src.RowSeparator(row))
	}

	return rv
}

func (t *Table) rebuildMenu(menu objc.ID) {
	for _, tg := range t.targets {
		objc.ReleaseTarget(tg)
	}

	t.targets = t.targets[:0]
	objc.Msg(menu, "removeAllItems")

	row := int(objc.Send[int64](t.View, "clickedRow"))
	colIdx := objc.Send[int64](t.View, "clickedColumn")
	col := ""

	if colIdx >= 0 {
		c := objc.Msg(objc.Msg(t.View, "tableColumns"), "objectAtIndex:", uint64(colIdx))
		col = objc.GoString(objc.Msg(c, "identifier"))
	}

	for _, it := range t.opts.Menu(row, col) {
		if it.separator() {
			objc.Msg(menu, "addItem:", objc.Msg(objc.Cls("NSMenuItem"), "separatorItem"))

			continue
		}

		mi := buildItem(it)
		if it.Run != nil {
			t.targets = append(t.targets, objc.Msg(mi, "target"))
		}

		objc.Msg(menu, "addItem:", mi)
	}
}

func gridCell(tv objc.ID, c CellText) objc.ID {
	cell := objc.Msg(tv, "makeViewWithIdentifier:owner:", objc.String(textCellID+"Grid"), objc.Nil)
	if cell == objc.Nil {
		cell = newTextCell(textCellID+"Grid", false, false)
		objc.Msg(objc.Msg(cell, "textField"), "setSelectable:", false)
	}

	tf := objc.Msg(cell, "textField")
	SetString(tf, c.Text)
	setSecondary(tf, c.Secondary)

	regular, italic, mono := gridFonts()

	switch {
	case c.Italic:
		objc.Msg(tf, "setFont:", italic)
	case c.Mono:
		objc.Msg(tf, "setFont:", mono)
	default:
		objc.Msg(tf, "setFont:", regular)
	}

	align := TextAlignmentLeft
	if c.Right {
		align = textAlignRight
	}

	objc.Msg(tf, "setAlignment:", align)

	return cell
}
