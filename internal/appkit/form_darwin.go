//go:build darwin

package appkit

import "github.com/paulourio/bqui/internal/objc"

// NewPanelWindow creates a fixed-size titled, closable window (settings).
func NewPanelWindow(title string, frame Rect) objc.ID {
	w := objc.Msg(objc.Msg(objc.Cls("NSWindow"), "alloc"),
		"initWithContentRect:styleMask:backing:defer:", frame, WindowStyleTitled|WindowStyleClosable, backingStoreBuffered, false)
	objc.Msg(w, "setTitle:", objc.String(title))
	objc.Msg(w, "setReleasedWhenClosed:", false)

	return w
}

// TextField creates an editable single-line text field.
func TextField(placeholder string, width float64) objc.ID {
	f := objc.Msg(objc.Cls("NSTextField"), "textFieldWithString:", objc.String(""))
	objc.Msg(f, "setPlaceholderString:", objc.String(placeholder))
	UseAutoLayout(f)
	Activate(EqualConst(Anchor(f, "widthAnchor"), width))

	return f
}

// StringValue reads a control's stringValue.
func StringValue(control objc.ID) string {
	return objc.GoString(objc.Msg(control, "stringValue"))
}

// Button creates a push button that runs fn.
func Button(title string, fn func()) objc.ID {
	return objc.Msg(objc.Cls("NSButton"), "buttonWithTitle:target:action:",
		objc.String(title), objc.NewTarget(func(objc.ID) { fn() }), objc.Sel(objc.ActionSelector))
}

// SetDefaultButton makes Return trigger a button.
func SetDefaultButton(button objc.ID) {
	objc.Msg(button, "setKeyEquivalent:", objc.String("\r"))
}

// Grid lays out rows of views (label column right-aligned, controls left).
func Grid(rows [][]objc.ID) objc.ID {
	arr := make([]objc.ID, len(rows))
	for i, r := range rows {
		arr[i] = objc.Array(r...)
	}

	g := objc.Msg(objc.Cls("NSGridView"), "gridViewWithViews:", objc.Array(arr...))
	objc.Msg(g, "setRowSpacing:", gridRowSpacing)
	objc.Msg(g, "setColumnSpacing:", gridColumnSpacing)
	objc.Msg(objc.Msg(g, "columnAtIndex:", int64(0)), "setXPlacement:", gridCellPlacementTrailing)
	objc.Msg(g, "setRowAlignment:", gridRowAlignmentFirstBaseline)
	UseAutoLayout(g)

	return g
}

// GridEmptyCell is a placeholder for an empty grid cell.
func GridEmptyCell() objc.ID { return objc.Msg(objc.Cls("NSGridCell"), "emptyContentView") }

const (
	gridRowSpacing                = 10.0
	gridColumnSpacing             = 10.0
	gridCellPlacementTrailing     = int64(3) // NSGridCellPlacementTrailing
	gridRowAlignmentFirstBaseline = int64(2) // NSGridRowAlignmentFirstBaseline
)
