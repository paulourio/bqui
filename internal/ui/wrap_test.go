//go:build darwin

package ui

import (
	"strings"
	"testing"
)

func TestNeedsCharWrap(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{name: "empty", in: "", want: false},
		{name: "whitespace_only", in: "   \n\t  ", want: false},
		{name: "prose_description", in: "This table contains holiday and special event data used to train forecasting models.", want: false},
		{name: "formatted_sql", in: "SELECT\n  word,\n  SUM(word_count) AS total\nFROM tbl\nGROUP BY word", want: false},
		{name: "exact_18_ascii_runes", in: strings.Repeat("a", 18), want: false},
		{name: "19_ascii_runes", in: strings.Repeat("a", 19), want: true},
		{name: "exact_18_multibyte_runes", in: strings.Repeat("é", 18), want: false},
		{name: "19_multibyte_runes", in: strings.Repeat("é", 19), want: true},
		{name: "kms_key", in: "projects/my-project/locations/us/keyRings/main-ring/cryptoKeys/bq-cmek-key-01", want: true},
		{name: "gcs_uri", in: "gs://ml-datasets-csv/holidays_and_events_for_forecasting.csv", want: true},
		{name: "full_table_id", in: "bigquery-public-data.google_analytics_sample.ga_sessions_20170801", want: true},
		{name: "description_with_url", in: "Wikipedia pageviews from http://dumps.wikimedia.your.org/other/pageviews/, partitioned by date", want: true},
		{name: "email_address", in: "data-engineering-team@example-organization.com", want: true},
		{name: "uuid_label", in: "trace_id: 1e501519-576e-4815-9328-7621dff96664", want: true},
		{name: "multiline_second_line_long_token", in: "short prefix\n" + strings.Repeat("x", 19), want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := needsCharWrap(tc.in); got != tc.want {
				t.Errorf("needsCharWrap(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
