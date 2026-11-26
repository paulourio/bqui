//go:build darwin

package appkit

import (
	"github.com/paulourio/bqui/internal/objc"
)

const (
	fontWeightRegular = 0.0
	hugeDimension     = 1e7
)

// CodeEditor is a plain-text, non-wrapping NSTextView in an NSScrollView,
// configured for source code (no smart substitutions, find bar, undo).
type CodeEditor struct {
	Scroll objc.ID
	Text   objc.ID
}

// NewCodeEditor creates a code editor using the monospaced system font.
func NewCodeEditor(pointSize float64) *CodeEditor {
	scroll := objc.Msg(objc.Cls("NSTextView"), "scrollableTextView")
	tv := objc.Msg(scroll, "documentView")

	objc.Msg(tv, "setFont:", objc.Msg(objc.Cls("NSFont"), "monospacedSystemFontOfSize:weight:", pointSize, fontWeightRegular))
	objc.Msg(tv, "setRichText:", false)
	objc.Msg(tv, "setImportsGraphics:", false)
	objc.Msg(tv, "setAllowsUndo:", true)
	objc.Msg(tv, "setUsesFindBar:", true)
	objc.Msg(tv, "setIncrementalSearchingEnabled:", true)
	objc.Msg(tv, "setAutomaticQuoteSubstitutionEnabled:", false)
	objc.Msg(tv, "setAutomaticDashSubstitutionEnabled:", false)
	objc.Msg(tv, "setAutomaticTextReplacementEnabled:", false)
	objc.Msg(tv, "setAutomaticSpellingCorrectionEnabled:", false)
	objc.Msg(tv, "setContinuousSpellCheckingEnabled:", false)
	objc.Msg(tv, "setSmartInsertDeleteEnabled:", false)
	objc.Msg(tv, "setTextContainerInset:", Size{Width: 8, Height: 8})

	// No soft wrapping: let the text view grow horizontally.
	objc.Msg(tv, "setHorizontallyResizable:", true)
	objc.Msg(tv, "setMaxSize:", Size{Width: hugeDimension, Height: hugeDimension})
	tc := objc.Msg(tv, "textContainer")
	objc.Msg(tc, "setWidthTracksTextView:", false)
	objc.Msg(tc, "setContainerSize:", Size{Width: hugeDimension, Height: hugeDimension})
	objc.Msg(scroll, "setHasHorizontalScroller:", true)

	return &CodeEditor{Scroll: scroll, Text: tv}
}

// SetText replaces the editor contents.
func (e *CodeEditor) SetText(s string) {
	objc.Msg(e.Text, "setString:", objc.String(s))
}

// String returns the editor contents.
func (e *CodeEditor) String() string {
	return objc.GoString(objc.Msg(e.Text, "string"))
}

// UsesTextKit2 reports whether the text view runs on NSTextLayoutManager.
func (e *CodeEditor) UsesTextKit2() bool {
	return objc.RespondsTo(e.Text, "textLayoutManager") && objc.Msg(e.Text, "textLayoutManager") != objc.Nil
}
