//go:build darwin

package appkit

import (
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/ebitengine/purego"

	"github.com/paulourio/bqui/internal/objc"
)

const bitmapImageFileTypePNG uint64 = 4

var (
	errNoBitmap      = errors.New("appkit: no bitmap rep for view")
	errPNGEncoding   = errors.New("appkit: PNG encoding failed")
	errWriteFailed   = errors.New("appkit: write failed")
	maxAXDepth       = 24
	maxAXChildren    = 200
	axStringSelector = []string{"accessibilityRole", "accessibilitySubrole", "accessibilityIdentifier", "accessibilityLabel", "accessibilityTitle"}
)

// WritePNG renders view (including subviews) offscreen and writes a PNG to
// path. It needs no screen-recording permission. Visual-effect materials
// (sidebar glass) may render flat.
func WritePNG(view objc.ID, path string) error {
	bounds := objc.Send[Rect](view, "bounds")

	rep := objc.Msg(view, "bitmapImageRepForCachingDisplayInRect:", bounds)
	if rep == objc.Nil {
		return fmt.Errorf("%w: bounds %+v", errNoBitmap, bounds)
	}

	objc.Msg(view, "cacheDisplayInRect:toBitmapImageRep:", bounds, rep)

	return writeRepPNG(rep, path)
}

const (
	coreGraphics = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"

	cgWindowListOptionIncludingWindow uint32 = 1 << 3
	cgWindowImageBoundsIgnoreFraming  uint32 = 1 << 0
	cgWindowImageBestResolution       uint32 = 1 << 3
)

var (
	cgOnce                   sync.Once
	cgWindowListCreateImage  func(bounds Rect, listOption, windowID, imageOption uint32) uintptr
	cgImageRelease           func(img uintptr)
	errWindowCaptureMissing  = errors.New("appkit: CGWindowListCreateImage unavailable")
	errWindowCaptureRejected = errors.New("appkit: window capture returned no image")

	// ErrCaptureBlank means the WindowServer returned a uniform image, which is
	// what macOS does when the process lacks Screen Recording permission.
	ErrCaptureBlank = errors.New("appkit: window capture is blank (Screen Recording permission missing?)")
)

const blankSampleGrid = 16

func loadCG() {
	h, err := purego.Dlopen(coreGraphics, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return
	}

	if _, err := purego.Dlsym(h, "CGWindowListCreateImage"); err != nil {
		return
	}

	purego.RegisterLibFunc(&cgWindowListCreateImage, h, "CGWindowListCreateImage")
	purego.RegisterLibFunc(&cgImageRelease, h, "CGImageRelease")
}

// WriteWindowPNG captures window as composited by the WindowServer (including
// Liquid Glass materials and the window shadow-free frame) and writes a PNG.
// Capturing the app's own windows does not need Screen Recording permission
// on the macOS versions tested; it fails cleanly where it does.
func WriteWindowPNG(window objc.ID, path string) error {
	cgOnce.Do(loadCG)

	if cgWindowListCreateImage == nil {
		return errWindowCaptureMissing
	}

	wid := objc.Send[int64](window, "windowNumber")
	if wid <= 0 || wid > math.MaxUint32 {
		return fmt.Errorf("%w: window number %d", errWindowCaptureRejected, wid)
	}

	null := Rect{Origin: Point{X: math.Inf(1), Y: math.Inf(1)}}

	img := cgWindowListCreateImage(null, cgWindowListOptionIncludingWindow, uint32(wid),
		cgWindowImageBoundsIgnoreFraming|cgWindowImageBestResolution)
	if img == 0 {
		return errWindowCaptureRejected
	}
	defer cgImageRelease(img)

	rep := objc.Msg(objc.Msg(objc.Cls("NSBitmapImageRep"), "alloc"), "initWithCGImage:", img)
	defer objc.Release(rep)

	if isUniform(rep) {
		return ErrCaptureBlank
	}

	return writeRepPNG(rep, path)
}

// isUniform samples a grid of pixels and reports whether they are all equal.
func isUniform(rep objc.ID) bool {
	w := objc.Send[int64](rep, "pixelsWide")
	h := objc.Send[int64](rep, "pixelsHigh")

	if w == 0 || h == 0 {
		return true
	}

	first := objc.Msg(rep, "colorAtX:y:", int64(0), int64(0))

	for i := range int64(blankSampleGrid) {
		for j := range int64(blankSampleGrid) {
			x, y := (2*i+1)*w/(2*blankSampleGrid), (2*j+1)*h/(2*blankSampleGrid)
			if !objc.Send[bool](objc.Msg(rep, "colorAtX:y:", x, y), "isEqual:", first) {
				return false
			}
		}
	}

	return true
}

func writeRepPNG(rep objc.ID, path string) error {
	data := objc.Msg(rep, "representationUsingType:properties:",
		bitmapImageFileTypePNG, objc.Msg(objc.Cls("NSDictionary"), "dictionary"))
	if data == objc.Nil {
		return errPNGEncoding
	}

	if !objc.Send[bool](data, "writeToFile:atomically:", objc.String(path), true) {
		return fmt.Errorf("%w: %s", errWriteFailed, path)
	}

	return nil
}

// AXNode is one element of an accessibility tree snapshot.
type AXNode struct {
	Role       string    `json:"role,omitempty"`
	Subrole    string    `json:"subrole,omitempty"`
	Identifier string    `json:"identifier,omitempty"`
	Label      string    `json:"label,omitempty"`
	Title      string    `json:"title,omitempty"`
	Children   []*AXNode `json:"children,omitempty"`
}

// AXTree snapshots the accessibility hierarchy below element (usually a
// window), as VoiceOver and UI tests see it.
func AXTree(element objc.ID) *AXNode {
	return axNode(element, 0)
}

func axNode(el objc.ID, depth int) *AXNode {
	n := &AXNode{}
	fields := []*string{&n.Role, &n.Subrole, &n.Identifier, &n.Label, &n.Title}

	for i, sel := range axStringSelector {
		if objc.RespondsTo(el, sel) {
			*fields[i] = objc.GoString(objc.Msg(el, sel))
		}
	}

	if depth >= maxAXDepth || !objc.RespondsTo(el, "accessibilityChildren") {
		return n
	}

	kids := objc.ArrayItems(objc.Msg(el, "accessibilityChildren"))
	if len(kids) > maxAXChildren {
		kids = kids[:maxAXChildren]
	}

	for _, k := range kids {
		n.Children = append(n.Children, axNode(k, depth+1))
	}

	return n
}

// Find returns the first node (depth-first) for which match is true.
func (n *AXNode) Find(match func(*AXNode) bool) *AXNode {
	if n == nil {
		return nil
	}

	if match(n) {
		return n
	}

	for _, c := range n.Children {
		if f := c.Find(match); f != nil {
			return f
		}
	}

	return nil
}
