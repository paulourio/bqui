//go:build darwin

package appkit

import (
	"github.com/paulourio/bqui/internal/objc"
)

// OutlineSource feeds an NSOutlineView from Go. Items are identified by
// non-zero uint64 handles (0 is the invisible root). Handles must stay below
// 2^55 so their NSNumber boxes are tagged pointers (identity-stable and free
// of lifetime management).
type OutlineSource interface {
	Children(parent uint64) []uint64
	Expandable(item uint64) bool
	Cell(item uint64) OutlineCell
}

// ColumnSource supplies text for columns other than the outline column.
type ColumnSource interface {
	CellText(item uint64, column string) CellText
}

// OutlineCell is the content of the outline (first) column.
type OutlineCell struct {
	Title     string
	Symbol    string // SF Symbol name, optional
	ToolTip   string
	Group     bool // source-list section header
	Secondary bool // dimmed text (placeholders)
	Wrap      bool // multi-line wrapping (needs OutlineOptions.AutomaticRowHeights)
	CharWrap  bool // character-wrap unbroken tokens when Wrap is true
}

// CellText is the content of a plain text column.
type CellText struct {
	Text      string
	Secondary bool
	Mono      bool
	Wrap      bool // multi-line (needs OutlineOptions.AutomaticRowHeights)
	CharWrap  bool // character-wrap unbroken tokens when Wrap is true
	Italic    bool // grid cells (NULL)
	Right     bool // right-aligned (numbers)
	ToolTip   string
	// Color overrides the text color (grid cells; nil = system colors).
	Color objc.ID
	// Spans, if set, replace Text with colored runs (grid cells).
	Spans []Span
}

// Span is a run of colored text (nil Color = label color).
type Span struct {
	Text  string
	Color objc.ID
}

// Column describes one outline column. The first column is the outline
// (disclosure) column.
type Column struct {
	ID       string
	Title    string
	Width    float64
	MinWidth float64
}

// OutlineOptions configures an Outline.
type OutlineOptions struct {
	Columns       []Column // empty: one headerless column
	OnSelect      func(item uint64)
	OnExpand      func(item uint64) // before an item expands (user or programmatic)
	OnDoubleClick func(item uint64)
	// Menu builds the context menu for the clicked item (0 = empty area).
	Menu        func(item uint64) []MenuItem
	Alternating bool // alternating row backgrounds
	// AutomaticRowHeights sizes each row to its cell's Auto Layout height
	// (multi-line values). Row heights cannot come from a Go delegate:
	// purego callbacks cannot return floating-point values.
	AutomaticRowHeights bool
	// NoHorizontalScroll disables the horizontal scrollbar even with multiple columns.
	NoHorizontalScroll bool
}

// Outline is a Go-backed NSOutlineView inside an NSScrollView.
type Outline struct {
	Scroll objc.ID
	View   objc.ID

	src           OutlineSource
	opts          OutlineOptions
	targets       []objc.ID // context-menu targets of the last built menu
	resizePending bool
	expanding     bool
}

const (
	outlineCellIconID  = "BQUIOutlineCellIcon"
	outlineCellPlainID = "BQUIOutlineCellPlain"
	textCellID         = "BQUITextCell"

	columnAutoresizeLastOnly    uint64 = 4
	columnResizeUserAndAuto     uint64 = 3
	defaultColumnWidth                 = 200.0
	monoFontSize                       = 11.0
	notificationObjectKeyString        = "NSObject"
	cellPad                            = 3.0
)

var (
	outlines     objc.Registry[*Outline]
	outlineClass = objc.LazyClass("BQUIOutlineController", "NSObject",
		[]string{"NSOutlineViewDataSource", "NSOutlineViewDelegate", "NSMenuDelegate"}, outlineMethods)
)

func outlineMethods() []objc.Method {
	get := outlines.Get

	return []objc.Method{
		{Sel: "outlineView:numberOfChildrenOfItem:", Fn: func(self objc.ID, _ objc.SEL, _, item objc.ID) int64 {
			defer enterDelegate()()

			o, ok := get(self)
			if !ok {
				return 0
			}

			return int64(len(o.src.Children(objc.Uint64(item))))
		}},
		{Sel: "outlineView:child:ofItem:", Fn: func(self objc.ID, _ objc.SEL, _ objc.ID, index int64, item objc.ID) objc.ID {
			defer enterDelegate()()

			o, ok := get(self)
			if !ok {
				return objc.Nil
			}

			kids := o.src.Children(objc.Uint64(item))
			if index < 0 || int(index) >= len(kids) {
				return objc.Nil
			}

			return objc.Number(kids[index])
		}},
		{Sel: "outlineView:isItemExpandable:", Fn: func(self objc.ID, _ objc.SEL, _, item objc.ID) bool {
			defer enterDelegate()()

			o, ok := get(self)

			return ok && o.src.Expandable(objc.Uint64(item))
		}},
		{Sel: "outlineView:isGroupItem:", Fn: func(self objc.ID, _ objc.SEL, _, item objc.ID) bool {
			defer enterDelegate()()

			o, ok := get(self)

			return ok && !o.expanding && o.src.Cell(objc.Uint64(item)).Group
		}},
		{Sel: "outlineView:viewForTableColumn:item:", Fn: func(self objc.ID, _ objc.SEL, ov, col, item objc.ID) objc.ID {
			defer enterDelegate()()

			o, ok := get(self)
			if !ok {
				return objc.Nil
			}

			return o.viewFor(ov, col, objc.Uint64(item))
		}},
		{Sel: "outlineViewSelectionDidChange:", Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
			defer enterDelegate()()

			if o, ok := get(self); ok && o.opts.OnSelect != nil {
				// Run after AppKit finishes delivering the selection change.
				objc.Main(func() { o.opts.OnSelect(o.Selected()) })
			}
		}},
		{Sel: "outlineViewItemWillExpand:", Fn: func(self objc.ID, _ objc.SEL, notif objc.ID) {
			defer enterDelegate()()

			if o, ok := get(self); ok {
				o.onItemWillExpand(notif)
			}
		}},
		{Sel: "outlineViewColumnDidResize:", Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
			defer enterDelegate()()

			if o, ok := get(self); ok {
				o.onColumnDidResize()
			}
		}},
		{Sel: "menuNeedsUpdate:", Fn: func(self objc.ID, _ objc.SEL, menu objc.ID) {
			defer enterDelegate()()

			if o, ok := get(self); ok {
				o.rebuildMenu(menu)
			}
		}},
	}
}

// NewOutline creates an outline view fed by src.
func NewOutline(src OutlineSource, opts OutlineOptions) *Outline {
	o := &Outline{src: src, opts: opts}
	o.View = objc.New("NSOutlineView")

	cols := opts.Columns
	if len(cols) == 0 {
		cols = []Column{{ID: "main"}}
		objc.Msg(o.View, "setHeaderView:", objc.Nil)
	}

	for i, c := range cols {
		col := objc.Msg(objc.Msg(objc.Cls("NSTableColumn"), "alloc"), "initWithIdentifier:", objc.String(c.ID))
		objc.Msg(objc.Msg(col, "headerCell"), "setStringValue:", objc.String(c.Title))
		objc.Msg(col, "setResizingMask:", columnResizeUserAndAuto)

		if c.Width > 0 {
			objc.Msg(col, "setWidth:", c.Width)
		} else if len(cols) > 1 {
			objc.Msg(col, "setWidth:", defaultColumnWidth)
		}

		if c.MinWidth > 0 {
			objc.Msg(col, "setMinWidth:", c.MinWidth)
		}

		objc.Msg(o.View, "addTableColumn:", col)

		if i == 0 {
			objc.Msg(o.View, "setOutlineTableColumn:", col)
		}

		objc.Release(col)
	}

	objc.Msg(o.View, "setColumnAutoresizingStyle:", columnAutoresizeLastOnly)
	if len(opts.Columns) > 1 {
		objc.Msg(o.View, "setStyle:", tableStylePlain)
		objc.Msg(o.View, "setIntercellSpacing:", Size{Width: 3, Height: 2})
	}
	objc.Msg(o.View, "setFloatsGroupRows:", false)
	objc.Msg(o.View, "setUsesAlternatingRowBackgroundColors:", opts.Alternating)
	objc.Msg(o.View, "setAutoresizesOutlineColumn:", false)
	objc.Msg(o.View, "setUsesAutomaticRowHeights:", opts.AutomaticRowHeights)

	ctl := objc.Msg(objc.Msg(outlineClass(), "alloc"), "init")
	outlines.Put(ctl, o)
	objc.Msg(o.View, "setDataSource:", ctl)
	objc.Msg(o.View, "setDelegate:", ctl)

	if opts.OnDoubleClick != nil {
		fn := opts.OnDoubleClick
		objc.Msg(o.View, "setTarget:", objc.NewTarget(func(objc.ID) {
			row := objc.Send[int64](o.View, "clickedRow")
			if row >= 0 {
				fn(objc.Uint64(objc.Msg(o.View, "itemAtRow:", row)))
			}
		}))
		objc.Msg(o.View, "setDoubleAction:", objc.Sel(objc.ActionSelector))
	}

	if opts.Menu != nil {
		menu := newMenu("")
		objc.Msg(menu, "setDelegate:", ctl)
		objc.Msg(o.View, "setMenu:", menu)
	}

	o.Scroll = objc.New("NSScrollView")
	objc.Msg(o.Scroll, "setDocumentView:", o.View)
	objc.Msg(o.Scroll, "setHasVerticalScroller:", true)
	objc.Msg(o.Scroll, "setHasHorizontalScroller:", len(cols) > 1 && !opts.NoHorizontalScroll)
	objc.Msg(o.Scroll, "setAutohidesScrollers:", true)
	objc.Msg(o.Scroll, "setDrawsBackground:", false)

	return o
}

// InvalidateRowHeights notifies the outline view that automatic row heights
// need recalculation and forces a layout pass.
func (o *Outline) InvalidateRowHeights() {
	if o == nil || o.View == objc.Nil {
		return
	}

	checkReentrancy("outline note heights")

	n := o.Rows()
	if n <= 0 {
		return
	}

	set := objc.Msg(objc.Cls("NSIndexSet"), "indexSetWithIndexesInRange:", nsRange{Location: 0, Length: uint64(n)})
	objc.Msg(o.View, "noteHeightOfRowsWithIndexesChanged:", set)
	objc.Msg(o.View, "layoutSubtreeIfNeeded")
}

func (o *Outline) onColumnDidResize() {
	if !o.opts.AutomaticRowHeights || o.resizePending {
		return
	}

	o.resizePending = true
	objc.Main(func() {
		o.resizePending = false
		o.InvalidateRowHeights()
	})
}

func (o *Outline) onItemWillExpand(notif objc.ID) {
	if o.opts.OnExpand == nil {
		return
	}

	info := objc.Msg(notif, "userInfo")
	item := objc.Msg(info, "objectForKey:", objc.String(notificationObjectKeyString))
	o.opts.OnExpand(objc.Uint64(item))
}

// Reload reloads item and its children (0 reloads everything). Expanded
// descendants stay expanded because item handles are identity-stable.
func (o *Outline) Reload(item uint64) {
	checkReentrancy("outline reload")

	if item == 0 {
		objc.Msg(o.View, "reloadData")

		return
	}

	objc.Msg(o.View, "reloadItem:reloadChildren:", objc.Number(item), true)
}

// Expand expands item; recursive also expands its descendants. Item 0 is the
// root, so Expand(0, true) expands the whole tree.
func (o *Outline) Expand(item uint64, recursive bool) {
	if o == nil || o.View == objc.Nil {
		return
	}

	checkReentrancy("outline expand")

	target := objc.Nil
	if item != 0 {
		target = objc.Number(item)
	}

	if o.opts.AutomaticRowHeights {
		objc.Msg(o.View, "setUsesAutomaticRowHeights:", false)
	}

	o.expanding = true
	objc.Msg(o.View, "expandItem:expandChildren:", target, recursive)
	o.expanding = false

	if o.opts.AutomaticRowHeights {
		objc.Msg(o.View, "setUsesAutomaticRowHeights:", true)
		o.InvalidateRowHeights()
	}
}

// ExpandAll expands each item in order, toggling automatic row heights and
// invalidating row heights once after all expansions complete.
func (o *Outline) ExpandAll(items []uint64) {
	if o == nil || o.View == objc.Nil || len(items) == 0 {
		return
	}

	checkReentrancy("outline expand")

	if o.opts.AutomaticRowHeights {
		objc.Msg(o.View, "setUsesAutomaticRowHeights:", false)
	}

	o.expanding = true
	for _, item := range items {
		target := objc.Nil
		if item != 0 {
			target = objc.Number(item)
		}

		objc.Msg(o.View, "expandItem:expandChildren:", target, false)
	}
	o.expanding = false

	if o.opts.AutomaticRowHeights {
		objc.Msg(o.View, "setUsesAutomaticRowHeights:", true)
		o.InvalidateRowHeights()
	}
}

// IsExpanded reports whether item is expanded.
func (o *Outline) IsExpanded(item uint64) bool {
	return objc.Send[bool](o.View, "isItemExpanded:", objc.Number(item))
}

// Rows returns the number of visible rows.
func (o *Outline) Rows() int64 {
	return objc.Send[int64](o.View, "numberOfRows")
}

// Select selects item's row (if visible) and scrolls to it.
func (o *Outline) Select(item uint64) bool {
	checkReentrancy("outline select")

	row := objc.Send[int64](o.View, "rowForItem:", objc.Number(item))
	if row < 0 {
		return false
	}

	set := objc.Msg(objc.Cls("NSIndexSet"), "indexSetWithIndex:", uint64(row))
	objc.Msg(o.View, "selectRowIndexes:byExtendingSelection:", set, false)
	objc.Msg(o.View, "scrollRowToVisible:", row)

	return true
}

// Selected returns the selected item handle, or 0.
func (o *Outline) Selected() uint64 {
	row := objc.Send[int64](o.View, "selectedRow")
	if row < 0 {
		return 0
	}

	return objc.Uint64(objc.Msg(o.View, "itemAtRow:", row))
}

func (o *Outline) rebuildMenu(menu objc.ID) {
	for _, t := range o.targets {
		objc.ReleaseTarget(t)
	}

	o.targets = o.targets[:0]
	objc.Msg(menu, "removeAllItems")

	item := uint64(0)
	if row := objc.Send[int64](o.View, "clickedRow"); row >= 0 {
		item = objc.Uint64(objc.Msg(o.View, "itemAtRow:", row))
	}

	for _, it := range o.opts.Menu(item) {
		if it.separator() {
			objc.Msg(menu, "addItem:", objc.Msg(objc.Cls("NSMenuItem"), "separatorItem"))

			continue
		}

		mi := buildItem(it)
		if it.Run != nil {
			o.targets = append(o.targets, objc.Msg(mi, "target"))
		}

		objc.Msg(menu, "addItem:", mi)
	}
}

func (o *Outline) viewFor(ov, col objc.ID, item uint64) objc.ID {
	colID := ""
	if col != objc.Nil {
		colID = objc.GoString(objc.Msg(col, "identifier"))
	}

	outlineCol := objc.Msg(ov, "outlineTableColumn")
	if col == objc.Nil || col == outlineCol {
		return o.outlineCell(ov, o.src.Cell(item))
	}

	cs, ok := o.src.(ColumnSource)
	if !ok {
		return objc.Nil
	}

	return textCell(ov, cs.CellText(item, colID), o.opts.AutomaticRowHeights)
}

func (o *Outline) outlineCell(ov objc.ID, c OutlineCell) objc.ID {
	auto := o.opts.AutomaticRowHeights
	hasIcon := c.Symbol != "" && !c.Group

	id := outlineCellPlainID
	if hasIcon {
		id = outlineCellIconID
	}

	if c.Wrap {
		id += "Wrap"
		if c.CharWrap {
			id += "Char"
		}
	}

	if auto {
		id += "Auto"
	}

	cell := objc.Msg(ov, "makeViewWithIdentifier:owner:", objc.String(id), objc.Nil)
	if cell == objc.Nil {
		cell = newOutlineCellWithBreak(id, hasIcon, c.Wrap, c.CharWrap, auto)
	}

	tf := objc.Msg(cell, "textField")
	SetString(tf, c.Title)
	setSecondary(tf, c.Secondary)

	if hasIcon {
		if img := objc.Msg(cell, "imageView"); img != objc.Nil {
			objc.Msg(img, "setImage:", SymbolImage(c.Symbol, c.Title))
			objc.Msg(img, "setHidden:", false)
		}
	}

	objc.Msg(cell, "setToolTip:", stringOrNil(c.ToolTip))

	return cell
}

// newOutlineCell builds an outline cell, omitting the leading icon view and its
// gutter when hasIcon is false.
func newOutlineCell(id string, hasIcon, wrap, auto bool) objc.ID {
	return newOutlineCellWithBreak(id, hasIcon, wrap, false, auto)
}

func newOutlineCellWithBreak(id string, hasIcon, wrap, charWrap, auto bool) objc.ID {
	const iconSize, gap = 16.0, 6.0

	cell := objc.New("NSTableCellView")
	SetIdentifier(cell, id)

	var tf objc.ID
	if wrap {
		mode := lineBreakByWordWrapping
		if charWrap {
			mode = lineBreakByCharWrapping
		}

		tf = newWrappingCellField(false, mode)
	} else {
		tf = Label("", 0)
	}

	UseAutoLayout(tf)
	SetHuggingPriority(tf, OrientVertical, PriorityDefaultLow)
	objc.Msg(cell, "addSubview:", tf)
	objc.Msg(cell, "setTextField:", tf)

	constraints := []objc.ID{
		Equal(Anchor(tf, "trailingAnchor"), Anchor(cell, "trailingAnchor"), -2),
	}

	if hasIcon {
		img := objc.New("NSImageView")
		UseAutoLayout(img)
		objc.Msg(cell, "addSubview:", img)
		objc.Msg(cell, "setImageView:", img)
		objc.Release(img)

		constraints = append(constraints,
			Equal(Anchor(img, "leadingAnchor"), Anchor(cell, "leadingAnchor"), 2),
			EqualConst(Anchor(img, "widthAnchor"), iconSize),
			EqualConst(Anchor(img, "heightAnchor"), iconSize),
			Equal(Anchor(tf, "leadingAnchor"), Anchor(img, "trailingAnchor"), gap),
		)

		if auto {
			constraints = append(constraints, Equal(Anchor(img, "topAnchor"), Anchor(cell, "topAnchor"), cellPad))
		} else {
			constraints = append(constraints, Equal(Anchor(img, "centerYAnchor"), Anchor(cell, "centerYAnchor"), 0))
		}
	} else {
		constraints = append(constraints, Equal(Anchor(tf, "leadingAnchor"), Anchor(cell, "leadingAnchor"), 2))
	}

	if auto {
		constraints = append(constraints,
			Equal(Anchor(tf, "topAnchor"), Anchor(cell, "topAnchor"), cellPad),
			Equal(Anchor(tf, "bottomAnchor"), Anchor(cell, "bottomAnchor"), -cellPad),
		)
	} else {
		constraints = append(constraints, Equal(Anchor(tf, "centerYAnchor"), Anchor(cell, "centerYAnchor"), 0))
	}

	Activate(constraints...)

	return objc.Msg(cell, "autorelease")
}

func textCell(ov objc.ID, c CellText, auto bool) objc.ID {
	id := textCellID
	if c.Wrap {
		id += "Wrap"
		if c.CharWrap {
			id += "Char"
		}
	}

	if auto {
		id += "Auto"
	}

	cell := objc.Msg(ov, "makeViewWithIdentifier:owner:", objc.String(id), objc.Nil)
	if cell == objc.Nil {
		cell = newTextCellWithBreak(id, c.Wrap, c.CharWrap, auto)
	}

	tf := objc.Msg(cell, "textField")
	SetString(tf, c.Text)
	setSecondary(tf, c.Secondary)

	font := objc.Msg(objc.Cls("NSFont"), "systemFontOfSize:", objc.Send[float64](objc.Cls("NSFont"), "systemFontSize"))
	if c.Mono {
		font = objc.Msg(objc.Cls("NSFont"), "monospacedSystemFontOfSize:weight:", monoFontSize, 0.0)
	}

	objc.Msg(tf, "setFont:", font)

	align := TextAlignmentLeft
	if c.Right {
		align = TextAlignmentRight
	}

	objc.Msg(tf, "setAlignment:", align)
	objc.Msg(cell, "setToolTip:", stringOrNil(c.ToolTip))

	return cell
}

func newTextCell(id string, wrap, auto bool) objc.ID {
	return newTextCellWithBreak(id, wrap, false, auto)
}

func newTextCellWithBreak(id string, wrap, charWrap, auto bool) objc.ID {
	cell := objc.New("NSTableCellView")
	SetIdentifier(cell, id)

	var tf objc.ID
	if wrap {
		mode := lineBreakByWordWrapping
		if charWrap {
			mode = lineBreakByCharWrapping
		}

		tf = newWrappingCellField(true, mode)
	} else {
		tf = Label("", 0)
		objc.Msg(tf, "setSelectable:", true)
	}

	UseAutoLayout(tf)
	SetHuggingPriority(tf, OrientVertical, PriorityDefaultLow)
	objc.Msg(cell, "addSubview:", tf)
	objc.Msg(cell, "setTextField:", tf)

	constraints := []objc.ID{
		Equal(Anchor(tf, "leadingAnchor"), Anchor(cell, "leadingAnchor"), 2),
		Equal(Anchor(tf, "trailingAnchor"), Anchor(cell, "trailingAnchor"), -2),
	}

	if auto {
		constraints = append(constraints,
			Equal(Anchor(tf, "topAnchor"), Anchor(cell, "topAnchor"), cellPad),
			Equal(Anchor(tf, "bottomAnchor"), Anchor(cell, "bottomAnchor"), -cellPad),
		)
	} else {
		constraints = append(constraints, Equal(Anchor(tf, "centerYAnchor"), Anchor(cell, "centerYAnchor"), 0))
	}

	Activate(constraints...)

	return objc.Msg(cell, "autorelease")
}

func newWrappingCellField(selectable bool, lineBreak uint64) objc.ID {
	tf := objc.Msg(objc.Cls("NSTextField"), "wrappingLabelWithString:", objc.String(""))
	objc.Msg(tf, "setSelectable:", selectable)
	objc.Msg(tf, "setLineBreakMode:", lineBreak)
	objc.Msg(tf, "setMaximumNumberOfLines:", int64(0))
	objc.Msg(tf, "setUsesSingleLineMode:", false)
	SetCompressionResistance(tf, OrientHorizontal, PriorityDefaultLow)
	SetCompressionResistance(tf, OrientVertical, PriorityDefaultHigh)

	return tf
}

func setSecondary(tf objc.ID, secondary bool) {
	color := "labelColor"
	if secondary {
		color = "secondaryLabelColor"
	}

	objc.Msg(tf, "setTextColor:", objc.Msg(objc.Cls("NSColor"), color))
}

func stringOrNil(s string) objc.ID {
	if s == "" {
		return objc.Nil
	}

	return objc.String(s)
}
