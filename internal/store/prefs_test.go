package store

import (
	"math"
	"testing"
)

func TestParseBytes(t *testing.T) {
	cases := map[string]int64{
		"": 0, "1024": 1024, "10 GB": 10e9, "1.5TiB": 3 << 39, "500mb": 500e6, "2 KiB": 2048, "7b": 7,
		"9223372036854774784 B": 9223372036854774784,
	}

	for in, want := range cases {
		if got, err := ParseBytes(in); err != nil || got != want {
			t.Errorf("ParseBytes(%q) = %d, %v; want %d", in, got, err, want)
		}
	}

	for _, bad := range []string{
		"ten", "-1 GB", "1e400", "GB",
		"9223372036854775807 B", "9223372036854775808 B", "8388608 TiB", "1e308 TiB",
	} {
		if _, err := ParseBytes(bad); err == nil {
			t.Errorf("ParseBytes(%q) succeeded", bad)
		}
	}

	if FormatBytes(10e9) != "10 GB" || FormatBytes(1.5e12) != "1.5 TB" || FormatBytes(512) != "512 B" {
		t.Errorf("format = %s %s %s", FormatBytes(10e9), FormatBytes(1.5e12), FormatBytes(512))
	}
}

func TestPreferencesRoundTrip(t *testing.T) {
	text := PreferenceText{Location: " EU ", DefaultDataset: "p.d", MaxBytesBilled: "10 GB", CostWarningUSD: "$1.50", MaxSpillBytes: "20GB"}

	p, err := text.Parse()
	if err != nil {
		t.Fatal(err)
	}

	if p.Location != "EU" || p.MaxBytesBilled != 10e9 || p.CostWarningUSD != 1.5 || p.MaxSpillBytes != 20e9 {
		t.Fatalf("prefs = %+v", p)
	}

	back, err := p.Text().Parse()
	if err != nil || back != p {
		t.Errorf("round trip = %+v %v", back, err)
	}

	for _, bad := range []PreferenceText{
		{DefaultDataset: "nodot"},
		{DefaultDataset: "a.b.c"},
		{DefaultDataset: "p . d"},
		{DefaultDataset: "p\td"},
		{DefaultDataset: "p:d.t"},
		{DefaultDataset: ".d"},
		{DefaultDataset: "p."},
		{MaxBytesBilled: "lots"},
		{CostWarningUSD: "-3"},
		{CostWarningUSD: "NaN"},
		{CostWarningUSD: "Inf"},
		{CostWarningUSD: "+Inf"},
		{CostWarningUSD: "-Inf"},
		{CostWarningUSD: "$NaN"},
		{MaxSpillBytes: "8388608 TiB"},
		{MaxBytesBilled: "9223372036854775808 B"},
		{MaxSpillBytes: "x"},
	} {
		if _, perr := bad.Parse(); perr == nil {
			t.Errorf("%+v parsed", bad)
		}
	}

	pZero, err := (PreferenceText{CostWarningUSD: "-0"}).Parse()
	if err != nil || pZero.CostWarningUSD != 0 || math.Signbit(pZero.CostWarningUSD) {
		t.Errorf("-0 CostWarningUSD = (%+v, %v)", pZero, err)
	}

	for _, ds := range []string{"example.com:proj.d", "example.com:proj:d", "p:d"} {
		parsed, derr := (PreferenceText{DefaultDataset: ds}).Parse()
		if derr != nil || parsed.DefaultDataset != ds {
			t.Errorf("PreferenceText{DefaultDataset:%q}.Parse() = (%+v, %v)", ds, parsed, derr)
		}
	}
}

func TestToggleStarPersists(t *testing.T) {
	s := &Settings{}

	if !s.ToggleStar("p", "b") || !s.ToggleStar("p", "a") || len(s.Starred) != 2 || s.Starred[0] != "p.a" {
		t.Fatalf("starred = %v", s.Starred)
	}

	if s.ToggleStar("p", "b") || len(s.Starred) != 1 {
		t.Fatalf("unstar = %v", s.Starred)
	}

	path := t.TempDir() + "/settings.json"
	s.StarredOnly = true

	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}

	back, err := LoadSettings(path)
	if err != nil || len(back.Starred) != 1 || !back.StarredOnly {
		t.Errorf("reloaded = %+v %v", back, err)
	}
}
