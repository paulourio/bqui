//go:build live

// Live tests talk to BigQuery. Run with:
//
//	BQUI_LIVE_PROJECT=my-project go test -tags live -run Live ./internal/app/
package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"google.golang.org/api/option"

	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/bqstorage"
	"github.com/paulourio/bqui/internal/export"
	"github.com/paulourio/bqui/internal/results"
)

// liveAllTypes covers every type, NULLs, empty arrays and nesting.
const liveAllTypes = `
SELECT * FROM UNNEST([
  STRUCT(1 AS i, 1.5 AS f, TRUE AS b, "s\nx" AS s, b"\x01\x02" AS bts, NUMERIC "12.5" AS n,
         BIGNUMERIC "1.25" AS bn, TIMESTAMP "2024-01-02 03:04:05.123456 UTC" AS ts, DATE "2024-01-02" AS d,
         TIME "03:04:05.5" AS t, DATETIME "2024-01-02 03:04:05" AS dt, ST_GEOGPOINT(1, 2) AS g,
         JSON '{"a":[1,2]}' AS j, INTERVAL -14 MONTH AS iv, RANGE(DATE "2024-01-01", NULL) AS r,
         RANGE(TIMESTAMP "2024-01-01 00:00:00 UTC", TIMESTAMP "2024-01-02 00:00:00.5 UTC") AS rts,
         [1, 2] AS arr, STRUCT(1 AS x, [STRUCT("a" AS y), STRUCT(NULL)] AS zs) AS st),
  STRUCT(NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
         [], NULL)
])`

func TestLiveRESTAndStorageAgree(t *testing.T) {
	project := os.Getenv("BQUI_LIVE_PROJECT")
	if project == "" {
		t.Skip("BQUI_LIVE_PROJECT not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client, err := bq.New(ctx, bq.Options{Project: project})
	if err != nil {
		t.Fatal(err)
	}

	sc, err := bqstorage.New(ctx, option.WithTokenSource(client.Tokens()))
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()

	opener := NewStorageOpener(sc, func() string { return project })

	run := func(inline int64, storage StorageOpener) RunSnapshot {
		rn := NewRunner(RunnerOptions{Backend: client, Storage: storage, Post: func(fn func()) { fn() }, SpillDir: t.TempDir()})
		r := rn.Start(bq.QueryRequest{SQL: liveAllTypes, RequireJob: true, MaxInlineRows: inline})

		select {
		case <-r.Done():
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}

		s := r.Snapshot()
		if s.State != RunDone {
			t.Fatalf("run %v: %v", s.State, s.Err)
		}

		return s
	}

	restRun := run(1000, nil)
	storageRun := run(1, opener) // first page smaller than the result: forces the storage path

	if restRun.Source != "inline" || storageRun.Source != "storage" {
		t.Fatalf("sources = %s / %s", restRun.Source, storageRun.Source)
	}

	for i := range int64(2) {
		a, err := restRun.Grid.Row(i)
		if err != nil {
			t.Fatal(err)
		}

		b, err := storageRun.Grid.Row(i)
		if err != nil {
			t.Fatal(err)
		}

		for c := range a {
			if !reflect.DeepEqual(a[c], b[c]) {
				t.Errorf("row %d field %s: REST %#v vs storage %#v", i, restRun.Grid.Fields()[c].Name, a[c], b[c])
			}
		}
	}
}

func TestLiveExportParquetAndArrow(t *testing.T) {
	project := os.Getenv("BQUI_LIVE_PROJECT")
	if project == "" {
		t.Skip("BQUI_LIVE_PROJECT not set")
	}

	ctx := context.Background()

	client, err := bq.New(ctx, bq.Options{Project: project})
	if err != nil {
		t.Fatal(err)
	}

	sc, err := bqstorage.New(ctx, option.WithTokenSource(client.Tokens()))
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()

	rn := NewRunner(RunnerOptions{
		Backend: client, Metadata: client, Storage: NewStorageOpener(sc, func() string { return project }),
		Post: func(fn func()) { fn() }, SpillDir: t.TempDir(),
	})

	ref := &bq.TableRef{Project: "bigquery-public-data", Dataset: "samples", Table: "shakespeare"}
	dir := t.TempDir()

	for _, f := range []export.Format{export.Parquet, export.CSV} {
		path := filepath.Join(dir, "shakespeare."+f.Ext())

		p := runExport(t, rn, ExportSource{Table: ref}, path, f)
		if p.Err != nil || p.Rows != 164656 {
			t.Fatalf("%s export: %+v", f, p)
		}

		info, err := os.Stat(path)
		if err != nil || info.Size() == 0 {
			t.Fatalf("%s file: %v", f, err)
		}

		t.Logf("%s: %d rows, %d bytes", f, p.Rows, info.Size())
	}
}

const liveComplexNestedSQL = `
SELECT item.id,
       item.unnamed_st,
       item.dup_st,
       item.null_st,
       item.empty_arr,
       item.items,
       item.num_val,
       item.bignum_val,
       item.json_val,
       item.date_rng
  FROM UNNEST([
         STRUCT(
           1 AS id,
           STRUCT(1 AS a, 'unnamed') AS unnamed_st,
           STRUCT(1 AS a, 2 AS a) AS dup_st,
           CAST(NULL AS STRUCT<x INT64, y ARRAY<STRING>>) AS null_st,
           ARRAY<INT64>[] AS empty_arr,
           [
             STRUCT(1 AS item_id, ['alpha', 'beta'] AS tags),
             STRUCT(2 AS item_id, ARRAY<STRING>[] AS tags)
           ] AS items,
           NUMERIC '12.34' AS num_val,
           BIGNUMERIC '98765432109876543210.12345' AS bignum_val,
           JSON '{"k":[1,true,"v"]}' AS json_val,
           RANGE(DATE '2025-01-01', DATE '2025-02-01') AS date_rng
         ),
         STRUCT(
           2 AS id,
           STRUCT(2 AS a, 'second') AS unnamed_st,
           STRUCT(3 AS a, 4 AS a) AS dup_st,
           STRUCT(99 AS x, ['z'] AS y) AS null_st,
           [10, 20] AS empty_arr,
           ARRAY<STRUCT<item_id INT64, tags ARRAY<STRING>>>[] AS items,
           NUMERIC '-0.5' AS num_val,
           BIGNUMERIC '0' AS bignum_val,
           JSON 'null' AS json_val,
           RANGE(DATE '2025-03-01', NULL) AS date_rng
         )
       ]) AS item`

func TestLiveNestedPublicDatasets(t *testing.T) {
	project := os.Getenv("BQUI_LIVE_PROJECT")
	if project == "" {
		t.Skip("BQUI_LIVE_PROJECT not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client, err := bq.New(ctx, bq.Options{Project: project})
	if err != nil {
		t.Fatal(err)
	}

	sc, err := bqstorage.New(ctx, option.WithTokenSource(client.Tokens()))
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()

	opener := NewStorageOpener(sc, func() string { return project })

	runQuery := func(sql string, inline int64, storage StorageOpener) RunSnapshot {
		rn := NewRunner(RunnerOptions{
			Backend: client, Metadata: client, Storage: storage,
			Post: func(fn func()) { fn() }, SpillDir: t.TempDir(),
		})
		r := rn.Start(bq.QueryRequest{
			SQL: sql, RequireJob: true, MaxInlineRows: inline, MaxBytesBilled: 10 << 30,
		})

		select {
		case <-r.Done():
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}

		s := r.Snapshot()
		if s.State != RunDone || s.Grid == nil {
			t.Fatalf("run %v: %v", s.State, s.Err)
		}

		t.Cleanup(r.Close)

		return s
	}

	assertParity := func(name string, a, b RunSnapshot) {
		t.Helper()

		ra, la, _ := a.Grid.Size()
		rb, lb, _ := b.Grid.Size()
		if ra != rb || la != lb {
			t.Fatalf("%s size mismatch: (%d, %d) vs (%d, %d)", name, ra, la, rb, lb)
		}

		if !reflect.DeepEqual(a.Grid.Columns(), b.Grid.Columns()) {
			t.Fatalf("%s columns mismatch", name)
		}

		for i := range ra {
			raRow, err := a.Grid.Row(i)
			if err != nil {
				t.Fatal(err)
			}

			rbRow, err := b.Grid.Row(i)
			if err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(raRow, rbRow) {
				t.Fatalf("%s row %d mismatch:\n a: %#v\n b: %#v", name, i, raRow, rbRow)
			}

			raw := results.AppendRowJSON(nil, a.Grid.Fields(), raRow)
			if !json.Valid(raw) {
				t.Fatalf("%s row %d invalid JSON: %s", name, i, raw)
			}
		}

		if la > 0 && len(a.Grid.Columns()) > 0 {
			if _, _, err := a.Grid.Cell(0, 0); err != nil {
				t.Fatalf("%s Grid.Cell(0, 0): %v", name, err)
			}
		}
	}

	t.Run("complex_nested_sql", func(t *testing.T) {
		inline := runQuery(liveComplexNestedSQL, 100, nil)
		rest := runQuery(liveComplexNestedSQL, 1, nil)
		storage := runQuery(liveComplexNestedSQL, 1, opener)

		if inline.Source != "inline" || rest.Source != "rest" || storage.Source != "storage" {
			t.Fatalf("unexpected sources: inline=%s rest=%s storage=%s", inline.Source, rest.Source, storage.Source)
		}

		assertParity("complex_inline_vs_rest", inline, rest)
		assertParity("complex_inline_vs_storage", inline, storage)
	})

	t.Run("github_nested", func(t *testing.T) {
		//nolint:unqueryvet // verifies full nested schema parity across all 197 leaf columns
		const sql = "SELECT * FROM `bigquery-public-data.samples.github_nested` WHERE url IS NOT NULL ORDER BY url, created_at LIMIT 10"
		inline := runQuery(sql, 100, nil)
		storage := runQuery(sql, 1, opener)

		if inline.Source != "inline" || storage.Source != "storage" {
			t.Fatalf("sources = %s / %s", inline.Source, storage.Source)
		}

		assertParity("github_nested", inline, storage)

		rn := NewRunner(RunnerOptions{
			Backend: client, Metadata: client, Storage: opener,
			Post: func(fn func()) { fn() }, SpillDir: t.TempDir(),
		})
		pr := rn.Preview(bq.TableRef{Project: "bigquery-public-data", Dataset: "samples", Table: "github_nested"})
		t.Cleanup(pr.Close)

		select {
		case <-pr.Done():
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}

		ps := pr.Snapshot()
		if ps.State != RunDone || ps.Source != "storage" || ps.Grid == nil {
			t.Fatalf("github_nested preview = %+v", ps)
		}

		if rows, _, _ := ps.Grid.Size(); rows == 0 {
			t.Fatal("github_nested preview returned 0 rows")
		}
	})

	t.Run("ga_sessions_20170801", func(t *testing.T) {
		//nolint:unqueryvet // verifies full nested schema parity across all 306 leaf columns
		const sql = "SELECT * FROM `bigquery-public-data.google_analytics_sample.ga_sessions_20170801` ORDER BY fullVisitorId, visitId LIMIT 10"
		inline := runQuery(sql, 100, nil)
		rest := runQuery(sql, 2, nil)
		storage := runQuery(sql, 1, opener)

		if inline.Source != "inline" || rest.Source != "rest" || storage.Source != "storage" {
			t.Fatalf("sources = %s / %s / %s", inline.Source, rest.Source, storage.Source)
		}

		assertParity("ga_sessions_inline_vs_rest", inline, rest)
		assertParity("ga_sessions_inline_vs_storage", inline, storage)
	})
}
