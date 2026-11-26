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
		for _, v := range []uint64{1, 42, 1 << 40, 1<<55 - 1} {
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
