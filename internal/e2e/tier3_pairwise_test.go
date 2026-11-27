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

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
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

func TestTier3PairwiseCombinations(t *testing.T) {
	t.Parallel()

	t.Run("P01_GithubNested_REST_vs_ArrowStorage_SpillStore_Grid_JSON", func(t *testing.T) {
		t.Parallel()

		ds := buildGithubNestedSubsetDataset(t)
		fields := results.FieldsFromREST(ds.restFields)

		restRows, err := bq.DecodeRows(ds.restFields, ds.restRows)
		if err != nil {
			t.Fatal(err)
		}

		m := &results.MemoryStore{}
		gMem := results.NewGrid(fields, m)
		results.AppendRows(gMem, m, restRows)
		gMem.Finish(nil)

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
		defer sp.Close()

		gSpill := results.NewGrid(fields, sp)
		if err := results.AppendSpilled(gSpill, sp, ds.arrowBatch); err != nil {
			t.Fatal(err)
		}

		gSpill.Finish(nil)

		_, linesMem, _ := gMem.Size()
		_, linesSpill, _ := gSpill.Size()
		if linesMem != linesSpill {
			t.Fatalf("line count mismatch: mem=%d spill=%d", linesMem, linesSpill)
		}

		for line := range linesMem {
			for col := range len(gMem.Columns()) {
				cA, _, _ := gMem.Cell(line, col)
				cB, _, _ := gSpill.Cell(line, col)
				if !reflect.DeepEqual(cA, cB) {
					t.Fatalf("cell(%d,%d) mismatch: %+v vs %+v", line, col, cA, cB)
				}
			}
		}

		for r := range int64(2) {
			rowA, _ := gMem.Row(r)
			rowB, _ := gSpill.Row(r)
			jsA := string(results.AppendRowJSON(nil, fields, rowA))
			jsB := string(results.AppendRowJSON(nil, fields, rowB))
			if jsA != jsB {
				t.Fatalf("row %d JSON mismatch:\n%s\nvs\n%s", r, jsA, jsB)
			}
		}
	})

	t.Run("P02_GASessions222_SchemaTree_Leaves_Grid_JSON", func(t *testing.T) {
		t.Parallel()

		tbl := loadAppTableFixture(t, "table.bigquery-public-data.google_analytics_sample.ga_sessions_20170801.json")
		schemaTree := app.SchemaTree(tbl)
		if got := app.CountFields(schemaTree); got < 222 {
			t.Fatalf("CountFields = %d, want >= 222", got)
		}

		fields := results.FieldsFromREST(tbl.Schema.Fields)
		leaves := results.Leaves(fields)
		if len(leaves) < 150 {
			t.Fatalf("leaves = %d, want >= 150", len(leaves))
		}

		ds := buildGASessionsSubsetDataset(t)
		subFields := results.FieldsFromREST(ds.restFields)
		rows, err := bq.DecodeRows(ds.restFields, ds.restRows)
		if err != nil {
			t.Fatal(err)
		}

		js := string(results.AppendRowJSON(nil, subFields, rows[0]))
		if !strings.Contains(js, `"/checkout"`) || !strings.Contains(js, `"visits":"1"`) {
			t.Fatalf("ga_sessions JSON missing nested values: %s", js)
		}
	})

	t.Run("P03_DeepNested_EmptyArrays_NullStructs_SpillStore_CSVLines_NDJSON", func(t *testing.T) {
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
		defer sp.Close()

		g := results.NewGrid(fields, sp)
		if aerr := results.AppendSpilled(g, sp, ds.arrowBatch); aerr != nil {
			t.Fatal(aerr)
		}

		g.Finish(nil)

		outDir := t.TempDir()
		rn := app.NewRunner(app.RunnerOptions{Post: func(fn func()) { fn() }})

		csvPath := filepath.Join(outDir, "deep.csv")
		ch1 := make(chan app.ExportProgress, 1)
		rn.Export(app.ExportSource{Grid: g}, csvPath, export.CSV, export.Options{CSVNesting: export.CSVLines}, func(p app.ExportProgress) {
			if p.Done {
				ch1 <- p
			}
		})
		p1 := <-ch1
		if p1.Err != nil {
			t.Fatalf("CSV export: %v", p1.Err)
		}

		csvBytes, _ := os.ReadFile(csvPath)
		recs, err := csv.NewReader(bytes.NewReader(csvBytes)).ReadAll()
		if err != nil || len(recs) != 5 {
			t.Fatalf("CSV lines = %d, err = %v", len(recs), err)
		}

		ndPath := filepath.Join(outDir, "deep.jsonl")
		ch2 := make(chan app.ExportProgress, 1)
		rn.Export(app.ExportSource{Grid: g}, ndPath, export.NDJSON, export.Options{}, func(p app.ExportProgress) {
			if p.Done {
				ch2 <- p
			}
		})
		p2 := <-ch2
		if p2.Err != nil || p2.Rows != 2 {
			t.Fatalf("NDJSON export: %+v", p2)
		}
	})

	t.Run("P04_ZeroRowNested_Runner_Grid_JSON_CSVHeaderOnly", func(t *testing.T) {
		t.Parallel()

		ds := buildGithubNestedSubsetDataset(t)
		be := &mockQueryBackend{
			result: &bq.QueryResult{
				Statement: "SELECT",
				Schema:    &bigquery.TableSchema{Fields: ds.restFields},
				TotalRows: 0,
			},
		}

		rn := app.NewRunner(app.RunnerOptions{Backend: be, Post: func(fn func()) { fn() }, SpillDir: t.TempDir()})
		snap := waitRunDone(t, rn.Start(bq.QueryRequest{SQL: "SELECT * FROM t LIMIT 0"}))

		rows, lines, complete := snap.Grid.Size()
		if rows != 0 || lines != 0 || !complete {
			t.Fatalf("grid size = (%d, %d, %v)", rows, lines, complete)
		}

		csvPath := filepath.Join(t.TempDir(), "empty.csv")
		ch := make(chan app.ExportProgress, 1)
		rn.Export(app.ExportSource{Grid: snap.Grid}, csvPath, export.CSV, export.Options{CSVNesting: export.CSVLines}, func(p app.ExportProgress) {
			if p.Done {
				ch <- p
			}
		})
		p := <-ch
		if p.Err != nil {
			t.Fatal(p.Err)
		}

		b, _ := os.ReadFile(csvPath)
		recs, err := csv.NewReader(bytes.NewReader(b)).ReadAll()
		if err != nil || len(recs) != 1 || len(recs[0]) != len(snap.Grid.Columns()) {
			t.Fatalf("expected header-only CSV with %d cols, got %+v (err=%v)", len(snap.Grid.Columns()), recs, err)
		}
	})

	t.Run("P05_Runner_ArrowStorage_Parquet_ArrowIPC_RoundTrip", func(t *testing.T) {
		t.Parallel()

		ds := buildGithubNestedSubsetDataset(t)
		tbl := &bigquery.Table{Type: "TABLE", Schema: &bigquery.TableSchema{Fields: ds.restFields}}
		rn := app.NewRunner(app.RunnerOptions{
			Backend:  &app.FakeBackend{},
			Metadata: &mockMetadataBackend{table: tbl},
			Storage: func(context.Context, bq.TableRef) (app.StorageSession, error) {
				return &mockStorageSession{schema: ds.arrowSchema, batches: [][]byte{ds.arrowBatch}, estRows: 2}, nil
			},
			Post:     func(fn func()) { fn() },
			SpillDir: t.TempDir(),
		})

		dir := t.TempDir()
		ref := &bq.TableRef{Project: "p", Dataset: "d", Table: "t"}

		for _, fmtKind := range []export.Format{export.Arrow, export.Parquet} {
			outPath := filepath.Join(dir, "out."+fmtKind.Ext())
			ch := make(chan app.ExportProgress, 1)
			rn.Export(app.ExportSource{Table: ref}, outPath, fmtKind, export.Options{}, func(p app.ExportProgress) {
				if p.Done {
					ch <- p
				}
			})

			p := <-ch
			if p.Err != nil || p.Rows != 2 {
				t.Fatalf("%s export failed: %+v", fmtKind, p)
			}

			if fmtKind == export.Arrow {
				f, err := os.Open(outPath)
				if err != nil {
					t.Fatal(err)
				}

				fr, err := ipc.NewFileReader(f)
				if err != nil {
					_ = f.Close()
					t.Fatalf("ipc.NewFileReader: %v", err)
				}

				if fr.NumRecords() != 1 {
					t.Fatalf("Arrow NumRecords = %d, want 1", fr.NumRecords())
				}

				_ = fr.Close()
				_ = f.Close()
			} else {
				pr, err := file.OpenParquetFile(outPath, false)
				if err != nil {
					t.Fatalf("OpenParquetFile: %v", err)
				}

				rr, err := pqarrow.NewFileReader(pr, pqarrow.ArrowReadProperties{}, memory.NewGoAllocator())
				if err != nil {
					_ = pr.Close()
					t.Fatal(err)
				}

				tblOut, err := rr.ReadTable(t.Context())
				if err != nil || tblOut.NumRows() != 2 {
					t.Fatalf("Parquet ReadTable rows=%d err=%v", tblOut.NumRows(), err)
				}

				tblOut.Release()
				_ = pr.Close()
			}
		}
	})

	t.Run("P06_Inspector_LongKMS_URI_SingleLineDesc_SchemaTree_TableSections", func(t *testing.T) {
		t.Parallel()

		kms := "projects/acme-prod/locations/us/keyRings/bq/cryptoKeys/" + strings.Repeat("k", 80)
		desc := strings.Repeat("Nested analytics table description token ", 15)
		tbl := &bigquery.Table{
			TableReference:          &bigquery.TableReference{ProjectId: "acme-prod", DatasetId: "analytics", TableId: "events_nested"},
			Type:                    "TABLE",
			Description:             desc,
			EncryptionConfiguration: &bigquery.EncryptionConfiguration{KmsKeyName: kms},
			TimePartitioning:        &bigquery.TimePartitioning{Type: "DAY", Field: "event_ts"},
			Schema: &bigquery.TableSchema{Fields: []*bigquery.TableFieldSchema{
				{Name: "event_ts", Type: "TIMESTAMP", Mode: "REQUIRED"},
				{Name: "ctx", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{{Name: "url", Type: "STRING", Description: desc}}},
			}},
		}

		secs := (app.Formatter{Loc: time.UTC}).TableSections(tbl)
		tree := app.SchemaTree(tbl)
		if len(secs) < 3 || app.CountFields(tree) != 3 || !tree[0].Partition {
			t.Fatalf("secs=%d count=%d tree=%+v", len(secs), app.CountFields(tree), tree)
		}
	})

	t.Run("P07_LargeGrid_Over1024Rows_SpillStore_RandomAccess_ColumnPath_Tree", func(t *testing.T) {
		t.Parallel()

		arrowSchema := arrow.NewSchema([]arrow.Field{
			{Name: "id", Type: arrow.PrimitiveTypes.Int64},
			{Name: "tags", Type: arrow.ListOf(arrow.BinaryTypes.String)},
		}, nil)
		fields := []results.Field{
			{Name: "id", Type: results.TypeInt64},
			{Name: "tags", Type: results.TypeString, Repeated: true},
		}

		buildBatch := func(start, n int) []byte {
			mem := memory.NewGoAllocator()
			b := array.NewRecordBuilder(mem, arrowSchema)
			defer b.Release()

			lb := b.Field(1).(*array.ListBuilder)
			vb := lb.ValueBuilder().(*array.StringBuilder)

			for i := range n {
				b.Field(0).(*array.Int64Builder).Append(int64(start + i))
				lb.Append(true)
				vb.Append("x")
				vb.Append("y")
			}

			rec := b.NewRecordBatch()
			defer rec.Release()

			return serializeArrowRecord(t, rec)
		}

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
		defer sp.Close()

		g := results.NewGrid(fields, sp)
		for chunk := range 3 {
			if aerr := results.AppendSpilled(g, sp, buildBatch(chunk*400, 400)); aerr != nil {
				t.Fatal(aerr)
			}
		}

		g.Finish(nil)

		rows, lines, complete := g.Size()
		if rows != 1200 || lines != 2400 || !complete {
			t.Fatalf("size = (%d, %d, %v)", rows, lines, complete)
		}

		c, pos, err := g.Cell(2399, 1)
		if err != nil || c.V != "y" || pos.Row != 1199 {
			t.Fatalf("last cell = (%+v, %+v, %v)", c, pos, err)
		}

		row1199, err := g.Row(1199)
		if err != nil {
			t.Fatal(err)
		}

		if got := results.ColumnPath(fields, row1199, g.Columns()[1], pos.Sub); got != "tags[1]" {
			t.Fatalf("ColumnPath = %q, want tags[1]", got)
		}
	})

	t.Run("P08_Runner_History_LocalRuns_Filter_JobSections_PlanFromJob", func(t *testing.T) {
		t.Parallel()

		fb := &app.FakeBackend{}
		rn := app.NewRunner(app.RunnerOptions{Backend: fb, Post: func(fn func()) { fn() }, SpillDir: t.TempDir()})
		snap := waitRunDone(t, rn.Start(bq.QueryRequest{SQL: "SELECT * FROM events LIMIT 5"}))

		jobDetails, err := fb.GetJob(t.Context(), *snap.Job)
		if err != nil {
			t.Fatal(err)
		}

		p := plan.FromJob(jobDetails)
		secs := (app.Formatter{Loc: time.UTC}).JobSections(jobDetails)
		if p == nil || len(p.Stages) != 13 || len(secs) < 4 {
			t.Fatalf("plan=%v secs=%d", p, len(secs))
		}

		db, err := store.Open(filepath.Join(t.TempDir(), "bqui.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		_ = db.RecordRun(t.Context(), store.Run{
			Started: time.Now().UTC(),
			Project: app.FakeProject,
			JobID:   snap.Job.JobID,
			SQL:     snap.SQL,
			State:   "done",
		})

		runs, _ := db.SearchRuns(t.Context(), "", 10)
		filtered := app.FilterJobRows(app.JobRowsFromRuns(runs), snap.Job.JobID)
		if len(filtered) != 1 {
			t.Fatalf("filtered local runs = %d, want 1", len(filtered))
		}
	})

	t.Run("P09_Theme_VSCodeImport_Fonts_JSONSpans_ResultsFormatCell", func(t *testing.T) {
		t.Parallel()

		vsc := []byte(`{"name":"Pairwise Dark","type":"dark","colors":{"editor.background":"#111111"},"tokenColors":[{"scope":"variable.other.property","settings":{"foreground":"#8be9fd"}},{"scope":"string","settings":{"foreground":"#f1fa8c"}}]}`)
		th, err := theme.ImportVSCode(vsc)
		if err != nil {
			t.Fatal(err)
		}

		pal := th.Palette(true)
		if pal.JSON.Key != "#8be9fd" || pal.JSON.String != "#f1fa8c" {
			t.Fatalf("JSON palette = %+v", pal.JSON)
		}

		fields := []results.Field{{Name: "msg", Type: results.TypeString}}
		js := string(results.AppendRowJSON(nil, fields, []any{"hello\tworld"}))
		spans := results.JSONSpans(js)
		if !slices.ContainsFunc(spans, func(s results.JSONSpan) bool { return s.Kind == results.JSONKey }) {
			t.Fatal("missing JSONKey span")
		}

		if fc := results.FormatCell(results.TypeString, "hello\tworld"); fc != "hello⇥world" {
			t.Fatalf("FormatCell = %q", fc)
		}
	})

	t.Run("P10_Prefs_BytesGuard_Runner_ExplainErrorBanner", func(t *testing.T) {
		t.Parallel()

		prefs, err := (store.PreferenceText{MaxBytesBilled: "1 MB"}).Parse()
		if err != nil {
			t.Fatal(err)
		}

		fb := &app.FakeBackend{}
		rn := app.NewRunner(app.RunnerOptions{Backend: fb, Post: func(fn func()) { fn() }, SpillDir: t.TempDir()})
		snap := waitRunDone(t, rn.Start(bq.QueryRequest{SQL: "SELECT 1", MaxBytesBilled: prefs.MaxBytesBilled}))

		if snap.State != app.RunFailed {
			t.Fatalf("expected bytes guard failure, got %v", snap.State)
		}

		prob := app.Explain(snap.Err)
		if prob.Title != "Query failed" || !strings.Contains(prob.Detail, "exceeded limit for bytes billed") {
			t.Fatalf("unexpected Explain output: %+v", prob)
		}
	})

	t.Run("P11_SmokeArtifacts_Report_AXTree_PerPanePNG_CleanLog_FullValidation", func(t *testing.T) {
		t.Parallel()

		dir := goldenSmokeDir(t, true)

		if _, err := validateSmokeArtifacts(dir, smokeValidationOpts{requireLayoutChecks: true, requirePerPanePNGs: true}); err != nil {
			t.Fatalf("validateSmokeArtifacts: %v", err)
		}

		if err := validateSmokeLog([]byte("smoke complete\n")); err != nil {
			t.Fatalf("validateSmokeLog: %v", err)
		}
	})

	t.Run("P12_SmokeArtifacts_Narrow900x600_Inspector440_300_TableRenderedCell_Validation", func(t *testing.T) {
		t.Parallel()

		dir := goldenSmokeDir(t, true)

		outRep, err := validateSmokeArtifacts(dir, smokeValidationOpts{requireLayoutChecks: true, requirePerPanePNGs: true})
		if err != nil {
			t.Fatalf("validateSmokeArtifacts: %v", err)
		}

		if len(outRep.Screenshots) != 7 {
			t.Fatalf("screenshots = %d, want 7", len(outRep.Screenshots))
		}
	})

	t.Run("P13_bquictl_CLI_Subcommand_Help_And_bqui_Stub_And_Bundle_Pipeline", func(t *testing.T) {
		t.Parallel()

		bquictl := buildBinary(t, "./cmd/bquictl", "bquictl", "", "")
		bquiLinux := buildBinary(t, "./cmd/bqui", "bqui", "linux", "amd64")
		bundle := buildBinary(t, "./tools/bundle", "bundle", "", "")

		_, _, rc1 := runSubprocess(t, "", bquictl, nil, "-version")
		_, _, rc2 := runSubprocess(t, "", bquiLinux, nil)

		appDir := filepath.Join(t.TempDir(), "bqui.app")
		_, _, rc3 := runSubprocess(t, "", bundle, nil, "-bin", bquiLinux, "-o", appDir, "-notices", "")

		if rc1 != 0 || rc2 != 2 || rc3 != 0 {
			t.Fatalf("exit codes = (%d, %d, %d), want (0, 2, 0)", rc1, rc2, rc3)
		}
	})

	t.Run("P14_CrossFormat_Unicode_ControlChars_gt4KB_JSON_CSVJSONCells_NDJSON", func(t *testing.T) {
		t.Parallel()

		longText := strings.Repeat("αβγ🚀\n\t\"comma,quote\"", 180) // > 4KB UTF-8 + special chars
		fields := []results.Field{
			{Name: "id", Type: results.TypeInt64},
			{Name: "meta", Type: results.TypeStruct, Fields: []results.Field{
				{Name: "note", Type: results.TypeString},
			}},
		}
		rows := [][]any{{int64(1), []any{longText}}}

		cell := results.FormatCell(results.TypeString, longText)
		if !utf8.ValidString(cell) || utf8.RuneCountInString(cell) != results.MaxCellRunes+1 {
			t.Fatalf("FormatCell rune count = %d", utf8.RuneCountInString(cell))
		}

		var csvBuf, ndBuf bytes.Buffer
		cw, err := export.NewRowWriter(&csvBuf, export.CSV, fields, export.Options{CSVNesting: export.CSVJSONCells})
		if err != nil {
			t.Fatalf("NewRowWriter CSV: %v", err)
		}

		if err = cw.WriteRows(rows); err != nil {
			t.Fatalf("CSV WriteRows: %v", err)
		}

		if err = cw.Close(); err != nil {
			t.Fatalf("CSV Close: %v", err)
		}

		nw, err := export.NewRowWriter(&ndBuf, export.NDJSON, fields, export.Options{})
		if err != nil {
			t.Fatalf("NewRowWriter NDJSON: %v", err)
		}

		if err = nw.WriteRows(rows); err != nil {
			t.Fatalf("NDJSON WriteRows: %v", err)
		}

		if err = nw.Close(); err != nil {
			t.Fatalf("NDJSON Close: %v", err)
		}

		ndLine := strings.TrimSpace(ndBuf.String())
		if len(ndLine) <= 4096 || !json.Valid([]byte(ndLine)) {
			t.Fatalf("NDJSON line len=%d valid=%v", len(ndLine), json.Valid([]byte(ndLine)))
		}

		recs, err := csv.NewReader(&csvBuf).ReadAll()
		if err != nil || len(recs) != 2 || !json.Valid([]byte(recs[1][1])) {
			t.Fatalf("CSVJSONCells invalid: len=%d err=%v", len(recs), err)
		}
	})
}
