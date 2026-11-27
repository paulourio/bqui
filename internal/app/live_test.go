//go:build live

// Live tests talk to BigQuery. Run with:
//
//	BQUI_LIVE_PROJECT=my-project go test -tags live -run Live ./internal/app/
package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"google.golang.org/api/option"

	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/bqstorage"
	"github.com/paulourio/bqui/internal/export"
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

	for _, f := range []export.Format{export.Parquet, export.Arrow, export.CSV} {
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
