package app

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"sync"
	"testing"
	"time"

	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/bqstorage"
	"github.com/paulourio/bqui/internal/results"
)

type recorder struct {
	mu      sync.Mutex
	updates int
	states  []RunState
}

func (r *recorder) onUpdate(run *Run) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.updates++

	s := run.Snapshot().State
	if len(r.states) == 0 || r.states[len(r.states)-1] != s {
		r.states = append(r.states, s)
	}
}

func newTestRunner(t *testing.T) (*Runner, *recorder) {
	t.Helper()

	rec := &recorder{}
	rn := NewRunner(RunnerOptions{
		Backend:  &FakeBackend{},
		Metadata: &FakeBackend{},
		Post:     func(fn func()) { fn() },
		OnUpdate: rec.onUpdate,
		SpillDir: t.TempDir(),
	})

	return rn, rec
}

func wait(t *testing.T, r *Run) RunSnapshot {
	t.Helper()

	select {
	case <-r.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("run did not finish")
	}

	return r.Snapshot()
}

func TestRunnerInlineNestedResult(t *testing.T) {
	rn, rec := newTestRunner(t)
	r := rn.Start(bq.QueryRequest{SQL: "SELECT * FROM orders LIMIT 25"})
	s := wait(t, r)

	if s.State != RunDone || s.Source != "inline" || s.Grid == nil || s.Job == nil || s.StagesDone != fakeStages {
		t.Fatalf("snapshot = %+v", s)
	}

	rows, lines, complete := s.Grid.Size()
	if rows != 25 || lines <= rows || !complete {
		t.Fatalf("grid = %d rows %d lines complete=%v", rows, lines, complete)
	}

	// Row 3 (order_id 4) has 3 items; its first item has tags tag0..tag(n).
	cols := s.Grid.Columns()
	if cols[3].Name != "customer.tier" || cols[4].Name != "items.sku" || cols[7].Name != "items.tags" {
		t.Fatalf("columns = %+v", cols)
	}

	first, _ := s.Grid.FirstLine(3)
	c, p, err := s.Grid.Cell(first, 0)

	if err != nil || c.V != int64(4) || p.Lines < 3 {
		t.Fatalf("row 3 = %+v %+v %v", c, p, err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()

	if rec.states[0] != RunRunning || rec.states[len(rec.states)-1] != RunDone {
		t.Errorf("state sequence = %v", rec.states)
	}
}

func TestRunnerPagesOverRESTWithoutStorage(t *testing.T) {
	rn, _ := newTestRunner(t)
	s := wait(t, rn.Start(bq.QueryRequest{SQL: "SELECT * FROM orders LIMIT 25000"}))

	if s.State != RunDone || s.Source != "rest" {
		t.Fatalf("snapshot = %+v", s)
	}

	rows, _, _ := s.Grid.Size()
	if rows != 25000 {
		t.Fatalf("rows = %d", rows)
	}
}

func TestRunnerErrorsCancelAndDDL(t *testing.T) {
	rn, _ := newTestRunner(t)

	s := wait(t, rn.Start(bq.QueryRequest{SQL: "SELEC broken"}))
	if s.State != RunFailed || !errors.Is(s.Err, bq.ErrQueryFailed) {
		t.Errorf("syntax error run = %+v", s)
	}

	r := rn.Start(bq.QueryRequest{SQL: "SELECT 1"})
	time.Sleep(50 * time.Millisecond)
	r.Cancel()

	if cs := wait(t, r); cs.State != RunCancelled {
		t.Errorf("cancelled run = %v (%v)", cs.State, cs.Err)
	}

	s = wait(t, rn.Start(bq.QueryRequest{SQL: "CREATE TABLE d.t (x INT64)"}))
	if s.State != RunDone || s.Grid != nil || s.Result.Statement != "CREATE" {
		t.Errorf("DDL run = %+v", s)
	}
}

// fakeStorage serves the fake orders rows as JSON blobs (instead of Arrow) to
// exercise the spill path without a gRPC server.
type fakeStorage struct {
	rows    int
	perBlob int
}

func (f fakeStorage) RawBatches(ctx context.Context) iter.Seq2[bqstorage.RawBatch, error] {
	return func(yield func(bqstorage.RawBatch, error) bool) {
		for start := 0; start < f.rows; start += f.perBlob {
			n := min(f.perBlob, f.rows-start)

			data, err := json.Marshal(fakeOrderRows(start, n))
			if err != nil || ctx.Err() != nil {
				yield(bqstorage.RawBatch{}, errors.Join(err, ctx.Err()))

				return
			}

			if !yield(bqstorage.RawBatch{Rows: int64(n), Data: data}, nil) {
				return
			}
		}
	}
}

func (f fakeStorage) DecodeRows(_ []results.Field, raw []byte) ([][]any, error) {
	var rows []*bigquery.TableRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}

	return bq.DecodeRows(FakeOrdersSchema().Fields, rows)
}

type destBackend struct{ FakeBackend }

// Query reports a destination table so the runner takes the storage path.
func (d *destBackend) Query(ctx context.Context, req bq.QueryRequest) (*bq.QueryResult, error) {
	res, err := d.FakeBackend.Query(ctx, req)
	if res != nil {
		res.Destination = &bq.TableRef{Project: FakeProject, Dataset: "_anon", Table: "anon1"}
	}

	return res, err
}

func TestRunnerStreamsLargeResultsThroughSpill(t *testing.T) {
	opener := func(context.Context, bq.TableRef) (StorageSession, error) {
		return fakeStorage{rows: 50_000, perBlob: 4096}, nil
	}

	rec := &recorder{}
	rn := NewRunner(RunnerOptions{
		Backend: &destBackend{}, Metadata: &FakeBackend{}, Storage: opener,
		Post: func(fn func()) { fn() }, OnUpdate: rec.onUpdate, SpillDir: t.TempDir(),
	})

	r := rn.Start(bq.QueryRequest{SQL: "SELECT * FROM orders LIMIT 50000"})
	s := wait(t, r)

	if s.State != RunDone || s.Source != "storage" {
		t.Fatalf("snapshot = %+v", s)
	}

	rows, lines, _ := s.Grid.Size()
	if rows != 50_000 || lines < rows {
		t.Fatalf("grid = %d rows %d lines", rows, lines)
	}

	// Random access deep into the spill file.
	if c, p, err := s.Grid.Cell(lines-1, 0); err != nil || p.Row != rows-1 || (c.Kind != results.Value && c.Kind != results.Blank) {
		t.Fatalf("last line = %+v %+v %v", c, p, err)
	}

	r.Close()

	rec.mu.Lock()
	updates := rec.updates
	rec.mu.Unlock()

	if updates > 200 {
		t.Errorf("UI updates not throttled: %d", updates)
	}
}

func TestRunnerPreviewFallsBackToQueryWithoutStorage(t *testing.T) {
	rn, _ := newTestRunner(t)
	s := wait(t, rn.Preview(bq.TableRef{Project: FakeProject, Dataset: "analytics", Table: "events"}))

	if s.State != RunDone || s.Kind != "preview" || s.Grid == nil {
		t.Fatalf("preview = %+v", s)
	}

	if rows, _, _ := s.Grid.Size(); rows != previewRows {
		t.Errorf("preview rows = %d", rows)
	}
}
