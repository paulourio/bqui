// Package bqstorage reads BigQuery tables through the Storage Read API in
// Apache Arrow format: parallel streams, LZ4-compressed batches, transparent
// resumption after transient errors, and bounded buffering.
package bqstorage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"sync"
	"time"

	storage "cloud.google.com/go/bigquery/storage/apiv1"
	"cloud.google.com/go/bigquery/storage/apiv1/storagepb"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ErrNoSchema is returned when a session carries no Arrow schema.
var ErrNoSchema = errors.New("bqstorage: session has no arrow schema")

const (
	defaultMaxStreams = 4
	maxRetries        = 5
	retryBase         = 200 * time.Millisecond
	bufferPerStream   = 2
)

// Table identifies the table to read.
type Table struct {
	Project, Dataset, Table string
}

func (t Table) path() string {
	return fmt.Sprintf("projects/%s/datasets/%s/tables/%s", t.Project, t.Dataset, t.Table)
}

// ReadOptions narrows and tunes a read session.
type ReadOptions struct {
	// BillingProject owns the read session (defaults to the table's project).
	BillingProject string
	// Fields selects top-level or nested ("a.b") columns; empty reads all.
	Fields []string
	// RowRestriction is a SQL boolean filter, e.g. `country = "BR"`.
	RowRestriction string
	// SnapshotTime reads the table as of a point in time (zero = now).
	SnapshotTime time.Time
	// MaxStreams caps parallelism (0 = 4). BigQuery may return fewer.
	MaxStreams int
	// Uncompressed disables LZ4 buffer compression.
	Uncompressed bool
	// Allocator for decoded Arrow buffers (nil = Go allocator).
	Allocator memory.Allocator
}

// Client wraps the gRPC BigQuery Read client.
type Client struct {
	rc *storage.BigQueryReadClient
}

// New creates a Client. Pass option.WithTokenSource(bq.Client.Tokens()) to
// share bqui's credentials.
func New(ctx context.Context, opts ...option.ClientOption) (*Client, error) {
	rc, err := storage.NewBigQueryReadClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("bqstorage: new client: %w", err)
	}

	return &Client{rc: rc}, nil
}

// Close releases the gRPC connection.
func (c *Client) Close() error {
	return c.rc.Close()
}

// Session is an open read session.
type Session struct {
	c        *Client
	pb       *storagepb.ReadSession
	schema   *arrow.Schema
	rawSchma []byte
	mem      memory.Allocator
}

// Open creates a read session for table.
func (c *Client) Open(ctx context.Context, table Table, opts ReadOptions) (*Session, error) {
	billing := opts.BillingProject
	if billing == "" {
		billing = table.Project
	}

	streams := opts.MaxStreams
	if streams <= 0 {
		streams = defaultMaxStreams
	}

	compression := storagepb.ArrowSerializationOptions_LZ4_FRAME
	if opts.Uncompressed {
		compression = storagepb.ArrowSerializationOptions_COMPRESSION_UNSPECIFIED
	}

	rs := &storagepb.ReadSession{
		Table:      table.path(),
		DataFormat: storagepb.DataFormat_ARROW,
		ReadOptions: &storagepb.ReadSession_TableReadOptions{
			SelectedFields: opts.Fields,
			RowRestriction: opts.RowRestriction,
			OutputFormatSerializationOptions: &storagepb.ReadSession_TableReadOptions_ArrowSerializationOptions{
				ArrowSerializationOptions: &storagepb.ArrowSerializationOptions{BufferCompression: compression},
			},
		},
	}

	if !opts.SnapshotTime.IsZero() {
		rs.TableModifiers = &storagepb.ReadSession_TableModifiers{SnapshotTime: timestamppb.New(opts.SnapshotTime)}
	}

	pb, err := c.rc.CreateReadSession(ctx, &storagepb.CreateReadSessionRequest{
		Parent:         "projects/" + billing,
		ReadSession:    rs,
		MaxStreamCount: int32(min(streams, 1<<20)),
	})
	if err != nil {
		return nil, fmt.Errorf("bqstorage: create session for %s: %w", table.path(), err)
	}

	raw := pb.GetArrowSchema().GetSerializedSchema()
	if len(raw) == 0 {
		return nil, ErrNoSchema
	}

	mem := opts.Allocator
	if mem == nil {
		mem = memory.NewGoAllocator()
	}

	rdr, err := ipc.NewReader(bytes.NewReader(raw), ipc.WithAllocator(mem))
	if err != nil {
		return nil, fmt.Errorf("bqstorage: decode schema: %w", err)
	}

	schema := rdr.Schema()
	rdr.Release()

	return &Session{c: c, pb: pb, schema: schema, rawSchma: raw, mem: mem}, nil
}

// Schema is the Arrow schema of every batch.
func (s *Session) Schema() *arrow.Schema { return s.schema }

// Streams is the number of streams BigQuery allocated (0 for an empty table).
func (s *Session) Streams() int { return len(s.pb.GetStreams()) }

// EstimatedRows is BigQuery's row-count estimate for the session.
func (s *Session) EstimatedRows() int64 { return s.pb.GetEstimatedRowCount() }

// EstimatedBytes is the estimated bytes scanned (used for Storage API pricing).
func (s *Session) EstimatedBytes() int64 { return s.pb.GetEstimatedTotalBytesScanned() }

// Name is the session resource name.
func (s *Session) Name() string { return s.pb.GetName() }

// Batch is one decoded record batch and the stream it came from.
type Batch struct {
	Stream int
	Record arrow.RecordBatch
}

// Batches reads all streams concurrently and yields record batches as they
// arrive (order across streams is not defined; order within a stream is).
// The consumer owns each Record and must Release it. Breaking out of the loop
// cancels outstanding reads.
func (s *Session) Batches(ctx context.Context) iter.Seq2[Batch, error] {
	return func(yield func(Batch, error) bool) {
		streams := s.pb.GetStreams()
		if len(streams) == 0 {
			return
		}

		rctx, cancel := context.WithCancel(ctx)
		defer cancel()

		out := make(chan Batch, bufferPerStream*len(streams))
		errc := make(chan error, len(streams))

		var wg sync.WaitGroup

		for i, st := range streams {
			wg.Go(func() {
				if err := s.readStream(rctx, i, st.GetName(), out); err != nil {
					errc <- err

					cancel()
				}
			})
		}

		go func() {
			wg.Wait()
			close(out)
		}()

		for b := range out {
			if !yield(b, nil) {
				cancel()
				drain(out)

				return
			}
		}

		select {
		case err := <-errc:
			yield(Batch{}, err)
		default:
		}
	}
}

func drain(ch <-chan Batch) {
	for b := range ch {
		b.Record.Release()
	}
}

// readStream reads one stream to the end, resuming at the current row
// offset after retryable errors.
func (s *Session) readStream(ctx context.Context, idx int, name string, out chan<- Batch) error {
	var (
		offset   int64
		attempts int
	)

	for {
		n, err := s.readFrom(ctx, idx, name, offset, out)
		offset += n

		if err == nil {
			return nil
		}

		if n > 0 {
			attempts = 0 // progress was made; reset the retry budget
		}

		if !retryable(err) || attempts >= maxRetries || ctx.Err() != nil {
			return fmt.Errorf("bqstorage: stream %d at row %d: %w", idx, offset, err)
		}

		attempts++

		select {
		case <-time.After(retryBase << attempts):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Session) readFrom(ctx context.Context, idx int, name string, offset int64, out chan<- Batch) (int64, error) {
	rr, err := s.c.rc.ReadRows(ctx, &storagepb.ReadRowsRequest{ReadStream: name, Offset: offset})
	if err != nil {
		return 0, err
	}

	var rows int64

	for {
		resp, err := rr.Recv()
		if errors.Is(err, io.EOF) {
			return rows, nil
		}

		if err != nil {
			return rows, err
		}

		batch := resp.GetArrowRecordBatch().GetSerializedRecordBatch()
		if len(batch) == 0 {
			continue
		}

		rec, err := s.decode(batch)
		if err != nil {
			return rows, err
		}

		select {
		case out <- Batch{Stream: idx, Record: rec}:
			rows += resp.GetRowCount()
		case <-ctx.Done():
			rec.Release()

			return rows, ctx.Err()
		}
	}
}

// decode turns one serialized batch into a Record. BigQuery's serialized
// schema followed by a serialized batch forms a valid Arrow IPC stream.
func (s *Session) decode(batch []byte) (arrow.RecordBatch, error) {
	rdr, err := ipc.NewReader(io.MultiReader(bytes.NewReader(s.rawSchma), bytes.NewReader(batch)),
		ipc.WithAllocator(s.mem), ipc.WithSchema(s.schema))
	if err != nil {
		return nil, fmt.Errorf("bqstorage: decode batch: %w", err)
	}
	defer rdr.Release()

	if !rdr.Next() {
		if err := rdr.Err(); err != nil {
			return nil, fmt.Errorf("bqstorage: decode batch: %w", err)
		}

		return nil, fmt.Errorf("bqstorage: decode batch: %w", io.ErrUnexpectedEOF)
	}

	rec := rdr.RecordBatch()
	rec.Retain()

	return rec, nil
}

func retryable(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.Internal, codes.ResourceExhausted, codes.Aborted:
		return true
	default:
		return false
	}
}
