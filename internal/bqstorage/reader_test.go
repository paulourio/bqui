package bqstorage

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"slices"
	"sync"
	"testing"

	"cloud.google.com/go/bigquery/storage/apiv1/storagepb"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const (
	testStreams      = 2
	batchesPerStream = 3
	rowsPerBatch     = 4
)

var testSchema = arrow.NewSchema([]arrow.Field{
	{Name: "id", Type: arrow.PrimitiveTypes.Int64},
	{Name: "name", Type: arrow.BinaryTypes.String, Nullable: true},
}, nil)

// fakeRead serves testStreams streams of LZ4-compressed Arrow batches. Stream
// 0 fails with UNAVAILABLE after its first batch on the first attempt.
type fakeRead struct {
	storagepb.UnimplementedBigQueryReadServer

	schema  []byte
	batches [testStreams][batchesPerStream][]byte

	mu      sync.Mutex
	offsets map[string][]int64
	failed  bool
	req     *storagepb.CreateReadSessionRequest
}

func newFakeRead(t *testing.T) *fakeRead {
	t.Helper()

	f := &fakeRead{offsets: make(map[string][]int64)}
	mem := memory.NewGoAllocator()

	// Schema-only stream = schema message + 8-byte end-of-stream marker.
	var sbuf bytes.Buffer

	w := ipc.NewWriter(&sbuf, ipc.WithSchema(testSchema), ipc.WithAllocator(mem))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	f.schema = sbuf.Bytes()[:sbuf.Len()-8]

	for s := range testStreams {
		for b := range batchesPerStream {
			rec := makeBatch(mem, int64(s*1000+b*rowsPerBatch))

			var buf bytes.Buffer

			bw := ipc.NewWriter(&buf, ipc.WithSchema(testSchema), ipc.WithAllocator(mem), ipc.WithLZ4())
			if err := bw.Write(rec); err != nil {
				t.Fatal(err)
			}

			rec.Release()

			if !bytes.HasPrefix(buf.Bytes(), f.schema) {
				t.Fatal("schema message prefix mismatch")
			}

			f.batches[s][b] = slices.Clone(buf.Bytes()[len(f.schema):])
		}
	}

	return f
}

func makeBatch(mem memory.Allocator, first int64) arrow.RecordBatch {
	b := array.NewRecordBuilder(mem, testSchema)
	defer b.Release()

	for i := range int64(rowsPerBatch) {
		b.Field(0).(*array.Int64Builder).Append(first + i)
		b.Field(1).(*array.StringBuilder).Append(fmt.Sprintf("row-%d", first+i))
	}

	return b.NewRecordBatch()
}

func (f *fakeRead) CreateReadSession(_ context.Context, req *storagepb.CreateReadSessionRequest) (*storagepb.ReadSession, error) {
	f.mu.Lock()
	f.req = req
	f.mu.Unlock()

	rs := &storagepb.ReadSession{
		Name:              "projects/p/locations/us/sessions/s1",
		EstimatedRowCount: testStreams * batchesPerStream * rowsPerBatch,
		Schema: &storagepb.ReadSession_ArrowSchema{
			ArrowSchema: &storagepb.ArrowSchema{SerializedSchema: f.schema},
		},
	}

	for i := range testStreams {
		rs.Streams = append(rs.Streams, &storagepb.ReadStream{Name: fmt.Sprintf("stream-%d", i)})
	}

	return rs, nil
}

func (f *fakeRead) ReadRows(req *storagepb.ReadRowsRequest, srv storagepb.BigQueryRead_ReadRowsServer) error {
	var idx int
	if _, err := fmt.Sscanf(req.GetReadStream(), "stream-%d", &idx); err != nil {
		return status.Error(codes.NotFound, "no stream")
	}

	f.mu.Lock()
	f.offsets[req.GetReadStream()] = append(f.offsets[req.GetReadStream()], req.GetOffset())
	failNow := idx == 0 && !f.failed
	f.failed = f.failed || failNow
	f.mu.Unlock()

	if req.GetOffset()%rowsPerBatch != 0 {
		return status.Error(codes.InvalidArgument, "unaligned offset in fake")
	}

	for b := int(req.GetOffset() / rowsPerBatch); b < batchesPerStream; b++ {
		err := srv.Send(&storagepb.ReadRowsResponse{
			RowCount: rowsPerBatch,
			Rows: &storagepb.ReadRowsResponse_ArrowRecordBatch{
				ArrowRecordBatch: &storagepb.ArrowRecordBatch{SerializedRecordBatch: f.batches[idx][b]},
			},
		})
		if err != nil {
			return err
		}

		if failNow {
			return status.Error(codes.Unavailable, "injected transient failure")
		}
	}

	return nil
}

func startFake(t *testing.T, f *fakeRead) *Client {
	t.Helper()

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	storagepb.RegisterBigQueryReadServer(srv, f)

	go func() { _ = srv.Serve(lis) }()

	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}

	c, err := New(context.Background(), option.WithGRPCConn(conn))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = c.Close() })

	return c
}

func TestReadAllStreamsWithResumeAndNoLeaks(t *testing.T) {
	f := newFakeRead(t)
	c := startFake(t, f)

	mem := memory.NewCheckedAllocator(memory.NewGoAllocator())
	defer mem.AssertSize(t, 0)

	sess, err := c.Open(context.Background(), Table{"p", "d", "t"}, ReadOptions{
		BillingProject: "billing",
		Fields:         []string{"id", "name"},
		RowRestriction: "id > 0",
		Allocator:      mem,
	})
	if err != nil {
		t.Fatal(err)
	}

	if sess.Streams() != testStreams || !sess.Schema().Equal(testSchema) {
		t.Fatalf("streams=%d schema=%v", sess.Streams(), sess.Schema())
	}

	if got := f.req.GetParent(); got != "projects/billing" {
		t.Errorf("parent = %q", got)
	}

	if f.req.GetReadSession().GetReadOptions().GetArrowSerializationOptions().GetBufferCompression() != storagepb.ArrowSerializationOptions_LZ4_FRAME {
		t.Error("LZ4 compression not requested")
	}

	var ids []int64

	for b, err := range sess.Batches(context.Background()) {
		if err != nil {
			t.Fatal(err)
		}

		col := b.Record.Column(0).(*array.Int64)
		for i := range col.Len() {
			ids = append(ids, col.Value(i))
		}

		if b.Record.Column(1).(*array.String).Value(0) == "" {
			t.Error("empty name")
		}

		b.Record.Release()
	}

	slices.Sort(ids)

	if want := testStreams * batchesPerStream * rowsPerBatch; len(ids) != want || len(slices.Compact(ids)) != want {
		t.Fatalf("got %d ids (%d unique), want %d: %v", len(ids), len(slices.Compact(slices.Clone(ids))), want, ids)
	}

	// Stream 0 failed after one batch and must resume at row 4.
	if got := f.offsets["stream-0"]; !slices.Equal(got, []int64{0, rowsPerBatch}) {
		t.Errorf("stream-0 offsets = %v, want [0 %d]", got, rowsPerBatch)
	}
}

func TestBreakingEarlyReleasesEverything(t *testing.T) {
	c := startFake(t, newFakeRead(t))

	mem := memory.NewCheckedAllocator(memory.NewGoAllocator())
	defer mem.AssertSize(t, 0)

	sess, err := c.Open(context.Background(), Table{"p", "d", "t"}, ReadOptions{Allocator: mem})
	if err != nil {
		t.Fatal(err)
	}

	for b, err := range sess.Batches(context.Background()) {
		if err != nil {
			t.Fatal(err)
		}

		b.Record.Release()

		break
	}
}
