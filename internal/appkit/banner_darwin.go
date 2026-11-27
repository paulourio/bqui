//go:build darwin

package appkit

import "github.com/paulourio/bqui/internal/objc"

// Banner is a red-bordered error box: an icon, a bold title, and wrapping,
// selectable detail and hint text.
type Banner struct {
	View objc.ID

	title, detail, hint, icon objc.ID
}

const (
	bannerRadius  = 8.0
	bannerPadding = 10.0
	bannerGap     = 4.0
	bannerIcon    = 18.0
	bannerFill    = 0.08
	boxTypeCustom = int64(4)
	bannerNoTitle = int64(0)
	vertical      = int64(1)
	stackLeading  = int64(5) // NSLayoutAttributeLeading
)

// NewBanner creates a hidden error banner (use with Auto Layout).
func NewBanner() *Banner {
	b := &Banner{}

	red := SystemColor("systemRedColor")
	box := objc.New("NSBox")
	objc.Msg(box, "setBoxType:", boxTypeCustom)
	objc.Msg(box, "setTitlePosition:", bannerNoTitle)
	objc.Msg(box, "setBorderColor:", red)
	objc.Msg(box, "setBorderWidth:", 1.0)
	objc.Msg(box, "setCornerRadius:", bannerRadius)
	objc.Msg(box, "setFillColor:", objc.Msg(red, "colorWithAlphaComponent:", bannerFill))
	objc.Msg(box, "setContentViewMargins:", Size{Width: 0, Height: 0})
	UseAutoLayout(box)

	icon := objc.Msg(objc.Cls("NSImageView"), "imageViewWithImage:", SymbolImage("exclamationmark.triangle.fill", "Error"))
	objc.Msg(icon, "setContentTintColor:", red)
	UseAutoLayout(icon)
	b.icon = icon

	b.title = Label("", 0)
	objc.Msg(b.title, "setFont:", objc.Msg(objc.Cls("NSFont"), "boldSystemFontOfSize:", 13.0))
	objc.Msg(b.title, "setTextColor:", red)

	b.detail = wrappingLabel("labelColor")
	b.hint = wrappingLabel("secondaryLabelColor")

	text := objc.Msg(objc.Cls("NSStackView"), "stackViewWithViews:", objc.Array(b.title, b.detail, b.hint))
	objc.Msg(text, "setOrientation:", vertical)
	objc.Msg(text, "setAlignment:", stackLeading)
	objc.Msg(text, "setSpacing:", bannerGap)
	UseAutoLayout(text)

	content := objc.Msg(box, "contentView")
	objc.Msg(content, "addSubview:", icon)
	objc.Msg(content, "addSubview:", text)

	Activate(
		Equal(Anchor(icon, "leadingAnchor"), Anchor(content, "leadingAnchor"), bannerPadding),
		Equal(Anchor(icon, "topAnchor"), Anchor(content, "topAnchor"), bannerPadding),
		EqualConst(Anchor(icon, "widthAnchor"), bannerIcon),
		EqualConst(Anchor(icon, "heightAnchor"), bannerIcon),
		Equal(Anchor(text, "leadingAnchor"), Anchor(icon, "trailingAnchor"), bannerPadding),
		Equal(Anchor(text, "trailingAnchor"), Anchor(content, "trailingAnchor"), -bannerPadding),
		Equal(Anchor(text, "topAnchor"), Anchor(content, "topAnchor"), bannerPadding),
		Equal(Anchor(content, "bottomAnchor"), Anchor(text, "bottomAnchor"), bannerPadding),
		// Wrapping labels fill the text column so they wrap at its width.
		Equal(Anchor(b.detail, "widthAnchor"), Anchor(text, "widthAnchor"), 0),
		Equal(Anchor(b.hint, "widthAnchor"), Anchor(text, "widthAnchor"), 0),
	)

	b.View = box
	SetHidden(box, true)

	return b
}

func wrappingLabel(color string) objc.ID {
	l := objc.Msg(objc.Cls("NSTextField"), "wrappingLabelWithString:", objc.String(""))
	objc.Msg(l, "setSelectable:", true)
	objc.Msg(l, "setTextColor:", SystemColor(color))
	objc.Msg(l, "setFont:", objc.Msg(objc.Cls("NSFont"), "systemFontOfSize:", 12.0))
	UseAutoLayout(l)

	return l
}

// Show fills and reveals the banner as an error (red).
func (b *Banner) Show(title, detail, hint string) { b.ShowStyled(title, detail, hint, false) }

// ShowStyled reveals the banner as an error (red) or a warning (orange).
// An empty detail or hint hides that line.
func (b *Banner) ShowStyled(title, detail, hint string, warning bool) {
	color := SystemColor("systemRedColor")
	if warning {
		color = SystemColor("systemOrangeColor")
	}

	objc.Msg(b.View, "setBorderColor:", color)
	objc.Msg(b.View, "setFillColor:", objc.Msg(color, "colorWithAlphaComponent:", bannerFill))
	objc.Msg(b.title, "setTextColor:", color)
	objc.Msg(b.icon, "setContentTintColor:", color)

	SetString(b.title, title)
	SetString(b.detail, detail)
	SetString(b.hint, hint)
	SetHidden(b.detail, detail == "")
	SetHidden(b.hint, hint == "")
	SetHidden(b.View, false)
}

// Hide hides the banner.
func (b *Banner) Hide() { SetHidden(b.View, true) }

// Visible reports whether the banner is shown.
func (b *Banner) Visible() bool { return !objc.Send[bool](b.View, "isHidden") }

// Text returns title, detail and hint (smoke checks).
func (b *Banner) Text() (title, detail, hint string) {
	return StringValue(b.title), StringValue(b.detail), StringValue(b.hint)
}

// VStack stacks views vertically with spacing; each view spans the stack
// width minus inset on both sides.
func VStack(spacing, inset float64, views ...objc.ID) objc.ID {
	st := objc.Msg(objc.Cls("NSStackView"), "stackViewWithViews:", objc.Array(views...))
	objc.Msg(st, "setOrientation:", vertical)
	objc.Msg(st, "setSpacing:", spacing)
	objc.Msg(st, "setDetachesHiddenViews:", true)
	UseAutoLayout(st)

	for _, v := range views {
		UseAutoLayout(v)
		Activate(Equal(Anchor(v, "widthAnchor"), Anchor(st, "widthAnchor"), -2*inset))
	}

	return st
}
