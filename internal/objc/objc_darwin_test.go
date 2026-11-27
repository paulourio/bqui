//go:build darwin

package objc

import (
	"math"
	"testing"
)

// rect mirrors CGRect; it exercises struct arguments and struct returns,
// which use objc_msgSend_stret on amd64 and plain objc_msgSend on arm64.
type rect struct {
	X, Y, W, H float64
}

func TestMain(m *testing.M) {
	if err := Load(); err != nil {
		panic(err)
	}

	m.Run()
}

func TestStructArgumentAndReturnRoundTrip(t *testing.T) {
	WithPool(func() {
		in := rect{X: 1.5, Y: -2.25, W: 1280, H: 800}
		v := Msg(Cls("NSValue"), "valueWithRect:", in)

		if got := Send[rect](v, "rectValue"); got != in {
			t.Fatalf("rectValue = %+v, want %+v", got, in)
		}
	})
}

func TestStringRoundTripUnicode(t *testing.T) {
	WithPool(func() {
		for _, s := range []string{"", "plain", "naïve ☃ 日本語 ⌘↩", "line\nbreak\ttab"} {
			if got := GoString(String(s)); got != s {
				t.Errorf("round trip %q = %q", s, got)
			}
		}
	})
}

func TestNumberIsIdentityStableTaggedPointer(t *testing.T) {
	WithPool(func() {
		for _, v := range []uint64{1, 42, 1 << 32, (1 << 32) + (1 << 8), 1 << 40, 1 << 54, (1 << 54) + 1, 1<<55 - 1} {
			a, b := Number(v), Number(v)
			if a != b {
				t.Errorf("Number(%d) not identity-stable: %#x != %#x", v, a, b)
			}

			if got := Uint64(a); got != v {
				t.Errorf("Uint64(Number(%d)) = %d", v, got)
			}
		}
	})
}

func TestScalarReturns(t *testing.T) {
	WithPool(func() {
		d := Msg(Cls("NSNumber"), "numberWithDouble:", math.Pi)
		if got := Send[float64](d, "doubleValue"); got != math.Pi {
			t.Errorf("doubleValue = %v", got)
		}

		i := Msg(Cls("NSNumber"), "numberWithLongLong:", int64(-7))
		if got := Send[int64](i, "longLongValue"); got != -7 {
			t.Errorf("longLongValue = %v", got)
		}

		if !Send[bool](i, "isEqualToNumber:", Msg(Cls("NSNumber"), "numberWithInt:", int32(-7))) {
			t.Error("isEqualToNumber: returned false")
		}
	})
}

func TestArrayRoundTrip(t *testing.T) {
	WithPool(func() {
		items := []ID{String("a"), String("b"), String("c")}
		got := ArrayItems(Array(items...))

		if len(got) != len(items) {
			t.Fatalf("len = %d", len(got))
		}

		for i := range items {
			if GoString(got[i]) != GoString(items[i]) {
				t.Errorf("item %d = %q", i, GoString(got[i]))
			}
		}

		if ArrayItems(Nil) != nil {
			t.Error("ArrayItems(nil) should be nil")
		}
	})
}

func TestConstReadsFrameworkString(t *testing.T) {
	WithPool(func() {
		id, err := Const(AppKit, "NSToolbarFlexibleSpaceItemIdentifier")
		if err != nil {
			t.Fatal(err)
		}

		if got := GoString(id); got != "NSToolbarFlexibleSpaceItem" {
			t.Errorf("constant = %q", got)
		}

		if _, err := Const(AppKit, "BQUIDefinitelyMissingSymbol"); err == nil {
			t.Error("missing symbol should fail")
		}
	})
}

func TestTargetInvokesGoCallback(t *testing.T) {
	WithPool(func() {
		var got ID

		target := NewTarget(func(sender ID) { got = sender })
		defer ReleaseTarget(target)

		sender := String("sender")
		Msg(target, "performSelector:withObject:", Sel(ActionSelector), sender)

		if got != sender {
			t.Fatalf("callback sender = %#x, want %#x", got, sender)
		}

		if !RespondsTo(target, ActionSelector) || RespondsTo(target, "bogusSelector:") {
			t.Error("RespondsTo mismatch")
		}
	})
}

func TestRegistry(t *testing.T) {
	var r Registry[string]

	r.Put(1, "one")

	if v, ok := r.Get(1); !ok || v != "one" {
		t.Fatalf("Get = %q, %v", v, ok)
	}

	r.Delete(1)

	if _, ok := r.Get(1); ok {
		t.Fatal("Delete did not remove")
	}
}

func TestNSRangeArgumentRoundTrip(t *testing.T) {
	type nsRange struct {
		Location uint64
		Length   uint64
	}

	WithPool(func() {
		set := Msg(Cls("NSIndexSet"), "indexSetWithIndexesInRange:", nsRange{Location: 3, Length: 4})
		if got := Send[uint64](set, "count"); got != 4 {
			t.Fatalf("count = %d, want 4", got)
		}

		if got := Send[uint64](set, "firstIndex"); got != 3 {
			t.Errorf("firstIndex = %d, want 3", got)
		}

		if got := Send[uint64](set, "lastIndex"); got != 6 {
			t.Errorf("lastIndex = %d, want 6", got)
		}

		if !Send[bool](set, "containsIndex:", uint64(5)) || Send[bool](set, "containsIndex:", uint64(7)) {
			t.Error("containsIndex: mismatch")
		}
	})
}

func TestSizeAndPointRoundTrip(t *testing.T) {
	type size struct {
		W, H float64
	}
	type point struct {
		X, Y float64
	}

	WithPool(func() {
		wantSz := size{W: 1234.5, H: 678.25}
		vs := Msg(Cls("NSValue"), "valueWithSize:", wantSz)
		if got := Send[size](vs, "sizeValue"); got != wantSz {
			t.Errorf("sizeValue = %+v, want %+v", got, wantSz)
		}

		wantPt := point{X: -42.5, Y: 99.75}
		vp := Msg(Cls("NSValue"), "valueWithPoint:", wantPt)
		if got := Send[point](vp, "pointValue"); got != wantPt {
			t.Errorf("pointValue = %+v, want %+v", got, wantPt)
		}
	})
}

func TestAttributedStringAttributes(t *testing.T) {
	WithPool(func() {
		fontKey, err := Const(AppKit, "NSFontAttributeName")
		if err != nil {
			t.Fatal(err)
		}

		colorKey, err := Const(AppKit, "NSForegroundColorAttributeName")
		if err != nil {
			t.Fatal(err)
		}

		font := Msg(Cls("NSFont"), "monospacedSystemFontOfSize:weight:", 11.0, 0.0)
		color := Msg(Cls("NSColor"), "labelColor")
		attrs := Msg(Cls("NSDictionary"), "dictionaryWithObjects:forKeys:",
			Array(font, color), Array(fontKey, colorKey))

		out := Msg(Msg(Cls("NSMutableAttributedString"), "alloc"), "init")
		defer Release(out)

		piece := Msg(Msg(Cls("NSAttributedString"), "alloc"), "initWithString:attributes:", String("hello ☃"), attrs)
		defer Release(piece)

		Msg(out, "appendAttributedString:", piece)

		if got := GoString(Msg(out, "string")); got != "hello ☃" {
			t.Errorf("attributed string = %q, want %q", got, "hello ☃")
		}

		if got := Send[uint64](out, "length"); got != 7 {
			t.Errorf("attributed length = %d, want 7", got)
		}
	})
}

func TestDelegateClassMultiArgCallbacks(t *testing.T) {
	var (
		gotTV   ID
		gotCol  ID
		gotRow  int64
		gotBool ID
	)

	sentinel := String("cell-view")
	delegateCls := LazyClass("BQUITestMultiArgDelegate", "NSObject", nil, func() []Method {
		return []Method{
			{Sel: "numberOfRowsInTableView:", Fn: func(_ ID, _ SEL, tv ID) int64 {
				gotTV = tv

				return 42
			}},
			{Sel: "tableView:viewForTableColumn:row:", Fn: func(_ ID, _ SEL, tv, col ID, row int64) ID {
				gotTV, gotCol, gotRow = tv, col, row

				return sentinel
			}},
			{Sel: "validateItem:", Fn: func(_ ID, _ SEL, item ID) bool {
				gotBool = item

				return item != Nil
			}},
		}
	})

	WithPool(func() {
		obj := Msg(Msg(delegateCls(), "alloc"), "init")
		defer Release(obj)

		tv := String("tv")
		col := String("col")

		if n := Send[int64](obj, "numberOfRowsInTableView:", tv); n != 42 || gotTV != tv {
			t.Errorf("numberOfRowsInTableView: got (%d, %#x), want (42, %#x)", n, gotTV, tv)
		}

		if view := Msg(obj, "tableView:viewForTableColumn:row:", tv, col, int64(17)); view != sentinel || gotCol != col || gotRow != 17 {
			t.Errorf("viewForTableColumn: got (%#x, %#x, %d)", view, gotCol, gotRow)
		}

		if !Send[bool](obj, "validateItem:", col) || gotBool != col {
			t.Error("validateItem: non-nil should return true")
		}

		if Send[bool](obj, "validateItem:", Nil) {
			t.Error("validateItem: nil should return false")
		}
	})
}
