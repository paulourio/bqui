//go:build darwin

package appkit

import "github.com/paulourio/bqui/internal/objc"

// TextFont selects the font TextWidth measures with.
type TextFont int

// Fonts for TextWidth.
const (
	FontRegular TextFont = iota
	FontMono
	FontHeader
)

const headerFontSize = 11.0

// TextWidth returns the rendered single-line width of text in the grid's
// regular or mono cell font, or in the table header font.
func TextWidth(text string, font TextFont) float64 {
	if font != FontHeader {
		return MeasureGridTextWidth(text, font == FontMono)
	}

	if text == "" {
		return 0
	}

	var w float64

	objc.WithPool(func() {
		fontKey, _ := attributeKeys()
		f := objc.Msg(objc.Cls("NSFont"), "systemFontOfSize:", headerFontSize)
		attrs := objc.Msg(objc.Cls("NSDictionary"), "dictionaryWithObject:forKey:", f, fontKey)
		w = objc.Send[Size](objc.String(text), "sizeWithAttributes:", attrs).Width
	})

	return w
}
