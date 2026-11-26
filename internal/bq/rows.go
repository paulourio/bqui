package bq

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	bigquery "google.golang.org/api/bigquery/v2"
)

// ErrBadRow is returned when a REST row does not match its schema.
var ErrBadRow = errors.New("bq: row does not match schema")

// Decoded values, by BigQuery type:
//
//	NULL                         nil
//	INT64                        int64
//	FLOAT64                      float64 (NaN and ±Inf included)
//	BOOL                         bool
//	BYTES                        []byte
//	TIMESTAMP                    time.Time (UTC, microsecond precision)
//	STRING, NUMERIC, BIGNUMERIC,
//	DATE, TIME, DATETIME,
//	GEOGRAPHY, JSON, INTERVAL,
//	RANGE                        string, exactly as BigQuery sent it
//	STRUCT/RECORD                []any, one value per field, in schema order
//	REPEATED (any type)          []any of the element values (never nil)

// DecodeRows converts REST rows into Go values following fields.
func DecodeRows(fields []*bigquery.TableFieldSchema, rows []*bigquery.TableRow) ([][]any, error) {
	out := make([][]any, 0, len(rows))

	for i, r := range rows {
		cells := make([]any, len(r.F))
		for j, c := range r.F {
			cells[j] = c.V
		}

		vals, err := decodeStruct(fields, cells)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", i, err)
		}

		out = append(out, vals)
	}

	return out, nil
}

func decodeStruct(fields []*bigquery.TableFieldSchema, cells []any) ([]any, error) {
	if len(cells) != len(fields) {
		return nil, fmt.Errorf("%w: %d cells for %d fields", ErrBadRow, len(cells), len(fields))
	}

	vals := make([]any, len(fields))

	for i, f := range fields {
		v, err := decodeField(f, cells[i])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}

		vals[i] = v
	}

	return vals, nil
}

func decodeField(f *bigquery.TableFieldSchema, raw any) (any, error) {
	if f.Mode != "REPEATED" {
		return decodeValue(f, raw)
	}

	// Repeated values arrive as [{"v": ...}, ...]; NULL arrays arrive as null or [].
	items, _ := raw.([]any)
	out := make([]any, 0, len(items))

	for _, it := range items {
		v, err := decodeValue(f, cellValue(it))
		if err != nil {
			return nil, err
		}

		out = append(out, v)
	}

	return out, nil
}

func cellValue(raw any) any {
	if m, ok := raw.(map[string]any); ok {
		return m["v"]
	}

	return raw
}

func decodeValue(f *bigquery.TableFieldSchema, raw any) (any, error) {
	if raw == nil {
		return nil, nil //nolint:nilnil // NULL is a valid value
	}

	if isRecord(f.Type) {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: RECORD is %T", ErrBadRow, raw)
		}

		cells, _ := m["f"].([]any)
		vals := make([]any, len(cells))

		for i, c := range cells {
			vals[i] = cellValue(c)
		}

		return decodeStruct(f.Fields, vals)
	}

	s, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("%w: %s value is %T", ErrBadRow, f.Type, raw)
	}

	return parseScalar(f.Type, s)
}

func isRecord(t string) bool { return t == "RECORD" || t == "STRUCT" }

func parseScalar(typ, s string) (any, error) {
	switch typ {
	case "INTEGER", "INT64":
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: INT64 %q", ErrBadRow, s)
		}

		return v, nil
	case "FLOAT", "FLOAT64":
		return parseFloat(s)
	case "BOOLEAN", "BOOL":
		return s == "true", nil
	case "BYTES":
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("%w: BYTES not base64", ErrBadRow)
		}

		return b, nil
	case "TIMESTAMP":
		return parseTimestamp(s)
	default:
		return s, nil
	}
}

func parseFloat(s string) (float64, error) {
	switch s {
	case "NaN":
		return math.NaN(), nil
	case "Infinity":
		return math.Inf(1), nil
	case "-Infinity":
		return math.Inf(-1), nil
	}

	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: FLOAT64 %q", ErrBadRow, s)
	}

	return v, nil
}

// parseTimestamp accepts int64 microseconds (useInt64Timestamp=true) and the
// legacy float-seconds form.
func parseTimestamp(s string) (time.Time, error) {
	if !strings.ContainsAny(s, ".eE") {
		us, err := strconv.ParseInt(s, 10, 64)
		if err == nil {
			return time.UnixMicro(us).UTC(), nil
		}
	}

	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: TIMESTAMP %q", ErrBadRow, s)
	}

	return time.UnixMicro(int64(math.Round(f * 1e6))).UTC(), nil
}

// AppendRowJSON appends one decoded row as a JSON object in the BigQuery
// console's JSON encoding: field order preserved, INT64/NUMERIC/BIGNUMERIC as
// strings, BYTES as base64, TIMESTAMP as "YYYY-MM-DD HH:MM:SS[.ffffff] UTC",
// FLOAT64 NaN/Infinity as strings.
func AppendRowJSON(dst []byte, fields []*bigquery.TableFieldSchema, row []any) []byte {
	return appendStruct(dst, fields, row)
}

func appendStruct(dst []byte, fields []*bigquery.TableFieldSchema, vals []any) []byte {
	dst = append(dst, '{')

	for i, f := range fields {
		if i > 0 {
			dst = append(dst, ',')
		}

		dst = appendJSONString(dst, f.Name)
		dst = append(dst, ':')

		var v any
		if i < len(vals) {
			v = vals[i]
		}

		dst = appendFieldJSON(dst, f, v)
	}

	return append(dst, '}')
}

func appendFieldJSON(dst []byte, f *bigquery.TableFieldSchema, v any) []byte {
	if f.Mode == "REPEATED" {
		items, _ := v.([]any)
		dst = append(dst, '[')

		for i, it := range items {
			if i > 0 {
				dst = append(dst, ',')
			}

			dst = appendValueJSON(dst, f, it)
		}

		return append(dst, ']')
	}

	return appendValueJSON(dst, f, v)
}

func appendValueJSON(dst []byte, f *bigquery.TableFieldSchema, v any) []byte {
	switch x := v.(type) {
	case nil:
		return append(dst, "null"...)
	case []any:
		if isRecord(f.Type) {
			return appendStruct(dst, f.Fields, x)
		}
	case int64:
		return appendJSONString(dst, strconv.FormatInt(x, 10))
	case float64:
		switch {
		case math.IsNaN(x):
			return appendJSONString(dst, "NaN")
		case math.IsInf(x, 1):
			return appendJSONString(dst, "Infinity")
		case math.IsInf(x, -1):
			return appendJSONString(dst, "-Infinity")
		}

		return strconv.AppendFloat(dst, x, 'g', -1, 64)
	case bool:
		return strconv.AppendBool(dst, x)
	case []byte:
		return appendJSONString(dst, base64.StdEncoding.EncodeToString(x))
	case time.Time:
		return appendJSONString(dst, FormatTimestamp(x))
	case string:
		if f.Type == "JSON" && json.Valid([]byte(x)) {
			var buf bytes.Buffer
			if json.Compact(&buf, []byte(x)) == nil {
				return append(dst, buf.Bytes()...)
			}
		}

		return appendJSONString(dst, x)
	}

	b, _ := json.Marshal(v)

	return append(dst, b...)
}

func appendJSONString(dst []byte, s string) []byte {
	b, _ := json.Marshal(s)

	return append(dst, b...)
}

// FormatTimestamp renders a TIMESTAMP like the BigQuery console:
// "2024-01-02 03:04:05.123456 UTC", omitting a zero fractional part.
func FormatTimestamp(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02 15:04:05") + " UTC"
	}

	return t.Format("2006-01-02 15:04:05.000000") + " UTC"
}
