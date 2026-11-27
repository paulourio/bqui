package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
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

	if s.State != RunDone || s.Source != "inline" || s.Grid == nil || s.Job == nil || s.StagesDone != s.Stages || s.Stages != 13 || s.JobDetails == nil {
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

func (f fakeStorage) DecodeRecord([]byte) (arrow.RecordBatch, error) {
	return nil, errors.New("fake storage has no Arrow records")
}

func (f fakeStorage) Schema() *arrow.Schema { return nil }
func (f fakeStorage) EstimatedRows() int64  { return int64(f.rows) }
func (f fakeStorage) EstimatedBytes() int64 { return int64(f.rows) * 100 }

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

func TestPreviewQueryTimesOutAndCancels(t *testing.T) {
	be := &FakeBackend{}
	rn := NewRunner(RunnerOptions{Backend: be, Post: func(fn func()) { fn() }, SpillDir: t.TempDir()})
	ref := bq.TableRef{Project: FakeProject, Dataset: "analytics", Table: "orders_view"}

	// The fake takes fakeRunTime to run; a shorter cap must time out.
	s := wait(t, rn.PreviewQuery(ref, FakeProject, 20*time.Millisecond))
	if s.State != RunFailed || !errors.Is(s.Err, ErrPreviewTimeout) {
		t.Fatalf("timed-out preview = %v %v", s.State, s.Err)
	}

	if p := Explain(s.Err); !p.Warning || !strings.Contains(p.Title, "10 seconds") {
		t.Errorf("explained = %+v", p)
	}

	s = wait(t, rn.PreviewQuery(ref, FakeProject, PreviewQueryTimeout))
	if s.State != RunDone || s.Grid == nil {
		t.Fatalf("preview = %v %v", s.State, s.Err)
	}

	if rows, _, _ := s.Grid.Size(); rows == 0 || rows > PreviewQueryRows {
		t.Errorf("preview rows = %d", rows)
	}
}

type arrowStorage struct {
	mem     memory.Allocator
	schema  *arrow.Schema
	batches [][]byte
	rows    int64
}

func (a *arrowStorage) RawBatches(ctx context.Context) iter.Seq2[bqstorage.RawBatch, error] {
	return func(yield func(bqstorage.RawBatch, error) bool) {
		for _, b := range a.batches {
			if ctx.Err() != nil {
				yield(bqstorage.RawBatch{}, ctx.Err())

				return
			}

			if !yield(bqstorage.RawBatch{Rows: a.rows, Data: b}, nil) {
				return
			}
		}
	}
}

func (a *arrowStorage) DecodeRecord(raw []byte) (arrow.RecordBatch, error) {
	rdr, err := ipc.NewReader(bytes.NewReader(raw), ipc.WithAllocator(a.mem))
	if err != nil {
		return nil, err
	}
	defer rdr.Release()

	if !rdr.Next() {
		if err := rdr.Err(); err != nil {
			return nil, err
		}

		return nil, errors.New("empty arrow ipc stream")
	}

	rec := rdr.RecordBatch()
	rec.Retain()

	return rec, nil
}

func (a *arrowStorage) DecodeRows(fields []results.Field, raw []byte) ([][]any, error) {
	rec, err := a.DecodeRecord(raw)
	if err != nil {
		return nil, err
	}
	defer rec.Release()

	return results.RowsFromRecord(fields, rec)
}

func (a *arrowStorage) Schema() *arrow.Schema { return a.schema }
func (a *arrowStorage) EstimatedRows() int64  { return a.rows }
func (a *arrowStorage) EstimatedBytes() int64 { return a.rows * 128 }

type arrowNestedBackend struct {
	FakeBackend
	schema *bigquery.TableSchema
}

func (b *arrowNestedBackend) Query(ctx context.Context, req bq.QueryRequest) (*bq.QueryResult, error) {
	res, err := b.FakeBackend.Query(ctx, req)
	if err != nil {
		return nil, err
	}

	res.Schema = b.schema
	res.Rows = nil
	res.TotalRows = 3
	res.Destination = &bq.TableRef{Project: FakeProject, Dataset: "_anon", Table: "nested_arrow"}

	return res, nil
}

func TestRunnerArrowStorageNestedPipeline(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.NewGoAllocator())
	defer mem.AssertSize(t, 0)

	tagList := arrow.ListOf(arrow.BinaryTypes.String)
	itemType := arrow.StructOf(
		arrow.Field{Name: "sku", Type: arrow.BinaryTypes.String, Nullable: true},
		arrow.Field{Name: "price", Type: &arrow.Decimal128Type{Precision: 38, Scale: 2}, Nullable: true},
		arrow.Field{Name: "tags", Type: tagList},
	)
	arrowSchema := arrow.NewSchema([]arrow.Field{
		{Name: "id", Type: arrow.PrimitiveTypes.Int64, Nullable: false},
		{Name: "meta", Type: arrow.StructOf(
			arrow.Field{Name: "owner", Type: arrow.BinaryTypes.String, Nullable: true},
			arrow.Field{Name: "t", Type: arrow.FixedWidthTypes.Time64us, Nullable: true},
		), Nullable: true},
		{Name: "items", Type: arrow.ListOf(itemType)},
	}, nil)

	bqSchema := &bigquery.TableSchema{Fields: []*bigquery.TableFieldSchema{
		{Name: "id", Type: "INTEGER", Mode: "REQUIRED"},
		{Name: "meta", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{
			{Name: "owner", Type: "STRING"},
			{Name: "t", Type: "TIME"},
		}},
		{Name: "items", Type: "RECORD", Mode: "REPEATED", Fields: []*bigquery.TableFieldSchema{
			{Name: "sku", Type: "STRING"},
			{Name: "price", Type: "NUMERIC"},
			{Name: "tags", Type: "STRING", Mode: "REPEATED"},
		}},
	}}

	b := array.NewRecordBuilder(mem, arrowSchema)
	defer b.Release()

	// Row 0: nested struct + 2 items (first item has 2 tags).
	b.Field(0).(*array.Int64Builder).Append(10)
	mb := b.Field(1).(*array.StructBuilder)
	mb.Append(true)
	mb.FieldBuilder(0).(*array.StringBuilder).Append("ana")
	mb.FieldBuilder(1).(*array.Time64Builder).Append(arrow.Time64((9*3600 + 15*60) * 1_000_000))

	ilb := b.Field(2).(*array.ListBuilder)
	ilb.Append(true)
	isb := ilb.ValueBuilder().(*array.StructBuilder)
	p1, _ := decimal128.FromString("19.95", 38, 2)
	isb.Append(true)
	isb.FieldBuilder(0).(*array.StringBuilder).Append("SKU-1")
	isb.FieldBuilder(1).(*array.Decimal128Builder).Append(p1)
	tlb := isb.FieldBuilder(2).(*array.ListBuilder)
	tlb.Append(true)
	tlb.ValueBuilder().(*array.StringBuilder).Append("blue")
	tlb.ValueBuilder().(*array.StringBuilder).Append("sale")

	p2, _ := decimal128.FromString("5.00", 38, 2)
	isb.Append(true)
	isb.FieldBuilder(0).(*array.StringBuilder).Append("SKU-2")
	isb.FieldBuilder(1).(*array.Decimal128Builder).Append(p2)
	tlb.Append(true)

	// Row 1: NULL struct + empty items list.
	b.Field(0).(*array.Int64Builder).Append(20)
	mb.AppendNull()
	ilb.Append(true)

	// Row 2: non-NULL struct with NULL field + 1 item.
	b.Field(0).(*array.Int64Builder).Append(30)
	mb.Append(true)
	mb.FieldBuilder(0).(*array.StringBuilder).AppendNull()
	mb.FieldBuilder(1).(*array.Time64Builder).Append(arrow.Time64(0))
	ilb.Append(true)
	isb.Append(true)
	isb.FieldBuilder(0).(*array.StringBuilder).Append("SKU-3")
	isb.FieldBuilder(1).(*array.Decimal128Builder).AppendNull()
	tlb.Append(true)
	tlb.ValueBuilder().(*array.StringBuilder).Append("new")

	rec := b.NewRecordBatch()
	defer rec.Release()

	var buf bytes.Buffer

	w := ipc.NewWriter(&buf, ipc.WithSchema(arrowSchema), ipc.WithAllocator(mem))
	if err := w.Write(rec); err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	sess := &arrowStorage{mem: mem, schema: arrowSchema, batches: [][]byte{buf.Bytes()}, rows: 3}
	be := &arrowNestedBackend{schema: bqSchema}
	rn := NewRunner(RunnerOptions{
		Backend: be, Metadata: &FakeBackend{},
		Storage: func(context.Context, bq.TableRef) (StorageSession, error) { return sess, nil },
		Post:    func(fn func()) { fn() }, SpillDir: t.TempDir(),
	})

	r := rn.Start(bq.QueryRequest{SQL: "SELECT id, meta, items FROM nested"})
	defer r.Close()

	s := wait(t, r)
	if s.State != RunDone || s.Source != "storage" || s.Grid == nil {
		t.Fatalf("snapshot = %+v", s)
	}

	rows, lines, complete := s.Grid.Size()
	if rows != 3 || lines != 5 || !complete {
		t.Fatalf("grid = %d rows %d lines complete=%v", rows, lines, complete)
	}

	// Verify Grid.Cell across sub-rows and NULL struct expansion.
	cPrice, _, err := s.Grid.Cell(0, 4) // items.price
	if err != nil || cPrice.Kind != results.Value || cPrice.V != "19.95" {
		t.Fatalf("line 0 price = %+v, err=%v", cPrice, err)
	}

	cTag2, _, err := s.Grid.Cell(1, 5) // items.tags[1]
	if err != nil || cTag2.Kind != results.Value || cTag2.V != "sale" {
		t.Fatalf("line 1 tag = %+v, err=%v", cTag2, err)
	}

	cNullOwner, _, err := s.Grid.Cell(3, 1) // row 1 meta.owner (NULL struct)
	if err != nil || cNullOwner.Kind != results.Null {
		t.Fatalf("row 1 meta.owner = %+v, err=%v", cNullOwner, err)
	}

	// Verify AppendRowJSON matches decoded row 0 structure.
	row0, err := s.Grid.Row(0)
	if err != nil {
		t.Fatal(err)
	}

	gotJSON := string(results.AppendRowJSON(nil, s.Grid.Fields(), row0))
	wantJSON := `{"id":"10","meta":{"owner":"ana","t":"09:15:00"},"items":[{"sku":"SKU-1","price":"19.95","tags":["blue","sale"]},{"sku":"SKU-2","price":"5","tags":[]}]}`

	if gotJSON != wantJSON {
		t.Fatalf("AppendRowJSON:\n got: %s\nwant: %s", gotJSON, wantJSON)
	}

	if !reflect.DeepEqual(results.Leaves(s.Grid.Fields())[0].Name, "id") {
		t.Fatal("unexpected first leaf")
	}
}
