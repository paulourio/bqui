package store

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Preferences are the user-tunable query and result settings.
type Preferences struct {
	// Location runs queries in a location ("" = automatic).
	Location string `json:"location,omitempty"`
	// DefaultDataset ("project.dataset") resolves unqualified table names.
	DefaultDataset string `json:"defaultDataset,omitempty"`
	// MaxBytesBilled fails queries that would bill more (0 = no limit).
	MaxBytesBilled int64 `json:"maxBytesBilled,omitempty"`
	// CostWarningUSD makes bqls warn above this estimated cost (0 = off).
	CostWarningUSD float64 `json:"costWarningUSD,omitempty"`
	// MaxSpillBytes caps a large result's spill file (0 = default).
	MaxSpillBytes int64 `json:"maxSpillBytes,omitempty"`
	// StudioRegion is where BigQuery Studio saved queries live ("" =
	// us-central1, which holds the US multi-region's queries).
	StudioRegion string `json:"studioRegion,omitempty"`
}

// DefaultStudioRegion holds saved queries for BigQuery's US multi-region.
const DefaultStudioRegion = "us-central1"

// Region returns the BigQuery Studio region to use.
func (p Preferences) Region() string {
	if p.StudioRegion == "" {
		return DefaultStudioRegion
	}

	return p.StudioRegion
}

// PreferenceText is Preferences as the user types them.
type PreferenceText struct {
	Location, DefaultDataset, MaxBytesBilled, CostWarningUSD, MaxSpillBytes, StudioRegion string
}

// Text formats preferences for editing.
func (p Preferences) Text() PreferenceText {
	t := PreferenceText{Location: p.Location, DefaultDataset: p.DefaultDataset, StudioRegion: p.StudioRegion}

	if p.MaxBytesBilled > 0 {
		t.MaxBytesBilled = FormatBytes(p.MaxBytesBilled)
	}

	if p.CostWarningUSD > 0 {
		t.CostWarningUSD = strconv.FormatFloat(p.CostWarningUSD, 'f', -1, 64)
	}

	if p.MaxSpillBytes > 0 {
		t.MaxSpillBytes = FormatBytes(p.MaxSpillBytes)
	}

	return t
}

// Parse validates typed preferences. Errors name the field.
func (t PreferenceText) Parse() (Preferences, error) {
	var (
		p   Preferences
		err error
	)

	p.Location = strings.TrimSpace(t.Location)
	p.DefaultDataset = strings.TrimSpace(t.DefaultDataset)
	p.StudioRegion = strings.ToLower(strings.TrimSpace(t.StudioRegion))

	switch p.StudioRegion {
	case "us":
		p.StudioRegion = DefaultStudioRegion // the console's "US" lives here
	case "eu":
		return p, errors.New("studio region: enter the region shown in the Dataform API (for example europe-west4)")
	}

	if p.DefaultDataset != "" {
		if !validDefaultDataset(p.DefaultDataset) {
			return p, errors.New("default dataset: use project.dataset")
		}
	}

	if p.MaxBytesBilled, err = ParseBytes(t.MaxBytesBilled); err != nil {
		return p, fmt.Errorf("maximum bytes billed: %w", err)
	}

	if p.MaxSpillBytes, err = ParseBytes(t.MaxSpillBytes); err != nil {
		return p, fmt.Errorf("result spill limit: %w", err)
	}

	if s := strings.TrimPrefix(strings.TrimSpace(t.CostWarningUSD), "$"); s != "" {
		if p.CostWarningUSD, err = strconv.ParseFloat(s, 64); err != nil || math.IsNaN(p.CostWarningUSD) || math.IsInf(p.CostWarningUSD, 0) || p.CostWarningUSD < 0 {
			return p, errors.New("cost warning: enter dollars, like 1.50")
		}

		if p.CostWarningUSD == 0 {
			p.CostWarningUSD = 0
		}
	}

	return p, nil
}

// validDefaultDataset checks a "project.dataset" or "project:dataset" reference
// (including domain-scoped projects such as "example.com:proj.dataset" and
// "example.com:proj:dataset") without internal whitespace.
func validDefaultDataset(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t\r\n`") {
		return false
	}

	var proj, ds string

	switch {
	case strings.Contains(s, ":"):
		i := strings.LastIndex(s, ":")
		prefix, rest := s[:i], s[i+1:]

		if strings.Contains(prefix, ".") && strings.Count(rest, ".") == 1 {
			j := strings.LastIndex(s, ".")
			proj, ds = s[:j], s[j+1:]
		} else {
			proj, ds = prefix, rest
		}
	case strings.Count(s, ".") == 1:
		proj, ds, _ = strings.Cut(s, ".")
	default:
		return false
	}

	return proj != "" && ds != "" && !strings.Contains(ds, ".")
}

var byteUnits = []struct {
	suffix string
	scale  float64
}{
	{"tib", 1 << 40},
	{"tb", 1e12},
	{"gib", 1 << 30},
	{"gb", 1e9},
	{"mib", 1 << 20},
	{"mb", 1e6},
	{"kib", 1 << 10},
	{"kb", 1e3},
	{"b", 1},
}

// ParseBytes parses sizes like "10 GB", "1.5TiB", "500mb" or "1024" ("" =
// 0). Decimal units are powers of 1000, binary units powers of 1024.
func ParseBytes(s string) (int64, error) {
	s = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	if s == "" {
		return 0, nil
	}

	scale := 1.0

	for _, u := range byteUnits {
		if num, ok := strings.CutSuffix(s, u.suffix); ok {
			s, scale = num, u.scale

			break
		}
	}

	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, errors.New("enter a size like 10 GB")
	}

	const maxInt64Float = 0x1p63 // float64(math.MaxInt64) rounds up to 2^63

	n := v * scale
	if math.IsInf(n, 0) || math.IsNaN(n) || n >= maxInt64Float {
		return 0, errors.New("size is too large")
	}

	return int64(n), nil
}

// FormatBytes formats a size with the largest exact-ish decimal unit
// ("10 GB", "1.5 TB", "512 B").
func FormatBytes(n int64) string {
	for _, u := range []struct {
		name  string
		scale float64
	}{{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3}} {
		if float64(n) >= u.scale {
			return strconv.FormatFloat(float64(n)/u.scale, 'f', -1, 64) + " " + u.name
		}
	}

	return strconv.FormatInt(n, 10) + " B"
}
