//go:build darwin

package appkit

import (
	"math"

	"github.com/paulourio/bqui/internal/objc"
)

const (
	measureMaxExtent     = 1e7
	stringDrawingOptions = uint64(1 | 2) // NSStringDrawingUsesLineFragmentOrigin | NSStringDrawingUsesFontLeading
)

// RowCellGeom describes the live geometry and wrapping state of an Outline cell.
type RowCellGeom struct {
	Found          bool
	RowBounds      Rect
	CellFrame      Rect
	TextFrame      Rect
	Text           string
	SingleLineSize Size
	WrappedSize    Size
	LineBreakMode  int
	MaxLines       int
	UsesSingleLine bool
}

// RenderedCellInfo describes a live rendered cell in a Table.
type RenderedCellInfo struct {
	Found    bool
	Hidden   bool
	Text     string
	Frame    Rect
	Bounds   Rect
	ColWidth float64
}

// SegmentGeom describes the computed geometry and measured label width of one segment.
type SegmentGeom struct {
	Label     string
	Frame     Rect
	TextWidth float64
}

// ViewBounds returns view's bounds rect, or zero if view is nil.
func ViewBounds(view objc.ID) Rect {
	if view == objc.Nil {
		return Rect{}
	}

	return objc.Send[Rect](view, "bounds")
}

// ViewFrameIn converts view's bounds into ancestor's coordinate space.
func ViewFrameIn(view, ancestor objc.ID) Rect {
	if view == objc.Nil {
		return Rect{}
	}

	return objc.Send[Rect](view, "convertRect:toView:", ViewBounds(view), ancestor)
}

// IsVisible reports whether view is non-nil and not hidden (nor inside a hidden ancestor).
func IsVisible(view objc.ID) bool {
	return view != objc.Nil && !objc.Send[bool](view, "isHiddenOrHasHiddenAncestor")
}

// MeasureText measures an NSTextField's attributedStringValue (or an
// NSAttributedString directly) constrained to maxWidth. When maxWidth <= 0,
// text is measured on a single unconstrained line.
func MeasureText(textFieldOrAttr objc.ID, maxWidth float64) Size {
	if textFieldOrAttr == objc.Nil {
		return Size{}
	}

	attr := textFieldOrAttr
	if objc.RespondsTo(textFieldOrAttr, "attributedStringValue") {
		attr = objc.Msg(textFieldOrAttr, "attributedStringValue")
	}

	return measureAttrString(attr, maxWidth)
}

func measureAttrString(attr objc.ID, maxWidth float64) Size {
	if attr == objc.Nil || !objc.RespondsTo(attr, "boundingRectWithSize:options:context:") {
		return Size{}
	}

	w := maxWidth
	if w <= 0 {
		w = measureMaxExtent
	}

	r := objc.Send[Rect](attr, "boundingRectWithSize:options:context:",
		Size{Width: w, Height: measureMaxExtent}, stringDrawingOptions, objc.Nil)

	return r.Size
}

func measureStringWithFont(text string, font objc.ID) Size {
	if text == "" {
		return Size{}
	}

	if font == objc.Nil {
		font = objc.Msg(objc.Cls("NSFont"), "systemFontOfSize:", objc.Send[float64](objc.Cls("NSFont"), "systemFontSize"))
	}

	fontKey, _ := attributeKeys()
	attrs := objc.Msg(objc.Cls("NSDictionary"), "dictionaryWithObjects:forKeys:", objc.Array(font), objc.Array(fontKey))
	attrStr := objc.Msg(objc.Msg(objc.Cls("NSAttributedString"), "alloc"), "initWithString:attributes:", objc.String(text), attrs)
	defer objc.Release(attrStr)

	return measureAttrString(attrStr, 0)
}

// OutlineRowGeometry inspects the live NSTableRowView / NSTableCellView /
// NSTextField at (row, colID) without instantiating offscreen rows.
func OutlineRowGeometry(o *Outline, row int, colID string) RowCellGeom {
	if o == nil || o.View == objc.Nil || row < 0 || int64(row) >= o.Rows() {
		return RowCellGeom{}
	}

	colIdx := objc.Send[int64](o.View, "columnWithIdentifier:", objc.String(colID))
	if colIdx < 0 {
		return RowCellGeom{}
	}

	rv := objc.Msg(o.View, "rowViewAtRow:makeIfNecessary:", int64(row), false)
	if rv == objc.Nil {
		return RowCellGeom{}
	}

	cv := objc.Msg(o.View, "viewAtColumn:row:makeIfNecessary:", colIdx, int64(row), false)
	if cv == objc.Nil {
		return RowCellGeom{}
	}

	tf := objc.Msg(cv, "textField")
	if tf == objc.Nil {
		return RowCellGeom{}
	}

	textBounds := ViewBounds(tf)

	wrapWidth := textBounds.Size.Width
	if wrapWidth <= 0 {
		wrapWidth = ViewBounds(cv).Size.Width
	}

	var lineBreak int
	if m := objc.Send[uint64](tf, "lineBreakMode"); m <= math.MaxInt {
		lineBreak = int(m)
	}

	return RowCellGeom{
		Found:          true,
		RowBounds:      ViewBounds(rv),
		CellFrame:      objc.Send[Rect](cv, "frame"),
		TextFrame:      objc.Send[Rect](tf, "frame"),
		Text:           StringValue(tf),
		SingleLineSize: MeasureText(tf, 0),
		WrappedSize:    MeasureText(tf, wrapWidth),
		LineBreakMode:  lineBreak,
		MaxLines:       int(objc.Send[int64](tf, "maximumNumberOfLines")),
		UsesSingleLine: objc.Send[bool](tf, "usesSingleLineMode"),
	}
}

// OutlineHeaderGeometry returns the live column width and measured header
// label width for colID on o.
func OutlineHeaderGeometry(o *Outline, colID string) (colWidth, textWidth float64, found bool) {
	if o == nil || o.View == objc.Nil {
		return 0, 0, false
	}

	colIdx := objc.Send[int64](o.View, "columnWithIdentifier:", objc.String(colID))
	if colIdx < 0 {
		return 0, 0, false
	}

	col := objc.Msg(objc.Msg(o.View, "tableColumns"), "objectAtIndex:", uint64(colIdx))
	if col == objc.Nil {
		return 0, 0, false
	}

	colWidth = objc.Send[float64](col, "width")
	hc := objc.Msg(col, "headerCell")
	title := objc.GoString(objc.Msg(hc, "stringValue"))
	font := objc.Msg(hc, "font")
	textWidth = measureStringWithFont(title, font).Width

	return colWidth, textWidth, true
}

// TableRenderedCell inspects the live rendered cell at (row, colID) without
// instantiating offscreen rows.
func TableRenderedCell(t *Table, row int, colID string) RenderedCellInfo {
	if t == nil || t.View == objc.Nil || row < 0 || row >= t.Rows() {
		return RenderedCellInfo{}
	}

	colIdx := objc.Send[int64](t.View, "columnWithIdentifier:", objc.String(colID))
	if colIdx < 0 {
		return RenderedCellInfo{}
	}

	col := objc.Msg(objc.Msg(t.View, "tableColumns"), "objectAtIndex:", uint64(colIdx))
	colWidth := objc.Send[float64](col, "width")

	cv := objc.Msg(t.View, "viewAtColumn:row:makeIfNecessary:", colIdx, int64(row), false)
	if cv == objc.Nil {
		return RenderedCellInfo{Found: false, ColWidth: colWidth}
	}

	tf := objc.Msg(cv, "textField")
	if tf == objc.Nil {
		return RenderedCellInfo{
			Found:    false,
			Hidden:   !IsVisible(cv),
			Bounds:   ViewBounds(cv),
			ColWidth: colWidth,
		}
	}

	return RenderedCellInfo{
		Found:    true,
		Hidden:   !IsVisible(cv) || !IsVisible(tf),
		Text:     StringValue(tf),
		Frame:    objc.Send[Rect](tf, "frame"),
		Bounds:   ViewBounds(cv),
		ColWidth: colWidth,
	}
}

// SegmentedGeometry returns each segment's label, computed frame in the
// segmented control's superview (or local bounds if unparented), and measured text width.
func SegmentedGeometry(s *Segmented) []SegmentGeom {
	if s == nil || s.View == objc.Nil {
		return nil
	}

	n := s.SegmentCount()
	if n <= 0 {
		return nil
	}

	bounds := ViewBounds(s.View)

	viewFrame := bounds
	if super := objc.Msg(s.View, "superview"); super != objc.Nil {
		viewFrame = ViewFrameIn(s.View, super)
	}

	widths := make([]float64, n)

	var explicitSum float64

	var autoCount int

	for i := range n {
		w := s.SegmentWidth(i)
		widths[i] = w

		if w > 0 {
			explicitSum += w
		} else {
			autoCount++
		}
	}

	var autoWidth float64
	if autoCount > 0 {
		autoWidth = max(0.0, bounds.Size.Width-explicitSum) / float64(autoCount)
	}

	font := objc.Msg(s.View, "font")
	out := make([]SegmentGeom, 0, n)
	x := viewFrame.Origin.X

	for i := range n {
		label := s.SegmentLabel(i)

		w := widths[i]
		if w <= 0 {
			w = autoWidth
		}

		out = append(out, SegmentGeom{
			Label:     label,
			Frame:     MakeRect(x, viewFrame.Origin.Y, w, viewFrame.Size.Height),
			TextWidth: measureStringWithFont(label, font).Width,
		})
		x += w
	}

	return out
}

// ToolbarOverflow compares window toolbar items against visibleItems (excluding
// system spaces and separators) and returns total, visible, and overflowed IDs.
func ToolbarOverflow(window objc.ID) (total, visible int, overflowed []string) {
	if window == objc.Nil {
		return 0, 0, nil
	}

	tb := objc.Msg(window, "toolbar")
	if tb == objc.Nil {
		return 0, 0, nil
	}

	sys := systemToolbarIDs()
	visSet := make(map[string]bool)

	for _, it := range objc.ArrayItems(objc.Msg(tb, "visibleItems")) {
		id := objc.GoString(objc.Msg(it, "itemIdentifier"))
		if id == "" || sys[id] {
			continue
		}

		visSet[id] = true
		visible++
	}

	for _, it := range objc.ArrayItems(objc.Msg(tb, "items")) {
		id := objc.GoString(objc.Msg(it, "itemIdentifier"))
		if id == "" || sys[id] {
			continue
		}

		total++

		if !visSet[id] {
			overflowed = append(overflowed, id)
		}
	}

	return total, visible, overflowed
}

func systemToolbarIDs() map[string]bool {
	return map[string]bool{
		objc.GoString(constant("NSToolbarFlexibleSpaceItemIdentifier", "NSToolbarFlexibleSpaceItem")):                                     true,
		objc.GoString(constant("NSToolbarSpaceItemIdentifier", "NSToolbarSpaceItem")):                                                     true,
		objc.GoString(constant("NSToolbarSeparatorItemIdentifier", "NSToolbarSeparatorItem")):                                             true,
		objc.GoString(constant("NSToolbarSidebarTrackingSeparatorItemIdentifier", "NSToolbarSidebarTrackingSeparatorItemIdentifier")):     true,
		objc.GoString(constant("NSToolbarInspectorTrackingSeparatorItemIdentifier", "NSToolbarInspectorTrackingSeparatorItemIdentifier")): true,
		"NSToolbarFlexibleSpaceItem":                        true,
		"NSToolbarSpaceItem":                                true,
		"NSToolbarSeparatorItem":                            true,
		"NSToolbarSidebarTrackingSeparatorItemIdentifier":   true,
		"NSToolbarInspectorTrackingSeparatorItemIdentifier": true,
	}
}

// WindowResize resizes window's content area to (w, h) and forces immediate layout and display.
func WindowResize(window objc.ID, w, h float64) {
	if window == objc.Nil {
		return
	}

	objc.Msg(window, "setContentSize:", Size{Width: w, Height: h})

	cv := objc.Msg(window, "contentView")
	if cv == objc.Nil {
		return
	}

	target := objc.Msg(cv, "superview")
	if target == objc.Nil {
		target = cv
	}

	objc.Msg(target, "layoutSubtreeIfNeeded")
	objc.Msg(target, "displayIfNeeded")
}
