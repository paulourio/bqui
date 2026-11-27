//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/export"
	"github.com/paulourio/bqui/internal/plan"
	"github.com/paulourio/bqui/internal/results"
	"github.com/paulourio/bqui/internal/store"
	"github.com/paulourio/bqui/internal/theme"
)

func TestTier2F1NestedBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("deep_5level_empty_arrays_at_every_level", func(t *testing.T) {
		t.Parallel()

		ds := buildDeepNestedDataset(t)
		fields := results.FieldsFromREST(ds.restFields)

		// Row with non-null meta struct whose groups array is empty.
		row := []any{
			int64(1), nil, nil, nil, nil,
			[]any{"empty-src", []any{}},
			nil,
		}

		if lines := results.Lines(fields, row); lines != 1 {
			t.Fatalf("Lines = %d, want 1", lines)
		}

		cells := results.Render(fields, row)
		cols := results.Leaves(fields)

		for i, col := range cols {
			if strings.HasPrefix(col.Name, "meta.groups.") && cells[i][0].Kind != results.Blank {
				t.Errorf("col %s on empty array = %+v, want Blank", col.Name, cells[i][0])
			}
		}
	})

	t.Run("null_struct_and_null_scalar_in_repeated_struct", func(t *testing.T) {
		t.Parallel()

		ds := buildDeepNestedDataset(t)
		fields := results.FieldsFromREST(ds.restFields)

		rows, err := bq.DecodeRows(ds.restFields, ds.restRows)
		if err != nil {
			t.Fatal(err)
		}

		cols := results.Leaves(fields)

		// Row 1 has nil meta struct -> every leaf under meta.* must be Null.
		cellsNil := results.Render(fields, rows[1])
		for i, col := range cols {
			if strings.HasPrefix(col.Name, "meta.") && cellsNil[i][0].Kind != results.Null {
				t.Errorf("nil struct col %s kind = %v, want Null", col.Name, cellsNil[i][0].Kind)
			}
		}

		// Row 0 line 2 (beta group) has nil code scalar inside repeated struct -> Null on line 2.
		cellsR0 := results.Render(fields, rows[0])
		for i, col := range cols {
			if col.Name == "meta.groups.leaf.code" && cellsR0[i][2].Kind != results.Null {
				t.Errorf("line 2 leaf.code kind = %v, want Null", cellsR0[i][2].Kind)
			}
		}
	})

	t.Run("sibling_repeated_arrays_unequal_lengths", func(t *testing.T) {
		t.Parallel()

		fields := []results.Field{
			{Name: "a", Type: results.TypeInt64, Repeated: true},
			{Name: "b", Type: results.TypeString, Repeated: true},
		}
		row := []any{
			[]any{int64(1), int64(2), int64(3)},
			[]any{"only"},
		}

		if lines := results.Lines(fields, row); lines != 3 {
			t.Fatalf("Lines = %d, want 3", lines)
		}

		cells := results.Render(fields, row)
		if cells[1][0].V != "only" || cells[1][1].Kind != results.Blank || cells[1][2].Kind != results.Blank {
			t.Fatalf("shorter sibling padding wrong: %+v", cells[1])
		}
	})

	t.Run("max_lines_per_row_cap_10000", func(t *testing.T) {
		t.Parallel()

		fields := []results.Field{{Name: "items", Type: results.TypeInt64, Repeated: true}}
		items := make([]any, maxAllowedLinesPerRow+250)
		for i := range items {
			items[i] = int64(i)
		}

		if got := results.Lines(fields, []any{items}); got != results.MaxLinesPerRow {
			t.Fatalf("Lines = %d, want %d", got, results.MaxLinesPerRow)
		}

		cells := results.Render(fields, []any{items})
		if len(cells[0]) != results.MaxLinesPerRow {
			t.Fatalf("rendered lines = %d, want %d", len(cells[0]), results.MaxLinesPerRow)
		}
	})

	t.Run("special_floats_NaN_PosInf_NegInf_and_bytes", func(t *testing.T) {
		t.Parallel()

		restFields := []*bigquery.TableFieldSchema{
			{Name: "f_nan", Type: "FLOAT"},
			{Name: "f_pinf", Type: "FLOAT"},
			{Name: "f_ninf", Type: "FLOAT"},
			{Name: "raw", Type: "BYTES"},
		}
		restRows := []*bigquery.TableRow{
			{F: []*bigquery.TableCell{
				{V: "NaN"},
				{V: "Infinity"},
				{V: "-Infinity"},
				{V: "/+8B"}, // base64 for {0xff, 0xef, 0x01}
			}},
		}

		rows, err := bq.DecodeRows(restFields, restRows)
		if err != nil {
			t.Fatal(err)
		}

		if !math.IsNaN(rows[0][0].(float64)) || !math.IsInf(rows[0][1].(float64), 1) || !math.IsInf(rows[0][2].(float64), -1) {
			t.Fatalf("special floats = %#v", rows[0])
		}

		js := string(results.AppendRowJSON(nil, results.FieldsFromREST(restFields), rows[0]))
		if !json.Valid([]byte(js)) || !strings.Contains(js, `"NaN"`) || !strings.Contains(js, `"-Infinity"`) {
			t.Fatalf("invalid special float JSON: %s", js)
		}
	})

	t.Run("arrow_schema_mismatch_returns_ErrArrowMismatch", func(t *testing.T) {
		t.Parallel()

		mem := memory.NewGoAllocator()
		schema := arrow.NewSchema([]arrow.Field{{Name: "a", Type: arrow.PrimitiveTypes.Int64}}, nil)
		b := array.NewRecordBuilder(mem, schema)
		defer b.Release()

		b.Field(0).(*array.Int64Builder).Append(1)
		rec := b.NewRecordBatch()
		defer rec.Release()

		// Wrong field count
		_, err := results.RowsFromRecord([]results.Field{{Name: "a", Type: results.TypeInt64}, {Name: "b", Type: results.TypeString}}, rec)
		if !errors.Is(err, results.ErrArrowMismatch) {
			t.Fatalf("err = %v, want ErrArrowMismatch", err)
		}

		// Repeated field backed by non-List Arrow array
		_, err = results.RowsFromRecord([]results.Field{{Name: "a", Type: results.TypeInt64, Repeated: true}}, rec)
		if !errors.Is(err, results.ErrArrowMismatch) {
			t.Fatalf("repeated mismatch err = %v, want ErrArrowMismatch", err)
		}
	})

	t.Run("ColumnPath_blank_continuation_of_shorter_sibling_repeated_record", func(t *testing.T) {
		t.Parallel()

		fields := []results.Field{
			{Name: "long", Type: results.TypeInt64, Repeated: true},
			{
				Name:     "short",
				Type:     results.TypeStruct,
				Repeated: true,
				Fields: []results.Field{
					{Name: "k", Type: results.TypeString},
					{
						Name: "inner",
						Type: results.TypeStruct,
						Fields: []results.Field{
							{Name: "code", Type: results.TypeString},
						},
					},
				},
			},
		}
		row := []any{
			[]any{int64(1), int64(2), int64(3)},
			[]any{[]any{"x", []any{"c0"}}},
		}

		if lines := results.Lines(fields, row); lines != 3 {
			t.Fatalf("Lines = %d, want 3", lines)
		}

		cols := results.Leaves(fields)
		cells := results.Render(fields, row)
		if cells[1][1].Kind != results.Blank || cells[2][1].Kind != results.Blank {
			t.Fatalf("expected Blank continuation cells on shorter sibling REPEATED RECORD: %+v %+v", cells[1][1], cells[2][1])
		}

		if got := results.ColumnPath(fields, row, cols[1], 0); got != "short[0].k" {
			t.Fatalf("ColumnPath(sub=0) = %q, want short[0].k", got)
		}

		if got := results.ColumnPath(fields, row, cols[1], 1); got != "short.k" {
			t.Fatalf("ColumnPath(Blank sub=1) = %q, want short.k", got)
		}

		if got := results.ColumnPath(fields, row, cols[2], 2); got != "short.inner.code" {
			t.Fatalf("ColumnPath(Blank sub=2) = %q, want short.inner.code", got)
		}
	})

	t.Run("ColumnPath_blank_continuation_nested_repeated_child", func(t *testing.T) {
		t.Parallel()

		fields := []results.Field{
			{Name: "long", Type: results.TypeInt64, Repeated: true},
			{
				Name:     "short",
				Type:     results.TypeStruct,
				Repeated: true,
				Fields: []results.Field{
					{Name: "tags", Type: results.TypeString, Repeated: true},
					{Name: "b", Type: results.TypeString},
					{Name: "c", Type: results.TypeString},
				},
			},
		}
		row := []any{
			[]any{int64(1), int64(2), int64(3)},
			[]any{
				[]any{[]any{"t0"}, "b0", "c0"},
				[]any{[]any{"t1"}, "b1", "c1"},
			},
		}

		if lines := results.Lines(fields, row); lines != 3 {
			t.Fatalf("Lines = %d, want 3", lines)
		}

		cols := results.Leaves(fields)
		cells := results.Render(fields, row)
		if cells[1][2].Kind != results.Blank {
			t.Fatalf("expected Blank cell on short.tags line 2, got %+v", cells[1][2])
		}

		if got := results.ColumnPath(fields, row, cols[1], 0); got != "short[0].tags[0]" {
			t.Fatalf("ColumnPath(sub=0) = %q, want short[0].tags[0]", got)
		}

		if got := results.ColumnPath(fields, row, cols[1], 1); got != "short[1].tags[0]" {
			t.Fatalf("ColumnPath(sub=1) = %q, want short[1].tags[0]", got)
		}

		if got := results.ColumnPath(fields, row, cols[1], 2); got != "short.tags" {
			t.Fatalf("ColumnPath(Blank sub=2) = %q, want short.tags", got)
		}
	})

	t.Run("empty_struct_renders_braces_across_REST_Arrow_grid_JSON_and_CSV", func(t *testing.T) {
		t.Parallel()

		restFields := []*bigquery.TableFieldSchema{
			{Name: "id", Type: "INT64"},
			{Name: "empty_s", Type: "RECORD"},
			{Name: "rep_empty_s", Type: "RECORD", Mode: "REPEATED"},
		}
		restRows := []*bigquery.TableRow{
			{F: []*bigquery.TableCell{
				{V: "1"},
				{V: map[string]any{"f": []any{}}},
				{V: []any{
					map[string]any{"v": map[string]any{"f": []any{}}},
					map[string]any{"v": nil},
				}},
			}},
		}

		decodedREST, err := bq.DecodeRows(restFields, restRows)
		if err != nil {
			t.Fatalf("DecodeRows: %v", err)
		}

		fields := results.FieldsFromREST(restFields)

		mem := memory.NewGoAllocator()
		emptyStructType := arrow.StructOf()
		arrowSchema := arrow.NewSchema([]arrow.Field{
			{Name: "id", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
			{Name: "empty_s", Type: emptyStructType, Nullable: true},
			{Name: "rep_empty_s", Type: arrow.ListOf(emptyStructType), Nullable: true},
		}, nil)

		rb := array.NewRecordBuilder(mem, arrowSchema)
		defer rb.Release()

		rb.Field(0).(*array.Int64Builder).Append(1)
		rb.Field(1).(*array.StructBuilder).Append(true)

		lb := rb.Field(2).(*array.ListBuilder)
		lb.Append(true)
		lsb := lb.ValueBuilder().(*array.StructBuilder)
		lsb.Append(true)
		lsb.AppendNull()

		rec := rb.NewRecordBatch()
		defer rec.Release()

		decodedArrow, err := results.RowsFromRecord(fields, rec)
		if err != nil {
			t.Fatalf("RowsFromRecord: %v", err)
		}

		restJSON := string(results.AppendRowJSON(nil, fields, decodedREST[0]))
		arrowJSON := string(results.AppendRowJSON(nil, fields, decodedArrow[0]))
		wantJSON := `{"id":"1","empty_s":{},"rep_empty_s":[{},null]}`
		if restJSON != wantJSON || arrowJSON != wantJSON {
			t.Fatalf("JSON mismatch: rest=%s arrow=%s want=%s", restJSON, arrowJSON, wantJSON)
		}

		cols := results.Leaves(fields)
		for _, row := range [][]any{decodedREST[0], decodedArrow[0]} {
			cells := results.Render(fields, row)
			if len(cells) != 3 || len(cells[0]) != 2 {
				t.Fatalf("unexpected grid shape: cols=%d lines=%d", len(cells), len(cells[0]))
			}

			if got := results.FormatCell(cols[1].Type, cells[1][0].V); got != "{}" {
				t.Fatalf("FormatCell(empty_s) = %q, want {}", got)
			}

			if got := results.FormatCell(cols[2].Type, cells[2][0].V); got != "{}" {
				t.Fatalf("FormatCell(rep_empty_s[0]) = %q, want {}", got)
			}

			if cells[2][1].Kind != results.Null {
				t.Fatalf("rep_empty_s[1] kind = %v, want Null", cells[2][1].Kind)
			}

			var buf bytes.Buffer
			w, werr := export.NewRowWriter(&buf, export.CSV, fields, export.Options{CSVNesting: export.CSVLines})
			if werr != nil {
				t.Fatal(werr)
			}

			if werr = w.WriteRows([][]any{row}); werr != nil {
				t.Fatal(werr)
			}

			_ = w.Close()

			recs, cerr := csv.NewReader(&buf).ReadAll()
			if cerr != nil || len(recs) != 3 {
				t.Fatalf("csv ReadAll = (%d, %v)", len(recs), cerr)
			}

			if recs[1][1] != "{}" || recs[1][2] != "{}" || recs[2][2] != "" {
				t.Fatalf("unexpected CSV rows for empty STRUCT: %+v", recs)
			}
		}
	})
}

func TestTier2F2RunnerAndRefBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("Runner_0_row_nested_query", func(t *testing.T) {
		t.Parallel()

		ds := buildGithubNestedSubsetDataset(t)
		be := &mockQueryBackend{
			result: &bq.QueryResult{
				Statement: "SELECT",
				Schema:    &bigquery.TableSchema{Fields: ds.restFields},
				TotalRows: 0,
				Rows:      nil,
			},
		}

		rn := app.NewRunner(app.RunnerOptions{
			Backend:  be,
			Post:     func(fn func()) { fn() },
			SpillDir: t.TempDir(),
		})

		snap := waitRunDone(t, rn.Start(bq.QueryRequest{SQL: "SELECT * FROM t WHERE FALSE"}))
		if snap.State != app.RunDone || snap.Grid == nil {
			t.Fatalf("0-row run state=%v err=%v", snap.State, snap.Err)
		}

		rows, lines, complete := snap.Grid.Size()
		if rows != 0 || lines != 0 || !complete {
			t.Fatalf("0-row grid size = (%d, %d, %v)", rows, lines, complete)
		}

		if len(snap.Grid.Columns()) < 6 {
			t.Fatalf("expected flattened nested columns on 0-row result, got %d", len(snap.Grid.Columns()))
		}
	})

	t.Run("Runner_MaxRESTRows_truncation", func(t *testing.T) {
		t.Parallel()

		ds := buildGithubNestedSubsetDataset(t)
		be := &mockQueryBackend{
			result: &bq.QueryResult{
				Job:       &bq.JobRef{Project: "p", JobID: "j1", Location: "US"},
				Statement: "SELECT",
				Schema:    &bigquery.TableSchema{Fields: ds.restFields},
				TotalRows: 10,
				Rows:      ds.restRows[:1],
				PageToken: "next",
			},
			pages: map[string]*bigquery.GetQueryResultsResponse{
				"next": {JobComplete: true, Schema: &bigquery.TableSchema{Fields: ds.restFields}, Rows: ds.restRows[1:]},
			},
		}

		rn := app.NewRunner(app.RunnerOptions{
			Backend:     be,
			MaxRESTRows: 1,
			Post:        func(fn func()) { fn() },
			SpillDir:    t.TempDir(),
		})

		snap := waitRunDone(t, rn.Start(bq.QueryRequest{SQL: "SELECT 1"}))
		if !snap.Truncated || snap.State != app.RunDone {
			t.Fatalf("expected Truncated=true, got %+v", snap)
		}
	})

	t.Run("Runner_MaxSpillBytes_truncation", func(t *testing.T) {
		t.Parallel()

		ds := buildDeepNestedDataset(t)
		be := &mockQueryBackend{
			result: &bq.QueryResult{
				Statement:   "SELECT",
				Schema:      &bigquery.TableSchema{Fields: ds.restFields},
				TotalRows:   4,
				Rows:        ds.restRows[:1],
				Destination: &bq.TableRef{Project: "p", Dataset: "d", Table: "t"},
			},
		}

		rn := app.NewRunner(app.RunnerOptions{
			Backend:       be,
			MaxSpillBytes: 1, // smaller than first batch -> stops after batch 1
			Storage: func(context.Context, bq.TableRef) (app.StorageSession, error) {
				return &mockStorageSession{
					schema:  ds.arrowSchema,
					batches: [][]byte{ds.arrowBatch, ds.arrowBatch},
					estRows: 4,
				}, nil
			},
			Post:     func(fn func()) { fn() },
			SpillDir: t.TempDir(),
		})

		snap := waitRunDone(t, rn.Start(bq.QueryRequest{SQL: "SELECT 1"}))
		if !snap.Truncated || snap.Source != "storage" {
			t.Fatalf("expected Truncated storage run, got truncated=%v source=%q", snap.Truncated, snap.Source)
		}
	})

	t.Run("Runner_Context_Cancel_and_Syntax_Error", func(t *testing.T) {
		t.Parallel()

		fb := &app.FakeBackend{}
		rn := app.NewRunner(app.RunnerOptions{Backend: fb, Post: func(fn func()) { fn() }, SpillDir: t.TempDir()})

		// Syntax error
		sErr := waitRunDone(t, rn.Start(bq.QueryRequest{SQL: "SELEC 1"}))
		if sErr.State != app.RunFailed || !errors.Is(sErr.Err, bq.ErrQueryFailed) {
			t.Fatalf("syntax error run = (%v, %v)", sErr.State, sErr.Err)
		}

		// Cancelled run
		block := make(chan struct{})
		defer close(block)

		be := &mockQueryBackend{blockCh: block}
		rnCancel := app.NewRunner(app.RunnerOptions{Backend: be, Post: func(fn func()) { fn() }, SpillDir: t.TempDir()})
		run := rnCancel.Start(bq.QueryRequest{SQL: "SELECT 1"})
		time.Sleep(20 * time.Millisecond)
		run.Cancel()

		sCan := waitRunDone(t, run)
		if sCan.State != app.RunCancelled {
			t.Fatalf("cancelled run state = %v, want RunCancelled", sCan.State)
		}
	})

	t.Run("bq_ParseTableRef_invalid_inputs", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			raw, def string
		}{
			{"", "p"},
			{"single_name", "p"},
			{"d.t", ""},
			{"a.b.c.d", "p"},
			{"p:a.b.c", ""},
			{"p..t", ""},
		} {
			if _, err := bq.ParseTableRef(tc.raw, tc.def); !errors.Is(err, bq.ErrBadRef) {
				t.Errorf("ParseTableRef(%q, %q) err = %v, want ErrBadRef", tc.raw, tc.def, err)
			}
		}
	})
}

func TestTier2F3ResultsAndJSONBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("JSON_gt_4KB_single_nested_row_no_truncation", func(t *testing.T) {
		t.Parallel()

		fields := []results.Field{
			{Name: "id", Type: results.TypeInt64},
			{Name: "events", Type: results.TypeStruct, Repeated: true, Fields: []results.Field{
				{Name: "payload", Type: results.TypeString},
			}},
		}

		longStr := strings.Repeat("x", 300)
		events := make([]any, 20)
		for i := range events {
			events[i] = []any{longStr}
		}

		js := string(results.AppendRowJSON(nil, fields, []any{int64(1), events}))
		if len(js) <= 4096 {
			t.Fatalf("expected >4KB JSON line, got %d bytes", len(js))
		}

		if !json.Valid([]byte(js)) {
			t.Fatal("4KB+ JSON line is invalid")
		}

		spans := results.JSONSpans(js)
		if len(spans) == 0 || spans[len(spans)-1].End != len(js) {
			t.Fatalf("JSONSpans did not cover full >4KB string")
		}
	})

	t.Run("cell_gt_500_runes_multibyte_UTF8_truncation", func(t *testing.T) {
		t.Parallel()

		longUTF8 := strings.Repeat("語🚀", 300) // 600 runes
		cell := results.FormatCell(results.TypeString, longUTF8)

		if !utf8.ValidString(cell) {
			t.Fatal("truncated UTF-8 cell is invalid UTF-8")
		}

		if got := utf8.RuneCountInString(cell); got != results.MaxCellRunes+1 {
			t.Fatalf("truncated rune count = %d, want %d", got, results.MaxCellRunes+1)
		}

		if !strings.HasSuffix(cell, "…") {
			t.Fatalf("missing ellipsis suffix in %q", cell[len(cell)-10:])
		}
	})

	t.Run("cell_control_chars_newline_crlf_tab_escaping", func(t *testing.T) {
		t.Parallel()

		got := results.FormatCell(results.TypeString, "a\r\nb\nc\rd\te")
		want := "a⏎b⏎c⏎d⇥e"
		if got != want {
			t.Fatalf("FormatCell = %q, want %q", got, want)
		}
	})

	t.Run("JSONSpans_truncated_string_and_invalid_json_degrades_gracefully", func(t *testing.T) {
		t.Parallel()

		for _, in := range []string{"", `{"unterminated": "abc\"def`, "not json 123 true false nul"} {
			spans := results.JSONSpans(in)
			pos := 0
			for _, sp := range spans {
				if sp.Start != pos || sp.End <= sp.Start || sp.End > len(in) {
					t.Fatalf("non-contiguous span %+v for %q", sp, in)
				}

				pos = sp.End
			}

			if pos != len(in) {
				t.Fatalf("spans ended at %d, want %d for %q", pos, len(in), in)
			}
		}
	})

	t.Run("SpillStore_closed_and_out_of_range_errors", func(t *testing.T) {
		t.Parallel()

		ds := buildDeepNestedDataset(t)
		fields := results.FieldsFromREST(ds.restFields)

		sp, err := results.NewSpillStore(t.TempDir(), func(raw []byte) ([][]any, error) {
			rec, derr := decodeArrowBatch(raw)
			if derr != nil {
				return nil, derr
			}
			defer rec.Release()

			return results.RowsFromRecord(fields, rec)
		})
		if err != nil {
			t.Fatal(err)
		}

		g := results.NewGrid(fields, sp)
		if err := results.AppendSpilled(g, sp, ds.arrowBatch); err != nil {
			t.Fatal(err)
		}

		g.Finish(nil)

		if _, _, err := g.Cell(-1, 0); !errors.Is(err, results.ErrOutOfRange) {
			t.Errorf("Cell(-1,0) err = %v, want ErrOutOfRange", err)
		}

		if _, _, err := g.Cell(0, 999); !errors.Is(err, results.ErrOutOfRange) {
			t.Errorf("Cell(0,999) err = %v, want ErrOutOfRange", err)
		}

		if _, err := sp.Load(99); !errors.Is(err, results.ErrOutOfRange) {
			t.Errorf("Load(99) err = %v, want ErrOutOfRange", err)
		}

		if err := sp.Close(); err != nil {
			t.Fatal(err)
		}

		if _, err := sp.Load(0); !errors.Is(err, results.ErrClosed) {
			t.Errorf("Load after Close err = %v, want ErrClosed", err)
		}
	})

	t.Run("export_CSV_with_newlines_quotes_and_commas_in_nested_fields", func(t *testing.T) {
		t.Parallel()

		fields := []results.Field{
			{Name: "id", Type: results.TypeInt64},
			{Name: "notes", Type: results.TypeString, Repeated: true},
		}
		rows := [][]any{
			{int64(1), []any{`a,b,"c"`, "line1\nline2\r\nline3", "=SUM(A1:A2)"}},
		}

		var buf bytes.Buffer
		w, err := export.NewRowWriter(&buf, export.CSV, fields, export.Options{CSVNesting: export.CSVLines})
		if err != nil {
			t.Fatal(err)
		}

		if werr := w.WriteRows(rows); werr != nil {
			t.Fatal(werr)
		}

		_ = w.Close()

		recs, err := csv.NewReader(&buf).ReadAll()
		if err != nil || len(recs) != 4 {
			t.Fatalf("csv ReadAll = (%d, %v)", len(recs), err)
		}

		if recs[1][1] != `a,b,"c"` || !strings.Contains(recs[2][1], "line2") {
			t.Fatalf("unexpected CSV round-trip values: %+v", recs)
		}
	})

	t.Run("export_zero_fields_CSVLines_CSVJSONCells_NDJSON_no_panic", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			name string
			fmt  export.Format
			opts export.Options
			want string
		}{
			{name: "csv_lines", fmt: export.CSV, opts: export.Options{CSVNesting: export.CSVLines}, want: "\n"},
			{name: "csv_json_cells", fmt: export.CSV, opts: export.Options{CSVNesting: export.CSVJSONCells}, want: "\n\n"},
			{name: "ndjson", fmt: export.NDJSON, opts: export.Options{}, want: "{}\n"},
		} {
			var buf bytes.Buffer

			w, err := export.NewRowWriter(&buf, tc.fmt, nil, tc.opts)
			if err != nil {
				t.Fatalf("%s NewRowWriter: %v", tc.name, err)
			}

			if err := w.WriteRows([][]any{{}}); err != nil {
				t.Fatalf("%s WriteRows: %v", tc.name, err)
			}

			if err := w.Close(); err != nil {
				t.Fatalf("%s Close: %v", tc.name, err)
			}

			if buf.String() != tc.want {
				t.Fatalf("%s output = %q, want %q", tc.name, buf.String(), tc.want)
			}
		}
	})
}

func TestTier2F4PlanBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("plan_FromJob_nil_and_empty_statistics", func(t *testing.T) {
		t.Parallel()

		if plan.FromJob(nil) != nil || plan.FromJob(&bigquery.Job{}) != nil {
			t.Fatal("expected nil plan for empty job")
		}
	})

	t.Run("plan_Stage_missing_ID", func(t *testing.T) {
		t.Parallel()

		p := plan.FromJob(loadPlanJobFixture(t))
		if _, ok := p.Stage(999999); ok {
			t.Fatal("expected missing stage ID to return ok=false")
		}
	})

	t.Run("plan_Flags_zero_avg_skew_no_divide_by_zero", func(t *testing.T) {
		t.Parallel()

		j := &bigquery.Job{
			Statistics: &bigquery.JobStatistics{
				Query: &bigquery.JobStatistics2{
					QueryPlan: []*bigquery.ExplainQueryStage{
						{Id: 0, Name: "S00", ComputeMsAvg: 0, ComputeMsMax: 500, ReadMsAvg: 0, ReadMsMax: 500, SlotMs: 10},
					},
				},
			},
		}

		p := plan.FromJob(j)
		flags := p.Flags(p.Stages[0])
		if len(flags) != 0 {
			t.Fatalf("expected 0 flags when avg==0 and single stage, got %v", flags)
		}
	})

	t.Run("plan_200_stage_DAG_layout_fast_and_valid", func(t *testing.T) {
		t.Parallel()

		stages := make([]*bigquery.ExplainQueryStage, 200)
		for i := range 200 {
			var inputs []int64
			if i > 0 {
				inputs = []int64{int64(i - 1)}
			}

			stages[i] = &bigquery.ExplainQueryStage{Id: int64(i), Name: "S", InputStages: inputs, SlotMs: int64(i + 1)}
		}

		p := plan.FromJob(&bigquery.Job{Statistics: &bigquery.JobStatistics{Query: &bigquery.JobStatistics2{QueryPlan: stages}}})
		l := p.Layout()
		if len(l.Nodes) != 200 || l.Width <= 0 || l.Height <= 0 {
			t.Fatalf("unexpected 200-stage layout: nodes=%d w=%v h=%v", len(l.Nodes), l.Width, l.Height)
		}
	})

	t.Run("plan_Insights_all_flags_present", func(t *testing.T) {
		t.Parallel()

		j := &bigquery.Job{
			Statistics: &bigquery.JobStatistics{
				Query: &bigquery.JobStatistics2{
					QueryPlan: []*bigquery.ExplainQueryStage{
						{Id: 1, Name: "S01", ComputeMsAvg: 100, ComputeMsMax: 900, ReadMsAvg: 100, ReadMsMax: 500, ShuffleOutputBytesSpilled: 4096, SlotMs: 900},
						{Id: 2, Name: "S02", SlotMs: 100},
					},
					PerformanceInsights: &bigquery.PerformanceInsights{
						StagePerformanceStandaloneInsights: []*bigquery.StagePerformanceStandaloneInsight{
							{
								StageId:                  1,
								SlotContention:           true,
								InsufficientShuffleQuota: true,
								PartitionSkew:            &bigquery.PartitionSkew{},
								HighCardinalityJoins:     []*bigquery.HighCardinalityJoin{{}},
							},
						},
						StagePerformanceChangeInsights: []*bigquery.StagePerformanceChangeInsight{
							{StageId: 1},
						},
					},
				},
			},
		}

		p := plan.FromJob(j)
		joined := strings.Join(p.Flags(p.Stages[0]), "|")
		for _, want := range []string{"slot contention", "insufficient shuffle quota", "partition skew", "high-cardinality join", "slower than previous runs", "compute skew", "read skew", "shuffle spilled"} {
			if !strings.Contains(joined, want) {
				t.Errorf("missing flag %q in %s", want, joined)
			}
		}
	})
}

func TestTier2F5OutlineWrappingBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("Outline_long_unbroken_KMS_URI_TableID_tokens", func(t *testing.T) {
		t.Parallel()

		longKMS := "projects/my-extremely-long-enterprise-gcp-project-id-12345/locations/us-central1/keyRings/enterprise-prod-keyring-01/cryptoKeys/bigquery-cmek-encryption-key-v1"
		longURI := "gs://enterprise-analytics-lakehouse-raw-us-central1-bucket/events/year=2026/month=11/day=27/part-00000-of-01024-snappy-parquet-very-long-unbroken-filename.parquet"
		tbl := &bigquery.Table{
			TableReference:            &bigquery.TableReference{ProjectId: "very-long-project-id-alpha-001", DatasetId: "very_long_dataset_name_analytics_v2", TableId: "fact_user_session_attribution_daily_partitioned_clustered_table"},
			Type:                      "EXTERNAL",
			EncryptionConfiguration:   &bigquery.EncryptionConfiguration{KmsKeyName: longKMS},
			ExternalDataConfiguration: &bigquery.ExternalDataConfiguration{SourceFormat: "PARQUET", SourceUris: []string{longURI}},
		}

		secs := (app.Formatter{Loc: time.UTC}).TableSections(tbl)
		var sawKMS, sawURI bool
		for _, s := range secs {
			for _, r := range s.Rows {
				if r.Key == "Encryption key" && r.Value == longKMS {
					sawKMS = true
				}

				if r.Key == "Source URIs" && r.Value == longURI {
					sawURI = true
				}
			}
		}

		if !sawKMS || !sawURI {
			t.Fatalf("long unbroken tokens altered or missing: kms=%v uri=%v", sawKMS, sawURI)
		}
	})

	t.Run("Outline_single_line_long_description_vs_multiline", func(t *testing.T) {
		t.Parallel()

		singleLine := strings.Repeat("word ", 120)
		multiLine := "Paragraph one.\nParagraph two with details.\nParagraph three."

		s1 := (app.Formatter{Loc: time.UTC}).TableSections(&bigquery.Table{Description: singleLine})
		s2 := (app.Formatter{Loc: time.UTC}).TableSections(&bigquery.Table{Description: multiLine})

		if s1[0].Rows[0].Value != singleLine || s2[0].Rows[0].Value != multiLine {
			t.Fatal("description text mutated in TableSections")
		}
	})

	t.Run("Outline_SchemaTree_missing_schema_and_empty_fields", func(t *testing.T) {
		t.Parallel()

		if app.SchemaTree(&bigquery.Table{}) != nil {
			t.Fatal("expected nil SchemaTree when Schema is nil")
		}

		sf := app.SchemaTree(&bigquery.Table{
			Schema: &bigquery.TableSchema{Fields: []*bigquery.TableFieldSchema{{Name: "c", Type: "STRING"}}},
		})
		if len(sf) != 1 || sf[0].Mode != "NULLABLE" {
			t.Fatalf("unexpected default mode: %+v", sf)
		}
	})

	t.Run("SmokeLog_rejects_panic_and_reentrancy_warning", func(t *testing.T) {
		t.Parallel()

		for _, bad := range []string{
			"panic: runtime error: invalid memory address",
			"fatal error: unexpected signal during runtime execution",
			"NSLayoutConstraint: Unable to simultaneously satisfy constraints.",
			"reentrant layoutSubtreeIfNeeded detected",
			"WARNING: Application performed a table update inside delegate",
		} {
			if err := validateSmokeLog([]byte(bad)); err == nil {
				t.Errorf("expected validateSmokeLog to reject %q", bad)
			}
		}
	})

	t.Run("SmokeValidator_rejects_unexpected_skipped_inspector_segments", func(t *testing.T) {
		t.Parallel()

		var rep smokeReport
		if err := json.Unmarshal(goldenSmokeReportJSON(true), &rep); err != nil {
			t.Fatal(err)
		}

		rep.Checks = append(rep.Checks, smokeCheck{Name: "inspector-segments", OK: true, Skipped: true, Detail: "skipped"})
		b, _ := json.Marshal(rep)

		if _, err := validateSmokeReport(b, smokeValidationOpts{requireLayoutChecks: true}); err == nil {
			t.Fatal("expected unexpected skipped check to be rejected")
		}
	})

	t.Run("SmokeValidator_allows_inspector_segments_but_rejects_unexpected_layout_skip", func(t *testing.T) {
		t.Parallel()

		var rep smokeReport
		if err := json.Unmarshal(goldenSmokeReportJSON(true), &rep); err != nil {
			t.Fatal(err)
		}

		rep.Checks = append(rep.Checks, smokeCheck{Name: "inspector-segments", OK: true, Skipped: true, Detail: "diagnostic"})
		bAllowed, _ := json.Marshal(rep)

		opts := smokeValidationOpts{
			allowSkipped:        map[string]bool{"inspector-segments": true},
			requireLayoutChecks: true,
		}
		if _, err := validateSmokeReport(bAllowed, opts); err != nil {
			t.Fatalf("expected allowed inspector-segments skip to pass, got: %v", err)
		}

		for i := range rep.Checks {
			if rep.Checks[i].Name == "layout-narrow-window" {
				rep.Checks[i].Skipped = true
			}
		}

		bDenied, _ := json.Marshal(rep)
		if _, err := validateSmokeReport(bDenied, opts); err == nil {
			t.Fatal("expected skipped layout-narrow-window check to be rejected")
		}
	})
}

func TestTier2F6GeometryReportBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("Report_rejects_malformed_json_and_zero_windowFrame", func(t *testing.T) {
		t.Parallel()

		if _, err := validateSmokeReport([]byte("{not-json"), smokeValidationOpts{}); err == nil {
			t.Fatal("expected malformed JSON error")
		}

		var rep smokeReport
		_ = json.Unmarshal(goldenSmokeReportJSON(false), &rep)
		rep.WindowFrame.Size.Width = 0
		b, _ := json.Marshal(rep)

		if _, err := validateSmokeReport(b, smokeValidationOpts{}); err == nil {
			t.Fatal("expected zero windowFrame width error")
		}
	})

	t.Run("Report_rejects_failed_nonzero", func(t *testing.T) {
		t.Parallel()

		var rep smokeReport
		_ = json.Unmarshal(goldenSmokeReportJSON(false), &rep)
		rep.Failed = 1
		b, _ := json.Marshal(rep)

		if _, err := validateSmokeReport(b, smokeValidationOpts{}); err == nil {
			t.Fatal("expected failed > 0 to be rejected")
		}
	})

	t.Run("Report_rejects_excessive_dispatchDelayNs", func(t *testing.T) {
		t.Parallel()

		var rep smokeReport
		_ = json.Unmarshal(goldenSmokeReportJSON(false), &rep)
		rep.DispatchDelay = int64(5 * time.Second)
		b, _ := json.Marshal(rep)

		if _, err := validateSmokeReport(b, smokeValidationOpts{}); err == nil {
			t.Fatal("expected excessive dispatchDelayNs to be rejected")
		}
	})

	t.Run("Report_rejects_missing_required_layout_checks", func(t *testing.T) {
		t.Parallel()

		// Baseline report without Feature #10 layout checks must fail requireLayoutChecks: true.
		if _, err := validateSmokeReport(goldenSmokeReportJSON(false), smokeValidationOpts{requireLayoutChecks: true}); err == nil {
			t.Fatal("expected missing layout checks to fail strict validation")
		}
	})

	t.Run("Report_rejects_clipped_or_truncated_check_failure", func(t *testing.T) {
		t.Parallel()

		var rep smokeReport
		_ = json.Unmarshal(goldenSmokeReportJSON(true), &rep)
		rep.Checks = append(rep.Checks, smokeCheck{Name: "layout-text-clipping", OK: false, Detail: "clipped text at 440pt"})
		b, _ := json.Marshal(rep)

		if _, err := validateSmokeReport(b, smokeValidationOpts{requireLayoutChecks: true}); err == nil {
			t.Fatal("expected failed clipping check to be rejected")
		}
	})
}

func TestTier2F7InspectorMetadataBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("TableSections_hidden_view_query_and_legacy_sql", func(t *testing.T) {
		t.Parallel()

		tbl := &bigquery.Table{
			Type: "VIEW",
			View: &bigquery.ViewDefinition{Query: "   ", UseLegacySql: true},
		}

		secs := (app.Formatter{Loc: time.UTC}).TableSections(tbl)
		var sawHidden, sawLegacy bool
		for _, s := range secs {
			for _, r := range s.Rows {
				if r.Key == "View query" && strings.Contains(r.Value, "Not visible") {
					sawHidden = true
				}

				if r.Key == "Legacy SQL" && r.Value == "Yes" {
					sawLegacy = true
				}
			}
		}

		if !sawHidden || !sawLegacy {
			t.Fatalf("hidden=%v legacy=%v", sawHidden, sawLegacy)
		}
	})

	t.Run("TableSections_snapshot_clone_streaming_buffer", func(t *testing.T) {
		t.Parallel()

		tbl := &bigquery.Table{
			Type:               "SNAPSHOT",
			SnapshotDefinition: &bigquery.SnapshotDefinition{BaseTableReference: &bigquery.TableReference{ProjectId: "p", DatasetId: "d", TableId: "src"}, SnapshotTime: "2026-11-27T00:00:00Z"},
			CloneDefinition:    &bigquery.CloneDefinition{BaseTableReference: &bigquery.TableReference{ProjectId: "p", DatasetId: "d", TableId: "orig"}, CloneTime: "2026-11-27T01:00:00Z"},
			StreamingBuffer:    &bigquery.Streamingbuffer{EstimatedRows: 42, OldestEntryTime: 1_700_000_000_000},
		}

		secs := (app.Formatter{Loc: time.UTC}).TableSections(tbl)
		var sawSnap, sawClone, sawBuf bool
		for _, s := range secs {
			for _, r := range s.Rows {
				if r.Key == "Base table" && r.Value == "p.d.src" {
					sawSnap = true
				}

				if r.Key == "Cloned from" && r.Value == "p.d.orig" {
					sawClone = true
				}

				if r.Key == "Streaming buffer rows" && r.Value == "42" {
					sawBuf = true
				}
			}
		}

		if !sawSnap || !sawClone || !sawBuf {
			t.Fatalf("snap=%v clone=%v buf=%v", sawSnap, sawClone, sawBuf)
		}
	})

	t.Run("StandardSQLType_nil_empty_struct_nested_array_range", func(t *testing.T) {
		t.Parallel()

		if got := app.StandardSQLType(nil); got != "" {
			t.Fatalf("nil = %q", got)
		}

		if got := app.StandardSQLType(&bigquery.StandardSqlDataType{TypeKind: "STRUCT"}); got != "STRUCT<>" {
			t.Fatalf("empty struct = %q", got)
		}

		nested := &bigquery.StandardSqlDataType{
			TypeKind: "ARRAY",
			ArrayElementType: &bigquery.StandardSqlDataType{
				TypeKind: "RANGE",
				RangeElementType: &bigquery.StandardSqlDataType{
					TypeKind: "TIMESTAMP",
				},
			},
		}

		if got := app.StandardSQLType(nested); got != "ARRAY<RANGE<TIMESTAMP>>" {
			t.Fatalf("nested = %q", got)
		}
	})

	t.Run("SchemaField_parameterized_types_STRING_NUMERIC_RANGE", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			f    *bigquery.TableFieldSchema
			want string
		}{
			{&bigquery.TableFieldSchema{Type: "STRING", MaxLength: 64}, "STRING(64)"},
			{&bigquery.TableFieldSchema{Type: "BYTES", MaxLength: 16}, "BYTES(16)"},
			{&bigquery.TableFieldSchema{Type: "NUMERIC", Precision: 10, Scale: 2}, "NUMERIC(10, 2)"},
			{&bigquery.TableFieldSchema{Type: "BIGNUMERIC", Precision: 38}, "BIGNUMERIC(38)"},
			{&bigquery.TableFieldSchema{Type: "RANGE", RangeElementType: &bigquery.TableFieldSchemaRangeElementType{Type: "DATETIME"}}, "RANGE<DATETIME>"},
		}

		for _, tc := range cases {
			if got := app.FieldType(tc.f); got != tc.want {
				t.Errorf("FieldType = %q, want %q", got, tc.want)
			}
		}
	})

	t.Run("ValueTree_MaxInspectorRunes_64KiB_truncation", func(t *testing.T) {
		t.Parallel()

		huge := strings.Repeat("a", results.MaxInspectorRunes+500)
		tree := results.Tree([]results.Field{{Name: "big", Type: results.TypeString}}, []any{huge})

		if !strings.Contains(tree[0].Display, "truncated; copy the value for all of it") {
			t.Fatalf("expected inspector truncation notice, got len=%d", len(tree[0].Display))
		}
	})

	t.Run("CountInt_negative_clamps_to_zero", func(t *testing.T) {
		t.Parallel()

		if got := app.CountInt(-42); got != "0" {
			t.Fatalf("CountInt(-42) = %q, want 0", got)
		}

		if got := app.CountInt(164656); got != "164,656" {
			t.Fatalf("CountInt(164656) = %q, want 164,656", got)
		}
	})
}

func TestTier2F8ShellAndAXBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("AXTree_rejects_missing_bottom_tab", func(t *testing.T) {
		t.Parallel()

		for _, missing := range requiredResultTabs {
			b := bytes.ReplaceAll(goldenAXTreeJSON(), []byte(`"`+missing+`"`), []byte(`"OtherTab"`))
			if err := validateAXTree(b); err == nil {
				t.Errorf("expected validateAXTree to fail when %q is missing", missing)
			}
		}
	})

	t.Run("AXTree_rejects_missing_toolbar_or_splitgroup", func(t *testing.T) {
		t.Parallel()

		noToolbar := bytes.ReplaceAll(goldenAXTreeJSON(), []byte(`"AXToolbar"`), []byte(`"AXGroup"`))
		if err := validateAXTree(noToolbar); err == nil {
			t.Fatal("expected missing AXToolbar error")
		}

		noSplit := bytes.ReplaceAll(goldenAXTreeJSON(), []byte(`"AXSplitGroup"`), []byte(`"AXGroup"`))
		if err := validateAXTree(noSplit); err == nil {
			t.Fatal("expected missing AXSplitGroup error")
		}
	})

	t.Run("AXTree_rejects_missing_window_root", func(t *testing.T) {
		t.Parallel()

		badRoot := bytes.Replace(goldenAXTreeJSON(), []byte(`"AXWindow"`), []byte(`"AXApplication"`), 1)
		if err := validateAXTree(badRoot); err == nil {
			t.Fatal("expected non-AXWindow root error")
		}
	})

	t.Run("AXTree_accepts_Runs_with_count_label", func(t *testing.T) {
		t.Parallel()

		withRunsCount := bytes.Replace(goldenAXTreeJSON(), []byte(`"Runs"`), []byte(`"Runs (2)"`), 1)
		if err := validateAXTree(withRunsCount); err != nil {
			t.Fatalf("expected Runs (2) tab label to pass, got: %v", err)
		}
	})

	t.Run("Report_rejects_narrow_window_failure", func(t *testing.T) {
		t.Parallel()

		var rep smokeReport
		_ = json.Unmarshal(goldenSmokeReportJSON(true), &rep)

		for i := range rep.Checks {
			if rep.Checks[i].Name == "layout-narrow-window" {
				rep.Checks[i].OK = false
				rep.Checks[i].Detail = "900x600 overlap detected"
			}
		}

		b, _ := json.Marshal(rep)
		if _, err := validateSmokeReport(b, smokeValidationOpts{requireLayoutChecks: true}); err == nil {
			t.Fatal("expected narrow window failure to be rejected")
		}
	})

	t.Run("Explain_nil_and_url_boilerplate_stripping", func(t *testing.T) {
		t.Parallel()

		if p := app.Explain(nil); p != (app.Problem{}) {
			t.Fatalf("Explain(nil) = %+v", p)
		}

		err := errors.New(`Get "https://bigquery.googleapis.com/bigquery/v2/projects/p/jobs": googleapi: Error 403: Access Denied: Project p, accessDenied`)
		p := app.Explain(err)
		if p.Title != "Permission denied" || strings.Contains(p.Detail, "https://") || strings.Contains(p.Detail, "googleapi:") {
			t.Fatalf("Explain did not clean boilerplate: %+v", p)
		}
	})
}

func TestTier2F9PrefsThemeHistoryBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("Prefs_ParseBytes_invalid_and_overflow", func(t *testing.T) {
		t.Parallel()

		if got, err := store.ParseBytes("9223372036854774784 B"); err != nil || got != 9223372036854774784 {
			t.Fatalf("ParseBytes max representable = (%d, %v)", got, err)
		}

		for _, bad := range []string{
			"-10 GB", "NaN", "Inf", "10000000 TB", "bogus",
			"9223372036854775807 B", "9223372036854775808 B", "8388608 TiB",
		} {
			if _, err := store.ParseBytes(bad); err == nil {
				t.Errorf("ParseBytes(%q) succeeded, want error", bad)
			}
		}
	})

	t.Run("Prefs_CostWarningUSD_rejects_NaN_Inf_and_marshals_valid", func(t *testing.T) {
		t.Parallel()

		for _, bad := range []string{"NaN", "Inf", "+Inf", "-Inf", "$NaN"} {
			if _, err := (store.PreferenceText{CostWarningUSD: bad}).Parse(); err == nil {
				t.Errorf("PreferenceText{CostWarningUSD:%q}.Parse() succeeded, want error", bad)
			}
		}

		valid, err := (store.PreferenceText{
			Location:       "US",
			DefaultDataset: "proj.ds",
			MaxBytesBilled: "10 GB",
			CostWarningUSD: "$1.50",
			MaxSpillBytes:  "2 GiB",
		}).Parse()
		if err != nil {
			t.Fatalf("valid PreferenceText.Parse: %v", err)
		}

		b, err := json.Marshal(valid)
		if err != nil {
			t.Fatalf("json.Marshal(valid): %v", err)
		}

		var back store.Preferences
		if err := json.Unmarshal(b, &back); err != nil || back != valid {
			t.Fatalf("json round-trip = (%+v, %v), want %+v", back, err, valid)
		}
	})

	t.Run("Theme_Invalid_Color_And_Missing_Name_Rejected", func(t *testing.T) {
		t.Parallel()

		if err := (&theme.Theme{}).Validate(); err == nil {
			t.Fatal("expected missing name error")
		}

		bad := &theme.Theme{Name: "Bad", Light: theme.Palette{Grid: theme.GridColors{Null: "#zzzzzz"}}}
		if err := bad.Validate(); err == nil {
			t.Fatal("expected bad color error")
		}

		if _, err := theme.ImportVSCode([]byte("{not json")); err == nil {
			t.Fatal("expected ImportVSCode error on invalid JSON")
		}
	})

	t.Run("History_FilterJobRows_multiword_case_insensitive_empty", func(t *testing.T) {
		t.Parallel()

		rows := []app.JobRow{
			{Ref: bq.JobRef{JobID: "job_abc"}, User: "alice@example.com", SQL: "SELECT user_id FROM analytics.events"},
			{Ref: bq.JobRef{JobID: "job_def"}, User: "bob@example.com", SQL: "SELECT count(*) FROM billing.costs"},
		}

		got := app.FilterJobRows(rows, "EVENTS ALICE")
		if len(got) != 1 || got[0].Ref.JobID != "job_abc" {
			t.Fatalf("FilterJobRows = %+v", got)
		}

		if empty := app.FilterJobRows(rows, "nonexistent"); len(empty) != 0 {
			t.Fatalf("expected 0 matches, got %d", len(empty))
		}
	})

	t.Run("Theme_List_ignores_corrupt_user_file_gracefully", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "broken.toml"), []byte("name = [invalid"), 0o600); err != nil {
			t.Fatal(err)
		}

		themes, errs := theme.List(dir)
		if len(themes) < len(theme.Builtins()) || len(errs) != 1 {
			t.Fatalf("List = (%d themes, %d errs)", len(themes), len(errs))
		}
	})
}

func TestTier2F10SmokeValidatorBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("Report_boundary_440pt_and_300pt_Inspector_checks_missing", func(t *testing.T) {
		t.Parallel()

		var rep smokeReport
		_ = json.Unmarshal(goldenSmokeReportJSON(true), &rep)

		var filtered []smokeCheck
		for _, c := range rep.Checks {
			if c.Name != "layout-inspector-wrap" {
				filtered = append(filtered, c)
			}
		}

		rep.Checks = filtered
		b, _ := json.Marshal(rep)

		if _, err := validateSmokeReport(b, smokeValidationOpts{requireLayoutChecks: true}); err == nil {
			t.Fatal("expected missing Inspector wrap check error")
		}
	})

	t.Run("Report_boundary_zero_checks_rejected", func(t *testing.T) {
		t.Parallel()

		var rep smokeReport
		_ = json.Unmarshal(goldenSmokeReportJSON(false), &rep)
		rep.Checks = nil
		b, _ := json.Marshal(rep)

		if _, err := validateSmokeReport(b, smokeValidationOpts{}); err == nil {
			t.Fatal("expected empty checks error")
		}
	})

	t.Run("Report_boundary_unknown_json_still_decodes_cleanly", func(t *testing.T) {
		t.Parallel()

		raw := goldenSmokeReportJSON(true)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		m["extraFutureField"] = "compatible"
		b, _ := json.Marshal(m)

		if _, err := validateSmokeReport(b, smokeValidationOpts{requireLayoutChecks: true}); err != nil {
			t.Fatalf("forward-compatible extra field failed: %v", err)
		}
	})

	t.Run("Report_boundary_duplicate_check_failure_caught", func(t *testing.T) {
		t.Parallel()

		var rep smokeReport
		_ = json.Unmarshal(goldenSmokeReportJSON(true), &rep)
		rep.Checks = append(rep.Checks, smokeCheck{Name: "results-grid", OK: false, Detail: "second run failed"})
		b, _ := json.Marshal(rep)

		if _, err := validateSmokeReport(b, smokeValidationOpts{requireLayoutChecks: true}); err == nil {
			t.Fatal("expected failing duplicate check to be caught")
		}
	})

	t.Run("SmokeLog_case_insensitive_panic_and_layout_recursion", func(t *testing.T) {
		t.Parallel()

		if err := validateSmokeLog([]byte("PANIC: something bad")); err == nil {
			t.Fatal("expected case-insensitive PANIC detection")
		}
	})

	t.Run("AXTree_deep_nested_children_handled", func(t *testing.T) {
		t.Parallel()

		var root axNode
		_ = json.Unmarshal(goldenAXTreeJSON(), &root)

		cur := &root.Children[0]
		for range 20 {
			cur.Children = append(cur.Children, axNode{Role: "AXGroup"})
			cur = &cur.Children[len(cur.Children)-1]
		}

		b, _ := json.Marshal(root)
		if err := validateAXTree(b); err != nil {
			t.Fatalf("deep AXTree failed: %v", err)
		}
	})

	t.Run("Report_rejects_baseline_only_checks_including_explorer_tables_and_inspector_schema", func(t *testing.T) {
		t.Parallel()

		var rep smokeReport
		_ = json.Unmarshal(goldenSmokeReportJSON(false), &rep)
		rep.Checks = append(rep.Checks,
			smokeCheck{Name: "explorer-tables", OK: true, Detail: "7 tables in samples"},
			smokeCheck{Name: "inspector-segments", OK: true, Skipped: true, Detail: "frame 182x16; widths 56/46"},
		)
		for i := range rep.Checks {
			if rep.Checks[i].Name == "inspector-schema" {
				rep.Checks[i].Detail = "222 fields (8 top-level rows), segment 0"
			}

			if rep.Checks[i].Name == "toolbar" {
				rep.Checks[i].Detail = "11 toolbar items"
			}
		}

		b, _ := json.Marshal(rep)
		opts := smokeValidationOpts{
			allowSkipped:        map[string]bool{"inspector-segments": true},
			requireLayoutChecks: true,
		}
		if _, err := validateSmokeReport(b, opts); err == nil {
			t.Fatal("expected baseline-only report lacking Feature #10 checks to be rejected")
		}
	})

	t.Run("Report_rejects_challenger_case1_placeholder_checks", func(t *testing.T) {
		t.Parallel()

		var rep smokeReport
		_ = json.Unmarshal(goldenSmokeReportJSON(false), &rep)
		for i := range rep.Checks {
			if rep.Checks[i].Name == "toolbar" {
				rep.Checks[i].Detail = "toolbar layout ok"
			}
		}

		rep.Checks = append(rep.Checks,
			smokeCheck{Name: "explorer-tables", OK: true, Detail: "7 tables in samples"},
			smokeCheck{Name: "narrow-placeholder", OK: true, Detail: "ok"},
			smokeCheck{Name: "editor-wrap", OK: true, Detail: "ok"},
			smokeCheck{Name: "nested-placeholder", OK: true, Detail: "ok"},
		)
		b, _ := json.Marshal(rep)

		if _, err := validateSmokeReport(b, smokeValidationOpts{requireLayoutChecks: true}); err == nil {
			t.Fatal("expected Challenger case1 placeholder checks to be rejected")
		}
	})

	t.Run("Report_accepts_challenger_case2_realistic_M4_checks", func(t *testing.T) {
		t.Parallel()

		var rep smokeReport
		_ = json.Unmarshal(goldenSmokeReportJSON(false), &rep)
		rep.Checks = append(rep.Checks,
			smokeCheck{Name: "window-resize-min", OK: true, Detail: "resized to 880x580 containment ok"},
			smokeCheck{Name: "inspector-details-height", OK: true, Detail: "height grew from 22 to 44 at 440pt and 300pt"},
			smokeCheck{Name: "results-cell-text", OK: true, Detail: "live NSTextField non-empty on row 0"},
			smokeCheck{Name: "bottom-bar-fit", OK: true, Detail: "ok"},
			smokeCheck{Name: "toolbar-items-fit", OK: true, Detail: "all 11 items visible, 0 clipped"},
		)
		b, _ := json.Marshal(rep)

		if _, err := validateSmokeReport(b, smokeValidationOpts{requireLayoutChecks: true}); err != nil {
			t.Fatalf("expected realistic M4 check names (Challenger case2) to be accepted, got: %v", err)
		}
	})

	t.Run("Report_rejects_table_substring_bypass_for_bottom_tabs_r2_a", func(t *testing.T) {
		t.Parallel()

		var rep smokeReport
		_ = json.Unmarshal(goldenSmokeReportJSON(false), &rep)
		rep.Checks = append(rep.Checks,
			smokeCheck{Name: "window-resize-min", OK: true, Detail: "resized to 880x580 containment ok"},
			smokeCheck{Name: "inspector-details-height", OK: true, Detail: "height grew from 22 to 44 at 440pt and 300pt"},
			smokeCheck{Name: "results-cell-text", OK: true, Detail: "live NSTextField non-empty on row 0"},
			smokeCheck{Name: "results-table-fit", OK: true, Detail: "table columns fit cleanly"},
			smokeCheck{Name: "toolbar-items-fit", OK: true, Detail: "all 11 items visible, 0 clipped"},
		)
		b, _ := json.Marshal(rep)

		if _, err := validateSmokeReport(b, smokeValidationOpts{requireLayoutChecks: true}); err == nil {
			t.Fatal("expected results-table-fit without bottom-tab check to be rejected")
		}
	})

	t.Run("Report_rejects_cancelled_substring_bypass_for_rendered_cell_r2_b", func(t *testing.T) {
		t.Parallel()

		var rep smokeReport
		_ = json.Unmarshal(goldenSmokeReportJSON(false), &rep)
		rep.Checks = append(rep.Checks,
			smokeCheck{Name: "window-resize-min", OK: true, Detail: "resized to 880x580 containment ok"},
			smokeCheck{Name: "inspector-details-height", OK: true, Detail: "height grew from 22 to 44 at 440pt and 300pt"},
			smokeCheck{Name: "results-cancelled-row", OK: true, Detail: "cancelled query row 0 empty"},
			smokeCheck{Name: "bottom-bar-fit", OK: true, Detail: "ok"},
			smokeCheck{Name: "toolbar-items-fit", OK: true, Detail: "all 11 items visible, 0 clipped"},
		)
		b, _ := json.Marshal(rep)

		if _, err := validateSmokeReport(b, smokeValidationOpts{requireLayoutChecks: true}); err == nil {
			t.Fatal("expected results-cancelled-row without TableRenderedCell check to be rejected")
		}
	})

	t.Run("Report_accepts_toolbar_hidden_zero_r2_e", func(t *testing.T) {
		t.Parallel()

		var rep smokeReport
		_ = json.Unmarshal(goldenSmokeReportJSON(false), &rep)
		rep.Checks = append(rep.Checks,
			smokeCheck{Name: "window-resize-min", OK: true, Detail: "resized to 880x580 containment ok"},
			smokeCheck{Name: "inspector-details-height", OK: true, Detail: "height grew from 22 to 44 at 440pt and 300pt"},
			smokeCheck{Name: "results-cell-text", OK: true, Detail: "live NSTextField non-empty on row 0"},
			smokeCheck{Name: "bottom-bar-fit", OK: true, Detail: "ok"},
			smokeCheck{Name: "toolbar-layout", OK: true, Detail: "total=11 hidden=0"},
		)
		b, _ := json.Marshal(rep)

		if _, err := validateSmokeReport(b, smokeValidationOpts{requireLayoutChecks: true}); err != nil {
			t.Fatalf("expected toolbar-layout with hidden=0 to be accepted, got: %v", err)
		}
	})

	t.Run("Report_rejects_single_all_in_one_layout_check", func(t *testing.T) {
		t.Parallel()

		var rep smokeReport
		_ = json.Unmarshal(goldenSmokeReportJSON(false), &rep)
		rep.Checks = append(rep.Checks,
			smokeCheck{
				Name:   "layout-all-in-one",
				OK:     true,
				Detail: "900x600 narrow window, inspector 440pt 300pt wrap height grew, TableRenderedCell nested results, 7 bottom-tab fit, toolbar overflow 0",
			},
			smokeCheck{Name: "extra-1", OK: true, Detail: "ok"},
			smokeCheck{Name: "extra-2", OK: true, Detail: "ok"},
			smokeCheck{Name: "extra-3", OK: true, Detail: "ok"},
			smokeCheck{Name: "extra-4", OK: true, Detail: "ok"},
		)
		b, _ := json.Marshal(rep)

		if _, err := validateSmokeReport(b, smokeValidationOpts{requireLayoutChecks: true}); err == nil {
			t.Fatal("expected single all-in-one layout check to be rejected")
		}
	})
}

func TestTier2F11PNGBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("PNGValidator_rejects_truncated_PNG", func(t *testing.T) {
		t.Parallel()

		p := filepath.Join(t.TempDir(), "trunc.png")
		writeTestPNG(t, p, 200, 200, false)

		b, _ := os.ReadFile(p)
		if err := os.WriteFile(p, b[:len(b)/2], 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := validatePNGFile(p); err == nil {
			t.Fatal("expected truncated PNG error")
		}
	})

	t.Run("PNGValidator_rejects_non_PNG_file", func(t *testing.T) {
		t.Parallel()

		p := filepath.Join(t.TempDir(), "not.png")
		if err := os.WriteFile(p, []byte("not a png image"), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := validatePNGFile(p); err == nil {
			t.Fatal("expected non-PNG error")
		}
	})

	t.Run("PNGValidator_rejects_missing_file", func(t *testing.T) {
		t.Parallel()

		if _, err := validatePNGFile(filepath.Join(t.TempDir(), "missing.png")); err == nil {
			t.Fatal("expected missing file error")
		}
	})

	t.Run("PNGValidator_rejects_uniform_solid_color_blank_PNG", func(t *testing.T) {
		t.Parallel()

		p := filepath.Join(t.TempDir(), "blank.png")
		writeTestPNG(t, p, 200, 200, true)

		if _, err := validatePNGFile(p); err == nil {
			t.Fatal("expected solid-color blank PNG error")
		}
	})

	t.Run("Report_rejects_missing_screenshot_file_on_disk", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "report.json"), goldenSmokeReportJSON(true), 0o600)
		_ = os.WriteFile(filepath.Join(dir, "ax.json"), goldenAXTreeJSON(), 0o600)
		// Intentionally omit creating the PNG files listed in report.screenshots.

		if _, err := validateSmokeArtifacts(dir, smokeValidationOpts{requireLayoutChecks: true}); err == nil {
			t.Fatal("expected missing screenshot artifact error")
		}
	})

	t.Run("PNGValidator_rejects_2x1_degenerate_PNG", func(t *testing.T) {
		t.Parallel()

		p := filepath.Join(t.TempDir(), "tiny.png")
		writeTestPNG(t, p, 2, 1, false)

		if _, err := validatePNGFile(p); err == nil {
			t.Fatal("expected 2x1 degenerate PNG to be rejected")
		}
	})

	t.Run("PNGValidator_rejects_single_differing_pixel_PNG", func(t *testing.T) {
		t.Parallel()

		p := filepath.Join(t.TempDir(), "onepixel.png")
		// Step size on 128x80 with 64x64 grid is (2, 1), so (2, 1) is guaranteed to be sampled.
		writeSingleDiffPixelPNG(t, p, 128, 80, 2, 1)

		if _, err := validatePNGFile(p); err == nil {
			t.Fatal("expected 128x80 single-differing-pixel PNG to be rejected")
		}
	})

	t.Run("Report_rejects_baseline_only_10_PNGs_without_per_pane_captures", func(t *testing.T) {
		t.Parallel()

		dir := goldenSmokeDir(t, false)

		var rep smokeReport
		_ = json.Unmarshal(goldenSmokeReportJSON(true), &rep)
		rep.Screenshots = []string{"segmented.png", "window-light.png", "window-dark.png"}
		b, _ := json.Marshal(rep)
		if err := os.WriteFile(filepath.Join(dir, "report.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := validateSmokeArtifacts(dir, smokeValidationOpts{requireLayoutChecks: true, requirePerPanePNGs: true}); err == nil {
			t.Fatal("expected 10-PNG baseline-only directory to fail Feature #11 per-pane validation")
		}
	})

	t.Run("Report_accepts_minimal_7_PNGs_with_all_4_Feature11_categories_r2_c", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "report.json"), goldenSmokeReportJSON(true), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, "ax.json"), goldenAXTreeJSON(), 0o600); err != nil {
			t.Fatal(err)
		}

		writeTestPNGVariant(t, filepath.Join(dir, "segmented.png"), 182, 16, "segmented.png")
		writeTestPNGVariant(t, filepath.Join(dir, "window-light.png"), 160, 100, "window-light.png")
		writeTestPNGVariant(t, filepath.Join(dir, "window-dark.png"), 160, 100, "window-dark.png")
		writeTestPNGVariant(t, filepath.Join(dir, "pane-results-nested.png"), 160, 100, "pane-results-nested.png")
		writeTestPNGVariant(t, filepath.Join(dir, "pane-json-nested.png"), 160, 100, "pane-json-nested.png")
		writeTestPNGVariant(t, filepath.Join(dir, "pane-inspector-details.png"), 160, 100, "pane-inspector-details.png")
		writeTestPNGVariant(t, filepath.Join(dir, "window-narrow.png"), 100, 68, "window-narrow.png")

		if _, err := validateSmokeArtifacts(dir, smokeValidationOpts{requireLayoutChecks: true, requirePerPanePNGs: true}); err != nil {
			t.Fatalf("expected 3 baseline + 4 Feature #11 PNGs (7 total) to pass, got: %v", err)
		}
	})

	t.Run("Report_rejects_duplicate_Feature11_PNGs_r2_d", func(t *testing.T) {
		t.Parallel()

		dir := goldenSmokeDir(t, true)
		lightBytes, err := os.ReadFile(filepath.Join(dir, "window-light.png"))
		if err != nil {
			t.Fatal(err)
		}

		for _, dup := range []string{"pane-json-nested.png", "pane-inspector-details.png"} {
			if err := os.WriteFile(filepath.Join(dir, dup), lightBytes, 0o600); err != nil {
				t.Fatal(err)
			}
		}

		if _, err := validateSmokeArtifacts(dir, smokeValidationOpts{requireLayoutChecks: true, requirePerPanePNGs: true}); err == nil {
			t.Fatal("expected duplicate Feature #11 PNGs to be rejected")
		}
	})

	t.Run("Report_rejects_full_width_narrow_window_PNG_r2_d", func(t *testing.T) {
		t.Parallel()

		dir := goldenSmokeDir(t, true)
		// Replace window-narrow.png with distinct pixel content but full main-window width (160 >= 160).
		writeTestPNGVariant(t, filepath.Join(dir, "window-narrow.png"), 160, 100, "window-narrow-full-width")

		if _, err := validateSmokeArtifacts(dir, smokeValidationOpts{requireLayoutChecks: true, requirePerPanePNGs: true}); err == nil {
			t.Fatal("expected full-width window-narrow.png to be rejected")
		}
	})

	t.Run("Report_rejects_single_multi_keyword_PNG_covering_4_categories_r2_f", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		var rep smokeReport
		_ = json.Unmarshal(goldenSmokeReportJSON(true), &rep)
		rep.Screenshots = []string{
			"segmented.png",
			"window-light.png",
			"window-dark.png",
			"pane-results-json-inspector-narrow.png",
			"extra-1.png",
			"extra-2.png",
			"extra-3.png",
		}
		b, _ := json.Marshal(rep)
		if err := os.WriteFile(filepath.Join(dir, "report.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, "ax.json"), goldenAXTreeJSON(), 0o600); err != nil {
			t.Fatal(err)
		}

		writeTestPNGVariant(t, filepath.Join(dir, "segmented.png"), 182, 16, "segmented.png")
		writeTestPNGVariant(t, filepath.Join(dir, "window-light.png"), 160, 100, "window-light.png")
		writeTestPNGVariant(t, filepath.Join(dir, "window-dark.png"), 160, 100, "window-dark.png")
		writeTestPNGVariant(t, filepath.Join(dir, "pane-results-json-inspector-narrow.png"), 100, 68, "multi")
		writeTestPNGVariant(t, filepath.Join(dir, "extra-1.png"), 160, 100, "extra-1")
		writeTestPNGVariant(t, filepath.Join(dir, "extra-2.png"), 160, 100, "extra-2")
		writeTestPNGVariant(t, filepath.Join(dir, "extra-3.png"), 160, 100, "extra-3")

		if _, err := validateSmokeArtifacts(dir, smokeValidationOpts{requireLayoutChecks: true, requirePerPanePNGs: true}); err == nil {
			t.Fatal("expected single multi-keyword PNG + 3 unrelated PNGs to be rejected")
		}
	})

	t.Run("Report_rejects_narrow_window_PNG_when_main_window_PNGs_absent", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()

		var rep smokeReport
		_ = json.Unmarshal(goldenSmokeReportJSON(true), &rep)
		rep.Screenshots = []string{
			"segmented.png",
			"pane-results-nested.png",
			"pane-json-nested.png",
			"pane-inspector-details.png",
			"window-narrow.png",
		}
		b, _ := json.Marshal(rep)
		if err := os.WriteFile(filepath.Join(dir, "report.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, "ax.json"), goldenAXTreeJSON(), 0o600); err != nil {
			t.Fatal(err)
		}

		writeTestPNGVariant(t, filepath.Join(dir, "segmented.png"), 182, 16, "segmented.png")
		writeTestPNGVariant(t, filepath.Join(dir, "pane-results-nested.png"), 160, 100, "pane-results-nested.png")
		writeTestPNGVariant(t, filepath.Join(dir, "pane-json-nested.png"), 160, 100, "pane-json-nested.png")
		writeTestPNGVariant(t, filepath.Join(dir, "pane-inspector-details.png"), 160, 100, "pane-inspector-details.png")
		writeTestPNGVariant(t, filepath.Join(dir, "window-narrow.png"), 1920, 1080, "window-narrow-oversized")

		if _, err := validateSmokeArtifacts(dir, smokeValidationOpts{requireLayoutChecks: true, requirePerPanePNGs: true}); err == nil {
			t.Fatal("expected oversized window-narrow.png without window-light/dark to be rejected")
		}
	})

	t.Run("Report_rejects_Feature11_PNGs_on_disk_omitted_from_report_screenshots", func(t *testing.T) {
		t.Parallel()

		dir := goldenSmokeDir(t, true)

		var rep smokeReport
		_ = json.Unmarshal(goldenSmokeReportJSON(true), &rep)
		rep.Screenshots = []string{"segmented.png", "window-light.png", "window-dark.png"}
		b, _ := json.Marshal(rep)
		if err := os.WriteFile(filepath.Join(dir, "report.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := validateSmokeArtifacts(dir, smokeValidationOpts{requireLayoutChecks: true, requirePerPanePNGs: true}); err == nil {
			t.Fatal("expected Feature #11 PNGs omitted from report.screenshots to be rejected")
		}
	})
}

func TestTier2F12CLIBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("bquictl_unknown_command_exits_2", func(t *testing.T) {
		t.Parallel()

		bin := buildBinary(t, "./cmd/bquictl", "bquictl", "", "")
		_, stderr, rc := runSubprocess(t, "", bin, nil, "no-such-cmd")
		if rc != 2 || !strings.Contains(stderr, "unknown command") {
			t.Fatalf("rc=%d stderr=%s", rc, stderr)
		}
	})

	t.Run("bquictl_bad_global_flag_exits_2", func(t *testing.T) {
		t.Parallel()

		bin := buildBinary(t, "./cmd/bquictl", "bquictl", "", "")
		_, stderr, rc := runSubprocess(t, "", bin, nil, "-not-a-flag")
		if rc != 2 || stderr == "" {
			t.Fatalf("rc=%d stderr=%s", rc, stderr)
		}
	})

	t.Run("bquictl_subcommand_bad_flag_exits_1", func(t *testing.T) {
		t.Parallel()

		bin := buildBinary(t, "./cmd/bquictl", "bquictl", "", "")
		_, _, rcQuery := runSubprocess(t, "", bin, []string{"GOOGLE_CLOUD_PROJECT=dummy"}, "query", "-bogus")
		_, _, rcRead := runSubprocess(t, "", bin, []string{"GOOGLE_CLOUD_PROJECT=dummy"}, "read", "-bogus")
		if rcQuery != 1 || rcRead != 1 {
			t.Fatalf("rcQuery=%d rcRead=%d, want both 1", rcQuery, rcRead)
		}
	})

	t.Run("bquictl_show_and_read_missing_arg_exit_1", func(t *testing.T) {
		t.Parallel()

		bin := buildBinary(t, "./cmd/bquictl", "bquictl", "", "")
		_, _, rcShow := runSubprocess(t, "", bin, []string{"GOOGLE_CLOUD_PROJECT=dummy"}, "show")
		_, _, rcRead := runSubprocess(t, "", bin, []string{"GOOGLE_CLOUD_PROJECT=dummy"}, "read")
		if rcShow != 1 || rcRead != 1 {
			t.Fatalf("rcShow=%d rcRead=%d, want both 1", rcShow, rcRead)
		}
	})

	t.Run("tools_bundle_missing_bin_flag_exits_2", func(t *testing.T) {
		t.Parallel()

		bin := buildBinary(t, "./tools/bundle", "bundle", "", "")
		_, stderr, rc := runSubprocess(t, "", bin, nil)
		if rc != 2 || !strings.Contains(stderr, "-bin is required") {
			t.Fatalf("rc=%d stderr=%s", rc, stderr)
		}
	})
}

func TestTier2HardeningRegressions(t *testing.T) {
	t.Parallel()

	t.Run("app_Formatter_and_SchemaTree_nil_inputs", func(t *testing.T) {
		t.Parallel()

		var f app.Formatter
		if f.TableSections(nil) != nil || f.DatasetSections(nil) != nil ||
			f.RoutineSections(nil) != nil || f.ModelSections(nil) != nil ||
			f.JobSections(nil) != nil || app.SchemaTree(nil) != nil {
			t.Fatal("expected nil sections/schema for nil inputs")
		}
	})

	t.Run("results_and_export_RECORD_type_parity", func(t *testing.T) {
		t.Parallel()

		recFields := []results.Field{
			{Name: "id", Type: results.TypeInt64},
			{Name: "meta", Type: "RECORD", Fields: []results.Field{{Name: "code", Type: results.TypeString}}},
		}
		row := []any{int64(1), []any{"ok"}}

		if cols := results.Leaves(recFields); len(cols) != 2 || cols[1].Name != "meta.code" {
			t.Fatalf("Leaves(RECORD) = %+v", cols)
		}

		if got := string(results.AppendRowJSON(nil, recFields, row)); got != `{"id":"1","meta":{"code":"ok"}}` {
			t.Fatalf("AppendRowJSON(RECORD) = %s", got)
		}

		if tree := results.Tree(recFields, row); len(tree) != 2 || len(tree[1].Children) != 1 || tree[1].Children[0].Display != "ok" {
			t.Fatalf("Tree(RECORD) = %+v", tree)
		}

		var buf bytes.Buffer
		w, err := export.NewRowWriter(&buf, export.CSV, recFields, export.Options{CSVNesting: export.CSVJSONCells})
		if err != nil {
			t.Fatal(err)
		}

		if werr := w.WriteRows([][]any{row}); werr != nil {
			t.Fatal(werr)
		}

		if cerr := w.Close(); cerr != nil {
			t.Fatal(cerr)
		}

		recs, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
		if err != nil || len(recs) != 2 || recs[1][1] != `{"code":"ok"}` {
			t.Fatalf("CSVJSONCells(RECORD) = %q, err=%v", recs, err)
		}
	})

	t.Run("bq_DecodeRows_bool_and_ref_whitespace_and_prefs_domain_project", func(t *testing.T) {
		t.Parallel()

		fields := []*bigquery.TableFieldSchema{{Name: "ok", Type: "BOOL"}}
		rows, err := bq.DecodeRows(fields, []*bigquery.TableRow{{F: []*bigquery.TableCell{{V: "TRUE"}}}})
		if err != nil || rows[0][0] != true {
			t.Fatalf("DecodeRows(TRUE) = (%v, %v)", rows, err)
		}

		if _, err := bq.DecodeRows(fields, []*bigquery.TableRow{{F: []*bigquery.TableCell{{V: "bad"}}}}); !errors.Is(err, bq.ErrBadRow) {
			t.Fatalf("DecodeRows(bad) err = %v, want ErrBadRow", err)
		}

		if _, err := bq.ParseTableRef("p . d . t", ""); !errors.Is(err, bq.ErrBadRef) {
			t.Errorf("ParseTableRef internal whitespace err = %v, want ErrBadRef", err)
		}

		if _, err := bq.ParseDatasetRef("p . d", "def"); !errors.Is(err, bq.ErrBadRef) {
			t.Errorf("ParseDatasetRef internal whitespace err = %v, want ErrBadRef", err)
		}

		if ds, err := bq.ParseDatasetRef("example.com:proj.d", ""); err != nil || ds.Project != "example.com:proj" || ds.Dataset != "d" {
			t.Errorf("ParseDatasetRef domain-scoped = (%+v, %v)", ds, err)
		}

		if _, err := (store.PreferenceText{DefaultDataset: "p . d"}).Parse(); err == nil {
			t.Error("PreferenceText{DefaultDataset: \"p . d\"} succeeded, want error")
		}

		if p, err := (store.PreferenceText{DefaultDataset: "example.com:proj.d"}).Parse(); err != nil || p.DefaultDataset != "example.com:proj.d" {
			t.Errorf("PreferenceText domain-scoped = (%+v, %v)", p, err)
		}
	})

	t.Run("store_QueryFileName_length_bound_and_ValidName_whitespace", func(t *testing.T) {
		t.Parallel()

		for _, title := range []string{strings.Repeat("a", 300), strings.Repeat("é", 200), strings.Repeat("🚀", 100)} {
			fn := store.QueryFileName(title)
			if len(fn) > 255 || !utf8.ValidString(fn) || !strings.HasSuffix(fn, ".sql") {
				t.Errorf("QueryFileName invalid: len=%d valid=%v name=%q", len(fn), utf8.ValidString(fn), fn)
			}
		}

		if err := store.ValidName("  spaced  "); !errors.Is(err, store.ErrBadName) {
			t.Errorf("ValidName(\"  spaced  \") = %v, want ErrBadName", err)
		}
	})

	t.Run("domain_scoped_table_ref_and_ObjectRefAt", func(t *testing.T) {
		t.Parallel()

		want := bq.TableRef{Project: "example.com:proj", Dataset: "d", Table: "t"}
		for _, raw := range []string{"example.com:proj.d.t", "`example.com:proj.d.t`", "example.com:proj:d.t", want.String(), want.SQL()} {
			got, err := bq.ParseTableRef(raw, "")
			if err != nil || got != want {
				t.Errorf("ParseTableRef(%q) = (%+v, %v), want %+v", raw, got, err, want)
			}
		}

		if _, err := bq.ParseTableRef("example.com:proj.d", ""); !errors.Is(err, bq.ErrBadRef) {
			t.Errorf("ParseTableRef(\"example.com:proj.d\") err = %v, want ErrBadRef", err)
		}

		o1, ok := app.ObjectRefAt("SELECT a FROM `example.com:proj.d.t`", 0, 20, "defp", "")
		if !ok || o1.Ref != want || o1.Start != 14 || o1.End != 36 {
			t.Errorf("ObjectRefAt(quoted domain) = (%+v, %v)", o1, ok)
		}

		o2, ok := app.ObjectRefAt("SELECT a FROM t", 0, 14, "defp", "example.com:proj.d")
		if !ok || o2.Ref != want {
			t.Errorf("ObjectRefAt(bare with domain defDataset) = (%+v, %v)", o2, ok)
		}

		if bad, ok := app.ObjectRefAt("SELECT a FROM `example.com:proj.d`", 0, 20, "defp", ""); ok {
			t.Errorf("ObjectRefAt(2-part domain) unexpectedly resolved: %+v", bad)
		}
	})

	t.Run("bq_DecodeRows_timestamp_NaN_Inf_and_REPEATED_non_slice", func(t *testing.T) {
		t.Parallel()

		tsFields := []*bigquery.TableFieldSchema{{Name: "ts", Type: "TIMESTAMP"}}
		for _, raw := range []string{"NaN", "Inf", "-Inf", "Infinity", "-Infinity", "1e20", "-1e20"} {
			if _, err := bq.DecodeRows(tsFields, []*bigquery.TableRow{{F: []*bigquery.TableCell{{V: raw}}}}); !errors.Is(err, bq.ErrBadRow) {
				t.Errorf("DecodeRows(TIMESTAMP %q) err = %v, want ErrBadRow", raw, err)
			}
		}

		repFields := []*bigquery.TableFieldSchema{{Name: "tags", Type: "STRING", Mode: "REPEATED"}}
		for _, bad := range []any{"not-a-slice", map[string]any{"v": "x"}, float64(1)} {
			if _, err := bq.DecodeRows(repFields, []*bigquery.TableRow{{F: []*bigquery.TableCell{{V: bad}}}}); !errors.Is(err, bq.ErrBadRow) {
				t.Errorf("DecodeRows(REPEATED %T) err = %v, want ErrBadRow", bad, err)
			}
		}
	})

	t.Run("nil_guards_plan_stage_jobs_and_schema_elements", func(t *testing.T) {
		t.Parallel()

		var p *plan.Plan
		if _, ok := p.Stage(0); ok || p.Flags(plan.Stage{ID: 1}) != nil || p.TotalSlotMs() != 0 || p.MaxSlotMs() != 0 {
			t.Fatal("nil *plan.Plan methods returned unexpected non-zero values")
		}

		if secs := app.StageSections(nil, plan.Stage{ID: 0, Name: "S00", Inputs: []int64{1}}); len(secs) == 0 {
			t.Fatal("StageSections(nil, ...) returned empty sections")
		}

		if got := app.JobRowFromList(nil); got != (app.JobRow{}) {
			t.Fatalf("JobRowFromList(nil) = %+v, want zero", got)
		}

		if got := app.JobRowFromJob(nil); got != (app.JobRow{}) {
			t.Fatalf("JobRowFromJob(nil) = %+v, want zero", got)
		}

		if got := app.FieldType(nil); got != "" {
			t.Fatalf("FieldType(nil) = %q, want empty", got)
		}

		rawFields := []*bigquery.TableFieldSchema{
			nil,
			{Name: "rec", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{nil, {Name: "k", Type: "STRING"}}},
		}
		tree := app.SchemaTree(&bigquery.Table{Schema: &bigquery.TableSchema{Fields: rawFields}})
		if len(tree) != 1 || len(tree[0].Children) != 1 || tree[0].Children[0].Name != "k" {
			t.Fatalf("SchemaTree with nil elements = %+v", tree)
		}

		res := results.FieldsFromREST(rawFields)
		if len(res) != 1 || len(res[0].Fields) != 1 || res[0].Fields[0].Name != "k" {
			t.Fatalf("FieldsFromREST with nil elements = %+v", res)
		}
	})
}
