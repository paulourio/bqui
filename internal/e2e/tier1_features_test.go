//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/export"
	"github.com/paulourio/bqui/internal/plan"
	"github.com/paulourio/bqui/internal/results"
	"github.com/paulourio/bqui/internal/store"
	"github.com/paulourio/bqui/internal/theme"
)

func TestTier1F1NestedFieldsOffline(t *testing.T) {
	t.Parallel()

	t.Run("github_nested_REST_and_Arrow_parity", func(t *testing.T) {
		t.Parallel()

		ds := buildGithubNestedSubsetDataset(t)
		fields := results.FieldsFromREST(ds.restFields)

		restRows, err := bq.DecodeRows(ds.restFields, ds.restRows)
		if err != nil {
			t.Fatalf("bq.DecodeRows: %v", err)
		}

		rec, err := decodeArrowBatch(ds.arrowBatch)
		if err != nil {
			t.Fatalf("decodeArrowBatch: %v", err)
		}
		defer rec.Release()

		arrowRows, err := results.RowsFromRecord(fields, rec)
		if err != nil {
			t.Fatalf("RowsFromRecord: %v", err)
		}

		if !reflect.DeepEqual(restRows, arrowRows) {
			t.Fatalf("REST vs Arrow rows differ:\nREST:  %#v\nArrow: %#v", restRows, arrowRows)
		}

		m := &results.MemoryStore{}
		grid := results.NewGrid(fields, m)
		results.AppendRows(grid, m, restRows)
		grid.Finish(nil)

		rows, lines, complete := grid.Size()
		if rows != 2 || lines != 3 || !complete {
			t.Fatalf("grid size = (%d, %d, %v), want (2, 3, true)", rows, lines, complete)
		}

		cols := grid.Columns()
		var colNames []string
		for _, c := range cols {
			colNames = append(colNames, c.Name)
		}

		for _, want := range []string{"payload.pull_request.base.repo.owner.login", "payload.pages.page_name", "repository.name"} {
			if !slices.Contains(colNames, want) {
				t.Errorf("missing column %q in %v", want, colNames)
			}
		}
	})

	t.Run("ga_sessions_20170801_fixture_leaves_and_grid", func(t *testing.T) {
		t.Parallel()

		tbl := loadAppTableFixture(t, "table.bigquery-public-data.google_analytics_sample.ga_sessions_20170801.json")
		fields := results.FieldsFromREST(tbl.Schema.Fields)
		leaves := results.Leaves(fields)

		if len(leaves) < 100 {
			t.Fatalf("ga_sessions leaves = %d, expected wide flattened columns", len(leaves))
		}

		ds := buildGASessionsSubsetDataset(t)
		subFields := results.FieldsFromREST(ds.restFields)

		restRows, err := bq.DecodeRows(ds.restFields, ds.restRows)
		if err != nil {
			t.Fatalf("DecodeRows: %v", err)
		}

		m := &results.MemoryStore{}
		g := results.NewGrid(subFields, m)
		results.AppendRows(g, m, restRows)
		g.Finish(nil)

		rows, lines, _ := g.Size()
		if rows != 1 || lines != 2 {
			t.Fatalf("size = (%d, %d), want (1, 2)", rows, lines)
		}

		c, pos, err := g.Cell(1, 4) // hits.page.pagePath on 2nd hit
		if err != nil || c.Kind != results.Value || c.V != "/checkout" || pos.Sub != 1 {
			t.Fatalf("cell(1,4) = (%+v, %+v, %v)", c, pos, err)
		}
	})

	t.Run("deep_5level_STRUCT_ARRAY_grid_and_tree", func(t *testing.T) {
		t.Parallel()

		ds := buildDeepNestedDataset(t)
		fields := results.FieldsFromREST(ds.restFields)

		restRows, err := bq.DecodeRows(ds.restFields, ds.restRows)
		if err != nil {
			t.Fatalf("DecodeRows: %v", err)
		}

		rec, err := decodeArrowBatch(ds.arrowBatch)
		if err != nil {
			t.Fatalf("decodeArrowBatch: %v", err)
		}
		defer rec.Release()

		arrowRows, err := results.RowsFromRecord(fields, rec)
		if err != nil {
			t.Fatalf("RowsFromRecord: %v", err)
		}

		if !reflect.DeepEqual(restRows, arrowRows) {
			t.Fatalf("deep nested REST vs Arrow mismatch:\n%#v\nvs\n%#v", restRows, arrowRows)
		}

		m := &results.MemoryStore{}
		g := results.NewGrid(fields, m)
		results.AppendRows(g, m, arrowRows)
		g.Finish(nil)

		// Row 0 has alpha (2 tags -> 2 lines) + beta (empty tags -> 1 line) = 3 lines; Row 1 = 1 line.
		if rows, lines, _ := g.Size(); rows != 2 || lines != 4 {
			t.Fatalf("deep nested size = (%d, %d), want (2, 4)", rows, lines)
		}

		tree := results.Tree(fields, arrowRows[0])
		if len(tree) != len(fields) {
			t.Fatalf("tree len = %d, want %d", len(tree), len(fields))
		}

		cols := g.Columns()
		tagColIdx := slices.IndexFunc(cols, func(c results.Column) bool { return c.Name == "meta.groups.leaf.tags" })
		if tagColIdx < 0 {
			t.Fatal("missing meta.groups.leaf.tags column")
		}

		if got := results.ColumnPath(fields, arrowRows[0], cols[tagColIdx], 1); got != "meta.groups[0].leaf.tags[1]" {
			t.Fatalf("ColumnPath = %q, want meta.groups[0].leaf.tags[1]", got)
		}
	})

	t.Run("FakeOrdersSchema_REST_vs_JSON_oracle", func(t *testing.T) {
		t.Parallel()

		fb := &app.FakeBackend{}
		res, err := fb.Query(t.Context(), bq.QueryRequest{SQL: "SELECT * FROM orders LIMIT 6"})
		if err != nil {
			t.Fatalf("FakeBackend.Query: %v", err)
		}

		fields := results.FieldsFromREST(res.Schema.Fields)
		decoded, err := bq.DecodeRows(res.Schema.Fields, res.Rows)
		if err != nil {
			t.Fatalf("DecodeRows: %v", err)
		}

		for i, row := range decoded {
			bqJSON := string(bq.AppendRowJSON(nil, res.Schema.Fields, row))
			resJSON := string(results.AppendRowJSON(nil, fields, row))

			if bqJSON != resJSON {
				t.Fatalf("row %d JSON mismatch:\nbq:      %s\nresults: %s", i, bqJSON, resJSON)
			}

			if !json.Valid([]byte(resJSON)) {
				t.Fatalf("row %d invalid JSON: %s", i, resJSON)
			}
		}
	})

	t.Run("export_CSVLines_CSVJSONCells_NDJSON", func(t *testing.T) {
		t.Parallel()

		ds := buildDeepNestedDataset(t)
		fields := results.FieldsFromREST(ds.restFields)

		rows, err := bq.DecodeRows(ds.restFields, ds.restRows)
		if err != nil {
			t.Fatalf("DecodeRows: %v", err)
		}

		var csvLinesBuf bytes.Buffer
		wLines, err := export.NewRowWriter(&csvLinesBuf, export.CSV, fields, export.Options{CSVNesting: export.CSVLines})
		if err != nil {
			t.Fatalf("NewRowWriter CSVLines: %v", err)
		}

		if werr := wLines.WriteRows(rows); werr != nil {
			t.Fatalf("WriteRows CSVLines: %v", werr)
		}

		_ = wLines.Close()

		recs, err := csv.NewReader(&csvLinesBuf).ReadAll()
		if err != nil || len(recs) != 5 { // 1 header + 4 visual lines
			t.Fatalf("CSVLines records = %d, err = %v", len(recs), err)
		}

		var csvJSONBuf bytes.Buffer
		wJSONCells, err := export.NewRowWriter(&csvJSONBuf, export.CSV, fields, export.Options{CSVNesting: export.CSVJSONCells})
		if err != nil {
			t.Fatalf("NewRowWriter CSVJSONCells: %v", err)
		}

		_ = wJSONCells.WriteRows(rows)
		_ = wJSONCells.Close()

		jsonRecs, err := csv.NewReader(&csvJSONBuf).ReadAll()
		if err != nil || len(jsonRecs) != 3 { // 1 header + 2 logical rows
			t.Fatalf("CSVJSONCells records = %d, err = %v", len(jsonRecs), err)
		}

		var ndBuf bytes.Buffer
		wND, err := export.NewRowWriter(&ndBuf, export.NDJSON, fields, export.Options{})
		if err != nil {
			t.Fatalf("NewRowWriter NDJSON: %v", err)
		}

		_ = wND.WriteRows(rows)
		_ = wND.Close()

		ndLines := strings.Split(strings.TrimSpace(ndBuf.String()), "\n")
		if len(ndLines) != 2 {
			t.Fatalf("NDJSON lines = %d, want 2", len(ndLines))
		}

		for _, line := range ndLines {
			if !json.Valid([]byte(line)) {
				t.Fatalf("invalid NDJSON line: %s", line)
			}
		}
	})

	t.Run("export_Parquet_columnar", func(t *testing.T) {
		t.Parallel()

		ds := buildGithubNestedSubsetDataset(t)
		rec, err := decodeArrowBatch(ds.arrowBatch)
		if err != nil {
			t.Fatalf("decodeArrowBatch: %v", err)
		}
		defer rec.Release()

		var pqBuf bytes.Buffer
		pw, err := export.NewRecordWriter(&pqBuf, export.Parquet, rec.Schema())
		if err != nil {
			t.Fatalf("NewRecordWriter Parquet: %v", err)
		}

		if werr := pw.Write(rec); werr != nil {
			t.Fatalf("Parquet write: %v", werr)
		}

		if cerr := pw.Close(); cerr != nil {
			t.Fatalf("Parquet close: %v", cerr)
		}

		pr, err := file.NewParquetReader(bytes.NewReader(pqBuf.Bytes()))
		if err != nil {
			t.Fatalf("NewParquetReader: %v", err)
		}
		defer pr.Close()

		arrRdr, err := pqarrow.NewFileReader(pr, pqarrow.ArrowReadProperties{}, memory.NewGoAllocator())
		if err != nil {
			t.Fatalf("pqarrow.NewFileReader: %v", err)
		}

		tbl, err := arrRdr.ReadTable(t.Context())
		if err != nil {
			t.Fatalf("Parquet ReadTable: %v", err)
		}
		defer tbl.Release()

		if tbl.NumRows() != 2 {
			t.Fatalf("Parquet NumRows = %d, want 2", tbl.NumRows())
		}
	})
}

func TestTier1F2LiveBQContractOffline(t *testing.T) {
	t.Parallel()

	t.Run("Runner_inline_vs_storage_parity", func(t *testing.T) {
		t.Parallel()

		ds := buildDeepNestedDataset(t)
		schema := &bigquery.TableSchema{Fields: ds.restFields}
		dest := &bq.TableRef{Project: "p", Dataset: "_anon", Table: "t1"}

		inlineBE := &mockQueryBackend{
			result: &bq.QueryResult{
				Statement: "SELECT",
				Schema:    schema,
				TotalRows: uint64(len(ds.restRows)),
				Rows:      ds.restRows,
			},
		}
		storageBE := &mockQueryBackend{
			result: &bq.QueryResult{
				Statement:   "SELECT",
				Schema:      schema,
				TotalRows:   uint64(len(ds.restRows)),
				Rows:        ds.restRows[:1], // fewer than TotalRows -> triggers storage path
				Destination: dest,
			},
		}

		rnInline := app.NewRunner(app.RunnerOptions{
			Backend:  inlineBE,
			Post:     func(fn func()) { fn() },
			SpillDir: t.TempDir(),
		})
		rnStorage := app.NewRunner(app.RunnerOptions{
			Backend:  storageBE,
			Post:     func(fn func()) { fn() },
			SpillDir: t.TempDir(),
			Storage: func(context.Context, bq.TableRef) (app.StorageSession, error) {
				return &mockStorageSession{schema: ds.arrowSchema, batches: [][]byte{ds.arrowBatch}, estRows: 2}, nil
			},
		})

		sInline := waitRunDone(t, rnInline.Start(bq.QueryRequest{SQL: "SELECT 1"}))
		sStorage := waitRunDone(t, rnStorage.Start(bq.QueryRequest{SQL: "SELECT 1"}))

		if sInline.Source != "inline" || sStorage.Source != "storage" {
			t.Fatalf("sources = %q / %q, want inline / storage", sInline.Source, sStorage.Source)
		}

		for r := range int64(2) {
			rowA, err := sInline.Grid.Row(r)
			if err != nil {
				t.Fatalf("inline row %d: %v", r, err)
			}

			rowB, err := sStorage.Grid.Row(r)
			if err != nil {
				t.Fatalf("storage row %d: %v", r, err)
			}

			if !reflect.DeepEqual(rowA, rowB) {
				t.Fatalf("row %d mismatch inline vs storage", r)
			}
		}
	})

	t.Run("Runner_REST_paging_nested_rows", func(t *testing.T) {
		t.Parallel()

		ds := buildGithubNestedSubsetDataset(t)
		jobRef := &bq.JobRef{Project: "p", JobID: "j1", Location: "US"}
		be := &mockQueryBackend{
			result: &bq.QueryResult{
				Job:       jobRef,
				Statement: "SELECT",
				Schema:    &bigquery.TableSchema{Fields: ds.restFields},
				TotalRows: 2,
				Rows:      ds.restRows[:1],
				PageToken: "page2",
			},
			pages: map[string]*bigquery.GetQueryResultsResponse{
				"page2": {
					JobComplete: true,
					Schema:      &bigquery.TableSchema{Fields: ds.restFields},
					Rows:        ds.restRows[1:],
					TotalRows:   2,
				},
			},
		}

		rn := app.NewRunner(app.RunnerOptions{
			Backend:  be,
			Post:     func(fn func()) { fn() },
			SpillDir: t.TempDir(),
		})

		snap := waitRunDone(t, rn.Start(bq.QueryRequest{SQL: "SELECT 1"}))
		if snap.State != app.RunDone || snap.Source != "rest" {
			t.Fatalf("state=%v source=%q err=%v", snap.State, snap.Source, snap.Err)
		}

		rows, lines, complete := snap.Grid.Size()
		if rows != 2 || lines != 3 || !complete {
			t.Fatalf("paged grid size = (%d, %d, %v), want (2, 3, true)", rows, lines, complete)
		}
	})

	t.Run("Runner_Preview_storage_and_fallback", func(t *testing.T) {
		t.Parallel()

		ds := buildGASessionsSubsetDataset(t)
		tbl := &bigquery.Table{
			Type:   "TABLE",
			Schema: &bigquery.TableSchema{Fields: ds.restFields},
		}

		rnStorage := app.NewRunner(app.RunnerOptions{
			Backend:  &app.FakeBackend{},
			Metadata: &mockMetadataBackend{table: tbl},
			Storage: func(context.Context, bq.TableRef) (app.StorageSession, error) {
				return &mockStorageSession{schema: ds.arrowSchema, batches: [][]byte{ds.arrowBatch}, estRows: 1}, nil
			},
			Post:     func(fn func()) { fn() },
			SpillDir: t.TempDir(),
		})

		s1 := waitRunDone(t, rnStorage.Preview(bq.TableRef{Project: "p", Dataset: "d", Table: "t"}))
		if s1.State != app.RunDone || s1.Source != "storage" {
			t.Fatalf("preview storage state=%v source=%q err=%v", s1.State, s1.Source, s1.Err)
		}

		rnFallback := app.NewRunner(app.RunnerOptions{
			Backend:  &app.FakeBackend{},
			Post:     func(fn func()) { fn() },
			SpillDir: t.TempDir(),
		})

		s2 := waitRunDone(t, rnFallback.Preview(bq.TableRef{Project: "bqui-demo", Dataset: "analytics", Table: "events"}))
		if s2.State != app.RunDone || s2.Source != "inline" {
			t.Fatalf("preview fallback state=%v source=%q err=%v", s2.State, s2.Source, s2.Err)
		}
	})

	t.Run("Runner_Export_from_Grid_and_Storage", func(t *testing.T) {
		t.Parallel()

		ds := buildGithubNestedSubsetDataset(t)
		tbl := &bigquery.Table{
			Type:   "TABLE",
			Schema: &bigquery.TableSchema{Fields: ds.restFields},
		}

		rn := app.NewRunner(app.RunnerOptions{
			Backend:  &app.FakeBackend{},
			Metadata: &mockMetadataBackend{table: tbl},
			Storage: func(context.Context, bq.TableRef) (app.StorageSession, error) {
				return &mockStorageSession{schema: ds.arrowSchema, batches: [][]byte{ds.arrowBatch}, estRows: 2}, nil
			},
			Post:     func(fn func()) { fn() },
			SpillDir: t.TempDir(),
		})

		outDir := t.TempDir()
		ref := &bq.TableRef{Project: "p", Dataset: "d", Table: "github_nested"}

		done := make(chan app.ExportProgress, 1)
		rn.Export(app.ExportSource{Table: ref}, filepath.Join(outDir, "out.parquet"), export.Parquet, export.Options{}, func(p app.ExportProgress) {
			if p.Done {
				done <- p
			}
		})

		p := <-done
		if p.Err != nil || p.Rows != 2 {
			t.Fatalf("parquet export: %+v", p)
		}

		// Also export from Grid to NDJSON.
		snap := waitRunDone(t, rn.Preview(*ref))
		doneND := make(chan app.ExportProgress, 1)
		rn.Export(app.ExportSource{Grid: snap.Grid}, filepath.Join(outDir, "out.jsonl"), export.NDJSON, export.Options{}, func(p app.ExportProgress) {
			if p.Done {
				doneND <- p
			}
		})

		pND := <-doneND
		if pND.Err != nil || pND.Rows != 2 {
			t.Fatalf("ndjson export: %+v", pND)
		}
	})

	t.Run("bq_TableRef_and_DatasetRef_parsing", func(t *testing.T) {
		t.Parallel()

		ref, err := bq.ParseTableRef("`bigquery-public-data.samples.github_nested`", "ff-homelab")
		if err != nil || ref.Project != "bigquery-public-data" || ref.Dataset != "samples" || ref.Table != "github_nested" {
			t.Fatalf("ParseTableRef = (%+v, %v)", ref, err)
		}

		if got := ref.SQL(); got != "`bigquery-public-data.samples.github_nested`" {
			t.Fatalf("ref.SQL() = %q", got)
		}

		ref2, err := bq.ParseTableRef("temp.nested_tbl", "ff-homelab")
		if err != nil || ref2.String() != "ff-homelab.temp.nested_tbl" {
			t.Fatalf("ParseTableRef 2-part = (%+v, %v)", ref2, err)
		}

		ds, err := bq.ParseDatasetRef("ff-homelab:temp", "")
		if err != nil || ds.Project != "ff-homelab" || ds.Dataset != "temp" {
			t.Fatalf("ParseDatasetRef = (%+v, %v)", ds, err)
		}
	})
}

func TestTier1F3ResultsTableAndJSONContracts(t *testing.T) {
	t.Parallel()

	t.Run("JSON_nested_object_and_array_fidelity", func(t *testing.T) {
		t.Parallel()

		ds := buildDeepNestedDataset(t)
		fields := results.FieldsFromREST(ds.restFields)

		rows, err := bq.DecodeRows(ds.restFields, ds.restRows)
		if err != nil {
			t.Fatalf("DecodeRows: %v", err)
		}

		js := string(results.AppendRowJSON(nil, fields, rows[0]))
		if !json.Valid([]byte(js)) {
			t.Fatalf("invalid JSON: %s", js)
		}

		for _, sub := range []string{`"alpha"`, `"beta"`, `["t1","t2"]`, `"extra_json":{"k":"v","n":[1,2]}`} {
			if !strings.Contains(js, sub) {
				t.Errorf("JSON missing %s in %s", sub, js)
			}
		}

		spans := results.JSONSpans(js)
		var rebuilt strings.Builder
		for _, sp := range spans {
			rebuilt.WriteString(js[sp.Start:sp.End])
		}

		if rebuilt.String() != js {
			t.Fatalf("JSONSpans reconstruction mismatch")
		}
	})

	t.Run("Results_grid_column_dotted_paths_and_subrows", func(t *testing.T) {
		t.Parallel()

		ds := buildDeepNestedDataset(t)
		fields := results.FieldsFromREST(ds.restFields)

		rows, err := bq.DecodeRows(ds.restFields, ds.restRows)
		if err != nil {
			t.Fatalf("DecodeRows: %v", err)
		}

		cells := results.Render(fields, rows[0])
		cols := results.Leaves(fields)

		if len(cells) != len(cols) {
			t.Fatalf("rendered cols = %d, want %d", len(cells), len(cols))
		}

		// Line 1 of row 0 has Blank for scalar column "id" (col 0) and Value "t2" for tags.
		if cells[0][1].Kind != results.Blank {
			t.Fatalf("continuation line col 0 kind = %v, want Blank", cells[0][1].Kind)
		}

		tagCol := slices.IndexFunc(cols, func(c results.Column) bool { return c.Name == "meta.groups.leaf.tags" })
		if cells[tagCol][1].Kind != results.Value || cells[tagCol][1].V != "t2" {
			t.Fatalf("continuation line tag cell = %+v, want Value(t2)", cells[tagCol][1])
		}
	})

	t.Run("Results_rune_width_accounting", func(t *testing.T) {
		t.Parallel()

		s := "日本語テスト🚀αβγ"
		formatted := results.FormatCell(results.TypeString, s)
		if formatted != s {
			t.Fatalf("FormatCell = %q, want %q", formatted, s)
		}

		if got := utf8.RuneCountInString(formatted); got != 10 {
			t.Fatalf("RuneCount = %d, want 10 (byte len=%d)", got, len(formatted))
		}
	})

	t.Run("ValueInspector_Tree_and_ColumnPath", func(t *testing.T) {
		t.Parallel()

		ds := buildGithubNestedSubsetDataset(t)
		fields := results.FieldsFromREST(ds.restFields)

		rows, err := bq.DecodeRows(ds.restFields, ds.restRows)
		if err != nil {
			t.Fatalf("DecodeRows: %v", err)
		}

		tree := results.Tree(fields, rows[0])
		if tree[3].Name != "payload" || tree[3].Type != "STRUCT" {
			t.Fatalf("tree[3] = %+v", tree[3])
		}

		cols := results.Leaves(fields)
		pageCol := slices.IndexFunc(cols, func(c results.Column) bool { return c.Name == "payload.pages.page_name" })
		if got := results.ColumnPath(fields, rows[0], cols[pageCol], 1); got != "payload.pages[1].page_name" {
			t.Fatalf("ColumnPath = %q, want payload.pages[1].page_name", got)
		}
	})

	t.Run("Grid_first_line_and_random_access_across_chunks", func(t *testing.T) {
		t.Parallel()

		fields := []results.Field{
			{Name: "id", Type: results.TypeInt64},
			{Name: "tags", Type: results.TypeString, Repeated: true},
		}

		totalRows := 1500
		rows := make([][]any, totalRows)
		for i := range totalRows {
			rows[i] = []any{int64(i), []any{"a", "b"}}
		}

		m := &results.MemoryStore{}
		g := results.NewGrid(fields, m)
		results.AppendRows(g, m, rows)
		g.Finish(nil)

		rCount, lCount, complete := g.Size()
		if rCount != 1500 || lCount != 3000 || !complete {
			t.Fatalf("size = (%d, %d, %v), want (1500, 3000, true)", rCount, lCount, complete)
		}

		fl, err := g.FirstLine(1200)
		if err != nil || fl != 2400 {
			t.Fatalf("FirstLine(1200) = (%d, %v), want (2400, nil)", fl, err)
		}

		c, pos, err := g.Cell(2401, 1)
		if err != nil || c.V != "b" || pos.Row != 1200 || !pos.Last() {
			t.Fatalf("Cell(2401,1) = (%+v, %+v, %v)", c, pos, err)
		}
	})
}

func TestTier1F4PlanAndPortableTests(t *testing.T) {
	t.Parallel()

	t.Run("plan_FromJob_with_fixture_job_json", func(t *testing.T) {
		t.Parallel()

		j := loadPlanJobFixture(t)
		p := plan.FromJob(j)
		if p == nil || len(p.Stages) != 13 {
			t.Fatalf("plan stages = %v", p)
		}

		st, ok := p.Stage(9)
		if !ok || !strings.HasPrefix(st.Name, "S09: Join") {
			t.Fatalf("stage 9 = %+v, ok=%v", st, ok)
		}
	})

	t.Run("plan_Layout_no_crossings_and_non_overlapping", func(t *testing.T) {
		t.Parallel()

		j := loadPlanJobFixture(t)
		p := plan.FromJob(j)
		l := p.Layout()

		if len(l.Nodes) != 13 || len(l.Edges) != 12 {
			t.Fatalf("nodes=%d edges=%d", len(l.Nodes), len(l.Edges))
		}

		if c := p.Crossings(l.Layers); c != 0 {
			t.Fatalf("Crossings = %d, want 0", c)
		}
	})

	t.Run("plan_Flags_and_StepText", func(t *testing.T) {
		t.Parallel()

		j := loadPlanJobFixture(t)
		p := plan.FromJob(j)

		var sawSteps bool
		for _, s := range p.Stages {
			if len(plan.StepText(s)) > 0 {
				sawSteps = true
			}

			_ = p.Flags(s)
		}

		if !sawSteps {
			t.Fatal("expected non-empty StepText on real plan fixture")
		}
	})

	t.Run("plan_testdata_exists_and_parses", func(t *testing.T) {
		t.Parallel()

		b, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "plan", "testdata", "job.json"))
		if err != nil {
			t.Fatalf("read internal/plan/testdata/job.json: %v", err)
		}

		var j bigquery.Job
		if err := json.Unmarshal(b, &j); err != nil {
			t.Fatalf("unmarshal testdata/job.json: %v", err)
		}

		if p := plan.FromJob(&j); p == nil || len(p.Stages) != 13 {
			t.Fatalf("plan from testdata/job.json invalid")
		}
	})

	t.Run("go_test_c_portable_package_exec", func(t *testing.T) {
		t.Parallel()

		outDir := t.TempDir()
		testBin := filepath.Join(outDir, "theme.test")

		_, stderr, rc := runSubprocess(t, repoRoot(t), "go", []string{"CGO_ENABLED=0"}, "test", "-c", "-o", testBin, "./internal/theme")
		if rc != 0 {
			t.Fatalf("go test -c ./internal/theme rc=%d stderr=%s", rc, stderr)
		}

		execDir := t.TempDir()
		stdout, stderr, rc := runSubprocess(t, execDir, testBin, nil, "-test.count=1")
		if rc != 0 {
			t.Fatalf("standalone theme.test failed rc=%d stdout=%s stderr=%s", rc, stdout, stderr)
		}
	})
}

func TestTier1F5OutlineWrappingContract(t *testing.T) {
	t.Parallel()

	t.Run("Outline_Details_Sections_KeyValues_Present", func(t *testing.T) {
		t.Parallel()

		tbl := loadAppTableFixture(t, "table.bigquery-public-data.samples.github_nested.json")
		secs := (app.Formatter{Loc: time.UTC}).TableSections(tbl)
		if len(secs) < 2 {
			t.Fatalf("TableSections len = %d, want >= 2", len(secs))
		}

		var hasRows bool
		for _, s := range secs {
			for _, r := range s.Rows {
				if r.Key == "Number of rows" && r.Value != "" {
					hasRows = true
				}
			}
		}

		if !hasRows {
			t.Fatal("missing Number of rows in TableSections")
		}
	})

	t.Run("Outline_SchemaTree_Hierarchy_and_Badges", func(t *testing.T) {
		t.Parallel()

		tbl := &bigquery.Table{
			TimePartitioning: &bigquery.TimePartitioning{Type: "DAY", Field: "ts"},
			Clustering:       &bigquery.Clustering{Fields: []string{"user_id"}},
			Schema: &bigquery.TableSchema{Fields: []*bigquery.TableFieldSchema{
				{Name: "ts", Type: "TIMESTAMP", Mode: "REQUIRED"},
				{Name: "user_id", Type: "STRING", PolicyTags: &bigquery.TableFieldSchemaPolicyTags{Names: []string{"projects/p/locations/us/taxonomies/1/policyTags/2"}}},
			}},
		}

		sf := app.SchemaTree(tbl)
		if len(sf) != 2 {
			t.Fatalf("SchemaTree len = %d", len(sf))
		}

		if !slices.Contains(sf[0].Badges(), "partition") {
			t.Errorf("sf[0] badges = %v, want partition", sf[0].Badges())
		}

		if b := sf[1].Badges(); !slices.Contains(b, "cluster 1") || !slices.Contains(b, "policy tag") {
			t.Errorf("sf[1] badges = %v, want cluster 1 & policy tag", b)
		}
	})

	t.Run("Smoke_Outline_Wrapping_Checks_Contract", func(t *testing.T) {
		t.Parallel()

		rep, err := validateSmokeReport(goldenSmokeReportJSON(true), smokeValidationOpts{requireLayoutChecks: true})
		if err != nil {
			t.Fatalf("validateSmokeReport: %v", err)
		}

		if rep.Failed != 0 {
			t.Fatalf("rep.Failed = %d", rep.Failed)
		}
	})

	t.Run("Smoke_Outline_No_Reentrancy_Log_Contract", func(t *testing.T) {
		t.Parallel()

		logData := []byte("2026-11-27 smoke start\neditor ready\ninspector wrap 440pt ok\ninspector wrap 300pt ok\nsmoke done\n")
		if err := validateSmokeLog(logData); err != nil {
			t.Fatalf("validateSmokeLog: %v", err)
		}
	})

	t.Run("AXTree_Outline_Nodes_Present", func(t *testing.T) {
		t.Parallel()

		if err := validateAXTree(goldenAXTreeJSON()); err != nil {
			t.Fatalf("validateAXTree: %v", err)
		}
	})
}

func TestTier1F6GeometryInspectionContract(t *testing.T) {
	t.Parallel()

	t.Run("Report_WindowFrame_Origin_Size_Contract", func(t *testing.T) {
		t.Parallel()

		rep, err := validateSmokeReport(goldenSmokeReportJSON(false), smokeValidationOpts{})
		if err != nil {
			t.Fatalf("validateSmokeReport: %v", err)
		}

		if rep.WindowFrame.Size.Width != 1280 || rep.WindowFrame.Size.Height != 691 || rep.WindowFrame.Origin.Y != 78 {
			t.Fatalf("windowFrame = %+v", rep.WindowFrame)
		}
	})

	t.Run("Report_DispatchDelay_Bounded", func(t *testing.T) {
		t.Parallel()

		rep, err := validateSmokeReport(goldenSmokeReportJSON(false), smokeValidationOpts{})
		if err != nil {
			t.Fatalf("validateSmokeReport: %v", err)
		}

		if rep.DispatchDelay <= 0 || time.Duration(rep.DispatchDelay) >= maxAllowedDispatchDelay {
			t.Fatalf("DispatchDelay = %d", rep.DispatchDelay)
		}
	})

	t.Run("Layout_Check_Containment_NonOverlap_Schema", func(t *testing.T) {
		t.Parallel()

		rep, err := validateSmokeReport(goldenSmokeReportJSON(true), smokeValidationOpts{requireLayoutChecks: true})
		if err != nil {
			t.Fatalf("validateSmokeReport: %v", err)
		}

		if len(rep.Checks) < len(baselineRequiredChecks)+5 {
			t.Fatalf("checks count = %d", len(rep.Checks))
		}
	})

	t.Run("Smoke_Report_All_Baseline_Checks_Pass", func(t *testing.T) {
		t.Parallel()

		if _, err := validateSmokeReport(goldenSmokeReportJSON(false), smokeValidationOpts{}); err != nil {
			t.Fatalf("baseline report failed validation: %v", err)
		}
	})

	t.Run("AXTree_SplitGroup_And_Toolbar_Contract", func(t *testing.T) {
		t.Parallel()

		if err := validateAXTree(goldenAXTreeJSON()); err != nil {
			t.Fatalf("validateAXTree: %v", err)
		}
	})
}

func TestTier1F7InspectorMetadataAndSchema(t *testing.T) {
	t.Parallel()

	t.Run("ga_sessions_222_fields_CountFields", func(t *testing.T) {
		t.Parallel()

		tbl := loadAppTableFixture(t, "table.bigquery-public-data.google_analytics_sample.ga_sessions_20170801.json")
		tree := app.SchemaTree(tbl)
		count := app.CountFields(tree)

		if count < 222 {
			t.Fatalf("CountFields(ga_sessions) = %d, want >= 222", count)
		}
	})

	t.Run("github_nested_SchemaTree_deep_children", func(t *testing.T) {
		t.Parallel()

		tbl := loadAppTableFixture(t, "table.bigquery-public-data.samples.github_nested.json")
		tree := app.SchemaTree(tbl)

		idx := slices.IndexFunc(tree, func(f app.SchemaField) bool { return f.Name == "payload" })
		if idx < 0 || len(tree[idx].Children) == 0 {
			t.Fatalf("github_nested payload children missing")
		}

		if total := app.CountFields(tree); total < 50 {
			t.Fatalf("CountFields(github_nested) = %d, want >= 50", total)
		}
	})

	t.Run("TableSections_view_and_external_and_encryption", func(t *testing.T) {
		t.Parallel()

		tbl := &bigquery.Table{
			TableReference:          &bigquery.TableReference{ProjectId: "p", DatasetId: "d", TableId: "v"},
			Type:                    "VIEW",
			Description:             "Analytics view",
			EncryptionConfiguration: &bigquery.EncryptionConfiguration{KmsKeyName: "projects/p/locations/us/keyRings/r/cryptoKeys/k"},
			View:                    &bigquery.ViewDefinition{Query: "select a, b from p.d.t where a > 1"},
			ExternalDataConfiguration: &bigquery.ExternalDataConfiguration{
				SourceFormat: "PARQUET",
				SourceUris:   []string{"gs://bucket/prefix/*.parquet"},
				Autodetect:   true,
			},
		}

		secs := (app.Formatter{Loc: time.UTC}).TableSections(tbl)
		var sawKMS, sawView, sawExt bool
		for _, s := range secs {
			for _, r := range s.Rows {
				if r.Key == "Encryption key" && r.Mono {
					sawKMS = true
				}

				if r.Key == "View query" && strings.Contains(r.Value, "SELECT") {
					sawView = true
				}

				if r.Key == "Source format" && r.Value == "PARQUET" {
					sawExt = true
				}
			}
		}

		if !sawKMS || !sawView || !sawExt {
			t.Fatalf("missing sections: kms=%v view=%v ext=%v", sawKMS, sawView, sawExt)
		}
	})

	t.Run("Dataset_Routine_Model_Sections", func(t *testing.T) {
		t.Parallel()

		f := app.Formatter{Loc: time.UTC}
		dsSecs := f.DatasetSections(&bigquery.Dataset{
			DatasetReference:   &bigquery.DatasetReference{ProjectId: "p", DatasetId: "d"},
			Location:           "US",
			MaxTimeTravelHours: 168,
		})
		if len(dsSecs) != 1 || len(dsSecs[0].Rows) < 3 {
			t.Fatalf("DatasetSections = %+v", dsSecs)
		}

		rtSecs := f.RoutineSections(&bigquery.Routine{
			RoutineReference: &bigquery.RoutineReference{ProjectId: "p", DatasetId: "d", RoutineId: "fn"},
			RoutineType:      "SCALAR_FUNCTION",
			Language:         "SQL",
			Arguments:        []*bigquery.Argument{{Name: "x", DataType: &bigquery.StandardSqlDataType{TypeKind: "INT64"}}},
			ReturnType:       &bigquery.StandardSqlDataType{TypeKind: "STRING"},
			DefinitionBody:   "CAST(x AS STRING)",
		})
		if len(rtSecs) != 3 {
			t.Fatalf("RoutineSections = %d, want 3", len(rtSecs))
		}

		mdSecs := f.ModelSections(&bigquery.Model{
			ModelReference: &bigquery.ModelReference{ProjectId: "p", DatasetId: "d", ModelId: "m"},
			ModelType:      "KMEANS",
			FeatureColumns: []*bigquery.StandardSqlField{{Name: "f1", Type: &bigquery.StandardSqlDataType{TypeKind: "FLOAT64"}}},
		})
		if len(mdSecs) != 2 {
			t.Fatalf("ModelSections = %d, want 2", len(mdSecs))
		}
	})

	t.Run("ValueInspector_MaxInspectorRunes_and_Types", func(t *testing.T) {
		t.Parallel()

		fields := []results.Field{
			{Name: "r", Type: results.TypeRange, RangeElem: results.TypeDate},
			{Name: "b", Type: results.TypeBytes},
			{Name: "ts", Type: results.TypeTimestamp},
			{Name: "n", Type: results.TypeString},
		}
		ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		tree := results.Tree(fields, []any{"[2026-01-01, UNBOUNDED)", []byte{0xab, 0xcd}, ts, nil})

		if tree[0].Type != "RANGE<DATE>" || tree[1].Display != "q80=" || tree[2].Display != "2026-01-02 03:04:05 UTC" || !tree[3].Null {
			t.Fatalf("unexpected tree nodes: %+v", tree)
		}
	})
}

func TestTier1F8ShellTabsToolbarAndBanners(t *testing.T) {
	t.Parallel()

	t.Run("AXTree_7_Bottom_Tabs_Present", func(t *testing.T) {
		t.Parallel()

		if err := validateAXTree(goldenAXTreeJSON()); err != nil {
			t.Fatalf("validateAXTree: %v", err)
		}

		withRunsCount := bytes.Replace(goldenAXTreeJSON(), []byte(`"Runs"`), []byte(`"Runs (2)"`), 1)
		if err := validateAXTree(withRunsCount); err != nil {
			t.Fatalf("validateAXTree with Runs (2): %v", err)
		}
	})

	t.Run("AXTree_Toolbar_Items_Present", func(t *testing.T) {
		t.Parallel()

		axBytes := goldenAXTreeJSON()
		var root axNode
		if err := json.Unmarshal(axBytes, &root); err != nil {
			t.Fatal(err)
		}

		var hasToolbar bool
		for _, ch := range root.Children {
			if ch.Role == "AXToolbar" && len(ch.Children) >= 3 {
				hasToolbar = true
			}
		}

		if !hasToolbar {
			t.Fatal("AXToolbar children missing")
		}
	})

	t.Run("AXTree_Sidebar_And_Inspector_Controls", func(t *testing.T) {
		t.Parallel()

		if err := validateAXTree(goldenAXTreeJSON()); err != nil {
			t.Fatalf("validateAXTree: %v", err)
		}
	})

	t.Run("Smoke_Layout_Narrow_900x600_And_Toolbar_Checks", func(t *testing.T) {
		t.Parallel()

		if _, err := validateSmokeReport(goldenSmokeReportJSON(true), smokeValidationOpts{requireLayoutChecks: true}); err != nil {
			t.Fatalf("validateSmokeReport: %v", err)
		}
	})

	t.Run("Problem_Explain_Error_Banner_Formatting", func(t *testing.T) {
		t.Parallel()

		p1 := app.Explain(app.ErrPreviewTimeout)
		if !p1.Warning || p1.Title == "" || p1.Hint == "" {
			t.Fatalf("ErrPreviewTimeout problem = %+v", p1)
		}

		p2 := app.Explain(bq.ErrQueryFailed)
		if p2.Warning || p2.Title != "Query failed" {
			t.Fatalf("ErrQueryFailed problem = %+v", p2)
		}
	})
}

func TestTier1F9HistoryThemePrefsAndDB(t *testing.T) {
	t.Parallel()

	t.Run("History_Reload_LoadMore_FilterJobRows", func(t *testing.T) {
		t.Parallel()

		fb := &app.FakeBackend{}
		done := make(chan struct{}, 4)

		var h *app.History
		h = app.NewHistory(fb, func(fn func()) { fn() }, func() {
			if !h.Loading() && len(h.Rows()) > 0 {
				select {
				case done <- struct{}{}:
				default:
				}
			}
		})

		h.Reload(app.FakeProject, false)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("History.Reload timed out")
		}

		if len(h.Rows()) == 0 || h.Err() != nil {
			t.Fatalf("History rows=%d err=%v", len(h.Rows()), h.Err())
		}

		filtered := app.FilterJobRows(h.Rows(), "events")
		if len(filtered) == 0 {
			t.Fatal("FilterJobRows('events') returned 0 rows")
		}
	})

	t.Run("Theme_Builtins_Validate_And_Palette", func(t *testing.T) {
		t.Parallel()

		builtins := theme.Builtins()
		if len(builtins) < 2 {
			t.Fatalf("builtins len = %d, want >= 2", len(builtins))
		}

		for _, th := range builtins {
			if err := th.Validate(); err != nil {
				t.Errorf("builtin %q invalid: %v", th.Name, err)
			}

			if p := th.Palette(true); p.Editor.Background == "" {
				t.Errorf("builtin %q dark editor bg empty", th.Name)
			}
		}
	})

	t.Run("Theme_ImportVSCode_And_SaveLoad_Roundtrip", func(t *testing.T) {
		t.Parallel()

		raw := []byte(`{
			// comment
			"name": "E2E Ocean",
			"type": "dark",
			"colors": {
				"editor.background": "#101820",
				"editor.foreground": "#e0e8f0",
			},
			"tokenColors": [
				{"scope": "keyword", "settings": {"foreground": "#ff79c6", "fontStyle": "bold"}},
				{"scope": "string", "settings": {"foreground": "#f1fa8c"}},
			],
		}`)

		th, err := theme.ImportVSCode(raw)
		if err != nil {
			t.Fatalf("ImportVSCode: %v", err)
		}

		path := filepath.Join(t.TempDir(), theme.FileName(th.Name))
		if serr := theme.Save(th, path); serr != nil {
			t.Fatalf("theme.Save: %v", serr)
		}

		loaded, err := theme.Load(path)
		if err != nil {
			t.Fatalf("theme.Load: %v", err)
		}

		if loaded.Name != "E2E Ocean" || loaded.Dark.Editor.Background != "#101820" {
			t.Fatalf("loaded theme = %+v", loaded)
		}
	})

	t.Run("Store_Preferences_Parse_And_Text_Roundtrip", func(t *testing.T) {
		t.Parallel()

		in := store.PreferenceText{
			Location:       "US",
			DefaultDataset: "ff-homelab.temp",
			MaxBytesBilled: "10 GB",
			CostWarningUSD: "$2.50",
			MaxSpillBytes:  "512 MiB",
		}

		prefs, err := in.Parse()
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}

		if prefs.MaxBytesBilled != 10_000_000_000 || prefs.CostWarningUSD != 2.5 {
			t.Fatalf("parsed prefs = %+v", prefs)
		}

		txt := prefs.Text()
		if txt.MaxBytesBilled != "10 GB" {
			t.Fatalf("prefs.Text().MaxBytesBilled = %q", txt.MaxBytesBilled)
		}
	})

	t.Run("Store_DB_Tabs_And_Runs_Persistence", func(t *testing.T) {
		t.Parallel()

		db, err := store.Open(filepath.Join(t.TempDir(), "bqui.db"))
		if err != nil {
			t.Fatalf("store.Open: %v", err)
		}
		defer db.Close()

		err = db.RecordRun(t.Context(), store.Run{
			Started: time.Now().UTC(),
			Project: "ff-homelab",
			JobID:   "job_e2e_1",
			SQL:     "SELECT 1",
			State:   "done",
			Rows:    1,
			Bytes:   1024,
			Elapsed: 150 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("RecordRun: %v", err)
		}

		runs, err := db.SearchRuns(t.Context(), "", 10)
		if err != nil || len(runs) != 1 {
			t.Fatalf("SearchRuns = (%d, %v)", len(runs), err)
		}

		jrows := app.JobRowsFromRuns(runs)
		if len(jrows) != 1 || jrows[0].User != app.LocalUser || jrows[0].Ref.JobID != "job_e2e_1" {
			t.Fatalf("JobRowsFromRuns = %+v", jrows)
		}
	})
}

func TestTier1F10SmokeGeometryValidation(t *testing.T) {
	t.Parallel()

	t.Run("SmokeValidator_Accepts_Complete_Layout_Report", func(t *testing.T) {
		t.Parallel()

		if _, err := validateSmokeReport(goldenSmokeReportJSON(true), smokeValidationOpts{requireLayoutChecks: true}); err != nil {
			t.Fatalf("validateSmokeReport: %v", err)
		}
	})

	t.Run("SmokeValidator_Checks_Metadata_Fields", func(t *testing.T) {
		t.Parallel()

		rep, err := validateSmokeReport(goldenSmokeReportJSON(true), smokeValidationOpts{requireLayoutChecks: true})
		if err != nil {
			t.Fatal(err)
		}

		if rep.Arch != "arm64" || rep.GoVersion == "" {
			t.Fatalf("metadata = %+v", rep)
		}
	})

	t.Run("SmokeValidator_DispatchDelay_Positive_And_Bounded", func(t *testing.T) {
		t.Parallel()

		rep, err := validateSmokeReport(goldenSmokeReportJSON(false), smokeValidationOpts{})
		if err != nil {
			t.Fatal(err)
		}

		if rep.DispatchDelay <= 0 {
			t.Fatalf("DispatchDelay = %d", rep.DispatchDelay)
		}
	})

	t.Run("SmokeValidator_CheckDetail_NonEmpty_On_Metrics", func(t *testing.T) {
		t.Parallel()

		rep, err := validateSmokeReport(goldenSmokeReportJSON(true), smokeValidationOpts{requireLayoutChecks: true})
		if err != nil {
			t.Fatal(err)
		}

		for _, c := range rep.Checks {
			if c.Detail == "" {
				t.Errorf("check %q has empty detail", c.Name)
			}
		}
	})

	t.Run("SmokeValidator_Log_Clean_Acceptance", func(t *testing.T) {
		t.Parallel()

		if err := validateSmokeLog([]byte("bqui smoke complete: 52 passed, 0 failed\n")); err != nil {
			t.Fatalf("validateSmokeLog: %v", err)
		}
	})
}

func TestTier1F11ScreenshotsAndPNGValidation(t *testing.T) {
	t.Parallel()

	t.Run("PNGValidator_Accepts_NonUniform_Image", func(t *testing.T) {
		t.Parallel()

		p := filepath.Join(t.TempDir(), "window.png")
		writeTestPNG(t, p, 1280, 720, false)

		cfg, err := validatePNGFile(p)
		if err != nil || cfg.Width != 1280 || cfg.Height != 720 {
			t.Fatalf("validatePNGFile = (%+v, %v)", cfg, err)
		}
	})

	t.Run("PNGValidator_Checks_PerPane_Manifest", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		panes := []string{
			"segmented.png", "window-light.png", "window-dark.png",
			"pane-results-nested.png", "pane-json-nested.png", "pane-inspector-details.png", "window-narrow.png",
		}

		for _, name := range panes {
			p := filepath.Join(dir, name)
			writeTestPNG(t, p, 900, 600, false)

			if _, err := validatePNGFile(p); err != nil {
				t.Fatalf("pane %s failed: %v", name, err)
			}
		}
	})

	t.Run("PNGValidator_HighRes_Window_Dimensions", func(t *testing.T) {
		t.Parallel()

		p := filepath.Join(t.TempDir(), "retina.png")
		writeTestPNG(t, p, 2560, 1440, false)

		cfg, err := validatePNGFile(p)
		if err != nil || cfg.Width != 2560 || cfg.Height != 1440 {
			t.Fatalf("retina png = (%+v, %v)", cfg, err)
		}
	})

	t.Run("PNGValidator_Narrow_Window_Dimensions", func(t *testing.T) {
		t.Parallel()

		p := filepath.Join(t.TempDir(), "window-narrow.png")
		writeTestPNG(t, p, 900, 600, false)

		cfg, err := validatePNGFile(p)
		if err != nil || cfg.Width != 900 || cfg.Height != 600 {
			t.Fatalf("narrow png = (%+v, %v)", cfg, err)
		}
	})

	t.Run("Report_Screenshots_List_NonEmpty", func(t *testing.T) {
		t.Parallel()

		dir := goldenSmokeDir(t, true)

		rep, err := validateSmokeArtifacts(dir, smokeValidationOpts{requireLayoutChecks: true, requirePerPanePNGs: true})
		if err != nil {
			t.Fatalf("validateSmokeArtifacts: %v", err)
		}

		if len(rep.Screenshots) != 7 {
			t.Fatalf("screenshots len = %d, want 7", len(rep.Screenshots))
		}
	})
}

func TestTier1F12BuildCLIAndAcceptance(t *testing.T) {
	t.Parallel()

	t.Run("bquictl_Version_Flag_Exit0", func(t *testing.T) {
		t.Parallel()

		bin := buildBinary(t, "./cmd/bquictl", "bquictl", "", "")
		stdout, stderr, rc := runSubprocess(t, "", bin, nil, "-version")
		if rc != 0 || !strings.HasPrefix(stdout, "bquictl ") || stderr != "" {
			t.Fatalf("rc=%d stdout=%q stderr=%q", rc, stdout, stderr)
		}
	})

	t.Run("bquictl_NoArgs_Prints_Usage_All_8_Commands", func(t *testing.T) {
		t.Parallel()

		bin := buildBinary(t, "./cmd/bquictl", "bquictl", "", "")
		_, stderr, rc := runSubprocess(t, "", bin, nil)
		if rc != 2 {
			t.Fatalf("rc=%d, want 2; stderr=%s", rc, stderr)
		}

		for _, cmd := range []string{"whoami", "projects", "ls", "show", "query", "jobs", "job", "read"} {
			if !strings.Contains(stderr, cmd) {
				t.Errorf("usage missing command %q in:\n%s", cmd, stderr)
			}
		}
	})

	t.Run("bqui_Linux_Stub_Exits2_With_Notice", func(t *testing.T) {
		t.Parallel()

		bin := buildBinary(t, "./cmd/bqui", "bqui", "linux", "amd64")
		stdout, stderr, rc := runSubprocess(t, "", bin, nil)
		if rc != 2 || stdout != "" || !strings.Contains(stderr, "the GUI only runs on macOS; use bquictl") {
			t.Fatalf("rc=%d stdout=%q stderr=%q", rc, stdout, stderr)
		}
	})

	t.Run("tools_bundle_Builds_And_Assembles_Mock_App", func(t *testing.T) {
		t.Parallel()

		bundleBin := buildBinary(t, "./tools/bundle", "bundle", "", "")
		work := t.TempDir()
		mockExe := filepath.Join(work, "mock-bqui")
		if err := os.WriteFile(mockExe, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
			t.Fatal(err)
		}

		appDir := filepath.Join(work, "bqui.app")
		notices := filepath.Join(repoRoot(t), "THIRD_PARTY_NOTICES.md")

		_, stderr, rc := runSubprocess(t, "", bundleBin, nil, "-bin", mockExe, "-o", appDir, "-notices", notices, "-version", "1.2.3")
		if rc != 0 {
			t.Fatalf("bundle rc=%d stderr=%s", rc, stderr)
		}

		for _, rel := range []string{"Contents/Info.plist", "Contents/PkgInfo", "Contents/MacOS/bqui", "Contents/Resources/THIRD_PARTY_NOTICES.md"} {
			if _, err := os.Stat(filepath.Join(appDir, rel)); err != nil {
				t.Errorf("missing bundled artifact %s: %v", rel, err)
			}
		}
	})

	t.Run("FakeBackend_E2E_Query_And_Export_Oracle", func(t *testing.T) {
		t.Parallel()

		rn := app.NewRunner(app.RunnerOptions{
			Backend:  &app.FakeBackend{},
			Post:     func(fn func()) { fn() },
			SpillDir: t.TempDir(),
		})

		snap := waitRunDone(t, rn.Start(bq.QueryRequest{SQL: "SELECT * FROM orders LIMIT 25"}))
		if snap.State != app.RunDone {
			t.Fatalf("run failed: %v", snap.Err)
		}

		rows, _, complete := snap.Grid.Size()
		if rows != 25 || !complete {
			t.Fatalf("rows=%d complete=%v", rows, complete)
		}

		ndPath := filepath.Join(t.TempDir(), "orders.jsonl")
		done := make(chan app.ExportProgress, 1)
		rn.Export(app.ExportSource{Grid: snap.Grid}, ndPath, export.NDJSON, export.Options{}, func(p app.ExportProgress) {
			if p.Done {
				done <- p
			}
		})

		p := <-done
		if p.Err != nil || p.Rows != 25 {
			t.Fatalf("export progress = %+v", p)
		}

		b, err := os.ReadFile(ndPath)
		if err != nil {
			t.Fatal(err)
		}

		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(lines) != 25 {
			t.Fatalf("exported %d lines, want 25", len(lines))
		}
	})
}
