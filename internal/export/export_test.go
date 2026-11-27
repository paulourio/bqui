package export

import (
	"bytes"
	"context"
	"encoding/csv"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"

	"github.com/paulourio/bqui/internal/results"
)

func fields() []results.Field {
	return []results.Field{
		{Name: "id", Type: results.TypeInt64},
		{Name: "note", Type: results.TypeString},
		{Name: "items", Type: results.TypeStruct, Repeated: true, Fields: []results.Field{
			{Name: "sku", Type: results.TypeString},
			{Name: "qty", Type: results.TypeInt64},
		}},
	}
}

func rows() [][]any {
	return [][]any{
		{int64(1), "has, comma \"quoted\"", []any{[]any{"A-1", int64(2)}, []any{"B-7", int64(1)}}},
		{int64(2), nil, []any{}},
	}
}

func write(t *testing.T, f Format, opts Options) string {
	t.Helper()

	var buf bytes.Buffer

	w, err := NewRowWriter(&buf, f, fields(), opts)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.WriteRows(rows()); err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.String()
}

func TestCSVLinesMatchesGrid(t *testing.T) {
	out := write(t, CSV, Options{})

	recs, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}

	want := [][]string{
		{"id", "note", "items.sku", "items.qty"},
		{"1", `has, comma "quoted"`, "A-1", "2"},
		{"", "", "B-7", "1"},
		{"2", "", "", ""},
	}

	if len(recs) != len(want) {
		t.Fatalf("records = %q", recs)
	}

	for i := range want {
		if strings.Join(recs[i], "|") != strings.Join(want[i], "|") {
			t.Errorf("line %d = %q, want %q", i, recs[i], want[i])
		}
	}
}

func TestCSVJSONCells(t *testing.T) {
	recs, err := csv.NewReader(strings.NewReader(write(t, CSV, Options{CSVNesting: CSVJSONCells}))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}

	if len(recs) != 3 || recs[0][2] != "items" || recs[1][2] != `[{"sku":"A-1","qty":"2"},{"sku":"B-7","qty":"1"}]` || recs[2][2] != "[]" {
		t.Fatalf("records = %q", recs)
	}
}

func TestNDJSON(t *testing.T) {
	out := write(t, NDJSON, Options{})
	lines := strings.Split(strings.TrimSpace(out), "\n")

	if len(lines) != 2 || !strings.HasPrefix(lines[1], `{"id":"2","note":null,"items":[]}`) {
		t.Fatalf("ndjson = %q", out)
	}

	if _, err := NewRowWriter(&bytes.Buffer{}, Parquet, fields(), Options{}); err == nil {
		t.Error("Parquet must refuse rows")
	}
}

func record(t *testing.T, mem memory.Allocator) arrow.RecordBatch {
	t.Helper()

	item := arrow.StructOf(arrow.Field{Name: "sku", Type: arrow.BinaryTypes.String, Nullable: true})
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "id", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
		{Name: "items", Type: arrow.ListOf(item)},
	}, nil)

	b := array.NewRecordBuilder(mem, schema)
	defer b.Release()

	b.Field(0).(*array.Int64Builder).AppendValues([]int64{1, 2}, nil)
	lb := b.Field(1).(*array.ListBuilder)
	sb := lb.ValueBuilder().(*array.StructBuilder)

	lb.Append(true)
	sb.Append(true)
	sb.FieldBuilder(0).(*array.StringBuilder).Append("A-1")
	lb.Append(true)

	return b.NewRecordBatch()
}

func TestParquetRoundTrip(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.NewGoAllocator())
	defer mem.AssertSize(t, 0)

	rec := record(t, mem)
	defer rec.Release()

	for _, f := range []Format{Parquet} {
		var buf bytes.Buffer

		w, err := NewRecordWriter(&buf, f, rec.Schema())
		if err != nil {
			t.Fatal(err)
		}

		if err := w.Write(rec); err != nil {
			t.Fatal(err)
		}

		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		var n int64
		var sku string

		switch f {
		case Parquet:
			pf, err := file.NewParquetReader(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatal(err)
			}

			fr, err := pqarrow.NewFileReader(pf, pqarrow.ArrowReadProperties{}, memory.NewGoAllocator())
			if err != nil {
				t.Fatal(err)
			}

			tbl, err := fr.ReadTable(context.Background())
			if err != nil {
				t.Fatal(err)
			}

			n = tbl.NumRows()
			sku = tbl.Column(1).Data().Chunk(0).(*array.List).ListValues().(*array.Struct).Field(0).(*array.String).Value(0)
			tbl.Release()
		case CSV, NDJSON:
		}

		if n != 2 || sku != "A-1" {
			t.Errorf("%s round trip: %d rows, sku %q", f, n, sku)
		}
	}
}

func TestRowWriterZeroFieldsDoesNotPanic(t *testing.T) {
	for _, tc := range []struct {
		name string
		fmt  Format
		opts Options
		want string
	}{
		{name: "csv_lines", fmt: CSV, opts: Options{CSVNesting: CSVLines}, want: "\n"},
		{name: "csv_json_cells", fmt: CSV, opts: Options{CSVNesting: CSVJSONCells}, want: "\n\n"},
		{name: "ndjson", fmt: NDJSON, opts: Options{}, want: "{}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer

			w, err := NewRowWriter(&buf, tc.fmt, nil, tc.opts)
			if err != nil {
				t.Fatal(err)
			}

			if err := w.WriteRows([][]any{{}}); err != nil {
				t.Fatal(err)
			}

			if err := w.Close(); err != nil {
				t.Fatal(err)
			}

			if buf.String() != tc.want {
				t.Errorf("got %q, want %q", buf.String(), tc.want)
			}
		})
	}
}

func TestRecordTypeParityCSVJSONCellsAndNDJSON(t *testing.T) {
	recFields := []results.Field{
		{Name: "id", Type: results.TypeInt64},
		{Name: "meta", Type: "RECORD", Fields: []results.Field{
			{Name: "a", Type: results.TypeString},
		}},
	}
	rows := [][]any{{int64(1), []any{"hello"}}}

	var csvBuf bytes.Buffer
	cw, err := NewRowWriter(&csvBuf, CSV, recFields, Options{CSVNesting: CSVJSONCells})
	if err != nil {
		t.Fatal(err)
	}

	if werr := cw.WriteRows(rows); werr != nil {
		t.Fatal(werr)
	}

	if cerr := cw.Close(); cerr != nil {
		t.Fatal(cerr)
	}

	recs, err := csv.NewReader(strings.NewReader(csvBuf.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[1][1] != `{"a":"hello"}` {
		t.Fatalf("CSVJSONCells(RECORD) = %q, want {\"a\":\"hello\"}", recs)
	}

	var jsonBuf bytes.Buffer
	jw, err := NewRowWriter(&jsonBuf, NDJSON, recFields, Options{})
	if err != nil {
		t.Fatal(err)
	}

	if werr := jw.WriteRows(rows); werr != nil {
		t.Fatal(werr)
	}

	if cerr := jw.Close(); cerr != nil {
		t.Fatal(cerr)
	}

	if got := strings.TrimSpace(jsonBuf.String()); got != `{"id":"1","meta":{"a":"hello"}}` {
		t.Fatalf("NDJSON(RECORD) = %q", got)
	}
}
