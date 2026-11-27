package store

import "testing"

func TestParseBytes(t *testing.T) {
	cases := map[string]int64{
		"": 0, "1024": 1024, "10 GB": 10e9, "1.5TiB": 3 << 39, "500mb": 500e6, "2 KiB": 2048, "7b": 7,
	}

	for in, want := range cases {
		if got, err := ParseBytes(in); err != nil || got != want {
			t.Errorf("ParseBytes(%q) = %d, %v; want %d", in, got, err, want)
		}
	}

	for _, bad := range []string{"ten", "-1 GB", "1e400", "GB"} {
		if _, err := ParseBytes(bad); err == nil {
			t.Errorf("ParseBytes(%q) succeeded", bad)
		}
	}

	if FormatBytes(10e9) != "10 GB" || FormatBytes(1.5e12) != "1.5 TB" || FormatBytes(512) != "512 B" {
		t.Errorf("format = %s %s %s", FormatBytes(10e9), FormatBytes(1.5e12), FormatBytes(512))
	}
}

func TestPreferencesRoundTrip(t *testing.T) {
	text := PreferenceText{Location: " EU ", DefaultDataset: "p.d", MaxBytesBilled: "10 GB", CostWarningUSD: "$1.50", MaxSpillBytes: "20GB", StudioRegion: "US"}

	p, err := text.Parse()
	if err != nil {
		t.Fatal(err)
	}

	if p.Location != "EU" || p.MaxBytesBilled != 10e9 || p.CostWarningUSD != 1.5 || p.MaxSpillBytes != 20e9 || p.Region() != "us-central1" {
		t.Fatalf("prefs = %+v", p)
	}

	back, err := p.Text().Parse()
	if err != nil || back != p {
		t.Errorf("round trip = %+v %v", back, err)
	}

	for _, bad := range []PreferenceText{
		{DefaultDataset: "nodot"},
		{DefaultDataset: "a.b.c"},
		{MaxBytesBilled: "lots"},
		{CostWarningUSD: "-3"},
		{MaxSpillBytes: "x"},
	} {
		if _, err := bad.Parse(); err == nil {
			t.Errorf("%+v parsed", bad)
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
