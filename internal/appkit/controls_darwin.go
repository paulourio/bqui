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

// SetRounded uses the classic rounded style. The automatic style draws no
// unselected labels in inspector panes on macOS 26.
func (s *Segmented) SetRounded() {
	const segmentStyleRounded = int64(1)

	objc.Msg(s.View, "setSegmentStyle:", segmentStyleRounded)
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
func SaveFile(title, defaultName string) (string, bool) { return SaveFileIn(title, "", defaultName) }

func fileURL(path string) objc.ID {
	return objc.Msg(objc.Cls("NSURL"), "fileURLWithPath:", objc.String(path))
}

// SaveFileIn runs a save panel starting in dir ("" = last used).
func SaveFileIn(title, dir, defaultName string) (string, bool) {
	panel := objc.Msg(objc.Cls("NSSavePanel"), "savePanel")
	objc.Msg(panel, "setTitle:", objc.String(title))
	objc.Msg(panel, "setNameFieldStringValue:", objc.String(defaultName))
	objc.Msg(panel, "setCanCreateDirectories:", true)

	if dir != "" {
		objc.Msg(panel, "setDirectoryURL:", fileURL(dir))
	}

	if objc.Send[int64](panel, "runModal") != modalResponseOK {
		return "", false
	}

	return objc.GoString(objc.Msg(objc.Msg(panel, "URL"), "path")), true
}

// OpenFile runs an open panel for one file with one of the extensions.
func OpenFile(title, dir string, extensions ...string) (string, bool) {
	panel := objc.Msg(objc.Cls("NSOpenPanel"), "openPanel")
	objc.Msg(panel, "setTitle:", objc.String(title))
	objc.Msg(panel, "setCanChooseFiles:", true)
	objc.Msg(panel, "setCanChooseDirectories:", false)
	objc.Msg(panel, "setAllowsMultipleSelection:", false)

	if dir != "" {
		objc.Msg(panel, "setDirectoryURL:", fileURL(dir))
	}

	if len(extensions) > 0 {
		types := make([]objc.ID, 0, len(extensions))

		for _, ext := range extensions {
			if t := objc.Msg(objc.Cls("UTType"), "typeWithFilenameExtension:", objc.String(ext)); t != objc.Nil {
				types = append(types, t)
			}
		}

		if len(types) > 0 {
			objc.Msg(panel, "setAllowedContentTypes:", objc.Array(types...))
		}
	}

	if objc.Send[int64](panel, "runModal") != modalResponseOK {
		return "", false
	}

	return objc.GoString(objc.Msg(objc.Msg(panel, "URL"), "path")), true
}

// Alert buttons returned by Confirm (first, second, third button).
const (
	AlertFirst  = 1000
	AlertSecond = 1001
	AlertThird  = 1002
)

// Confirm runs a modal alert and returns which button was clicked
// (AlertFirst, AlertSecond, ...). The first button is the default.
func Confirm(message, info string, buttons ...string) int {
	alert := objc.New("NSAlert")
	objc.Msg(alert, "setMessageText:", objc.String(message))
	objc.Msg(alert, "setInformativeText:", objc.String(info))

	for _, b := range buttons {
		objc.Msg(alert, "addButtonWithTitle:", objc.String(b))
	}

	return int(objc.Send[int64](alert, "runModal"))
}

// RevealInFinder selects a file in a Finder window.
func RevealInFinder(path string) {
	ws := objc.Msg(objc.Cls("NSWorkspace"), "sharedWorkspace")
	objc.Msg(ws, "activateFileViewerSelectingURLs:", objc.Array(fileURL(path)))
}

// SetWindowDocument shows a document title as the window subtitle and the
// edited dot in the close button.
func SetWindowDocument(window objc.ID, subtitle string, edited bool) {
	objc.Msg(window, "setSubtitle:", objc.String(subtitle))
	objc.Msg(window, "setDocumentEdited:", edited)
}

const promptFieldWidth = 280.0

// Prompt asks for one line of text (OK/Cancel); ok is false on Cancel.
func Prompt(message, info, value string) (text string, ok bool) {
	alert := objc.New("NSAlert")
	objc.Msg(alert, "setMessageText:", objc.String(message))
	objc.Msg(alert, "setInformativeText:", objc.String(info))
	objc.Msg(alert, "addButtonWithTitle:", objc.String("OK"))
	objc.Msg(alert, "addButtonWithTitle:", objc.String("Cancel"))

	field := objc.Msg(objc.Msg(objc.Cls("NSTextField"), "alloc"), "initWithFrame:", MakeRect(0, 0, promptFieldWidth, 24))
	objc.Msg(field, "setStringValue:", objc.String(value))
	objc.Msg(alert, "setAccessoryView:", field)
	objc.Msg(objc.Msg(alert, "window"), "setInitialFirstResponder:", field)

	if objc.Send[int64](alert, "runModal") != AlertFirst {
		return "", false
	}

	return objc.GoString(objc.Msg(field, "stringValue")), true
}
