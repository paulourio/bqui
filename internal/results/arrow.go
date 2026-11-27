package results

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
)

// ErrArrowMismatch is returned when an Arrow column does not fit its field.
var ErrArrowMismatch = errors.New("results: arrow column does not match schema")

const microsPerMilli = 1000

type getter func(i int) any

// RowsFromRecord converts an Arrow record batch from the Storage Read API
// into rows with exactly the values bq.DecodeRows produces for the same
// data over REST, so both paths render identically. Strings and bytes are
// copied: rows outlive the record.
func RowsFromRecord(fields []Field, rec arrow.RecordBatch) ([][]any, error) {
	if int(rec.NumCols()) != len(fields) {
		return nil, fmt.Errorf("%w: %d columns for %d fields", ErrArrowMismatch, rec.NumCols(), len(fields))
	}

	getters := make([]getter, len(fields))

	for i, f := range fields {
		g, err := fieldGetter(f, rec.Column(i))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}

		getters[i] = g
	}

	n := int(rec.NumRows())
	rows := make([][]any, n)

	for r := range n {
		row := make([]any, len(fields))
		for c, g := range getters {
			row[c] = g(r)
		}

		rows[r] = row
	}

	return rows, nil
}

func fieldGetter(f Field, arr arrow.Array) (getter, error) {
	if !f.Repeated {
		return valueGetter(f, arr)
	}

	list, ok := arr.(*array.List)
	if !ok {
		return nil, fmt.Errorf("%w: repeated field is %s", ErrArrowMismatch, arr.DataType())
	}

	elem, err := valueGetter(f, list.ListValues())
	if err != nil {
		return nil, err
	}

	return func(i int) any {
		out := []any{} // REST decodes NULL/empty arrays as empty slices

		if list.IsNull(i) {
			return out
		}

		start, end := list.ValueOffsets(i)
		for j := start; j < end; j++ {
			out = append(out, elem(int(j)))
		}

		return out
	}, nil
}

//nolint:gocyclo,cyclop,funlen // one case per BigQuery type
func valueGetter(f Field, arr arrow.Array) (getter, error) {
	nullable := func(g getter) getter {
		return func(i int) any {
			if arr.IsNull(i) {
				return nil
			}

			return g(i)
		}
	}

	if f.Type == TypeRange {
		return rangeGetter(f, arr)
	}

	switch a := arr.(type) {
	case *array.Int64:
		return nullable(func(i int) any { return a.Value(i) }), nil
	case *array.Float64:
		return nullable(func(i int) any { return a.Value(i) }), nil
	case *array.Boolean:
		return nullable(func(i int) any { return a.Value(i) }), nil
	case *array.String:
		return nullable(func(i int) any { return strings.Clone(a.Value(i)) }), nil
	case *array.LargeString:
		return nullable(func(i int) any { return strings.Clone(a.Value(i)) }), nil
	case *array.Binary:
		return nullable(func(i int) any { return append([]byte(nil), a.Value(i)...) }), nil
	case *array.Decimal128:
		dt, ok := a.DataType().(*arrow.Decimal128Type)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrArrowMismatch, a.DataType())
		}

		scale := dt.Scale

		return nullable(func(i int) any { return trimDecimal(a.Value(i).ToString(scale)) }), nil
	case *array.Decimal256:
		dt, ok := a.DataType().(*arrow.Decimal256Type)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrArrowMismatch, a.DataType())
		}

		scale := dt.Scale

		return nullable(func(i int) any { return trimDecimal(a.Value(i).ToString(scale)) }), nil
	case *array.Date32:
		return nullable(func(i int) any { return a.Value(i).ToTime().Format(time.DateOnly) }), nil
	case *array.Time64:
		tt, ok := a.DataType().(*arrow.Time64Type)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrArrowMismatch, a.DataType())
		}

		unit := tt.Unit

		return nullable(func(i int) any { return FormatTimeOfDay(a.Value(i).ToTime(unit)) }), nil
	case *array.Timestamp:
		tt, ok := a.DataType().(*arrow.TimestampType)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrArrowMismatch, a.DataType())
		}

		unit := tt.Unit
		if f.Type == TypeDatetime {
			return nullable(func(i int) any { return FormatDatetime(a.Value(i).ToTime(unit)) }), nil
		}

		return nullable(func(i int) any { return a.Value(i).ToTime(unit).UTC() }), nil
	case *array.MonthDayNanoInterval:
		return nullable(func(i int) any { return FormatInterval(a.Value(i)) }), nil
	case *array.Struct:
		return structGetter(f, a)
	}

	return nil, fmt.Errorf("%w: %s for %s", ErrArrowMismatch, arr.DataType(), f.Type)
}

func structGetter(f Field, a *array.Struct) (getter, error) {
	if a.NumField() != len(f.Fields) {
		return nil, fmt.Errorf("%w: struct has %d fields, schema %d", ErrArrowMismatch, a.NumField(), len(f.Fields))
	}

	subs := make([]getter, len(f.Fields))

	for j, sf := range f.Fields {
		g, err := fieldGetter(sf, a.Field(j))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", sf.Name, err)
		}

		subs[j] = g
	}

	return func(i int) any {
		if a.IsNull(i) {
			return nil
		}

		out := make([]any, len(subs))
		for j, g := range subs {
			out[j] = g(i)
		}

		return out
	}, nil
}

// rangeGetter renders RANGE<T> (an Arrow struct<start, end>) the way the
// REST path does: "[start, end)" with UNBOUNDED for NULL bounds.
func rangeGetter(f Field, arr arrow.Array) (getter, error) {
	st, ok := arr.(*array.Struct)
	if !ok || st.NumField() != 2 {
		return nil, fmt.Errorf("%w: RANGE is %s", ErrArrowMismatch, arr.DataType())
	}

	elem := Field{Name: "bound", Type: f.RangeElem}

	start, err := valueGetter(elem, st.Field(0))
	if err != nil {
		return nil, err
	}

	end, err := valueGetter(elem, st.Field(1))
	if err != nil {
		return nil, err
	}

	return func(i int) any {
		if st.IsNull(i) {
			return nil
		}

		return "[" + rangeBound(start(i)) + ", " + rangeBound(end(i)) + ")"
	}, nil
}

func rangeBound(v any) string {
	switch x := v.(type) {
	case nil:
		return "UNBOUNDED"
	case time.Time:
		return FormatTimestamp(x)
	case string:
		return x
	}

	return fmt.Sprint(v)
}

// trimDecimal turns "12.500000000" into "12.5" and "12.000000000" into "12".
func trimDecimal(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}

	s = strings.TrimRight(s, "0")

	return strings.TrimSuffix(s, ".")
}

func fraction(micros int) string {
	if micros == 0 {
		return ""
	}

	return fmt.Sprintf(".%06d", micros)
}

// FormatTimeOfDay renders TIME like BigQuery: "03:04:05" or "03:04:05.500000".
func FormatTimeOfDay(t time.Time) string {
	return t.Format("15:04:05") + fraction(t.Nanosecond()/int(time.Microsecond))
}

// FormatDatetime renders DATETIME like BigQuery: "2024-01-02T03:04:05[.ffffff]".
func FormatDatetime(t time.Time) string {
	return t.Format("2006-01-02T15:04:05") + fraction(t.Nanosecond()/int(time.Microsecond))
}

// FormatInterval renders INTERVAL like BigQuery's canonical form
// "Y-M D H:M:S[.fff|.ffffff]", e.g. "1-2 3 4:5:6.000001", "-1-2 0 0:0:0",
// "0-0 0 -26:1:1", "0-0 0 0:0:0.500".
func FormatInterval(v arrow.MonthDayNanoInterval) string {
	ym := ""

	months := int64(v.Months)
	if months < 0 {
		ym, months = "-", -months
	}

	ym += strconv.FormatInt(months/12, 10) + "-" + strconv.FormatInt(months%12, 10)

	micros := v.Nanoseconds / int64(time.Microsecond)

	sign := ""
	if micros < 0 {
		sign, micros = "-", -micros
	}

	secs := micros / 1_000_000
	frac := micros % 1_000_000

	hms := fmt.Sprintf("%s%d:%d:%d", sign, secs/3600, secs%3600/60, secs%60)

	switch {
	case frac == 0:
	case frac%microsPerMilli == 0:
		hms += fmt.Sprintf(".%03d", frac/microsPerMilli)
	default:
		hms += fmt.Sprintf(".%06d", frac)
	}

	return ym + " " + strconv.FormatInt(int64(v.Days), 10) + " " + hms
}
