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

// OutlineCell is the visible content of one outline row.
type OutlineCell struct {
	Title   string
	Symbol  string // SF Symbol name, optional
	ToolTip string
	Group   bool // source-list section header
}

// Outline is a Go-backed NSOutlineView inside an NSScrollView.
type Outline struct {
	Scroll objc.ID
	View   objc.ID

	src      OutlineSource
	onSelect func(item uint64)
}

const outlineCellID = "BQUIOutlineCell"

var (
	outlines     objc.Registry[*Outline]
	outlineClass = objc.LazyClass("BQUIOutlineController", "NSObject",
		[]string{"NSOutlineViewDataSource", "NSOutlineViewDelegate"}, outlineMethods)
)

func outlineMethods() []objc.Method {
	get := outlines.Get

	return []objc.Method{
		{Sel: "outlineView:numberOfChildrenOfItem:", Fn: func(self objc.ID, _ objc.SEL, _, item objc.ID) int64 {
			o, ok := get(self)
			if !ok {
				return 0
			}

			return int64(len(o.src.Children(objc.Uint64(item))))
		}},
		{Sel: "outlineView:child:ofItem:", Fn: func(self objc.ID, _ objc.SEL, _ objc.ID, index int64, item objc.ID) objc.ID {
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
			o, ok := get(self)

			return ok && o.src.Expandable(objc.Uint64(item))
		}},
		{Sel: "outlineView:isGroupItem:", Fn: func(self objc.ID, _ objc.SEL, _, item objc.ID) bool {
			o, ok := get(self)

			return ok && o.src.Cell(objc.Uint64(item)).Group
		}},
		{Sel: "outlineView:viewForTableColumn:item:", Fn: func(self objc.ID, _ objc.SEL, ov, _, item objc.ID) objc.ID {
			o, ok := get(self)
			if !ok {
				return objc.Nil
			}

			return o.cellView(ov, o.src.Cell(objc.Uint64(item)))
		}},
		{Sel: "outlineViewSelectionDidChange:", Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
			o, ok := get(self)
			if !ok || o.onSelect == nil {
				return
			}

			o.onSelect(o.Selected())
		}},
	}
}

// NewOutline creates an outline view (single column, no header) fed by src.
func NewOutline(src OutlineSource, onSelect func(item uint64)) *Outline {
	o := &Outline{src: src, onSelect: onSelect}

	o.View = objc.New("NSOutlineView")
	col := objc.Msg(objc.Msg(objc.Cls("NSTableColumn"), "alloc"), "initWithIdentifier:", objc.String("main"))
	objc.Msg(o.View, "addTableColumn:", col)
	objc.Msg(o.View, "setOutlineTableColumn:", col)
	objc.Msg(o.View, "setHeaderView:", objc.Nil)
	objc.Msg(o.View, "setFloatsGroupRows:", false)

	ctl := objc.Msg(objc.Msg(outlineClass(), "alloc"), "init")
	outlines.Put(ctl, o)
	objc.Msg(o.View, "setDataSource:", ctl)
	objc.Msg(o.View, "setDelegate:", ctl)

	o.Scroll = objc.New("NSScrollView")
	objc.Msg(o.Scroll, "setDocumentView:", o.View)
	objc.Msg(o.Scroll, "setHasVerticalScroller:", true)
	objc.Msg(o.Scroll, "setAutohidesScrollers:", true)
	objc.Msg(o.Scroll, "setDrawsBackground:", false)

	return o
}

// Reload reloads item and its children (0 reloads everything).
func (o *Outline) Reload(item uint64) {
	if item == 0 {
		objc.Msg(o.View, "reloadData")

		return
	}

	objc.Msg(o.View, "reloadItem:reloadChildren:", objc.Number(item), true)
}

// Expand expands item; recursive also expands its descendants. Item 0 is the
// root, so Expand(0, true) expands the whole tree.
func (o *Outline) Expand(item uint64, recursive bool) {
	target := objc.Nil
	if item != 0 {
		target = objc.Number(item)
	}

	objc.Msg(o.View, "expandItem:expandChildren:", target, recursive)
}

// Selected returns the selected item handle, or 0.
func (o *Outline) Selected() uint64 {
	row := objc.Send[int64](o.View, "selectedRow")
	if row < 0 {
		return 0
	}

	return objc.Uint64(objc.Msg(o.View, "itemAtRow:", row))
}

func (o *Outline) cellView(ov objc.ID, c OutlineCell) objc.ID {
	cell := objc.Msg(ov, "makeViewWithIdentifier:owner:", objc.String(outlineCellID), objc.Nil)
	if cell == objc.Nil {
		cell = newOutlineCell()
	}

	tf := objc.Msg(cell, "textField")
	SetString(tf, c.Title)

	img := objc.Msg(cell, "imageView")
	if c.Symbol != "" && !c.Group {
		objc.Msg(img, "setImage:", SymbolImage(c.Symbol, c.Title))
		objc.Msg(img, "setHidden:", false)
	} else {
		objc.Msg(img, "setImage:", objc.Nil)
		objc.Msg(img, "setHidden:", true)
	}

	if c.ToolTip != "" {
		objc.Msg(cell, "setToolTip:", objc.String(c.ToolTip))
	}

	return cell
}

func newOutlineCell() objc.ID {
	const iconSize, gap = 16.0, 6.0

	cell := objc.New("NSTableCellView")
	SetIdentifier(cell, outlineCellID)

	img := objc.New("NSImageView")
	UseAutoLayout(img)
	objc.Msg(cell, "addSubview:", img)
	objc.Msg(cell, "setImageView:", img)

	tf := Label("", 0)
	UseAutoLayout(tf)
	objc.Msg(cell, "addSubview:", tf)
	objc.Msg(cell, "setTextField:", tf)

	Activate(
		Equal(Anchor(img, "leadingAnchor"), Anchor(cell, "leadingAnchor"), 2),
		Equal(Anchor(img, "centerYAnchor"), Anchor(cell, "centerYAnchor"), 0),
		EqualConst(Anchor(img, "widthAnchor"), iconSize),
		EqualConst(Anchor(img, "heightAnchor"), iconSize),
		Equal(Anchor(tf, "leadingAnchor"), Anchor(img, "trailingAnchor"), gap),
		Equal(Anchor(tf, "trailingAnchor"), Anchor(cell, "trailingAnchor"), -2),
		Equal(Anchor(tf, "centerYAnchor"), Anchor(cell, "centerYAnchor"), 0),
	)

	return cell
}
