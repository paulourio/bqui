//go:build darwin

package appkit

import (
	"github.com/paulourio/bqui/internal/objc"
)

const segmentSwitchTrackingSelectOne uint64 = 0

// Segmented is an NSSegmentedControl with Go callbacks.
type Segmented struct {
	View objc.ID
}

// NewSegmented creates a select-one segmented control.
func NewSegmented(labels []string, onChange func(index int)) *Segmented {
	s := &Segmented{}
	s.View = objc.New("NSSegmentedControl")
	objc.Msg(s.View, "setSegmentCount:", int64(len(labels)))
	objc.Msg(s.View, "setTrackingMode:", segmentSwitchTrackingSelectOne)

	for i, l := range labels {
		objc.Msg(s.View, "setLabel:forSegment:", objc.String(l), int64(i))
		objc.Msg(s.View, "setWidth:forSegment:", segmentWidth(l), int64(i))
		objc.Msg(s.View, "setEnabled:forSegment:", true, int64(i))
	}

	objc.Msg(s.View, "setTarget:", objc.NewTarget(func(objc.ID) { onChange(s.Selected()) }))
	objc.Msg(s.View, "setAction:", objc.Sel(objc.ActionSelector))
	objc.Msg(s.View, "sizeToFit")
	s.Select(0)

	return s
}

// segmentWidth sizes a segment for its label (explicit widths render every
// segment reliably; auto widths left later segments blank on macOS 26).
func segmentWidth(label string) float64 {
	const perChar, padding, minWidth = 7.5, 24.0, 64.0

	return max(minWidth, float64(len([]rune(label)))*perChar+padding)
}

// Selected returns the selected segment index (-1 when none).
func (s *Segmented) Selected() int {
	return int(objc.Send[int64](s.View, "selectedSegment"))
}

// Select selects a segment without firing the callback.
func (s *Segmented) Select(i int) {
	objc.Msg(s.View, "setSelectedSegment:", int64(i))
}

// SetEnabled enables or disables one segment.
func (s *Segmented) SetEnabled(i int, enabled bool) {
	objc.Msg(s.View, "setEnabled:forSegment:", enabled, int64(i))
}

// NewSearchField creates an NSSearchField that calls onChange as the user types.
func NewSearchField(placeholder string, onChange func(text string)) objc.ID {
	f := objc.New("NSSearchField")
	objc.Msg(f, "setPlaceholderString:", objc.String(placeholder))
	objc.Msg(f, "setSendsSearchStringImmediately:", true)
	objc.Msg(f, "setTarget:", objc.NewTarget(func(sender objc.ID) {
		onChange(objc.GoString(objc.Msg(sender, "stringValue")))
	}))
	objc.Msg(f, "setAction:", objc.Sel(objc.ActionSelector))

	return f
}

// BoldLabel creates a label with the bold system font.
func BoldLabel(text string, pointSize float64) objc.ID {
	l := Label(text, 0)
	objc.Msg(l, "setFont:", objc.Msg(objc.Cls("NSFont"), "boldSystemFontOfSize:", pointSize))

	return l
}

// SecondaryLabel creates a dimmed label.
func SecondaryLabel(text string) objc.ID {
	l := Label(text, 0)
	objc.Msg(l, "setTextColor:", objc.Msg(objc.Cls("NSColor"), "secondaryLabelColor"))

	return l
}

// CopyToPasteboard places s on the general pasteboard as plain text.
func CopyToPasteboard(s string) {
	pb := objc.Msg(objc.Cls("NSPasteboard"), "generalPasteboard")
	objc.Msg(pb, "clearContents")
	objc.Send[bool](pb, "setString:forType:", objc.String(s), objc.String("public.utf8-plain-text"))
}

// OpenURL opens url with the default handler (browser for https).
func OpenURL(url string) bool {
	u := objc.Msg(objc.Cls("NSURL"), "URLWithString:", objc.String(url))
	if u == objc.Nil {
		return false
	}

	return objc.Send[bool](objc.Msg(objc.Cls("NSWorkspace"), "sharedWorkspace"), "openURL:", u)
}

// SetHidden hides or shows a view.
func SetHidden(v objc.ID, hidden bool) {
	objc.Msg(v, "setHidden:", hidden)
}

const modalResponseOK int64 = 1

// SaveFile runs a save panel and returns the chosen path.
func SaveFile(title, defaultName string) (string, bool) {
	panel := objc.Msg(objc.Cls("NSSavePanel"), "savePanel")
	objc.Msg(panel, "setTitle:", objc.String(title))
	objc.Msg(panel, "setNameFieldStringValue:", objc.String(defaultName))
	objc.Msg(panel, "setCanCreateDirectories:", true)

	if objc.Send[int64](panel, "runModal") != modalResponseOK {
		return "", false
	}

	return objc.GoString(objc.Msg(objc.Msg(panel, "URL"), "path")), true
}
