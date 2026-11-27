//go:build darwin

package appkit

import (
	"github.com/paulourio/bqui/internal/objc"
)

// CanvasHandler draws a Canvas and receives its clicks. Coordinates are
// flipped (origin top-left).
type CanvasHandler interface {
	Draw(bounds Rect)
	MouseDown(p Point)
}

var (
	canvases    objc.Registry[CanvasHandler]
	canvasClass = objc.LazyClass("BQUICanvasView", "NSView", nil, func() []objc.Method {
		return []objc.Method{
			// drawRect: receives the dirty rect; drawing the bounds is simpler
			// and cheap for plan graphs (the rect argument is ignored).
			{Sel: "drawRect:", Fn: func(self objc.ID, _ objc.SEL) {
				if h, ok := canvases.Get(self); ok {
					h.Draw(objc.Send[Rect](self, "bounds"))
				}
			}},
			{Sel: "isFlipped", Fn: func(objc.ID, objc.SEL) bool { return true }},
			{Sel: "acceptsFirstMouse:", Fn: func(objc.ID, objc.SEL, objc.ID) bool { return true }},
			{Sel: "mouseDown:", Fn: func(self objc.ID, _ objc.SEL, event objc.ID) {
				h, ok := canvases.Get(self)
				if !ok {
					return
				}

				win := objc.Send[Point](event, "locationInWindow")
				h.MouseDown(objc.Send[Point](self, "convertPoint:fromView:", win, objc.Nil))
			}},
		}
	})
)

// NewCanvas creates a flipped view drawn by h.
func NewCanvas(h CanvasHandler) objc.ID {
	v := objc.Msg(objc.Msg(canvasClass(), "alloc"), "initWithFrame:", MakeRect(0, 0, 100, 100))
	canvases.Put(v, h)

	return v
}

// SetNeedsDisplay schedules a redraw.
func SetNeedsDisplay(v objc.ID) { objc.Msg(v, "setNeedsDisplay:", true) }

// SetFrameSize resizes a view.
func SetFrameSize(v objc.ID, w, h float64) { objc.Msg(v, "setFrameSize:", Size{Width: w, Height: h}) }

// SystemColor returns an NSColor class color by selector, e.g. "labelColor".
func SystemColor(name string) objc.ID { return objc.Msg(objc.Cls("NSColor"), name) }

// RGB returns an sRGB NSColor.
func RGB(r, g, b, a float64) objc.ID {
	return objc.Msg(objc.Cls("NSColor"), "colorWithSRGBRed:green:blue:alpha:", r, g, b, a)
}

// Blend mixes two colors (0 = a, 1 = b).
func Blend(a, b objc.ID, t float64) objc.ID {
	if c := objc.Msg(a, "blendedColorWithFraction:ofColor:", t, b); c != objc.Nil {
		return c
	}

	return a
}

// SystemFont returns the system font (mono picks the monospaced variant).
func SystemFont(size, weight float64, mono bool) objc.ID {
	if mono {
		return objc.Msg(objc.Cls("NSFont"), "monospacedSystemFontOfSize:weight:", size, weight)
	}

	return objc.Msg(objc.Cls("NSFont"), "systemFontOfSize:weight:", size, weight)
}

// FontWeight values.
const (
	WeightRegular  = 0.0
	WeightSemibold = 0.3
)

// FillRoundedRect fills a rounded rectangle (inside Draw).
func FillRoundedRect(r Rect, radius float64, color objc.ID) {
	path := objc.Msg(objc.Cls("NSBezierPath"), "bezierPathWithRoundedRect:xRadius:yRadius:", r, radius, radius)
	objc.Msg(color, "setFill")
	objc.Msg(path, "fill")
}

// StrokeRoundedRect outlines a rounded rectangle (inside Draw).
func StrokeRoundedRect(r Rect, radius, width float64, color objc.ID) {
	path := objc.Msg(objc.Cls("NSBezierPath"), "bezierPathWithRoundedRect:xRadius:yRadius:", r, radius, radius)
	objc.Msg(path, "setLineWidth:", width)
	objc.Msg(color, "setStroke")
	objc.Msg(path, "stroke")
}

// StrokeCurve draws a vertical S-curve from a to b (inside Draw).
func StrokeCurve(a, b Point, width float64, color objc.ID) {
	path := objc.New("NSBezierPath")
	defer objc.Release(path)

	mid := (a.Y + b.Y) / 2
	objc.Msg(path, "moveToPoint:", a)
	objc.Msg(path, "curveToPoint:controlPoint1:controlPoint2:", b, Point{X: a.X, Y: mid}, Point{X: b.X, Y: mid})
	objc.Msg(path, "setLineWidth:", width)
	objc.Msg(color, "setStroke")
	objc.Msg(path, "stroke")
}

// DrawText draws single-line text truncated to r (inside Draw).
func DrawText(s string, r Rect, font, color objc.ID) {
	style := objc.New("NSMutableParagraphStyle")
	defer objc.Release(style)

	objc.Msg(style, "setLineBreakMode:", lineBreakByTruncatingTail)

	attrs := objc.Msg(objc.Cls("NSMutableDictionary"), "dictionary")
	objc.Msg(attrs, "setObject:forKey:", font, constant("NSFontAttributeName", "NSFont"))
	objc.Msg(attrs, "setObject:forKey:", color, constant("NSForegroundColorAttributeName", "NSColor"))
	objc.Msg(attrs, "setObject:forKey:", style, constant("NSParagraphStyleAttributeName", "NSParagraphStyle"))
	objc.Msg(objc.String(s), "drawInRect:withAttributes:", r, attrs)
}

// MagnifyingScroll wraps a document view in a scroll view with pinch and
// ⌘-scroll zoom.
func MagnifyingScroll(doc objc.ID, minZoom, maxZoom float64) objc.ID {
	sv := objc.New("NSScrollView")
	objc.Msg(sv, "setDocumentView:", doc)
	objc.Msg(sv, "setHasVerticalScroller:", true)
	objc.Msg(sv, "setHasHorizontalScroller:", true)
	objc.Msg(sv, "setAutohidesScrollers:", true)
	objc.Msg(sv, "setAllowsMagnification:", true)
	objc.Msg(sv, "setMinMagnification:", minZoom)
	objc.Msg(sv, "setMaxMagnification:", maxZoom)

	return sv
}
