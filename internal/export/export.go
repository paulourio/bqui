// Package export writes BigQuery results to files: CSV (flattened
// console-style lines or JSON cells), newline-delimited JSON, Parquet and
// Arrow IPC. Row formats take decoded rows; columnar formats take Arrow
// records straight from the Storage Read API, preserving nesting.
package export

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"

	"github.com/paulourio/bqui/internal/results"
)

// Format is an export file format.
type Format int

// Formats.
const (
	CSV Format = iota
	NDJSON
	Parquet
	Arrow
)

var formatInfo = [...]struct {
	name, ext string
	columnar  bool
}{
	CSV:     {"CSV", "csv", false},
	NDJSON:  {"Newline-delimited JSON", "jsonl", false},
	Parquet: {"Parquet", "parquet", true},
	Arrow:   {"Arrow IPC", "arrow", true},
}

func (f Format) String() string { return formatInfo[f].name }

// Ext is the file extension (without dot).
func (f Format) Ext() string { return formatInfo[f].ext }

// Columnar formats need Arrow records (Storage Read API sources).
func (f Format) Columnar() bool { return formatInfo[f].columnar }

// Formats lists every format.
func Formats() []Format { return []Format{CSV, NDJSON, Parquet, Arrow} }

// CSVNesting chooses how CSV represents nested and repeated data.
type CSVNesting int

// CSV nesting modes.
const (
	// CSVLines flattens like the grid: leaf columns, repeated values on
	// continuation lines (blank cells elsewhere).
	CSVLines CSVNesting = iota
	// CSVJSONCells keeps one line per row and writes STRUCT/ARRAY fields as
	// JSON text (CSV export only; the grid never does this).
	CSVJSONCells
)

// Options tunes row formats.
type Options struct {
	CSVNesting CSVNesting
	NoHeader   bool
}

// ErrColumnar is returned when a columnar format is given rows.
var ErrColumnar = errors.New("export: Parquet and Arrow need a Storage Read API source")

// RowWriter writes decoded rows.
type RowWriter interface {
	WriteRows(rows [][]any) error
	Close() error
}

// NewRowWriter creates a CSV or NDJSON writer.
func NewRowWriter(w io.Writer, f Format, fields []results.Field, opts Options) (RowWriter, error) {
	switch f {
	case CSV:
		return newCSV(w, fields, opts)
	case NDJSON:
		return &ndjsonWriter{w: w, fields: fields}, nil
	case Parquet, Arrow:
	}

	return nil, ErrColumnar
}

type ndjsonWriter struct {
	w      io.Writer
	fields []results.Field
	buf    []byte
}

func (n *ndjsonWriter) WriteRows(rows [][]any) error {
	n.buf = n.buf[:0]

	for _, r := range rows {
		n.buf = append(results.AppendRowJSON(n.buf, n.fields, r), '\n')
	}

	_, err := n.w.Write(n.buf)

	return err
}

func (n *ndjsonWriter) Close() error { return nil }

type csvWriter struct {
	cw     *csv.Writer
	fields []results.Field
	cols   []results.Column
	mode   CSVNesting
}

func newCSV(w io.Writer, fields []results.Field, opts Options) (*csvWriter, error) {
	c := &csvWriter{cw: csv.NewWriter(w), fields: fields, cols: results.Leaves(fields), mode: opts.CSVNesting}

	if opts.NoHeader {
		return c, nil
	}

	var header []string

	if c.mode == CSVJSONCells {
		for _, f := range fields {
			header = append(header, f.Name)
		}
	} else {
		for _, col := range c.cols {
			header = append(header, col.Name)
		}
	}

	return c, c.cw.Write(header)
}

func (c *csvWriter) WriteRows(rows [][]any) error {
	for _, r := range rows {
		var err error
		if c.mode == CSVJSONCells {
			err = c.writeJSONCells(r)
		} else {
			err = c.writeLines(r)
		}

		if err != nil {
			return err
		}
	}

	c.cw.Flush()

	return c.cw.Error()
}

func (c *csvWriter) writeLines(row []any) error {
	cells := results.Render(c.fields, row)
	if len(cells) == 0 {
		return nil
	}

	lines := len(cells[0])
	rec := make([]string, len(cells))

	for l := range lines {
		for i := range cells {
			rec[i] = csvCell(c.cols[i].Type, cells[i][l])
		}

		if err := c.cw.Write(rec); err != nil {
			return err
		}
	}

	return nil
}

func (c *csvWriter) writeJSONCells(row []any) error {
	rec := make([]string, len(c.fields))

	for i, f := range c.fields {
		var v any
		if i < len(row) {
			v = row[i]
		}

		switch {
		case f.Repeated || results.IsStructType(f.Type):
			// Encode the field as a one-field object and keep only its value.
			obj := string(results.AppendRowJSON(nil, []results.Field{f}, []any{v}))
			key, _ := json.Marshal(f.Name)
			rec[i] = strings.TrimSuffix(strings.TrimPrefix(obj, "{"+string(key)+":"), "}")
		case v == nil:
			rec[i] = ""
		default:
			rec[i] = results.FormatValue(f.Type, v)
		}
	}

	return c.cw.Write(rec)
}

// csvCell renders a grid cell: NULL and blanks are empty (BigQuery's CSV
// convention), values use the console formatting.
func csvCell(typ string, c results.Cell) string {
	if c.Kind != results.Value {
		return ""
	}

	return results.FormatValue(typ, c.V)
}

func (c *csvWriter) Close() error {
	c.cw.Flush()

	return c.cw.Error()
}

// RecordWriter writes Arrow records.
type RecordWriter interface {
	Write(rec arrow.RecordBatch) error
	Close() error
}

// NewRecordWriter creates a Parquet (zstd) or Arrow IPC file writer.
func NewRecordWriter(w io.Writer, f Format, schema *arrow.Schema) (RecordWriter, error) {
	switch f {
	case Parquet:
		props := parquet.NewWriterProperties(parquet.WithCompression(compress.Codecs.Zstd), parquet.WithDictionaryDefault(true))

		fw, err := pqarrow.NewFileWriter(schema, w, props, pqarrow.NewArrowWriterProperties(pqarrow.WithStoreSchema()))
		if err != nil {
			return nil, fmt.Errorf("export: parquet: %w", err)
		}

		return fw, nil
	case Arrow:
		fw, err := ipc.NewFileWriter(w, ipc.WithSchema(schema), ipc.WithZstd())
		if err != nil {
			return nil, fmt.Errorf("export: arrow: %w", err)
		}

		return fw, nil
	case CSV, NDJSON:
	}

	return nil, fmt.Errorf("export: %s is a row format", f)
}
