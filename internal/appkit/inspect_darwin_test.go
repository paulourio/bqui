//go:build darwin

package appkit

import (
	"os"
	"runtime"
	"testing"

	"github.com/paulourio/bqui/internal/objc"
)

func TestMain(m *testing.M) {
	runtime.LockOSThread()

	if err := objc.Load(); err != nil {
		panic(err)
	}

	done := make(chan int, 1)

	go func() {
		done <- m.Run()
	}()

	rl := objc.Msg(objc.Cls("NSRunLoop"), "mainRunLoop")

	mode, err := objc.Const(objc.Foundation, "NSDefaultRunLoopMode")
	if err != nil || mode == objc.Nil {
		mode = objc.String("kCFRunLoopDefaultMode")
	}

	for {
		select {
		case code := <-done:
			os.Exit(code)
		default:
			date := objc.Msg(objc.Cls("NSDate"), "dateWithTimeIntervalSinceNow:", 0.01)
			objc.Send[bool](rl, "runMode:beforeDate:", mode, date)
		}
	}
}

func onMain(t *testing.T, fn func()) {
	t.Helper()

	done := make(chan struct{})

	objc.Main(func() {
		defer close(done)

		fn()
	})
	<-done
}

func TestInspectNilGuards(t *testing.T) {
	onMain(t, func() {
		if b := ViewBounds(objc.Nil); b != (Rect{}) {
			t.Errorf("ViewBounds(nil) = %+v, want zero", b)
		}

		if f := ViewFrameIn(objc.Nil, objc.Nil); f != (Rect{}) {
			t.Errorf("ViewFrameIn(nil, nil) = %+v, want zero", f)
		}

		if IsVisible(objc.Nil) {
			t.Error("IsVisible(nil) should be false")
		}

		if sz := MeasureText(objc.Nil, 100); sz != (Size{}) {
			t.Errorf("MeasureText(nil) = %+v, want zero", sz)
		}

		if g := OutlineRowGeometry(nil, 0, "a"); g.Found {
			t.Error("OutlineRowGeometry(nil) should not be found")
		}

		if c := TableRenderedCell(nil, 0, "a"); c.Found {
			t.Error("TableRenderedCell(nil) should not be found")
		}

		if segs := SegmentedGeometry(nil); segs != nil {
			t.Errorf("SegmentedGeometry(nil) = %+v, want nil", segs)
		}

		total, vis, over := ToolbarOverflow(objc.Nil)
		if total != 0 || vis != 0 || over != nil {
			t.Errorf("ToolbarOverflow(nil) = (%d, %d, %v), want zeros", total, vis, over)
		}

		WindowResize(objc.Nil, 800, 600)
		SetContentMinSize(objc.Nil, 400, 300)
		(*Outline)(nil).InvalidateRowHeights()
		(&Outline{}).InvalidateRowHeights()
	})
}

func TestTextAlignmentForArch(t *testing.T) {
	if TextAlignmentLeft != 0 {
		t.Errorf("TextAlignmentLeft = %d, want 0", TextAlignmentLeft)
	}

	switch runtime.GOARCH {
	case "amd64":
		if TextAlignmentCenter != 2 || TextAlignmentRight != 1 {
			t.Errorf("darwin/amd64 alignments = (Center:%d, Right:%d), want (2, 1)", TextAlignmentCenter, TextAlignmentRight)
		}
	case "arm64":
		if TextAlignmentCenter != 1 || TextAlignmentRight != 2 {
			t.Errorf("darwin/arm64 alignments = (Center:%d, Right:%d), want (1, 2)", TextAlignmentCenter, TextAlignmentRight)
		}
	default:
		t.Fatalf("unexpected GOARCH %q", runtime.GOARCH)
	}
}

func TestWrappingLabelAndAddCenteredWrapping(t *testing.T) {
	onMain(t, func() {
		const longText = "Run a query to inspect its execution plan and stage details across multiple wrapped lines."

		AddCenteredWrapping(objc.Nil, objc.Nil, 16)

		lbl := WrappingLabel(longText, 12)
		if objc.Send[bool](lbl, "usesSingleLineMode") {
			t.Error("WrappingLabel usesSingleLineMode should be false")
		}

		if got := objc.Send[uint64](lbl, "lineBreakMode"); got != lineBreakByWordWrapping {
			t.Errorf("WrappingLabel lineBreakMode = %d, want %d", got, lineBreakByWordWrapping)
		}

		if got := objc.Send[int64](lbl, "maximumNumberOfLines"); got != 0 {
			t.Errorf("WrappingLabel maximumNumberOfLines = %d, want 0", got)
		}

		if prio := objc.Send[float32](lbl, "contentCompressionResistancePriorityForOrientation:", OrientHorizontal); prio != PriorityDefaultLow {
			t.Errorf("WrappingLabel horizontal compression priority = %v, want %v", prio, PriorityDefaultLow)
		}

		if prio := objc.Send[float32](lbl, "contentCompressionResistancePriorityForOrientation:", OrientVertical); prio != PriorityDefaultHigh {
			t.Errorf("WrappingLabel vertical compression priority = %v, want %v", prio, PriorityDefaultHigh)
		}

		parent := NewView()
		objc.Msg(parent, "setFrame:", MakeRect(0, 0, 220, 200))

		note := SecondaryLabel(longText)
		AddCenteredWrapping(parent, note, 16)
		objc.Msg(parent, "layoutSubtreeIfNeeded")

		if objc.Send[bool](note, "usesSingleLineMode") {
			t.Error("AddCenteredWrapping usesSingleLineMode should be false")
		}

		if got := objc.Send[uint64](note, "lineBreakMode"); got != lineBreakByWordWrapping {
			t.Errorf("AddCenteredWrapping lineBreakMode = %d, want %d", got, lineBreakByWordWrapping)
		}

		if got := objc.Send[int64](note, "alignment"); got != TextAlignmentCenter {
			t.Errorf("AddCenteredWrapping alignment = %d, want %d", got, TextAlignmentCenter)
		}

		if prio := objc.Send[float32](note, "contentCompressionResistancePriorityForOrientation:", OrientHorizontal); prio != PriorityDefaultLow {
			t.Errorf("AddCenteredWrapping horizontal compression priority = %v, want %v", prio, PriorityDefaultLow)
		}

		if cell := objc.Msg(note, "cell"); cell == objc.Nil || !objc.Send[bool](cell, "wraps") {
			t.Error("AddCenteredWrapping cell wraps should be true")
		}

		frame := objc.Send[Rect](note, "frame")
		if frame.Size.Width > 220-32+1 {
			t.Errorf("wrapped label width = %v, exceeds parent width minus insets (%v)", frame.Size.Width, 220-32)
		}

		if frame.Origin.X < 16-0.5 || frame.Origin.X+frame.Size.Width > 220-16+0.5 {
			t.Errorf("wrapped label frame = %+v, exceeds horizontal bounds [16, 204]", frame)
		}

		single := MeasureText(note, 0)
		wrapped := MeasureText(note, frame.Size.Width)

		if wrapped.Height <= single.Height {
			t.Errorf("wrapped height %v should exceed single-line height %v", wrapped.Height, single.Height)
		}

		for _, tc := range []struct {
			name string
			make func() objc.ID
		}{
			{name: "SecondaryLabel", make: func() objc.ID { return SecondaryLabel(longText) }},
			{name: "WrappingLabel", make: func() objc.ID { return WrappingLabel(longText, 12) }},
		} {
			win := NewWindow("wrap-"+tc.name, MakeRect(0, 0, 220, 200))
			cv := objc.Msg(win, "contentView")
			child := tc.make()
			AddCenteredWrapping(cv, child, 16)
			WindowResize(win, 220, 200)

			f := objc.Send[Rect](child, "frame")
			s := MeasureText(child, 0)

			if f.Origin.X < 16-0.5 || f.Origin.X+f.Size.Width > 220-16+0.5 || f.Size.Width > 220-32+1 {
				t.Errorf("%s at 220px: frame=%+v exceeds [16, 204] (max width 188)", tc.name, f)
			}

			if f.Size.Height <= s.Height {
				t.Errorf("%s at 220px: frame.Height=%v <= single-line height %v (did not wrap)", tc.name, f.Size.Height, s.Height)
			}

			WindowResize(win, 160, 200)

			fn := objc.Send[Rect](child, "frame")
			if fn.Origin.X < 16-0.5 || fn.Origin.X+fn.Size.Width > 160-16+0.5 || fn.Size.Width > 160-32+1 {
				t.Errorf("%s at 160px: frame=%+v exceeds [16, 144] (max width 128)", tc.name, fn)
			}

			if fn.Size.Height <= f.Size.Height {
				t.Errorf("%s narrowed 220->160: frame.Height=%v <= 220px height %v", tc.name, fn.Size.Height, f.Size.Height)
			}

			objc.Msg(win, "close")
		}
	})
}

func TestSegmentedHelpersAndGeometry(t *testing.T) {
	onMain(t, func() {
		s := NewSegmented([]string{"Schema", "Details", "Preview"}, func(int) {})
		if s.SegmentCount() != 3 {
			t.Fatalf("SegmentCount = %d, want 3", s.SegmentCount())
		}

		s.SetFont(11)
		s.SetSegmentLabel(1, "Runs (12)")
		s.FitWidths(16, 40)

		if got := s.SegmentLabel(1); got != "Runs (12)" {
			t.Errorf("SegmentLabel(1) = %q, want %q", got, "Runs (12)")
		}

		geoms := SegmentedGeometry(s)
		if len(geoms) != 3 {
			t.Fatalf("len(SegmentedGeometry) = %d, want 3", len(geoms))
		}

		for i, g := range geoms {
			if g.TextWidth <= 0 {
				t.Errorf("segment %d (%q) TextWidth = %v, want > 0", i, g.Label, g.TextWidth)
			}

			if g.Frame.Size.Width < g.TextWidth {
				t.Errorf("segment %d (%q) Frame.Width = %v < TextWidth = %v", i, g.Label, g.Frame.Size.Width, g.TextWidth)
			}

			if s.SegmentWidth(i) != g.Frame.Size.Width {
				t.Errorf("segment %d width mismatch: SegmentWidth=%v, Geom=%v", i, s.SegmentWidth(i), g.Frame.Size.Width)
			}
		}

		if s.SegmentLabel(-1) != "" || s.SegmentWidth(99) != 0 {
			t.Error("out-of-range segment queries should return zero values")
		}
	})
}

type stubOutlineSrc struct {
	items []uint64
}

func (s *stubOutlineSrc) Children(parent uint64) []uint64 {
	if parent == 0 {
		return s.items
	}

	return nil
}

func (*stubOutlineSrc) Expandable(uint64) bool { return false }

func (s *stubOutlineSrc) Cell(item uint64) OutlineCell {
	if item == 3 {
		return OutlineCell{
			Title:    "very_long_unbroken_outline_column_identifier",
			Wrap:     true,
			CharWrap: true,
		}
	}

	return OutlineCell{Title: "Property", Wrap: item == 1}
}

func (*stubOutlineSrc) CellText(item uint64, _ string) CellText {
	if item == 3 {
		return CellText{Text: "STRING", Wrap: true}
	}

	return CellText{
		Text:     "projects/example-project/locations/us/keyRings/ring/cryptoKeys/very_long_unbroken_token_identifier",
		Wrap:     true,
		CharWrap: item == 2,
	}
}

func TestOutlineCellsAndOptions(t *testing.T) {
	onMain(t, func() {
		plainCell := newOutlineCell("testPlain", false, true, true)
		objc.Msg(plainCell, "setFrame:", MakeRect(0, 0, 180, 40))
		objc.Msg(plainCell, "layoutSubtreeIfNeeded")

		if objc.Msg(plainCell, "imageView") != objc.Nil {
			t.Error("plain outline cell should not allocate an NSImageView")
		}

		plainTF := objc.Msg(plainCell, "textField")
		plainFrame := objc.Send[Rect](plainTF, "frame")

		if plainFrame.Origin.X > 4 {
			t.Errorf("plain outline cell text leading = %v, want <= 4 (no 24pt gutter)", plainFrame.Origin.X)
		}

		if objc.Send[bool](plainTF, "usesSingleLineMode") {
			t.Error("wrapping outline cell should have usesSingleLineMode=false")
		}

		iconCell := newOutlineCell("testIcon", true, false, false)
		objc.Msg(iconCell, "setFrame:", MakeRect(0, 0, 180, 24))
		objc.Msg(iconCell, "layoutSubtreeIfNeeded")

		if objc.Msg(iconCell, "imageView") == objc.Nil {
			t.Error("icon outline cell should allocate an NSImageView")
		}

		iconFrame := objc.Send[Rect](objc.Msg(iconCell, "textField"), "frame")
		if iconFrame.Origin.X <= plainFrame.Origin.X+10 {
			t.Errorf("icon outline cell text leading = %v, expected > plain (%v) + 10", iconFrame.Origin.X, plainFrame.Origin.X)
		}

		charCell := newTextCellWithBreak("testCharWrap", true, true, true)
		charTF := objc.Msg(charCell, "textField")

		if got := objc.Send[uint64](charTF, "lineBreakMode"); got != lineBreakByCharWrapping {
			t.Errorf("charWrap lineBreakMode = %d, want %d", got, lineBreakByCharWrapping)
		}

		if prio := objc.Send[float32](charTF, "contentCompressionResistancePriorityForOrientation:", OrientHorizontal); prio != PriorityDefaultLow {
			t.Errorf("text cell horizontal compression priority = %v, want %v", prio, PriorityDefaultLow)
		}

		wordCell := newTextCell("testWordWrap", true, true)
		wordTF := objc.Msg(wordCell, "textField")

		if got := objc.Send[uint64](wordTF, "lineBreakMode"); got != lineBreakByWordWrapping {
			t.Errorf("wordWrap lineBreakMode = %d, want %d", got, lineBreakByWordWrapping)
		}

		o := NewOutline(&stubOutlineSrc{items: []uint64{1, 2}}, OutlineOptions{
			Columns: []Column{
				{ID: "key", Title: "Property", Width: 100},
				{ID: "value", Title: "Value", Width: 140},
			},
			AutomaticRowHeights: true,
			NoHorizontalScroll:  true,
		})
		if objc.Send[bool](o.Scroll, "hasHorizontalScroller") {
			t.Error("NoHorizontalScroll=true should disable horizontal scroller")
		}

		o.Reload(0)
		o.InvalidateRowHeights()

		rightCell := textCell(o.View, CellText{Text: "42", Right: true}, true)
		if got := objc.Send[int64](objc.Msg(rightCell, "textField"), "alignment"); got != TextAlignmentRight {
			t.Errorf("Outline right-aligned cell alignment = %d, want %d", got, TextAlignmentRight)
		}

		if g := OutlineRowGeometry(o, -1, "value"); g.Found {
			t.Error("negative row should return Found=false")
		}

		if g := OutlineRowGeometry(o, 0, "missing"); g.Found {
			t.Error("missing column should return Found=false")
		}
	})
}

func TestOutlineAutomaticRowHeightsWithSingleLineSibling(t *testing.T) {
	onMain(t, func() {
		o := NewOutline(&stubOutlineSrc{items: []uint64{1, 2}}, OutlineOptions{
			Columns: []Column{
				{ID: "key", Title: "Property", Width: 100, MinWidth: 60},
				{ID: "value", Title: "Value", Width: 160, MinWidth: 80},
			},
			AutomaticRowHeights: true,
			NoHorizontalScroll:  true,
		})

		if got := objc.Send[int64](o.View, "style"); got != tableStylePlain {
			t.Errorf("multi-column outline style = %d, want %d (Plain)", got, tableStylePlain)
		}

		win := NewWindow("outline-auto-height", MakeRect(0, 0, 280, 240))
		defer objc.Msg(win, "close")

		AddFilling(objc.Msg(win, "contentView"), o.Scroll, 0)
		o.Reload(0)
		WindowResize(win, 280, 240)
		o.InvalidateRowHeights()

		colIdx := objc.Send[int64](o.View, "columnWithIdentifier:", objc.String("value"))
		objc.Msg(o.View, "viewAtColumn:row:makeIfNecessary:", colIdx, int64(1), true)

		g := OutlineRowGeometry(o, 1, "value")
		if !g.Found {
			t.Fatal("expected wrapped row 1 cell to be found")
		}

		if g.RowBounds.Size.Height <= 28 {
			t.Errorf("row 1 height = %v, want > 28 (single-line sibling must not clamp AutomaticRowHeights)", g.RowBounds.Size.Height)
		}

		if g.WrappedSize.Height > g.TextFrame.Size.Height+2 {
			t.Errorf("wrapped text clipped: WrappedSize.Height=%v > TextFrame.Height=%v", g.WrappedSize.Height, g.TextFrame.Size.Height)
		}

		// Verify OutlineCell.CharWrap uses separate reuse IDs and wraps long unbroken outline-column titles.
		plainCharCell := o.outlineCell(o.View, OutlineCell{Title: "very_long_unbroken_outline_column_identifier", Wrap: true, CharWrap: true})
		if got := objc.GoString(objc.Msg(plainCharCell, "identifier")); got != "BQUIOutlineCellPlainWrapCharAuto" {
			t.Errorf("plain charWrap reuse ID = %q, want %q", got, "BQUIOutlineCellPlainWrapCharAuto")
		}

		iconCharCell := o.outlineCell(o.View, OutlineCell{Title: "very_long_unbroken_outline_column_identifier", Symbol: "tablecells", Wrap: true, CharWrap: true})
		if got := objc.GoString(objc.Msg(iconCharCell, "identifier")); got != "BQUIOutlineCellIconWrapCharAuto" {
			t.Errorf("icon charWrap reuse ID = %q, want %q", got, "BQUIOutlineCellIconWrapCharAuto")
		}

		o2 := NewOutline(&stubOutlineSrc{items: []uint64{3}}, OutlineOptions{
			Columns: []Column{
				{ID: "key", Title: "Property", Width: 100, MinWidth: 60},
				{ID: "value", Title: "Value", Width: 140, MinWidth: 60},
			},
			AutomaticRowHeights: true,
			NoHorizontalScroll:  true,
		})
		win2 := NewWindow("outline-charwrap", MakeRect(0, 0, 260, 200))
		defer objc.Msg(win2, "close")

		AddFilling(objc.Msg(win2, "contentView"), o2.Scroll, 0)
		o2.Reload(0)
		WindowResize(win2, 260, 200)
		o2.InvalidateRowHeights()

		keyColIdx := objc.Send[int64](o2.View, "columnWithIdentifier:", objc.String("key"))
		objc.Msg(o2.View, "viewAtColumn:row:makeIfNecessary:", keyColIdx, int64(0), true)

		gk := OutlineRowGeometry(o2, 0, "key")
		if !gk.Found {
			t.Fatal("expected outline CharWrap row 0 key cell to be found")
		}

		if gk.LineBreakMode != int(lineBreakByCharWrapping) {
			t.Errorf("outline CharWrap LineBreakMode = %d, want %d", gk.LineBreakMode, lineBreakByCharWrapping)
		}

		if gk.RowBounds.Size.Height <= 22 {
			t.Errorf("outline CharWrap row height = %v, want > 22", gk.RowBounds.Size.Height)
		}

		if gk.WrappedSize.Height > gk.TextFrame.Size.Height+2 {
			t.Errorf("outline CharWrap clipped: WrappedSize.Height=%v > TextFrame.Height=%v", gk.WrappedSize.Height, gk.TextFrame.Size.Height)
		}

		// Nil / uninitialized Outline guards must not panic.
		(*Outline)(nil).Expand(0, true)
		(&Outline{}).Expand(0, true)
	})
}
