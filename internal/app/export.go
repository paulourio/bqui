package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/export"
	"github.com/paulourio/bqui/internal/results"
)

// ExportSource is what to export: a table through the Storage Read API
// (any format), or an in-memory grid (row formats only).
type ExportSource struct {
	Table *bq.TableRef
	Grid  *results.Grid
}

// ExportProgress reports an export (delivered on the UI thread).
type ExportProgress struct {
	Path       string
	Rows       int64
	Bytes      int64
	EstRows    int64
	Elapsed    time.Duration
	Done       bool
	Err        error
	Cancelled  bool
	ScannedEst int64 // Storage Read API bytes (pricing hint)
}

// ErrNoSpace is returned when the destination volume is clearly too small.
var ErrNoSpace = errors.New("app: not enough free disk space")

const (
	exportBuffer  = 1 << 20
	exportChunk   = 1000
	exportUpdates = 200 * time.Millisecond
	partialSuffix = ".partial"
	filePerm      = 0o644
)

// Export writes src to path in format f. It returns a cancel function;
// progress is posted to the UI thread until a final update with Done set.
func (rn *Runner) Export(src ExportSource, path string, f export.Format, opts export.Options, progress func(ExportProgress)) func() {
	ctx, cancel := context.WithCancel(context.Background())
	ex := &exporter{rn: rn, src: src, path: path, format: f, opts: opts, progress: progress, started: time.Now()}

	go func() {
		err := ex.run(ctx)
		ex.mu.Lock()
		p := ex.p
		ex.mu.Unlock()

		p.Done, p.Elapsed = true, time.Since(ex.started)
		p.Cancelled = ctx.Err() != nil && err != nil

		if !p.Cancelled {
			p.Err = err
		}

		rn.opts.Post(func() { progress(p) })
	}()

	return cancel
}

type exporter struct {
	rn       *Runner
	src      ExportSource
	path     string
	format   export.Format
	opts     export.Options
	progress func(ExportProgress)
	started  time.Time

	mu       sync.Mutex
	p        ExportProgress
	lastPost time.Time
}

func (ex *exporter) report(rows, bytes int64) {
	ex.mu.Lock()
	ex.p.Rows += rows
	ex.p.Bytes = bytes
	ex.p.Elapsed = time.Since(ex.started)
	p := ex.p
	post := time.Since(ex.lastPost) >= exportUpdates

	if post {
		ex.lastPost = time.Now()
	}
	ex.mu.Unlock()

	if post {
		ex.rn.opts.Post(func() { ex.progress(p) })
	}
}

func (ex *exporter) run(ctx context.Context) (err error) {
	ex.p.Path = ex.path

	tmp := ex.path + partialSuffix

	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, filePerm)
	if err != nil {
		return fmt.Errorf("app: export: %w", err)
	}

	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()

	cw := &countingWriter{w: bufio.NewWriterSize(f, exportBuffer)}

	switch {
	case ex.src.Table != nil && ex.rn.opts.Storage != nil:
		err = ex.fromStorage(ctx, cw)
	case ex.src.Grid != nil:
		err = ex.fromGrid(ctx, cw)
	default:
		err = errors.New("app: nothing to export (results have no destination table)")
	}

	if err != nil {
		return err
	}

	if err = cw.w.Flush(); err != nil {
		return fmt.Errorf("app: export: %w", err)
	}

	if err = f.Close(); err != nil {
		return fmt.Errorf("app: export: %w", err)
	}

	if err = os.Rename(tmp, ex.path); err != nil {
		return fmt.Errorf("app: export: %w", err)
	}

	ex.report(0, cw.n)

	return nil
}

func (ex *exporter) fromStorage(ctx context.Context, cw *countingWriter) error {
	fields, sess, err := ex.openTable(ctx)
	if err != nil {
		return err
	}

	if ex.format.Columnar() {
		return ex.writeRecords(ctx, cw, sess)
	}

	return ex.writeRows(ctx, cw, sess, fields)
}

// openTable checks the table and opens a read session.
func (ex *exporter) openTable(ctx context.Context) ([]results.Field, StorageSession, error) {
	if ex.rn.opts.Metadata == nil {
		return nil, nil, errors.New("app: export needs table metadata")
	}

	t, err := ex.rn.opts.Metadata.GetTable(ctx, *ex.src.Table)
	if err != nil {
		return nil, nil, err
	}

	if t.Type == "VIEW" || t.Type == "MATERIALIZED_VIEW" {
		return nil, nil, fmt.Errorf("cannot export a %s directly; query it and export the results", lowerType(t.Type))
	}

	if t.Schema == nil {
		return nil, nil, errors.New("app: table has no schema")
	}

	sess, err := ex.rn.opts.Storage(ctx, *ex.src.Table)
	if err != nil {
		return nil, nil, err
	}

	ex.mu.Lock()
	ex.p.EstRows, ex.p.ScannedEst = sess.EstimatedRows(), sess.EstimatedBytes()
	ex.mu.Unlock()

	if err := checkSpace(filepath.Dir(ex.path), sess.EstimatedBytes()); err != nil {
		return nil, nil, err
	}

	return results.FieldsFromREST(t.Schema.Fields), sess, nil
}

// writeRecords streams Arrow records into Parquet or Arrow IPC.
func (ex *exporter) writeRecords(ctx context.Context, cw *countingWriter, sess StorageSession) error {
	rw, err := export.NewRecordWriter(cw, ex.format, sess.Schema())
	if err != nil {
		return err
	}

	for b, berr := range sess.RawBatches(ctx) {
		if berr != nil {
			return berr
		}

		rec, derr := sess.DecodeRecord(b.Data)
		if derr != nil {
			return derr
		}

		werr := rw.Write(rec)
		n := rec.NumRows()
		rec.Release()

		if werr != nil {
			return werr
		}

		ex.report(n, cw.n)
	}

	return rw.Close()
}

// writeRows streams decoded rows into CSV or NDJSON.
func (ex *exporter) writeRows(ctx context.Context, cw *countingWriter, sess StorageSession, fields []results.Field) error {
	rw, err := export.NewRowWriter(cw, ex.format, fields, ex.opts)
	if err != nil {
		return err
	}

	for b, berr := range sess.RawBatches(ctx) {
		if berr != nil {
			return berr
		}

		rows, derr := sess.DecodeRows(fields, b.Data)
		if derr != nil {
			return derr
		}

		if werr := rw.WriteRows(rows); werr != nil {
			return werr
		}

		ex.report(int64(len(rows)), cw.n)
	}

	return rw.Close()
}

func (ex *exporter) fromGrid(ctx context.Context, cw *countingWriter) error {
	if ex.format.Columnar() {
		return export.ErrColumnar
	}

	g := ex.src.Grid
	total, _, _ := g.Size()

	ex.mu.Lock()
	ex.p.EstRows = total
	ex.mu.Unlock()

	rw, err := export.NewRowWriter(cw, ex.format, g.Fields(), ex.opts)
	if err != nil {
		return err
	}

	batch := make([][]any, 0, exportChunk)

	for i := range total {
		if err := ctx.Err(); err != nil {
			return err
		}

		row, err := g.Row(i)
		if err != nil {
			return err
		}

		batch = append(batch, row)

		if len(batch) == exportChunk || i == total-1 {
			if err := rw.WriteRows(batch); err != nil {
				return err
			}

			ex.report(int64(len(batch)), cw.n)
			batch = batch[:0]
		}
	}

	return rw.Close()
}

type countingWriter struct {
	w *bufio.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)

	return n, err
}

// checkSpace fails when the volume has less free space than the estimate.
func checkSpace(dir string, need int64) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return nil //nolint:nilerr // best effort: unknown free space does not block exports
	}

	free := int64(st.Bavail) * int64(st.Bsize) //nolint:gosec,unconvert,nolintlint // field widths differ by OS (uint32 on macOS, int64 on Linux)
	if need > 0 && free < need {
		return fmt.Errorf("%w: need about %s, %s free", ErrNoSpace, Bytes(need), Bytes(free))
	}

	return nil
}

// StorageReadUSD estimates Storage Read API cost (on-demand, per TiB read).
func StorageReadUSD(bytes int64) float64 {
	const pricePerTiB = 1.10

	return float64(bytes) / (1 << 40) * pricePerTiB
}
