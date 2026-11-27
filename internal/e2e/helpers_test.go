//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"iter"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/bqstorage"
	"github.com/paulourio/bqui/internal/results"
)

const (
	maxAllowedDispatchDelay = 2 * time.Second
	maxAllowedLinesPerRow   = 10_000
	minSmokePNGWidth        = 100
	minSmokePNGHeight       = 50
	minSegmentedPNGHeight   = 12
	minPNGDiffPixels        = 16
	pngSampleGrid           = 64
)

func repoRoot(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", wd)
		}

		dir = parent
	}
}

func buildBinary(t *testing.T, pkg, name, goos, goarch string) string {
	t.Helper()

	out := filepath.Join(t.TempDir(), name)
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", out, pkg)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")

	if goos != "" {
		cmd.Env = append(cmd.Env, "GOOS="+goos)
	}

	if goarch != "" {
		cmd.Env = append(cmd.Env, "GOARCH="+goarch)
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("go build %s (%s/%s): %v\n%s", pkg, goos, goarch, err, stderr.String())
	}

	return out
}

func runSubprocess(t *testing.T, dir, bin string, env []string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}

	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			return outBuf.String(), errBuf.String(), exitErr.ExitCode()
		}

		t.Fatalf("run %s %v: %v\nstderr: %s", bin, args, err, errBuf.String())
	}

	return outBuf.String(), errBuf.String(), 0
}

func loadAppTableFixture(t *testing.T, filename string) *bigquery.Table {
	t.Helper()

	p := filepath.Join(repoRoot(t), "internal", "app", "fixtures", filename)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read fixture %s: %v", filename, err)
	}

	var tbl bigquery.Table
	if err := json.Unmarshal(b, &tbl); err != nil {
		t.Fatalf("unmarshal fixture %s: %v", filename, err)
	}

	return &tbl
}

func loadPlanJobFixture(t *testing.T) *bigquery.Job {
	t.Helper()

	const filename = "plan.job.json"
	p := filepath.Join(repoRoot(t), "internal", "app", "fixtures", filename)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read fixture %s: %v", filename, err)
	}

	var job bigquery.Job
	if err := json.Unmarshal(b, &job); err != nil {
		t.Fatalf("unmarshal fixture %s: %v", filename, err)
	}

	return &job
}

// nestedDataset holds paired REST and Arrow IPC representations of identical logical rows.
type nestedDataset struct {
	restFields  []*bigquery.TableFieldSchema
	restRows    []*bigquery.TableRow
	arrowSchema *arrow.Schema
	arrowBatch  []byte
}

func serializeArrowRecord(t *testing.T, rec arrow.RecordBatch) []byte {
	t.Helper()

	var buf bytes.Buffer
	w := ipc.NewWriter(&buf, ipc.WithSchema(rec.Schema()), ipc.WithLZ4())

	if err := w.Write(rec); err != nil {
		t.Fatalf("arrow ipc write: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("arrow ipc close: %v", err)
	}

	return buf.Bytes()
}

func decodeArrowBatch(raw []byte) (arrow.RecordBatch, error) {
	r, err := ipc.NewReader(bytes.NewReader(raw), ipc.WithAllocator(memory.NewGoAllocator()))
	if err != nil {
		return nil, err
	}
	defer r.Release()

	if !r.Next() {
		if err := r.Err(); err != nil {
			return nil, err
		}

		return nil, errors.New("empty arrow ipc stream")
	}

	rec := r.RecordBatch()
	rec.Retain()

	return rec, nil
}

// buildDeepNestedDataset builds a 5-level STRUCT<ARRAY<STRUCT<ARRAY<STRING>>>> + scalar types
// dataset with identical REST and Arrow IPC data.
func buildDeepNestedDataset(t *testing.T) nestedDataset {
	t.Helper()

	restFields := []*bigquery.TableFieldSchema{
		{Name: "id", Type: "INTEGER", Mode: "REQUIRED"},
		{Name: "ts", Type: "TIMESTAMP"},
		{Name: "score", Type: "FLOAT"},
		{Name: "amount", Type: "NUMERIC"},
		{Name: "raw", Type: "BYTES"},
		{Name: "meta", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{
			{Name: "source", Type: "STRING"},
			{Name: "groups", Type: "RECORD", Mode: "REPEATED", Fields: []*bigquery.TableFieldSchema{
				{Name: "name", Type: "STRING"},
				{Name: "leaf", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{
					{Name: "code", Type: "INTEGER"},
					{Name: "tags", Type: "STRING", Mode: "REPEATED"},
				}},
			}},
		}},
		{Name: "extra_json", Type: "JSON"},
	}

	ts0 := time.Date(2026, 11, 27, 12, 30, 0, 123_456_000, time.UTC)

	restRows := []*bigquery.TableRow{
		{F: []*bigquery.TableCell{
			{V: "101"},
			{V: strconv.FormatInt(ts0.UnixMicro(), 10)},
			{V: "99.5"},
			{V: "123.45"},
			{V: "AQI="}, // base64 for {0x01, 0x02}
			{V: map[string]any{"f": []any{
				map[string]any{"v": "api"},
				map[string]any{"v": []any{
					map[string]any{"v": map[string]any{"f": []any{
						map[string]any{"v": "alpha"},
						map[string]any{"v": map[string]any{"f": []any{
							map[string]any{"v": "7"},
							map[string]any{"v": []any{map[string]any{"v": "t1"}, map[string]any{"v": "t2"}}},
						}}},
					}}},
					map[string]any{"v": map[string]any{"f": []any{
						map[string]any{"v": "beta"},
						map[string]any{"v": map[string]any{"f": []any{
							map[string]any{"v": nil},
							map[string]any{"v": []any{}},
						}}},
					}}},
				}},
			}}},
			{V: `{"k":"v","n":[1,2]}`},
		}},
		// Row 1: NULL struct, NULL scalars, empty repeated
		{F: []*bigquery.TableCell{
			{V: "102"},
			{V: nil},
			{V: nil},
			{V: nil},
			{V: nil},
			{V: nil},
			{V: nil},
		}},
	}

	leafType := arrow.StructOf(
		arrow.Field{Name: "code", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
		arrow.Field{Name: "tags", Type: arrow.ListOf(arrow.BinaryTypes.String)},
	)
	groupType := arrow.StructOf(
		arrow.Field{Name: "name", Type: arrow.BinaryTypes.String, Nullable: true},
		arrow.Field{Name: "leaf", Type: leafType, Nullable: true},
	)
	metaType := arrow.StructOf(
		arrow.Field{Name: "source", Type: arrow.BinaryTypes.String, Nullable: true},
		arrow.Field{Name: "groups", Type: arrow.ListOf(groupType)},
	)
	arrowSchema := arrow.NewSchema([]arrow.Field{
		{Name: "id", Type: arrow.PrimitiveTypes.Int64, Nullable: false},
		{Name: "ts", Type: &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}, Nullable: true},
		{Name: "score", Type: arrow.PrimitiveTypes.Float64, Nullable: true},
		{Name: "amount", Type: &arrow.Decimal128Type{Precision: 38, Scale: 9}, Nullable: true},
		{Name: "raw", Type: arrow.BinaryTypes.Binary, Nullable: true},
		{Name: "meta", Type: metaType, Nullable: true},
		{Name: "extra_json", Type: arrow.BinaryTypes.String, Nullable: true},
	}, nil)

	mem := memory.NewGoAllocator()
	b := array.NewRecordBuilder(mem, arrowSchema)
	defer b.Release()

	b.Field(0).(*array.Int64Builder).AppendValues([]int64{101, 102}, nil)
	b.Field(1).(*array.TimestampBuilder).Append(arrow.Timestamp(ts0.UnixMicro()))
	b.Field(1).(*array.TimestampBuilder).AppendNull()
	b.Field(2).(*array.Float64Builder).Append(99.5)
	b.Field(2).(*array.Float64Builder).AppendNull()

	amt, _ := decimal128.FromString("123.45", 38, 9)
	b.Field(3).(*array.Decimal128Builder).Append(amt)
	b.Field(3).(*array.Decimal128Builder).AppendNull()
	b.Field(4).(*array.BinaryBuilder).Append([]byte{0x01, 0x02})
	b.Field(4).(*array.BinaryBuilder).AppendNull()

	mb := b.Field(5).(*array.StructBuilder)
	mb.Append(true)
	mb.FieldBuilder(0).(*array.StringBuilder).Append("api")
	gb := mb.FieldBuilder(1).(*array.ListBuilder)
	gb.Append(true)
	gsb := gb.ValueBuilder().(*array.StructBuilder)

	// Group 0: alpha
	gsb.Append(true)
	gsb.FieldBuilder(0).(*array.StringBuilder).Append("alpha")
	lb := gsb.FieldBuilder(1).(*array.StructBuilder)
	lb.Append(true)
	lb.FieldBuilder(0).(*array.Int64Builder).Append(7)
	tb := lb.FieldBuilder(1).(*array.ListBuilder)
	tb.Append(true)
	tb.ValueBuilder().(*array.StringBuilder).Append("t1")
	tb.ValueBuilder().(*array.StringBuilder).Append("t2")

	// Group 1: beta
	gsb.Append(true)
	gsb.FieldBuilder(0).(*array.StringBuilder).Append("beta")
	lb.Append(true)
	lb.FieldBuilder(0).(*array.Int64Builder).AppendNull()
	tb.Append(true) // empty tags

	mb.AppendNull()

	b.Field(6).(*array.StringBuilder).Append(`{"k":"v","n":[1,2]}`)
	b.Field(6).(*array.StringBuilder).AppendNull()

	rec := b.NewRecordBatch()
	defer rec.Release()

	return nestedDataset{
		restFields:  restFields,
		restRows:    restRows,
		arrowSchema: arrowSchema,
		arrowBatch:  serializeArrowRecord(t, rec),
	}
}

// buildGithubNestedSubsetDataset returns paired REST and Arrow data using fields
// extracted from the real github_nested fixture schema.
func buildGithubNestedSubsetDataset(t *testing.T) nestedDataset {
	t.Helper()

	restFields := []*bigquery.TableFieldSchema{
		{Name: "type", Type: "STRING"},
		{Name: "actor", Type: "STRING"},
		{Name: "repository", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{
			{Name: "name", Type: "STRING"},
			{Name: "owner", Type: "STRING"},
			{Name: "fork", Type: "BOOLEAN"},
		}},
		{Name: "payload", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{
			{Name: "action", Type: "STRING"},
			{Name: "pull_request", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{
				{Name: "id", Type: "INTEGER"},
				{Name: "base", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{
					{Name: "ref", Type: "STRING"},
					{Name: "repo", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{
						{Name: "name", Type: "STRING"},
						{Name: "owner", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{
							{Name: "login", Type: "STRING"},
						}},
					}},
				}},
			}},
			{Name: "pages", Type: "RECORD", Mode: "REPEATED", Fields: []*bigquery.TableFieldSchema{
				{Name: "page_name", Type: "STRING"},
				{Name: "action", Type: "STRING"},
			}},
		}},
	}

	restRows := []*bigquery.TableRow{
		{F: []*bigquery.TableCell{
			{V: "PullRequestEvent"},
			{V: "octocat"},
			{V: map[string]any{"f": []any{
				map[string]any{"v": "bqui"},
				map[string]any{"v": "paulourio"},
				map[string]any{"v": "false"},
			}}},
			{V: map[string]any{"f": []any{
				map[string]any{"v": "opened"},
				map[string]any{"v": map[string]any{"f": []any{
					map[string]any{"v": "42"},
					map[string]any{"v": map[string]any{"f": []any{
						map[string]any{"v": "main"},
						map[string]any{"v": map[string]any{"f": []any{
							map[string]any{"v": "bqui"},
							map[string]any{"v": map[string]any{"f": []any{
								map[string]any{"v": "paulourio"},
							}}},
						}}},
					}}},
				}}},
				map[string]any{"v": []any{
					map[string]any{"v": map[string]any{"f": []any{map[string]any{"v": "Home"}, map[string]any{"v": "edited"}}}},
					map[string]any{"v": map[string]any{"f": []any{map[string]any{"v": "Setup"}, map[string]any{"v": "created"}}}},
				}},
			}}},
		}},
		{F: []*bigquery.TableCell{
			{V: "PushEvent"},
			{V: "alice"},
			{V: map[string]any{"f": []any{
				map[string]any{"v": "widgets"},
				map[string]any{"v": "alice"},
				map[string]any{"v": "true"},
			}}},
			{V: map[string]any{"f": []any{
				map[string]any{"v": nil},
				map[string]any{"v": nil},
				map[string]any{"v": []any{}},
			}}},
		}},
	}

	ownerType := arrow.StructOf(arrow.Field{Name: "login", Type: arrow.BinaryTypes.String, Nullable: true})
	baseRepoType := arrow.StructOf(
		arrow.Field{Name: "name", Type: arrow.BinaryTypes.String, Nullable: true},
		arrow.Field{Name: "owner", Type: ownerType, Nullable: true},
	)
	baseType := arrow.StructOf(
		arrow.Field{Name: "ref", Type: arrow.BinaryTypes.String, Nullable: true},
		arrow.Field{Name: "repo", Type: baseRepoType, Nullable: true},
	)
	prType := arrow.StructOf(
		arrow.Field{Name: "id", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
		arrow.Field{Name: "base", Type: baseType, Nullable: true},
	)
	pageType := arrow.StructOf(
		arrow.Field{Name: "page_name", Type: arrow.BinaryTypes.String, Nullable: true},
		arrow.Field{Name: "action", Type: arrow.BinaryTypes.String, Nullable: true},
	)
	payloadType := arrow.StructOf(
		arrow.Field{Name: "action", Type: arrow.BinaryTypes.String, Nullable: true},
		arrow.Field{Name: "pull_request", Type: prType, Nullable: true},
		arrow.Field{Name: "pages", Type: arrow.ListOf(pageType)},
	)
	repoType := arrow.StructOf(
		arrow.Field{Name: "name", Type: arrow.BinaryTypes.String, Nullable: true},
		arrow.Field{Name: "owner", Type: arrow.BinaryTypes.String, Nullable: true},
		arrow.Field{Name: "fork", Type: arrow.FixedWidthTypes.Boolean, Nullable: true},
	)
	arrowSchema := arrow.NewSchema([]arrow.Field{
		{Name: "type", Type: arrow.BinaryTypes.String, Nullable: true},
		{Name: "actor", Type: arrow.BinaryTypes.String, Nullable: true},
		{Name: "repository", Type: repoType, Nullable: true},
		{Name: "payload", Type: payloadType, Nullable: true},
	}, nil)

	mem := memory.NewGoAllocator()
	b := array.NewRecordBuilder(mem, arrowSchema)
	defer b.Release()

	b.Field(0).(*array.StringBuilder).AppendValues([]string{"PullRequestEvent", "PushEvent"}, nil)
	b.Field(1).(*array.StringBuilder).AppendValues([]string{"octocat", "alice"}, nil)

	rb := b.Field(2).(*array.StructBuilder)
	rb.Append(true)
	rb.FieldBuilder(0).(*array.StringBuilder).Append("bqui")
	rb.FieldBuilder(1).(*array.StringBuilder).Append("paulourio")
	rb.FieldBuilder(2).(*array.BooleanBuilder).Append(false)
	rb.Append(true)
	rb.FieldBuilder(0).(*array.StringBuilder).Append("widgets")
	rb.FieldBuilder(1).(*array.StringBuilder).Append("alice")
	rb.FieldBuilder(2).(*array.BooleanBuilder).Append(true)

	pb := b.Field(3).(*array.StructBuilder)
	// Row 0 payload
	pb.Append(true)
	pb.FieldBuilder(0).(*array.StringBuilder).Append("opened")
	prb := pb.FieldBuilder(1).(*array.StructBuilder)
	prb.Append(true)
	prb.FieldBuilder(0).(*array.Int64Builder).Append(42)
	bb := prb.FieldBuilder(1).(*array.StructBuilder)
	bb.Append(true)
	bb.FieldBuilder(0).(*array.StringBuilder).Append("main")
	brb := bb.FieldBuilder(1).(*array.StructBuilder)
	brb.Append(true)
	brb.FieldBuilder(0).(*array.StringBuilder).Append("bqui")
	ob := brb.FieldBuilder(1).(*array.StructBuilder)
	ob.Append(true)
	ob.FieldBuilder(0).(*array.StringBuilder).Append("paulourio")

	pagesb := pb.FieldBuilder(2).(*array.ListBuilder)
	pagesb.Append(true)
	psb := pagesb.ValueBuilder().(*array.StructBuilder)
	psb.Append(true)
	psb.FieldBuilder(0).(*array.StringBuilder).Append("Home")
	psb.FieldBuilder(1).(*array.StringBuilder).Append("edited")
	psb.Append(true)
	psb.FieldBuilder(0).(*array.StringBuilder).Append("Setup")
	psb.FieldBuilder(1).(*array.StringBuilder).Append("created")

	// Row 1 payload
	pb.Append(true)
	pb.FieldBuilder(0).(*array.StringBuilder).AppendNull()
	prb.AppendNull()
	pagesb.Append(true)

	rec := b.NewRecordBatch()
	defer rec.Release()

	return nestedDataset{
		restFields:  restFields,
		restRows:    restRows,
		arrowSchema: arrowSchema,
		arrowBatch:  serializeArrowRecord(t, rec),
	}
}

// buildGASessionsSubsetDataset builds paired REST and Arrow data mirroring ga_sessions_20170801.
func buildGASessionsSubsetDataset(t *testing.T) nestedDataset {
	t.Helper()

	restFields := []*bigquery.TableFieldSchema{
		{Name: "fullVisitorId", Type: "STRING"},
		{Name: "totals", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{
			{Name: "visits", Type: "INTEGER"},
			{Name: "hits", Type: "INTEGER"},
		}},
		{Name: "hits", Type: "RECORD", Mode: "REPEATED", Fields: []*bigquery.TableFieldSchema{
			{Name: "hitNumber", Type: "INTEGER"},
			{Name: "page", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{
				{Name: "pagePath", Type: "STRING"},
			}},
		}},
	}

	restRows := []*bigquery.TableRow{
		{F: []*bigquery.TableCell{
			{V: "100001"},
			{V: map[string]any{"f": []any{map[string]any{"v": "1"}, map[string]any{"v": "2"}}}},
			{V: []any{
				map[string]any{"v": map[string]any{"f": []any{
					map[string]any{"v": "1"},
					map[string]any{"v": map[string]any{"f": []any{map[string]any{"v": "/home"}}}},
				}}},
				map[string]any{"v": map[string]any{"f": []any{
					map[string]any{"v": "2"},
					map[string]any{"v": map[string]any{"f": []any{map[string]any{"v": "/checkout"}}}},
				}}},
			}},
		}},
	}

	totalsType := arrow.StructOf(
		arrow.Field{Name: "visits", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
		arrow.Field{Name: "hits", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
	)
	pageType := arrow.StructOf(arrow.Field{Name: "pagePath", Type: arrow.BinaryTypes.String, Nullable: true})
	hitType := arrow.StructOf(
		arrow.Field{Name: "hitNumber", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
		arrow.Field{Name: "page", Type: pageType, Nullable: true},
	)
	arrowSchema := arrow.NewSchema([]arrow.Field{
		{Name: "fullVisitorId", Type: arrow.BinaryTypes.String, Nullable: true},
		{Name: "totals", Type: totalsType, Nullable: true},
		{Name: "hits", Type: arrow.ListOf(hitType)},
	}, nil)

	mem := memory.NewGoAllocator()
	b := array.NewRecordBuilder(mem, arrowSchema)
	defer b.Release()

	b.Field(0).(*array.StringBuilder).Append("100001")
	tb := b.Field(1).(*array.StructBuilder)
	tb.Append(true)
	tb.FieldBuilder(0).(*array.Int64Builder).Append(1)
	tb.FieldBuilder(1).(*array.Int64Builder).Append(2)

	hb := b.Field(2).(*array.ListBuilder)
	hb.Append(true)
	hsb := hb.ValueBuilder().(*array.StructBuilder)
	hsb.Append(true)
	hsb.FieldBuilder(0).(*array.Int64Builder).Append(1)
	pgb := hsb.FieldBuilder(1).(*array.StructBuilder)
	pgb.Append(true)
	pgb.FieldBuilder(0).(*array.StringBuilder).Append("/home")

	hsb.Append(true)
	hsb.FieldBuilder(0).(*array.Int64Builder).Append(2)
	pgb.Append(true)
	pgb.FieldBuilder(0).(*array.StringBuilder).Append("/checkout")

	rec := b.NewRecordBatch()
	defer rec.Release()

	return nestedDataset{
		restFields:  restFields,
		restRows:    restRows,
		arrowSchema: arrowSchema,
		arrowBatch:  serializeArrowRecord(t, rec),
	}
}

// mockQueryBackend implements app.QueryBackend for opaque-box app.Runner testing.
type mockQueryBackend struct {
	mu      sync.Mutex
	result  *bq.QueryResult
	pages   map[string]*bigquery.GetQueryResultsResponse
	err     error
	blockCh chan struct{}
	cancel  bool
}

func (m *mockQueryBackend) Query(ctx context.Context, req bq.QueryRequest) (*bq.QueryResult, error) {
	if m.blockCh != nil {
		if req.OnJob != nil {
			req.OnJob(bq.JobRef{Project: "mock-proj", JobID: "job_blocked", Location: "US"})
		}

		select {
		case <-m.blockCh:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.err != nil {
		return nil, m.err
	}

	return m.result, nil
}

func (m *mockQueryBackend) CancelJob(context.Context, bq.JobRef) error {
	m.mu.Lock()
	m.cancel = true
	m.mu.Unlock()

	return nil
}

func (m *mockQueryBackend) ResultPage(_ context.Context, _ bq.JobRef, token string, _ int64) (*bigquery.GetQueryResultsResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if p, ok := m.pages[token]; ok {
		return p, nil
	}

	return nil, fmt.Errorf("missing page token %q", token)
}

// mockStorageSession implements app.StorageSession over in-memory Arrow IPC batches.
type mockStorageSession struct {
	schema  *arrow.Schema
	batches [][]byte
	estRows int64
}

func (s *mockStorageSession) RawBatches(ctx context.Context) iter.Seq2[bqstorage.RawBatch, error] {
	return func(yield func(bqstorage.RawBatch, error) bool) {
		for _, b := range s.batches {
			if ctx.Err() != nil {
				yield(bqstorage.RawBatch{}, ctx.Err())

				return
			}

			if !yield(bqstorage.RawBatch{Data: b}, nil) {
				return
			}
		}
	}
}

func (s *mockStorageSession) DecodeRows(fields []results.Field, raw []byte) ([][]any, error) {
	rec, err := decodeArrowBatch(raw)
	if err != nil {
		return nil, err
	}
	defer rec.Release()

	return results.RowsFromRecord(fields, rec)
}

func (s *mockStorageSession) DecodeRecord(raw []byte) (arrow.RecordBatch, error) {
	return decodeArrowBatch(raw)
}

func (s *mockStorageSession) Schema() *arrow.Schema { return s.schema }
func (s *mockStorageSession) EstimatedRows() int64  { return s.estRows }
func (s *mockStorageSession) EstimatedBytes() int64 { return 1024 }

type mockMetadataBackend struct {
	app.Backend
	table *bigquery.Table
}

func (m *mockMetadataBackend) GetTable(context.Context, bq.TableRef) (*bigquery.Table, error) {
	return m.table, nil
}

func waitRunDone(t *testing.T, r *app.Run) app.RunSnapshot {
	t.Helper()

	select {
	case <-r.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for app.Run")
	}

	return r.Snapshot()
}

// External smoke artifact schemas (mirrored without importing internal/smoke or internal/appkit).
type smokePoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type smokeSize struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type smokeRect struct {
	Origin smokePoint `json:"origin"`
	Size   smokeSize  `json:"size"`
}

type smokeCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type smokeReport struct {
	Version       string       `json:"version"`
	GoVersion     string       `json:"goVersion"`
	Arch          string       `json:"arch"`
	OSVersion     string       `json:"osVersion"`
	WindowFrame   smokeRect    `json:"windowFrame"`
	DispatchDelay int64        `json:"dispatchDelayNs"`
	Screenshots   []string     `json:"screenshots"`
	Checks        []smokeCheck `json:"checks"`
	Failed        int          `json:"failed"`
}

type axNode struct {
	Role       string   `json:"role,omitempty"`
	Subrole    string   `json:"subrole,omitempty"`
	Identifier string   `json:"identifier,omitempty"`
	Label      string   `json:"label,omitempty"`
	Title      string   `json:"title,omitempty"`
	Children   []axNode `json:"children,omitempty"`
}

type smokeValidationOpts struct {
	allowSkipped        map[string]bool
	requireLayoutChecks bool
	requirePerPanePNGs  bool
}

var baselineRequiredChecks = []string{
	"editor-ready",
	"explorer-flow",
	"inspector-schema",
	"results-done",
	"results-grid",
	"results-nesting",
	"results-json",
	"json-view",
	"value-inspector",
	"export-csv",
	"export-json",
	"dispatch",
	"toolbar",
	"split-panes",
	"ax-AXOutline",
	"ax-AXSplitGroup",
	"ax-AXToolbar",
}

// baselineDiagnosticChecks lists additional pre-M4 smoke checks emitted by
// internal/smoke/smoke_darwin.go so none of them can vacuously satisfy
// Feature #10 layout-check coverage.
var baselineDiagnosticChecks = []string{
	"editor-selftest",
	"editor-completion",
	"editor-diagnostics",
	"editor-tokens",
	"editor-font",
	"editor-console",
	"explorer-projects",
	"explorer-datasets",
	"explorer-tables",
	"explorer-shards",
	"explorer-scale",
	"explorer-filter",
	"inspector-dataset",
	"inspector-segments",
	"editor-run-at-cursor",
	"results-error",
	"project-choices",
	"results-error-banner",
	"results-cancel",
	"results-preview",
	"results-null",
	"results-separator",
	"jobs-information",
	"jobs-history",
	"jobs-inspector",
	"jobs-reopen",
	"jobs-project",
	"plan-details",
	"plan-graph",
	"workbook-start",
	"workbook-autosave",
	"workbook-rename",
	"workspace-session",
	"workbook-runs",
	"workspace-history",
	"theme-switch",
	"theme-json",
	"theme-screenshot",
	"theme-import",
	"prefs-window",
	"prefs-bytes-guard",
	"inspect-table-preview",
	"inspect-view-preview",
	"inspect-preview-warning",
	"explorer-starred",
	"definition-full",
	"definition-partial",
	"main-menu",
	"sidebar-rows",
	"ax-editor-host",
	"capture-light",
	"capture-dark",
}

var baselineSmokePNGs = map[string]bool{
	"error-banner.png":      true,
	"inspector-preview.png": true,
	"offscreen-dark.png":    true,
	"offscreen-light.png":   true,
	"segmented.png":         true,
	"settings.png":          true,
	"theme-midnight.png":    true,
	"window-dark.png":       true,
	"window-light.png":      true,
}

func isBaselineSmokeCheck(name string) bool {
	return slices.Contains(baselineRequiredChecks, name) || slices.Contains(baselineDiagnosticChecks, name)
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}

	return false
}

func tokenizeSmokeText(s string) map[string]bool {
	tokens := make(map[string]bool)

	addSplit := func(part string) {
		low := strings.ToLower(part)
		if low != "" {
			tokens[low] = true
		}

		// Also split camelCase boundaries (e.g. "TableRenderedCell" -> "table",
		// "rendered", "cell"; "tabBar" -> "tab", "bar") without matching substrings
		// inside unrelated words such as "table" or "cancelled".
		start := 0
		runes := []rune(part)
		for i := 1; i < len(runes); i++ {
			if unicode.IsUpper(runes[i]) && unicode.IsLower(runes[i-1]) {
				tokens[strings.ToLower(string(runes[start:i]))] = true
				start = i
			}
		}

		if start > 0 && start < len(runes) {
			tokens[strings.ToLower(string(runes[start:]))] = true
		}
	}

	for _, part := range strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		addSplit(part)
	}

	return tokens
}

func hasAnyToken(tokens map[string]bool, wants ...string) bool {
	for _, w := range wants {
		if tokens[w] {
			return true
		}
	}

	return false
}

func validateSmokeReport(data []byte, opts smokeValidationOpts) (*smokeReport, error) {
	var rep smokeReport
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, fmt.Errorf("parse report.json: %w", err)
	}

	if rep.Version == "" || rep.GoVersion == "" || rep.Arch == "" || rep.OSVersion == "" {
		return nil, fmt.Errorf("missing metadata fields in report.json: version=%q go=%q arch=%q os=%q",
			rep.Version, rep.GoVersion, rep.Arch, rep.OSVersion)
	}

	if rep.WindowFrame.Size.Width <= 0 || rep.WindowFrame.Size.Height <= 0 {
		return nil, fmt.Errorf("invalid windowFrame size: %+v", rep.WindowFrame.Size)
	}

	if rep.DispatchDelay <= 0 || time.Duration(rep.DispatchDelay) >= maxAllowedDispatchDelay {
		return nil, fmt.Errorf("dispatchDelayNs out of bounds: %d", rep.DispatchDelay)
	}

	if rep.Failed != 0 {
		return nil, fmt.Errorf("report.failed = %d, want 0", rep.Failed)
	}

	if len(rep.Checks) == 0 {
		return nil, errors.New("report.checks is empty")
	}

	seen := make(map[string]smokeCheck, len(rep.Checks))
	for _, c := range rep.Checks {
		if c.Name == "" {
			return nil, errors.New("check has empty name")
		}

		if !c.OK {
			return nil, fmt.Errorf("check %q failed: %s", c.Name, c.Detail)
		}

		if c.Skipped && !opts.allowSkipped[c.Name] {
			return nil, fmt.Errorf("check %q unexpectedly skipped: %s", c.Name, c.Detail)
		}

		seen[c.Name] = c
	}

	for _, req := range baselineRequiredChecks {
		if _, ok := seen[req]; !ok {
			return nil, fmt.Errorf("missing required baseline check %q", req)
		}
	}

	if opts.requireLayoutChecks {
		if err := validateLayoutCheckCoverage(rep.Checks); err != nil {
			return nil, err
		}
	}

	return &rep, nil
}

func validateLayoutCheckCoverage(checks []smokeCheck) error {
	type layoutCandidate struct {
		low    string
		tokens map[string]bool
	}

	seenNames := make(map[string]bool, len(checks))
	candidates := make([]layoutCandidate, 0, len(checks))

	for _, c := range checks {
		if !c.OK || c.Skipped || isBaselineSmokeCheck(c.Name) || seenNames[c.Name] {
			continue
		}

		seenNames[c.Name] = true
		raw := c.Name + " " + c.Detail
		candidates = append(candidates, layoutCandidate{
			low:    strings.ToLower(raw),
			tokens: tokenizeSmokeText(raw),
		})
	}

	type layoutCategory struct {
		errMsg string
		match  func(low string, tokens map[string]bool) bool
	}

	categories := []layoutCategory{
		{
			errMsg: "missing narrow/resized (900x600) layout check",
			match: func(low string, _ map[string]bool) bool {
				return containsAny(low, "narrow", "resize", "min-size") &&
					containsAny(low, "900", "600", "880", "580", "contain", "overlap", "window", "frame", "layout")
			},
		},
		{
			errMsg: "missing Inspector wrapping/row-height (440pt/300pt) check",
			match: func(low string, _ map[string]bool) bool {
				return containsAny(low, "inspector", "details", "outline", "wrap") &&
					((strings.Contains(low, "440") && strings.Contains(low, "300")) ||
						(strings.Contains(low, "height") && containsAny(low, "grew", "wrap", "440", "300")))
			},
		},
		{
			errMsg: "missing nested TableRenderedCell check",
			match: func(low string, tokens map[string]bool) bool {
				return hasAnyToken(tokens, "renderedcell", "tablerenderedcell", "nstextfield", "cell", "cells") &&
					containsAny(low, "nested", "results", "json", "non-empty", "row")
			},
		},
		{
			errMsg: "missing bottom-tab fit check",
			match: func(low string, tokens map[string]bool) bool {
				return containsAny(low, "bottom-tab", "bottom-bar", "tabs-fit", "tab-fit", "segmented-fit") ||
					(hasAnyToken(tokens, "tab", "tabs", "tabbar", "segment", "segments", "segmented") &&
						containsAny(low, "fit", "bottom", "7", "clip", "overflow"))
			},
		},
		{
			errMsg: "missing toolbar overflow layout check",
			match: func(low string, _ map[string]bool) bool {
				return strings.Contains(low, "toolbar") &&
					containsAny(low, "overflow", "clipped", "fit", "visible", "hidden")
			},
		},
	}

	for _, cat := range categories {
		var found bool

		for _, cand := range candidates {
			if cat.match(cand.low, cand.tokens) {
				found = true

				break
			}
		}

		if !found {
			return errors.New(cat.errMsg)
		}
	}

	used := make([]bool, len(candidates))

	var canAssign func(catIdx int) bool
	canAssign = func(catIdx int) bool {
		if catIdx == len(categories) {
			return true
		}

		for i, cand := range candidates {
			if used[i] || !categories[catIdx].match(cand.low, cand.tokens) {
				continue
			}

			used[i] = true
			if canAssign(catIdx + 1) {
				return true
			}

			used[i] = false
		}

		return false
	}

	if !canAssign(0) {
		return errors.New("Feature #10 layout coverage requires 5 distinct non-baseline checks (one per category)")
	}

	return nil
}

var requiredResultTabs = []string{
	"Results",
	"Runs",
	"JSON",
	"Execution details",
	"Execution graph",
	"Job information",
	"History",
}

func validateAXTree(data []byte) error {
	var root axNode
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse ax.json: %w", err)
	}

	if root.Role != "AXWindow" {
		return fmt.Errorf("ax root role = %q, want AXWindow", root.Role)
	}

	roles := make(map[string]bool)
	labels := make(map[string]bool)

	var walk func(n axNode)
	walk = func(n axNode) {
		if n.Role != "" {
			roles[n.Role] = true
		}

		if n.Label != "" {
			labels[n.Label] = true
		}

		if n.Title != "" {
			labels[n.Title] = true
		}

		for _, ch := range n.Children {
			walk(ch)
		}
	}

	walk(root)

	for _, r := range []string{"AXWindow", "AXOutline", "AXSplitGroup", "AXToolbar"} {
		if !roles[r] {
			return fmt.Errorf("ax.json missing role %q", r)
		}
	}

	for _, tab := range requiredResultTabs {
		if tab == "Runs" {
			var foundRuns bool
			for label := range labels {
				if label == "Runs" || strings.HasPrefix(label, "Runs (") {
					foundRuns = true

					break
				}
			}

			if !foundRuns {
				return fmt.Errorf("ax.json missing bottom tab %q", tab)
			}

			continue
		}

		if !labels[tab] {
			return fmt.Errorf("ax.json missing bottom tab %q", tab)
		}
	}

	for _, btn := range []string{"Schema", "Details", "Preview", "Run", "Stop", "Inspector"} {
		if !labels[btn] {
			return fmt.Errorf("ax.json missing control label %q", btn)
		}
	}

	return nil
}

type pngArtifactInfo struct {
	name      string
	cfg       image.Config
	pixelHash [32]byte
}

func validatePNGFile(path string) (image.Config, error) {
	return validatePNGFileMin(path, minSmokePNGWidth, minSmokePNGHeight)
}

func validatePNGFileMin(path string, minW, minH int) (image.Config, error) {
	info, err := validatePNGFileInfo(path, minW, minH)

	return info.cfg, err
}

func validatePNGFileInfo(path string, minW, minH int) (pngArtifactInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return pngArtifactInfo{}, err
	}
	defer f.Close()

	cfg, err := png.DecodeConfig(f)
	if err != nil {
		return pngArtifactInfo{}, fmt.Errorf("decode png config %s: %w", filepath.Base(path), err)
	}

	if cfg.Width < minW || cfg.Height < minH {
		return pngArtifactInfo{cfg: cfg}, fmt.Errorf("png %s dimensions %dx%d below minimum %dx%d", filepath.Base(path), cfg.Width, cfg.Height, minW, minH)
	}

	if _, serr := f.Seek(0, 0); serr != nil {
		return pngArtifactInfo{cfg: cfg}, serr
	}

	img, err := png.Decode(f)
	if err != nil {
		return pngArtifactInfo{cfg: cfg}, fmt.Errorf("decode png pixels %s: %w", filepath.Base(path), err)
	}

	bounds := img.Bounds()
	first := img.At(bounds.Min.X, bounds.Min.Y)
	r0, g0, b0, a0 := first.RGBA()

	stepX := max(1, bounds.Dx()/pngSampleGrid)
	stepY := max(1, bounds.Dy()/pngSampleGrid)

	var totalSamples, diffCount int
	distinctColors := make(map[[4]uint32]struct{})

	for y := bounds.Min.Y; y < bounds.Max.Y; y += stepY {
		for x := bounds.Min.X; x < bounds.Max.X; x += stepX {
			totalSamples++
			r, g, b, a := img.At(x, y).RGBA()
			distinctColors[[4]uint32{r, g, b, a}] = struct{}{}

			if r != r0 || g != g0 || b != b0 || a != a0 {
				diffCount++
			}
		}
	}

	if diffCount == 0 {
		return pngArtifactInfo{cfg: cfg}, fmt.Errorf("png %s has uniform solid-color pixels (blank capture)", filepath.Base(path))
	}

	if diffCount < minPNGDiffPixels || (diffCount*100 < totalSamples && len(distinctColors) < minPNGDiffPixels) {
		return pngArtifactInfo{cfg: cfg}, fmt.Errorf("png %s has insufficient non-uniform pixels (%d/%d differing, %d distinct colors)",
			filepath.Base(path), diffCount, totalSamples, len(distinctColors))
	}

	return pngArtifactInfo{
		name:      strings.ToLower(filepath.Base(path)),
		cfg:       cfg,
		pixelHash: pixelSHA256(img),
	}, nil
}

func pixelSHA256(img image.Image) [32]byte {
	b := img.Bounds()
	h := sha256.New()

	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:4], uint32(max(0, b.Dx())))
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(max(0, b.Dy())))
	_, _ = h.Write(hdr[:])

	switch typed := img.(type) {
	case *image.RGBA:
		rowBytes := b.Dx() * 4
		for y := b.Min.Y; y < b.Max.Y; y++ {
			off := typed.PixOffset(b.Min.X, y)
			_, _ = h.Write(typed.Pix[off : off+rowBytes])
		}
	case *image.NRGBA:
		rowBytes := b.Dx() * 4
		for y := b.Min.Y; y < b.Max.Y; y++ {
			off := typed.PixOffset(b.Min.X, y)
			_, _ = h.Write(typed.Pix[off : off+rowBytes])
		}
	default:
		row := make([]byte, max(0, b.Dx())*4)
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				r, g, bl, a := img.At(x, y).RGBA()
				off := (x - b.Min.X) * 4
				row[off] = uint8(r >> 8)
				row[off+1] = uint8(g >> 8)
				row[off+2] = uint8(bl >> 8)
				row[off+3] = uint8(a >> 8)
			}

			_, _ = h.Write(row)
		}
	}

	var sum [32]byte
	copy(sum[:], h.Sum(nil))

	return sum
}

func validateSmokeArtifacts(dir string, opts smokeValidationOpts) (*smokeReport, error) {
	repData, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		return nil, err
	}

	rep, err := validateSmokeReport(repData, opts)
	if err != nil {
		return nil, err
	}

	axData, err := os.ReadFile(filepath.Join(dir, "ax.json"))
	if err != nil {
		return nil, err
	}

	if err = validateAXTree(axData); err != nil {
		return nil, err
	}

	if len(rep.Screenshots) == 0 {
		return nil, errors.New("report.screenshots is empty")
	}

	pngMinHeight := func(name string) int {
		if strings.EqualFold(filepath.Base(name), "segmented.png") {
			return minSegmentedPNGHeight
		}

		return minSmokePNGHeight
	}

	reportedPNG := make(map[string]pngArtifactInfo, len(rep.Screenshots))
	for _, s := range rep.Screenshots {
		info, pngErr := validatePNGFileInfo(filepath.Join(dir, s), minSmokePNGWidth, pngMinHeight(s))
		if pngErr != nil {
			return nil, pngErr
		}

		reportedPNG[info.name] = info
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".png") {
			continue
		}

		if _, pngErr := validatePNGFileInfo(filepath.Join(dir, e.Name()), minSmokePNGWidth, pngMinHeight(e.Name())); pngErr != nil {
			return nil, pngErr
		}
	}

	if opts.requireLayoutChecks || opts.requirePerPanePNGs {
		if err := validatePerPanePNGs(reportedPNG); err != nil {
			return nil, err
		}
	}

	return rep, nil
}

func validatePerPanePNGs(seenPNG map[string]pngArtifactInfo) error {
	nonBaseline := make([]pngArtifactInfo, 0, len(seenPNG))
	for name, info := range seenPNG {
		if baselineSmokePNGs[name] {
			continue
		}

		nonBaseline = append(nonBaseline, info)
	}

	slices.SortFunc(nonBaseline, func(a, b pngArtifactInfo) int {
		return strings.Compare(a.name, b.name)
	})

	if len(nonBaseline) < 4 {
		return fmt.Errorf("found only %d non-baseline PNGs, want >= 4 Feature #11 per-pane/narrow captures", len(nonBaseline))
	}

	type perPaneCategory struct {
		label string
		match func(name string) bool
	}

	categories := []perPaneCategory{
		{
			label: "results/nested pane",
			match: func(name string) bool {
				return strings.Contains(name, "results") || strings.Contains(name, "nested")
			},
		},
		{
			label: "json pane",
			match: func(name string) bool {
				return strings.Contains(name, "json")
			},
		},
		{
			label: "inspector pane",
			match: func(name string) bool {
				return strings.Contains(name, "inspector")
			},
		},
		{
			label: "narrow/resized window",
			match: func(name string) bool {
				return strings.Contains(name, "narrow") || strings.Contains(name, "resize")
			},
		},
	}

	for _, cat := range categories {
		var found bool
		for _, info := range nonBaseline {
			if cat.match(info.name) {
				found = true

				break
			}
		}

		if !found {
			return fmt.Errorf("missing Feature #11 non-baseline %s PNG", cat.label)
		}
	}

	var mainWindows []pngArtifactInfo

	baselineWindowHashes := make(map[[32]byte]bool)
	for _, mainName := range []string{"window-light.png", "window-dark.png"} {
		if info, ok := seenPNG[mainName]; ok {
			mainWindows = append(mainWindows, info)
			baselineWindowHashes[info.pixelHash] = true
		}
	}

	if len(mainWindows) == 0 {
		return errors.New("missing baseline window-light.png or window-dark.png to verify narrow window width")
	}

	narrowerThanMain := func(info pngArtifactInfo) (bool, pngArtifactInfo) {
		for _, mw := range mainWindows {
			if info.cfg.Width >= mw.cfg.Width {
				return false, mw
			}
		}

		return true, pngArtifactInfo{}
	}

	var narrowCandidates, validNarrowCandidates int
	var lastWideNarrow, lastWideMain pngArtifactInfo
	for _, info := range nonBaseline {
		if !categories[3].match(info.name) {
			continue
		}

		narrowCandidates++
		if ok, mw := narrowerThanMain(info); ok {
			validNarrowCandidates++
		} else {
			lastWideNarrow = info
			lastWideMain = mw
		}
	}

	if narrowCandidates > 0 && validNarrowCandidates == 0 {
		return fmt.Errorf("narrow/resized PNG %q width %d is not smaller than main window %q width %d",
			lastWideNarrow.name, lastWideNarrow.cfg.Width, lastWideMain.name, lastWideMain.cfg.Width)
	}

	canAssign := func(requireDistinctHashes bool) bool {
		usedFile := make([]bool, len(nonBaseline))
		usedHashes := make(map[[32]byte]bool, len(categories))

		var search func(catIdx int) bool
		search = func(catIdx int) bool {
			if catIdx == len(categories) {
				return true
			}

			for i, info := range nonBaseline {
				if usedFile[i] || !categories[catIdx].match(info.name) {
					continue
				}

				if catIdx == 3 {
					if ok, _ := narrowerThanMain(info); !ok {
						continue
					}
				}

				if requireDistinctHashes && (usedHashes[info.pixelHash] || baselineWindowHashes[info.pixelHash]) {
					continue
				}

				usedFile[i] = true
				if requireDistinctHashes {
					usedHashes[info.pixelHash] = true
				}

				if search(catIdx + 1) {
					return true
				}

				usedFile[i] = false
				if requireDistinctHashes {
					delete(usedHashes, info.pixelHash)
				}
			}

			return false
		}

		return search(0)
	}

	if canAssign(true) {
		return nil
	}

	if !canAssign(false) {
		return errors.New("Feature #11 categories require 4 distinct non-baseline PNG files (one per category)")
	}

	return errors.New("Feature #11 per-pane/narrow PNGs must have distinct pixel content (duplicate PNG hashes detected)")
}

func validateSmokeLog(data []byte) error {
	s := string(data)
	low := strings.ToLower(s)

	patterns := []string{
		"panic:",
		"fatal error:",
		"unable to simultaneously satisfy constraints",
		"nslayoutconstraint",
		"reentrant",
		"layoutSubtreeIfNeeded",
		"warning: application performed",
	}

	for _, pat := range patterns {
		if strings.Contains(low, strings.ToLower(pat)) {
			return fmt.Errorf("smoke.log contains forbidden pattern %q", pat)
		}
	}

	return nil
}

func goldenSmokeReportJSON(withLayout bool) []byte {
	checks := make([]smokeCheck, 0, len(baselineRequiredChecks)+8)
	for _, name := range baselineRequiredChecks {
		checks = append(checks, smokeCheck{Name: name, OK: true, Detail: "ok"})
	}

	screenshots := []string{"segmented.png", "window-light.png", "window-dark.png"}

	if withLayout {
		checks = append(checks,
			smokeCheck{Name: "layout-inspector-wrap", OK: true, Detail: "440pt and 300pt wrapping height grew"},
			smokeCheck{Name: "layout-narrow-window", OK: true, Detail: "900x600 containment and non-overlap"},
			smokeCheck{Name: "layout-results-rendered-cell", OK: true, Detail: "TableRenderedCell non-empty on nested Results and JSON"},
			smokeCheck{Name: "layout-bottom-tabs", OK: true, Detail: "7 tabs fit cleanly at 1280px"},
			smokeCheck{Name: "layout-toolbar-overflow", OK: true, Detail: "0 toolbar items overflowed"},
		)
		screenshots = append(screenshots, "pane-results-nested.png", "pane-json-nested.png", "pane-inspector-details.png", "window-narrow.png")
	}

	rep := smokeReport{
		Version:       "1.0.0-e2e",
		GoVersion:     "go1.27.3",
		Arch:          "arm64",
		OSVersion:     "Version 26.7.3",
		WindowFrame:   smokeRect{Origin: smokePoint{X: 0, Y: 78}, Size: smokeSize{Width: 1280, Height: 691}},
		DispatchDelay: 125_000,
		Screenshots:   screenshots,
		Checks:        checks,
		Failed:        0,
	}

	b, _ := json.MarshalIndent(rep, "", "  ")

	return b
}

func goldenSmokeDir(t *testing.T, withLayout bool) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.json"), goldenSmokeReportJSON(withLayout), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "ax.json"), goldenAXTreeJSON(), 0o600); err != nil {
		t.Fatal(err)
	}

	for name := range baselineSmokePNGs {
		w, h := 160, 100
		if name == "segmented.png" {
			w, h = 182, 16
		}

		writeTestPNGVariant(t, filepath.Join(dir, name), w, h, name)
	}

	if withLayout {
		for _, name := range []string{"pane-results-nested.png", "pane-json-nested.png", "pane-inspector-details.png"} {
			writeTestPNGVariant(t, filepath.Join(dir, name), 160, 100, name)
		}

		writeTestPNGVariant(t, filepath.Join(dir, "window-narrow.png"), 100, 68, "window-narrow.png")
	}

	return dir
}

func goldenAXTreeJSON() []byte {
	var segs []axNode
	for _, tab := range requiredResultTabs {
		segs = append(segs, axNode{Role: "AXRadioButton", Subrole: "AXSegment", Label: tab})
	}

	root := axNode{
		Role:  "AXWindow",
		Title: "bqui – workbook / Untitled-1",
		Children: []axNode{
			{
				Role: "AXSplitGroup",
				Children: []axNode{
					{Role: "AXOutline"},
					{Role: "AXRadioGroup", Children: segs},
					{Role: "AXButton", Label: "Schema"},
					{Role: "AXButton", Label: "Details"},
					{Role: "AXButton", Label: "Preview"},
				},
			},
			{
				Role: "AXToolbar",
				Children: []axNode{
					{Role: "AXButton", Label: "Run"},
					{Role: "AXButton", Label: "Stop"},
					{Role: "AXButton", Label: "Inspector"},
				},
			},
		},
	}

	b, _ := json.MarshalIndent(root, "", "  ")

	return b
}

func writeTestPNG(t *testing.T, path string, w, h int, uniform bool) {
	t.Helper()

	if !uniform {
		writeTestPNGVariant(t, path, w, h, "")

		return
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))

	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{R: 30, G: 30, B: 30, A: 255})
		}
	}

	encodeTestPNG(t, path, img)
}

func writeTestPNGVariant(t *testing.T, path string, w, h int, salt string) {
	t.Helper()

	var xorR, xorG, xorB uint8
	if salt != "" {
		sum := sha256.Sum256([]byte(salt))
		xorR, xorG, xorB = sum[0], sum[1], sum[2]
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))

	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8((x*255)/max(1, w)) ^ xorR,
				G: uint8((y*255)/max(1, h)) ^ xorG,
				B: 120 ^ xorB,
				A: 255,
			})
		}
	}

	encodeTestPNG(t, path, img)
}

func encodeTestPNG(t *testing.T, path string, img image.Image) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create png %s: %v", path, err)
	}

	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		t.Fatalf("encode png %s: %v", path, err)
	}

	if err := f.Close(); err != nil {
		t.Fatalf("close png %s: %v", path, err)
	}
}

func writeSingleDiffPixelPNG(t *testing.T, path string, w, h, px, py int) {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, w, h))

	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{R: 0, G: 0, B: 0, A: 255})
		}
	}

	img.SetRGBA(px, py, color.RGBA{R: 255, G: 255, B: 255, A: 255})

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create png %s: %v", path, err)
	}

	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		t.Fatalf("encode png %s: %v", path, err)
	}

	if err := f.Close(); err != nil {
		t.Fatalf("close png %s: %v", path, err)
	}
}
