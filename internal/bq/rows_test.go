package bq

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	bigquery "google.golang.org/api/bigquery/v2"
)

// nestedSchema covers scalars, a STRUCT, and a REPEATED RECORD containing a
// REPEATED scalar (array of struct of array).
func nestedSchema() []*bigquery.TableFieldSchema {
	return []*bigquery.TableFieldSchema{
		{Name: "id", Type: "INTEGER"},
		{Name: "score", Type: "FLOAT"},
		{Name: "ok", Type: "BOOLEAN"},
		{Name: "blob", Type: "BYTES"},
		{Name: "ts", Type: "TIMESTAMP"},
		{Name: "amount", Type: "NUMERIC"},
		{Name: "doc", Type: "JSON"},
		{Name: "customer", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{
			{Name: "name", Type: "STRING"},
			{Name: "tier", Type: "STRING"},
		}},
		{Name: "items", Type: "RECORD", Mode: "REPEATED", Fields: []*bigquery.TableFieldSchema{
			{Name: "sku", Type: "STRING"},
			{Name: "tags", Type: "STRING", Mode: "REPEATED"},
		}},
	}
}

// rowJSON is how the REST API encodes one row of nestedSchema.
const rowJSON = `{"f":[
  {"v":"1001"},
  {"v":"NaN"},
  {"v":"true"},
  {"v":"AQID"},
  {"v":"1704164645123456"},
  {"v":"12.500000000"},
  {"v":"{\"a\": [1, 2]}"},
  {"v":{"f":[{"v":"Ana"},{"v":null}]}},
  {"v":[
    {"v":{"f":[{"v":"A-1"},{"v":[{"v":"x"},{"v":"y"}]}]}},
    {"v":{"f":[{"v":"B-7"},{"v":[]}]}}
  ]}
]}`

func decodeFixture(t *testing.T) []any {
	t.Helper()

	var row bigquery.TableRow
	if err := json.Unmarshal([]byte(rowJSON), &row); err != nil {
		t.Fatal(err)
	}

	rows, err := DecodeRows(nestedSchema(), []*bigquery.TableRow{&row})
	if err != nil {
		t.Fatal(err)
	}

	return rows[0]
}

func TestDecodeRowsNestedAndTyped(t *testing.T) {
	r := decodeFixture(t)

	if r[0] != int64(1001) {
		t.Errorf("id = %#v", r[0])
	}

	if f, ok := r[1].(float64); !ok || !math.IsNaN(f) {
		t.Errorf("score = %#v", r[1])
	}

	if r[2] != true {
		t.Errorf("ok = %#v", r[2])
	}

	if b, ok := r[3].([]byte); !ok || len(b) != 3 || b[2] != 3 {
		t.Errorf("blob = %#v", r[3])
	}

	want := time.Date(2024, 1, 2, 3, 4, 5, 123456000, time.UTC)
	if ts, ok := r[4].(time.Time); !ok || !ts.Equal(want) {
		t.Errorf("ts = %v", r[4])
	}

	if r[5] != "12.500000000" {
		t.Errorf("amount kept exact? %#v", r[5])
	}

	cust := r[7].([]any)
	if cust[0] != "Ana" || cust[1] != nil {
		t.Errorf("customer = %#v", cust)
	}

	items := r[8].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}

	first := items[0].([]any)
	if first[0] != "A-1" || len(first[1].([]any)) != 2 {
		t.Errorf("items[0] = %#v", first)
	}

	if tags := items[1].([]any)[1].([]any); len(tags) != 0 {
		t.Errorf("empty repeated should decode to empty slice, got %#v", tags)
	}
}

func TestAppendRowJSONConsoleEncoding(t *testing.T) {
	got := string(AppendRowJSON(nil, nestedSchema(), decodeFixture(t)))
	want := `{"id":"1001","score":"NaN","ok":true,"blob":"AQID","ts":"2024-01-02 03:04:05.123456 UTC",` +
		`"amount":"12.500000000","doc":{"a":[1,2]},"customer":{"name":"Ana","tier":null},` +
		`"items":[{"sku":"A-1","tags":["x","y"]},{"sku":"B-7","tags":[]}]}`

	if got != want {
		t.Errorf("JSON mismatch\n got: %s\nwant: %s", got, want)
	}

	if !json.Valid([]byte(got)) {
		t.Error("output is not valid JSON")
	}
}

func TestDecodeRowsErrors(t *testing.T) {
	fields := []*bigquery.TableFieldSchema{{Name: "n", Type: "INT64"}}

	bad := []*bigquery.TableRow{{F: []*bigquery.TableCell{{V: "not-a-number"}}}}
	if _, err := DecodeRows(fields, bad); err == nil {
		t.Error("expected error for bad INT64")
	}

	short := []*bigquery.TableRow{{F: nil}}
	if _, err := DecodeRows(fields, short); err == nil {
		t.Error("expected error for cell count mismatch")
	}
}

func TestTimestampRangeNormalized(t *testing.T) {
	f := &bigquery.TableFieldSchema{Name: "r", Type: "RANGE", RangeElementType: &bigquery.TableFieldSchemaRangeElementType{Type: "TIMESTAMP"}}
	rows, err := DecodeRows([]*bigquery.TableFieldSchema{f}, []*bigquery.TableRow{{F: []*bigquery.TableCell{{V: "[1704067200000000, 1704153600500000)"}}}})

	if err != nil || rows[0][0] != "[2024-01-01 00:00:00 UTC, 2024-01-02 00:00:00.500000 UTC)" {
		t.Fatalf("range = %#v, %v", rows[0][0], err)
	}

	if got := normalizeTimestampRange("[1704067200000000, UNBOUNDED)"); got != "[2024-01-01 00:00:00 UTC, UNBOUNDED)" {
		t.Errorf("unbounded = %q", got)
	}
}

func TestParseTimestampForms(t *testing.T) {
	want := time.Date(2024, 1, 2, 3, 4, 5, 500000000, time.UTC)

	for _, s := range []string{"1704164645500000", "1.7041646455E9", "1704164645.5"} {
		got, err := parseTimestamp(s)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseTimestamp(%q) = %v, %v", s, got, err)
		}
	}

	if FormatTimestamp(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)) != "2024-01-02 03:04:05 UTC" {
		t.Error("whole-second timestamp should omit fraction")
	}
}
