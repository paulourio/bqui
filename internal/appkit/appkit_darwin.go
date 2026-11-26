//go:build darwin

// Package appkit holds hand-written AppKit bindings: only the classes,
// selectors and constants bqui uses.
package appkit

import (
	"errors"
	"fmt"

	"github.com/paulourio/bqui/internal/objc"
)

// Point mirrors CGPoint.
type Point struct{ X, Y float64 }

// Size mirrors CGSize.
type Size struct{ Width, Height float64 }

// Rect mirrors CGRect/NSRect.
type Rect struct {
	Origin Point
	Size   Size
}

// MakeRect builds a Rect.
func MakeRect(x, y, w, h float64) Rect {
	return Rect{Origin: Point{X: x, Y: y}, Size: Size{Width: w, Height: h}}
}

// NSWindowStyleMask values.
const (
	WindowStyleTitled                 uint64 = 1 << 0
	WindowStyleClosable               uint64 = 1 << 1
	WindowStyleMiniaturizable         uint64 = 1 << 2
	WindowStyleResizable              uint64 = 1 << 3
	WindowStyleUnifiedTitleAndToolbar uint64 = 1 << 12
	WindowStyleFullSizeContentView    uint64 = 1 << 15
)

const (
	backingStoreBuffered         uint64 = 2
	activationPolicyRegular      int64  = 0
	bitmapImageFileTypePNG       uint64 = 4
	textAlignmentCenter          int64  = 1
	autoresizeFlexibleMargins uint64 = 1<<0 | 1<<2 | 1<<3 | 1<<5 // NSViewMinX/MaxX/MinY/MaxYMargin
)

var errWriteFailed = errors.New("appkit: write failed")

// SharedApplication returns NSApp configured as a regular (Dock) application.
func SharedApplication() objc.ID {
	app := objc.Msg(objc.Cls("NSApplication"), "sharedApplication")
	objc.Send[bool](app, "setActivationPolicy:", activationPolicyRegular)

	return app
}

// NewWindow creates a titled, resizable window with a full-size content view.
func NewWindow(title string, frame Rect) objc.ID {
	style := WindowStyleTitled | WindowStyleClosable | WindowStyleMiniaturizable |
		WindowStyleResizable | WindowStyleFullSizeContentView

	w := objc.Msg(objc.Msg(objc.Cls("NSWindow"), "alloc"),
		"initWithContentRect:styleMask:backing:defer:", frame, style, backingStoreBuffered, false)
	objc.Msg(w, "setTitle:", objc.String(title))
	objc.Msg(w, "setReleasedWhenClosed:", false)

	return w
}

// NewLabel creates a non-editable, centered NSTextField label.
func NewLabel(text string, pointSize float64) objc.ID {
	l := objc.Msg(objc.Cls("NSTextField"), "labelWithString:", objc.String(text))
	objc.Msg(l, "setFont:", objc.Msg(objc.Cls("NSFont"), "systemFontOfSize:", pointSize))
	objc.Msg(l, "setAlignment:", textAlignmentCenter)

	return l
}

// CenterIn sizes view to fit, centers it in parent and keeps it centered on resize.
func CenterIn(view, parent objc.ID) {
	objc.Msg(view, "sizeToFit")
	vs := objc.Send[Rect](view, "frame").Size
	pb := objc.Send[Rect](parent, "bounds").Size
	objc.Msg(view, "setFrame:", MakeRect((pb.Width-vs.Width)/2, (pb.Height-vs.Height)/2, vs.Width, vs.Height))
	objc.Msg(view, "setAutoresizingMask:", autoresizeFlexibleMargins)
	objc.Msg(parent, "addSubview:", view)
}

// MenuItem describes one item of an application menu.
type MenuItem struct {
	Title  string
	Action string // selector sent to the first responder; "" adds a separator
	Key    string
}

// SetMainMenu installs a main menu whose first (application) menu holds items.
func SetMainMenu(app objc.ID, appName string, items []MenuItem) {
	mainMenu := objc.Msg(objc.Msg(objc.Cls("NSMenu"), "alloc"), "initWithTitle:", objc.String(""))
	appItem := objc.Msg(objc.Msg(objc.Cls("NSMenuItem"), "alloc"), "init")
	objc.Msg(mainMenu, "addItem:", appItem)

	appMenu := objc.Msg(objc.Msg(objc.Cls("NSMenu"), "alloc"), "initWithTitle:", objc.String(appName))

	for _, it := range items {
		if it.Action == "" {
			objc.Msg(appMenu, "addItem:", objc.Msg(objc.Cls("NSMenuItem"), "separatorItem"))

			continue
		}

		mi := objc.Msg(objc.Msg(objc.Cls("NSMenuItem"), "alloc"),
			"initWithTitle:action:keyEquivalent:",
			objc.String(it.Title), objc.Sel(it.Action), objc.String(it.Key))
		objc.Msg(appMenu, "addItem:", mi)
	}

	objc.Msg(appItem, "setSubmenu:", appMenu)
	objc.Msg(app, "setMainMenu:", mainMenu)
}

// WritePNG renders view (including subviews) offscreen and writes a PNG to
// path. It needs no screen-recording permission.
func WritePNG(view objc.ID, path string) error {
	bounds := objc.Send[Rect](view, "bounds")

	rep := objc.Msg(view, "bitmapImageRepForCachingDisplayInRect:", bounds)
	if rep == objc.Nil {
		return fmt.Errorf("appkit: no bitmap rep for view bounds %+v", bounds)
	}

	objc.Msg(view, "cacheDisplayInRect:toBitmapImageRep:", bounds, rep)

	data := objc.Msg(rep, "representationUsingType:properties:",
		bitmapImageFileTypePNG, objc.Msg(objc.Cls("NSDictionary"), "dictionary"))
	if data == objc.Nil {
		return fmt.Errorf("appkit: PNG encoding failed")
	}

	if !objc.Send[bool](data, "writeToFile:atomically:", objc.String(path), true) {
		return fmt.Errorf("%w: %s", errWriteFailed, path)
	}

	return nil
}

// OSVersion returns NSProcessInfo's human-readable OS version string.
func OSVersion() string {
	pi := objc.Msg(objc.Cls("NSProcessInfo"), "processInfo")

	return objc.GoString(objc.Msg(pi, "operatingSystemVersionString"))
}
