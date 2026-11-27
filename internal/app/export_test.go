package app

import (
	"context"
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/export"
)

func runExport(t *testing.T, rn *Runner, src ExportSource, path string, f export.Format) ExportProgress {
	t.Helper()

	done := make(chan ExportProgress, 1)
	rn.Export(src, path, f, export.Options{}, func(p ExportProgress) {
		if p.Done {
			done <- p
		}
	})

	select {
	case p := <-done:
		return p
	case <-time.After(20 * time.Second):
		t.Fatal("export did not finish")
	}

	return ExportProgress{}
}

func TestExportFromGrid(t *testing.T) {
	rn, _ := newTestRunner(t)
	s := wait(t, rn.Start(bq.QueryRequest{SQL: "SELECT * FROM orders LIMIT 300"}))
	dir := t.TempDir()

	p := runExport(t, rn, ExportSource{Grid: s.Grid}, filepath.Join(dir, "out.csv"), export.CSV)
	if p.Err != nil || p.Rows != 300 || p.Bytes == 0 {
		t.Fatalf("csv export = %+v", p)
	}

	f, _ := os.Open(filepath.Join(dir, "out.csv"))
	defer f.Close()

	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}

	_, lines, _ := s.Grid.Size()
	if int64(len(recs)) != lines+1 || recs[0][3] != "customer.tier" {
		t.Fatalf("csv has %d records for %d lines; header %q", len(recs), lines, recs[0])
	}

	p = runExport(t, rn, ExportSource{Grid: s.Grid}, filepath.Join(dir, "out.ndjson"), export.NDJSON)
	if p.Err != nil {
		t.Fatal(p.Err)
	}

	b, _ := os.ReadFile(filepath.Join(dir, "out.ndjson"))
	if n := strings.Count(string(b), "\n"); n != 300 {
		t.Errorf("ndjson lines = %d", n)
	}

	// Columnar formats need storage; nothing is left behind on failure.
	p = runExport(t, rn, ExportSource{Grid: s.Grid}, filepath.Join(dir, "out.parquet"), export.Parquet)
	if !errors.Is(p.Err, export.ErrColumnar) {
		t.Errorf("parquet from grid: %v", p.Err)
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), partialSuffix) || e.Name() == "out.parquet" {
			t.Errorf("left behind %s", e.Name())
		}
	}
}

func TestExportFromStorageAndCancel(t *testing.T) {
	opener := func(context.Context, bq.TableRef) (StorageSession, error) {
		return fakeStorage{rows: 20_000, perBlob: 1000}, nil
	}

	rn := NewRunner(RunnerOptions{
		Backend: &FakeBackend{}, Metadata: storageMeta{&FakeBackend{}}, Storage: opener,
		Post: func(fn func()) { fn() }, SpillDir: t.TempDir(),
	})
	dir := t.TempDir()
	ref := &bq.TableRef{Project: FakeProject, Dataset: "analytics", Table: "events"}

	p := runExport(t, rn, ExportSource{Table: ref}, filepath.Join(dir, "events.ndjson"), export.NDJSON)
	if p.Err != nil || p.Rows != 20_000 || p.EstRows != 20_000 {
		t.Fatalf("storage export = %+v", p)
	}

	// Cancel immediately: no file, no partial.
	done := make(chan ExportProgress, 1)
	cancel := rn.Export(ExportSource{Table: ref}, filepath.Join(dir, "c.csv"), export.CSV, export.Options{}, func(p ExportProgress) {
		if p.Done {
			done <- p
		}
	})
	cancel()

	if p := <-done; !p.Cancelled {
		t.Errorf("cancelled export = %+v", p)
	}

	if _, err := os.Stat(filepath.Join(dir, "c.csv")); !os.IsNotExist(err) {
		t.Error("cancelled export left a file")
	}
}

// storageMeta serves the fake orders schema for any table.
type storageMeta struct{ *FakeBackend }

func (storageMeta) GetTable(context.Context, bq.TableRef) (*bigquery.Table, error) {
	return &bigquery.Table{Type: "TABLE", Schema: FakeOrdersSchema()}, nil
}
