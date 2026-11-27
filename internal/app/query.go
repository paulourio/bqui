package app

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/bqstorage"
	"github.com/paulourio/bqui/internal/results"
)

// QueryBackend runs queries and pages their results.
type QueryBackend interface {
	Query(ctx context.Context, req bq.QueryRequest) (*bq.QueryResult, error)
	CancelJob(ctx context.Context, ref bq.JobRef) error
	ResultPage(ctx context.Context, job bq.JobRef, pageToken string, maxRows int64) (*bigquery.GetQueryResultsResponse, error)
}

var _ QueryBackend = (*bq.Client)(nil)

// StorageSession streams a table through the Storage Read API.
type StorageSession interface {
	RawBatches(ctx context.Context) iter.Seq2[bqstorage.RawBatch, error]
	DecodeRows(fields []results.Field, raw []byte) ([][]any, error)
	// DecodeRecord returns the raw batch as an Arrow record (caller releases).
	DecodeRecord(raw []byte) (arrow.RecordBatch, error)
	Schema() *arrow.Schema
	EstimatedRows() int64
	EstimatedBytes() int64
}

// StorageOpener opens a read session for a table (nil when unavailable).
type StorageOpener func(ctx context.Context, ref bq.TableRef) (StorageSession, error)

// NewStorageOpener adapts a bqstorage client; billing returns the project
// that owns read sessions.
func NewStorageOpener(c *bqstorage.Client, billing func() string) StorageOpener {
	return func(ctx context.Context, ref bq.TableRef) (StorageSession, error) {
		s, err := c.Open(ctx, bqstorage.Table{Project: ref.Project, Dataset: ref.Dataset, Table: ref.Table},
			bqstorage.ReadOptions{BillingProject: billing()})
		if err != nil {
			return nil, err
		}

		return storageSession{s}, nil
	}
}

type storageSession struct{ s *bqstorage.Session }

func (s storageSession) RawBatches(ctx context.Context) iter.Seq2[bqstorage.RawBatch, error] {
	return s.s.RawBatches(ctx)
}

func (s storageSession) DecodeRecord(raw []byte) (arrow.RecordBatch, error) { return s.s.Decode(raw) }
func (s storageSession) Schema() *arrow.Schema                              { return s.s.Schema() }
func (s storageSession) EstimatedRows() int64                               { return s.s.EstimatedRows() }

func (s storageSession) EstimatedBytes() int64 { return s.s.EstimatedBytes() }

func (s storageSession) DecodeRows(fields []results.Field, raw []byte) ([][]any, error) {
	rec, err := s.s.Decode(raw)
	if err != nil {
		return nil, err
	}
	defer rec.Release()

	return results.RowsFromRecord(fields, rec)
}

// RunState is the lifecycle of a run.
type RunState int

// Run states.
const (
	RunRunning RunState = iota
	RunFetching
	RunDone
	RunFailed
	RunCancelled
)

func (s RunState) String() string {
	return [...]string{"running", "fetching", "done", "failed", "cancelled"}[s]
}

// RunSnapshot is a consistent view of a run for the UI.
type RunSnapshot struct {
	ID         int
	Kind       string // "query" or "preview"
	Title      string
	SQL        string // full query text (queries only)
	State      RunState
	Err        error
	Started    time.Time
	Elapsed    time.Duration
	Job        *bq.JobRef
	Result     *bq.QueryResult
	JobDetails *bigquery.Job // latest job snapshot (plan, statistics)
	// ResultTable holds the full results (destination or previewed table).
	ResultTable *bq.TableRef
	Grid        *results.Grid
	Stages      int
	StagesDone  int
	SlotMs      int64
	Source      string // "inline", "rest", "storage"
	Truncated   bool   // stopped at a row or size limit
}

// Run is one query execution or table preview.
type Run struct {
	runner *Runner
	cancel context.CancelFunc

	mu    sync.Mutex
	snap  RunSnapshot
	spill *results.SpillStore
	done  chan struct{}
}

// Snapshot returns the run's current state.
func (r *Run) Snapshot() RunSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()

	s := r.snap
	if s.State == RunRunning || s.State == RunFetching {
		s.Elapsed = time.Since(s.Started)
	}

	return s
}

// Done is closed when the run reaches a final state.
func (r *Run) Done() <-chan struct{} { return r.done }

// Cancel stops local work and asks BigQuery to cancel the job.
func (r *Run) Cancel() {
	r.cancel()

	r.mu.Lock()
	job := r.snap.Job
	running := r.snap.State == RunRunning
	r.mu.Unlock()

	if job != nil && running {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), cancelTimeout)
			defer cancel()

			if err := r.runner.opts.Backend.CancelJob(ctx, *job); err != nil {
				r.runner.log.Warn("cancel job", slog.String("job", job.JobID), slog.Any("error", err))
			}
		}()
	}
}

// Close releases the run's spill file. Call when the results are discarded.
func (r *Run) Close() {
	r.Cancel()
	<-r.done

	r.mu.Lock()
	sp := r.spill
	r.spill = nil
	r.mu.Unlock()

	if sp != nil {
		_ = sp.Close()
	}
}

func (r *Run) update(fn func(s *RunSnapshot)) {
	r.mu.Lock()
	fn(&r.snap)
	r.mu.Unlock()

	r.runner.notify(r)
}

// RunnerOptions configures a Runner.
type RunnerOptions struct {
	Backend  QueryBackend
	Metadata Backend       // table schemas for previews
	Jobs     JobsBackend   // past jobs (OpenJob)
	Storage  StorageOpener // nil disables the Storage Read API path
	// Post runs fn on the UI thread; OnUpdate is called there.
	Post     func(fn func())
	OnUpdate func(*Run)
	SpillDir string
	// MaxRESTRows caps rows paged over REST when storage is unavailable.
	MaxRESTRows int64
	// MaxSpillBytes caps the spill file per run.
	MaxSpillBytes int64
	Logger        *slog.Logger
}

const (
	cancelTimeout        = 15 * time.Second
	updateEvery          = 150 * time.Millisecond
	defaultMaxRESTRows   = 1_000_000
	defaultMaxSpillBytes = 8 << 30
	restPageRows         = 10_000
	previewRows          = 1000
)

// Runner executes queries and previews and streams their results into grids.
type Runner struct {
	opts RunnerOptions
	log  *slog.Logger

	maxSpill atomic.Int64

	mu     sync.Mutex
	nextID int
	lastUI map[int]time.Time
}

// SetMaxSpillBytes changes the per-run spill cap (<= 0 restores the default).
func (rn *Runner) SetMaxSpillBytes(n int64) {
	if n <= 0 {
		n = defaultMaxSpillBytes
	}

	rn.maxSpill.Store(n)
}

// NewRunner creates a Runner.
func NewRunner(opts RunnerOptions) *Runner {
	if opts.MaxRESTRows <= 0 {
		opts.MaxRESTRows = defaultMaxRESTRows
	}

	if opts.MaxSpillBytes <= 0 {
		opts.MaxSpillBytes = defaultMaxSpillBytes
	}

	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	rn := &Runner{opts: opts, log: log, lastUI: make(map[int]time.Time)}
	rn.maxSpill.Store(opts.MaxSpillBytes)

	return rn
}

func (rn *Runner) newRun(kind, title string) (*Run, context.Context) {
	rn.mu.Lock()
	rn.nextID++
	id := rn.nextID
	rn.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	r := &Run{runner: rn, cancel: cancel, done: make(chan struct{})}
	r.snap = RunSnapshot{ID: id, Kind: kind, Title: title, State: RunRunning, Started: time.Now()}

	return r, ctx
}

// notify posts the run to the UI, throttled while results stream in.
func (rn *Runner) notify(r *Run) {
	s := r.Snapshot()
	final := s.State == RunDone || s.State == RunFailed || s.State == RunCancelled

	rn.mu.Lock()
	last := rn.lastUI[s.ID]
	throttled := !final && s.State == RunFetching && time.Since(last) < updateEvery

	if !throttled {
		rn.lastUI[s.ID] = time.Now()
	}

	if final {
		delete(rn.lastUI, s.ID)
	}
	rn.mu.Unlock()

	if !throttled && rn.opts.OnUpdate != nil {
		rn.opts.Post(func() { rn.opts.OnUpdate(r) })
	}
}

// Start runs a query asynchronously.
func (rn *Runner) Start(req bq.QueryRequest) *Run {
	r, ctx := rn.newRun("query", firstLine(req.SQL))
	r.snap.SQL = req.SQL

	go func() {
		defer close(r.done)

		r.finish(ctx, rn.execute(ctx, r, req))
	}()

	rn.notify(r)

	return r
}

// Preview reads the first rows of a table through the Storage Read API
// (free of query cost), or runs a LIMIT query when storage is unavailable.
func (rn *Runner) Preview(ref bq.TableRef) *Run {
	r, ctx := rn.newRun("preview", "Preview of "+ref.String())

	go func() {
		defer close(r.done)

		r.finish(ctx, rn.preview(ctx, r, ref))
	}()

	rn.notify(r)

	return r
}

func (r *Run) finish(ctx context.Context, err error) {
	r.update(func(s *RunSnapshot) {
		s.Elapsed = time.Since(s.Started)

		switch {
		case err == nil:
			s.State = RunDone
		case ctx.Err() != nil:
			s.State = RunCancelled
		default:
			s.State, s.Err = RunFailed, err
		}

		if s.Grid != nil {
			if s.State == RunFailed {
				s.Grid.Finish(err)
			} else {
				s.Grid.Finish(nil)
			}
		}
	})
}

func (rn *Runner) execute(ctx context.Context, r *Run, req bq.QueryRequest) error {
	req.OnJob = func(ref bq.JobRef) {
		r.update(func(s *RunSnapshot) { s.Job = &ref })
	}
	req.OnProgress = func(j *bigquery.Job) {
		r.update(func(s *RunSnapshot) { s.progress(j) })
	}

	res, err := rn.opts.Backend.Query(ctx, req)
	if err != nil {
		return err
	}

	r.update(func(s *RunSnapshot) {
		s.Result = res
		s.ResultTable = res.Destination
		if res.Job != nil {
			s.Job = res.Job
		}

		if res.JobDetails != nil {
			s.progress(res.JobDetails)
		}
	})

	if res.Schema == nil || len(res.Schema.Fields) == 0 {
		return nil // DDL/DML/script without a final result set
	}

	fields := results.FieldsFromREST(res.Schema.Fields)

	rows, err := bq.DecodeRows(res.Schema.Fields, res.Rows)
	if err != nil {
		return err
	}

	switch {
	case uint64(len(rows)) >= res.TotalRows:
		return rn.memoryResult(r, fields, rows, "inline")
	case rn.opts.Storage != nil && res.Destination != nil:
		return rn.storageResult(ctx, r, fields, *res.Destination, 0)
	case res.Job != nil:
		return rn.restResult(ctx, r, fields, res.Schema.Fields, *res.Job, rows, res.PageToken)
	default:
		return rn.memoryResult(r, fields, rows, "inline")
	}
}

func (rn *Runner) memoryResult(r *Run, fields []results.Field, rows [][]any, source string) error {
	m := &results.MemoryStore{}
	g := results.NewGrid(fields, m)
	results.AppendRows(g, m, rows)

	r.update(func(s *RunSnapshot) {
		s.Grid, s.Source = g, source
	})

	return nil
}

// restResult pages getQueryResults (used when storage is unavailable, e.g.
// script results without a destination table).
func (rn *Runner) restResult(ctx context.Context, r *Run, fields []results.Field, schema []*bigquery.TableFieldSchema,
	job bq.JobRef, first [][]any, token string,
) error {
	m := &results.MemoryStore{}
	g := results.NewGrid(fields, m)
	results.AppendRows(g, m, first)

	r.update(func(s *RunSnapshot) {
		s.Grid, s.Source, s.State = g, "rest", RunFetching
	})

	loaded := int64(len(first))

	for token != "" {
		if loaded >= rn.opts.MaxRESTRows {
			r.update(func(s *RunSnapshot) { s.Truncated = true })

			return nil
		}

		page, err := rn.opts.Backend.ResultPage(ctx, job, token, restPageRows)
		if err != nil {
			return err
		}

		rows, err := bq.DecodeRows(schema, page.Rows)
		if err != nil {
			return err
		}

		results.AppendRows(g, m, rows)
		loaded += int64(len(rows))
		token = page.PageToken

		r.update(func(*RunSnapshot) {})
	}

	return nil
}

// storageResult streams a table into a spill-backed grid. limit > 0 stops
// after that many rows (previews).
func (rn *Runner) storageResult(ctx context.Context, r *Run, fields []results.Field, ref bq.TableRef, limit int64) error {
	sess, err := rn.opts.Storage(ctx, ref)
	if err != nil {
		return err
	}

	spill, err := results.NewSpillStore(rn.opts.SpillDir, func(raw []byte) ([][]any, error) {
		return sess.DecodeRows(fields, raw)
	})
	if err != nil {
		return err
	}

	g := results.NewGrid(fields, spill)

	r.update(func(s *RunSnapshot) {
		s.Grid, s.Source, s.State = g, "storage", RunFetching
	})

	r.mu.Lock()
	r.spill = spill
	r.mu.Unlock()

	sctx, stop := context.WithCancel(ctx)
	defer stop()

	for b, err := range sess.RawBatches(sctx) {
		if err != nil {
			return err
		}

		if err := results.AppendSpilled(g, spill, b.Data); err != nil {
			return err
		}

		r.update(func(*RunSnapshot) {})

		rows, _, _ := g.Size()

		switch {
		case limit > 0 && rows >= limit:
			return nil
		case spill.Bytes() >= rn.maxSpill.Load():
			r.update(func(s *RunSnapshot) { s.Truncated = true })

			return nil
		}
	}

	return nil
}

// OpenJob shows a finished job's results: its destination table through the
// Storage Read API, or getQueryResults pages when storage is unavailable.
func (rn *Runner) OpenJob(ref bq.JobRef) *Run {
	r, ctx := rn.newRun("job", "Results of "+ref.JobID)

	go func() {
		defer close(r.done)

		r.finish(ctx, rn.openJob(ctx, r, ref))
	}()

	rn.notify(r)

	return r
}

func (rn *Runner) openJob(ctx context.Context, r *Run, ref bq.JobRef) error {
	if rn.opts.Jobs == nil {
		return errors.New("app: job history is unavailable")
	}

	job, err := rn.opts.Jobs.GetJob(ctx, ref)
	if err != nil {
		return err
	}

	r.update(func(s *RunSnapshot) {
		s.Job = &ref
		s.progress(job)
		s.Result = jobResult(job)
	})

	if job.Status != nil && job.Status.ErrorResult != nil {
		return fmt.Errorf("%w: %s", bq.ErrQueryFailed, job.Status.ErrorResult.Message)
	}

	dest := Destination(job)
	if dest == nil {
		return nil // no result set (DDL, DML, scripts)
	}

	r.update(func(s *RunSnapshot) { s.ResultTable = dest })

	if rn.opts.Storage != nil && rn.opts.Metadata != nil {
		t, terr := rn.opts.Metadata.GetTable(ctx, *dest)
		if terr != nil {
			return fmt.Errorf("results of %s are no longer available (temporary tables expire after about 24 hours): %w", ref.JobID, terr)
		}

		if t.Schema != nil {
			return rn.storageResult(ctx, r, results.FieldsFromREST(t.Schema.Fields), *dest, 0)
		}
	}

	page, err := rn.opts.Backend.ResultPage(ctx, ref, "", restPageRows)
	if err != nil {
		return err
	}

	if page.Schema == nil {
		return nil
	}

	rows, err := bq.DecodeRows(page.Schema.Fields, page.Rows)
	if err != nil {
		return err
	}

	return rn.restResult(ctx, r, results.FieldsFromREST(page.Schema.Fields), page.Schema.Fields, ref, rows, page.PageToken)
}

// jobResult summarizes a finished job for the status line.
func jobResult(j *bigquery.Job) *bq.QueryResult {
	res := &bq.QueryResult{}

	if st := j.Statistics; st != nil {
		res.SlotMs = st.TotalSlotMs

		if q := st.Query; q != nil {
			res.Statement, res.BytesProc, res.BytesBilled = q.StatementType, q.TotalBytesProcessed, q.TotalBytesBilled
			res.CacheHit, res.DMLAffected = q.CacheHit, q.NumDmlAffectedRows
		}
	}

	return res
}

func (rn *Runner) preview(ctx context.Context, r *Run, ref bq.TableRef) error {
	if rn.opts.Storage == nil {
		// Without storage (fake backend), fall back to a LIMIT query.
		//nolint:unqueryvet // previews show every column, like the console
		return rn.execute(ctx, r, bq.QueryRequest{SQL: fmt.Sprintf("SELECT * FROM %s LIMIT %d", ref.SQL(), previewRows)})
	}

	if rn.opts.Metadata == nil {
		return errors.New("app: preview needs table metadata")
	}

	t, err := rn.opts.Metadata.GetTable(ctx, ref)
	if err != nil {
		return err
	}

	if t.Type == "VIEW" || t.Type == "MATERIALIZED_VIEW" {
		return fmt.Errorf("preview is not available for %s; run a query instead", lowerType(t.Type))
	}

	if t.Schema == nil {
		return errors.New("app: table has no schema")
	}

	r.update(func(s *RunSnapshot) { s.ResultTable = &ref })

	return rn.storageResult(ctx, r, results.FieldsFromREST(t.Schema.Fields), ref, previewRows)
}

func (s *RunSnapshot) progress(j *bigquery.Job) {
	if j == nil || j.Statistics == nil {
		return
	}

	s.JobDetails = j

	s.SlotMs = j.Statistics.TotalSlotMs

	if q := j.Statistics.Query; q != nil {
		s.Stages, s.StagesDone = len(q.QueryPlan), 0

		for _, st := range q.QueryPlan {
			if st.Status == "COMPLETE" {
				s.StagesDone++
			}
		}
	}
}

func firstLine(sql string) string {
	for i, c := range sql {
		if c == '\n' {
			return sql[:i]
		}
	}

	return sql
}

func lowerType(t string) string {
	b := []byte(t)
	for i, c := range b {
		switch {
		case c == '_':
			b[i] = ' '
		case c >= 'A' && c <= 'Z':
			b[i] = c + 'a' - 'A'
		}
	}

	return string(b)
}

// View previews run a query, so they are capped in rows and time.
const (
	PreviewQueryRows    = 1024
	PreviewQueryTimeout = 10 * time.Second
)

// ErrPreviewTimeout means a view preview query did not finish in time; its
// job was cancelled.
var ErrPreviewTimeout = errors.New("app: preview did not load in time")

// PreviewQuery previews a view (or any table) with a LIMIT query billed to
// project. If it takes longer than timeout the job is cancelled and the run
// fails with ErrPreviewTimeout.
func (rn *Runner) PreviewQuery(ref bq.TableRef, project string, timeout time.Duration) *Run {
	r, ctx := rn.newRun("preview", "Preview of "+ref.String())

	go func() {
		defer close(r.done)

		var timedOut atomic.Bool

		timer := time.AfterFunc(timeout, func() {
			timedOut.Store(true)
			r.Cancel() // stops local work and cancels the BigQuery job
		})

		//nolint:unqueryvet // previews show every column, like the console
		err := rn.execute(ctx, r, bq.QueryRequest{
			SQL:     fmt.Sprintf("SELECT * FROM %s LIMIT %d", ref.SQL(), PreviewQueryRows),
			Project: project, MaxInlineRows: PreviewQueryRows,
		})

		timer.Stop()

		if timedOut.Load() {
			r.finish(context.Background(), fmt.Errorf("%w (%s)", ErrPreviewTimeout, timeout))

			return
		}

		r.finish(ctx, err)
	}()

	rn.notify(r)

	return r
}
