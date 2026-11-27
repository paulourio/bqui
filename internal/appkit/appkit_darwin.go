//go:build darwin

// Package appkit holds hand-written AppKit bindings: only the classes,
// selectors and constants bqui uses. All functions must be called on the
// main thread.
package appkit

import (
	"runtime"

	"github.com/paulourio/bqui/internal/objc"
)

// Point mirrors CGPoint.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Size mirrors CGSize.
type Size struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Rect mirrors CGRect/NSRect.
type Rect struct {
	Origin Point `json:"origin"`
	Size   Size  `json:"size"`
}

// MakeRect builds a Rect.
func MakeRect(x, y, w, h float64) Rect {
	return Rect{Origin: Point{X: x, Y: y}, Size: Size{Width: w, Height: h}}
}

// TextAlignmentLeft is NSTextAlignmentLeft.
const TextAlignmentLeft int64 = 0

// NSTextAlignment Center and Right values swap between darwin/amd64
// (Right=1, Center=2) and darwin/arm64 (Center=1, Right=2) per Apple's
// <AppKit/NSText.h> TARGET_ABI_USES_IOS_VALUES.
var (
	// TextAlignmentCenter is NSTextAlignmentCenter for the target Darwin architecture.
	TextAlignmentCenter = textAlignmentForArch(2, 1)
	// TextAlignmentRight is NSTextAlignmentRight for the target Darwin architecture.
	TextAlignmentRight = textAlignmentForArch(1, 2)
)

func textAlignmentForArch(amd64Val, arm64Val int64) int64 {
	if runtime.GOARCH == "amd64" {
		return amd64Val
	}

	return arm64Val
}

const lineBreakByTruncatingTail uint64 = 4

// Label creates a non-editable NSTextField label using the system font.
func Label(text string, pointSize float64) objc.ID {
	l := objc.Msg(objc.Cls("NSTextField"), "labelWithString:", objc.String(text))
	if pointSize > 0 {
		objc.Msg(l, "setFont:", objc.Msg(objc.Cls("NSFont"), "systemFontOfSize:", pointSize))
	}

	objc.Msg(l, "setLineBreakMode:", lineBreakByTruncatingTail)

	return l
}

// SetString sets the stringValue of a control.
func SetString(control objc.ID, s string) {
	objc.Msg(control, "setStringValue:", objc.String(s))
}

// SymbolImage returns an SF Symbols image, or nil when the symbol is unknown.
func SymbolImage(name, description string) objc.ID {
	return objc.Msg(objc.Cls("NSImage"), "imageWithSystemSymbolName:accessibilityDescription:",
		objc.String(name), objc.String(description))
}

// NewView returns an empty NSView that uses Auto Layout.
func NewView() objc.ID {
	v := objc.New("NSView")
	UseAutoLayout(v)

	return v
}

// UseAutoLayout disables autoresizing-mask constraint translation.
func UseAutoLayout(v objc.ID) {
	objc.Msg(v, "setTranslatesAutoresizingMaskIntoConstraints:", false)
}

// Anchor returns a layout anchor of v, e.g. "leadingAnchor".
func Anchor(v objc.ID, name string) objc.ID {
	return objc.Msg(v, name)
}

// Equal returns an inactive constraint a == b + constant.
func Equal(a, b objc.ID, constant float64) objc.ID {
	return objc.Msg(a, "constraintEqualToAnchor:constant:", b, constant)
}

// EqualConst returns an inactive constraint dimension == constant.
func EqualConst(dim objc.ID, constant float64) objc.ID {
	return objc.Msg(dim, "constraintEqualToConstant:", constant)
}

// Activate activates constraints.
func Activate(constraints ...objc.ID) {
	objc.Msg(objc.Cls("NSLayoutConstraint"), "activateConstraints:", objc.Array(constraints...))
}

// AddFilling adds child to parent pinned to all edges with an inset.
func AddFilling(parent, child objc.ID, inset float64) {
	UseAutoLayout(child)
	objc.Msg(parent, "addSubview:", child)
	Activate(
		Equal(Anchor(child, "leadingAnchor"), Anchor(parent, "leadingAnchor"), inset),
		Equal(Anchor(child, "trailingAnchor"), Anchor(parent, "trailingAnchor"), -inset),
		Equal(Anchor(child, "topAnchor"), Anchor(parent, "topAnchor"), inset),
		Equal(Anchor(child, "bottomAnchor"), Anchor(parent, "bottomAnchor"), -inset),
	)
}

// AddCentered adds child to parent, centered in both axes.
func AddCentered(parent, child objc.ID) {
	UseAutoLayout(child)
	objc.Msg(parent, "addSubview:", child)
	Activate(
		Equal(Anchor(child, "centerXAnchor"), Anchor(parent, "centerXAnchor"), 0),
		Equal(Anchor(child, "centerYAnchor"), Anchor(parent, "centerYAnchor"), 0),
	)
}

// SetIdentifier sets the NSUserInterfaceItemIdentifier of a view or item.
func SetIdentifier(v objc.ID, id string) {
	objc.Msg(v, "setIdentifier:", objc.String(id))
}

// OSVersion returns NSProcessInfo's human-readable OS version string.
func OSVersion() string {
	pi := objc.Msg(objc.Cls("NSProcessInfo"), "processInfo")

	return objc.GoString(objc.Msg(pi, "operatingSystemVersionString"))
}

// constant reads an exported AppKit NSString constant, falling back to its
// documented value when the symbol is missing (older OS versions).
func constant(name, fallback string) objc.ID {
	if id, err := objc.Const(objc.AppKit, name); err == nil && id != objc.Nil {
		return id
	}

	return objc.String(fallback)
}
