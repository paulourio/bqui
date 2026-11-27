package results

import "testing"

func TestJSONSpans(t *testing.T) {
	s := `{"id":"7","n":-1.5e3,"ok":true,"x":null,"s":"a\"b:","arr":[1, false]} …`

	var got []string

	for _, sp := range JSONSpans(s) {
		if sp.Kind != JSONPunct {
			got = append(got, string("PKSNL"[sp.Kind])+s[sp.Start:sp.End])
		}
	}

	want := []string{`K"id"`, `S"7"`, `K"n"`, `N-1.5e3`, `K"ok"`, `Ltrue`, `K"x"`, `Lnull`, `K"s"`, `S"a\"b:"`, `K"arr"`, `N1`, `Lfalse`}
	if len(got) != len(want) {
		t.Fatalf("spans = %q", got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("span %d = %q, want %q", i, got[i], want[i])
		}
	}

	// Spans cover the text exactly, in order.
	end := 0
	for _, sp := range JSONSpans(s) {
		if sp.Start != end {
			t.Fatalf("gap at %d", end)
		}

		end = sp.End
	}

	if end != len(s) {
		t.Errorf("covered %d of %d", end, len(s))
	}

	if sp := JSONSpans(`{"trunc`); len(sp) != 2 || sp[1].Kind != JSONString {
		t.Errorf("truncated = %+v", sp)
	}
}
