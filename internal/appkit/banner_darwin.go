//go:build darwin

package appkit

import "github.com/paulourio/bqui/internal/objc"

// Banner is a red-bordered error box: an icon, a bold title, and wrapping,
// selectable detail and hint text. As a note it can also list items that
// start with a link (ShowNoteLinks).
type Banner struct {
	View objc.ID

	title, detail, hint, icon objc.ID

	links objc.ID      // vertical stack of link rows
	rows  []bannerLink // reused across calls, grown on demand
	runs  []func()     // the shown links' actions, by row
}

// BannerLink is one row of a note: Label is a clickable link that runs Run,
// followed by wrapping Text.
type BannerLink struct {
	Label, Text string
	Run         func()
}

type bannerLink struct {
	row, button, text objc.ID
}

const (
	bannerRadius  = 8.0
	bannerPadding = 10.0
	bannerGap     = 4.0
	bannerIcon    = 18.0
	bannerFill    = 0.08
	noteFill      = 0.05
	// bannerTitleSize is the title's point size; fontWeightMedium is
	// NSFontWeightMedium.
	bannerTitleSize  = 13.0
	fontWeightMedium = 0.23
	bannerTextSize   = 12.0
	linkGap          = 8.0
	boxTypeCustom    = int64(4)
	bannerNoTitle    = int64(0)
	vertical         = int64(1)
	stackLeading     = int64(5) // NSLayoutAttributeLeading
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

	b.title = WrappingLabel("", 0)
	objc.Msg(b.title, "setFont:", objc.Msg(objc.Cls("NSFont"), "boldSystemFontOfSize:", 13.0))
	objc.Msg(b.title, "setTextColor:", red)
	UseAutoLayout(b.title)

	b.detail = wrappingLabel("labelColor")
	b.hint = wrappingLabel("secondaryLabelColor")

	b.links = objc.Msg(objc.Cls("NSStackView"), "stackViewWithViews:", objc.Array())
	objc.Msg(b.links, "setOrientation:", vertical)
	objc.Msg(b.links, "setAlignment:", stackLeading)
	objc.Msg(b.links, "setSpacing:", bannerGap)
	UseAutoLayout(b.links)

	text := objc.Msg(objc.Cls("NSStackView"), "stackViewWithViews:", objc.Array(b.title, b.detail, b.links, b.hint))
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
		Equal(Anchor(b.title, "widthAnchor"), Anchor(text, "widthAnchor"), 0),
		Equal(Anchor(b.detail, "widthAnchor"), Anchor(text, "widthAnchor"), 0),
		Equal(Anchor(b.hint, "widthAnchor"), Anchor(text, "widthAnchor"), 0),
		Equal(Anchor(b.links, "widthAnchor"), Anchor(text, "widthAnchor"), 0),
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
	objc.Msg(b.View, "setBorderWidth:", 1.0)
	objc.Msg(b.View, "setFillColor:", objc.Msg(color, "colorWithAlphaComponent:", bannerFill))
	objc.Msg(b.title, "setTextColor:", color)
	objc.Msg(b.title, "setFont:", objc.Msg(objc.Cls("NSFont"), "boldSystemFontOfSize:", bannerTitleSize))
	objc.Msg(b.icon, "setImage:", SymbolImage("exclamationmark.triangle.fill", "Error"))
	objc.Msg(b.icon, "setContentTintColor:", color)

	b.fill(title, detail, hint)
}

// ShowNote reveals the banner as a quiet note: no border, a faint neutral
// fill, a gray icon and plain text. For expected, transient states (SQL
// being typed) where red would read as a serious failure.
func (b *Banner) ShowNote(title, detail, hint string) {
	objc.Msg(b.View, "setBorderWidth:", 0.0)
	objc.Msg(b.View, "setFillColor:", objc.Msg(SystemColor("labelColor"), "colorWithAlphaComponent:", noteFill))
	objc.Msg(b.title, "setTextColor:", SystemColor("labelColor"))
	objc.Msg(b.title, "setFont:", objc.Msg(objc.Cls("NSFont"), "systemFontOfSize:weight:", bannerTitleSize, fontWeightMedium))
	objc.Msg(b.icon, "setImage:", SymbolImage("exclamationmark.circle", "Note"))
	objc.Msg(b.icon, "setContentTintColor:", SystemColor("secondaryLabelColor"))

	b.fill(title, detail, hint)
}

// ShowNoteLinks shows a note whose items each start with a link.
func (b *Banner) ShowNoteLinks(title string, links []BannerLink, hint string) {
	b.ShowNote(title, "", hint)

	for len(b.rows) < len(links) {
		b.rows = append(b.rows, b.newLinkRow(len(b.rows)))
	}

	b.runs = b.runs[:0]

	for i, r := range b.rows {
		if i >= len(links) {
			SetHidden(r.row, true)

			continue
		}

		l := links[i]
		b.runs = append(b.runs, l.Run)
		setLinkTitle(r.button, l.Label)
		SetString(r.text, l.Text)
		SetHidden(r.row, false)
	}

	SetHidden(b.links, len(links) == 0)
}

// LinkRows returns the shown link rows as "label text" (smoke checks).
func (b *Banner) LinkRows() []string {
	out := make([]string, len(b.runs))

	for i := range b.runs {
		out[i] = objc.GoString(objc.Msg(objc.Msg(b.rows[i].button, "attributedTitle"), "string")) + " " + StringValue(b.rows[i].text)
	}

	return out
}

// LinkButton returns the i-th link's button (smoke checks).
func (b *Banner) LinkButton(i int) objc.ID {
	if i < 0 || i >= len(b.runs) {
		return objc.Nil
	}

	return b.rows[i].button
}

// newLinkRow builds one reusable row: a link button and wrapping text
// sharing a baseline. Its button runs b.runs[i].
func (b *Banner) newLinkRow(i int) bannerLink {
	r := bannerLink{row: NewView(), text: wrappingLabel("labelColor")}
	r.button = Button("", func() {
		if i < len(b.runs) && b.runs[i] != nil {
			b.runs[i]()
		}
	})
	objc.Msg(r.button, "setBordered:", false)
	UseAutoLayout(r.button)
	SetHuggingPriority(r.button, OrientHorizontal, PriorityRequired)
	SetCompressionResistance(r.button, OrientHorizontal, PriorityRequired)

	objc.Msg(r.row, "addSubview:", r.button)
	objc.Msg(r.row, "addSubview:", r.text)
	objc.Msg(b.links, "addArrangedSubview:", r.row)

	Activate(
		Equal(Anchor(r.row, "widthAnchor"), Anchor(b.links, "widthAnchor"), 0),
		Equal(Anchor(r.button, "leadingAnchor"), Anchor(r.row, "leadingAnchor"), 0),
		Equal(Anchor(r.button, "topAnchor"), Anchor(r.row, "topAnchor"), 0),
		Equal(Anchor(r.text, "leadingAnchor"), Anchor(r.button, "trailingAnchor"), linkGap),
		Equal(Anchor(r.text, "trailingAnchor"), Anchor(r.row, "trailingAnchor"), 0),
		Equal(Anchor(r.text, "firstBaselineAnchor"), Anchor(r.button, "firstBaselineAnchor"), 0),
		GreaterOrEqual(Anchor(r.row, "bottomAnchor"), Anchor(r.text, "bottomAnchor"), 0),
		GreaterOrEqual(Anchor(r.row, "bottomAnchor"), Anchor(r.button, "bottomAnchor"), 0),
	)

	return r
}

// setLinkTitle styles a borderless button's title as a link: accent color,
// underlined, in the banner's text size.
func setLinkTitle(button objc.ID, title string) {
	fontKey, colorKey := attributeKeys()
	underline := constant("NSUnderlineStyleAttributeName", "NSUnderline")
	attrs := objc.Msg(objc.Cls("NSDictionary"), "dictionaryWithObjects:forKeys:",
		objc.Array(objc.Msg(objc.Cls("NSFont"), "systemFontOfSize:", bannerTextSize), SystemColor("linkColor"), objc.Number(1)),
		objc.Array(fontKey, colorKey, underline))
	str := objc.Msg(objc.Msg(objc.Cls("NSAttributedString"), "alloc"), "initWithString:attributes:", objc.String(title), attrs)
	objc.Msg(button, "setAttributedTitle:", str)
	objc.Msg(str, "release")
}

func (b *Banner) fill(title, detail, hint string) {
	SetHidden(b.links, true)
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
