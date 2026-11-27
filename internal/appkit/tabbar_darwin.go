//go:build darwin

package appkit

import "github.com/paulourio/bqui/internal/objc"

// TabBar is a row of borderless buttons with colored titles that behaves
// like a select-one segmented control. Inspector panes use it because, in
// the macOS 26 inspector, bezeled controls (segmented controls and push
// buttons alike) draw nothing for their unselected parts; titles colored
// explicitly render like labels.
type TabBar struct {
	View    objc.ID
	buttons []objc.ID
	labels  []string
	enabled []bool
	sel     int
}

const (
	tabSpacing  = 14.0
	tabFontSize = 13.0
)

// NewTabBar creates a tab bar; onChange receives the clicked index.
func NewTabBar(labels []string, onChange func(index int)) *TabBar {
	t := &TabBar{}

	for i, l := range labels {
		b := Button(l, func() {
			if t.enabled[i] {
				t.Select(i)
				onChange(i)
			}
		})
		objc.Msg(b, "setBordered:", false)
		t.buttons = append(t.buttons, b)
		t.labels = append(t.labels, l)
		t.enabled = append(t.enabled, true)
	}

	t.View = objc.Msg(objc.Cls("NSStackView"), "stackViewWithViews:", objc.Array(t.buttons...))
	objc.Msg(t.View, "setSpacing:", tabSpacing)
	t.Select(0)

	return t
}

// restyle colors each title: accent and bold when selected, secondary
// otherwise, tertiary when disabled.
func (t *TabBar) restyle() {
	fontKey, colorKey := attributeKeys()

	for i, b := range t.buttons {
		color, font := SystemColor("secondaryLabelColor"), objc.Msg(objc.Cls("NSFont"), "systemFontOfSize:", tabFontSize)

		switch {
		case !t.enabled[i]:
			color = SystemColor("tertiaryLabelColor")
		case i == t.sel:
			color, font = SystemColor("controlAccentColor"), objc.Msg(objc.Cls("NSFont"), "boldSystemFontOfSize:", tabFontSize)
		}

		attrs := objc.Msg(objc.Cls("NSDictionary"), "dictionaryWithObjects:forKeys:", objc.Array(font, color), objc.Array(fontKey, colorKey))
		title := objc.Msg(objc.Msg(objc.Msg(objc.Cls("NSAttributedString"), "alloc"), "initWithString:attributes:", objc.String(t.labels[i]), attrs), "autorelease")
		objc.Msg(b, "setAttributedTitle:", title)
	}
}

// Selected returns the selected tab.
func (t *TabBar) Selected() int { return t.sel }

// Select selects a tab without firing the callback.
func (t *TabBar) Select(i int) {
	t.sel = i
	t.restyle()
}

// SetEnabled enables or disables one tab.
func (t *TabBar) SetEnabled(i int, enabled bool) {
	t.enabled[i] = enabled
	t.restyle()
}

// Enabled reports whether a tab is enabled.
func (t *TabBar) Enabled(i int) bool { return t.enabled[i] }

// Button returns one tab's button (diagnostics).
func (t *TabBar) Button(i int) objc.ID { return t.buttons[i] }
