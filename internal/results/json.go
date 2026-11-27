package results

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"math"
	"strconv"
	"time"
)

// AppendRowJSON appends one row as a JSON object in the BigQuery console's
// JSON encoding: field order preserved, INT64/NUMERIC/BIGNUMERIC as strings,
// BYTES as base64, TIMESTAMP as "YYYY-MM-DD HH:MM:SS[.ffffff] UTC", FLOAT64
// NaN/Infinity as strings, JSON values inlined.
func AppendRowJSON(dst []byte, fields []Field, row []any) []byte {
	return appendStruct(dst, fields, row)
}

func appendStruct(dst []byte, fields []Field, vals []any) []byte {
	dst = append(dst, '{')

	for i, f := range fields {
		if i > 0 {
			dst = append(dst, ',')
		}

		dst = appendString(dst, f.Name)
		dst = append(dst, ':')

		var v any
		if i < len(vals) {
			v = vals[i]
		}

		if f.Repeated {
			items, _ := v.([]any)
			dst = append(dst, '[')

			for j, it := range items {
				if j > 0 {
					dst = append(dst, ',')
				}

				dst = appendValue(dst, f, it)
			}

			dst = append(dst, ']')

			continue
		}

		dst = appendValue(dst, f, v)
	}

	return append(dst, '}')
}

func appendValue(dst []byte, f Field, v any) []byte {
	switch x := v.(type) {
	case nil:
		return append(dst, "null"...)
	case []any:
		if f.Type == TypeStruct {
			return appendStruct(dst, f.Fields, x)
		}
	case int64:
		return appendString(dst, strconv.FormatInt(x, 10))
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return appendString(dst, FormatFloat(x))
		}

		return strconv.AppendFloat(dst, x, 'g', -1, 64)
	case bool:
		return strconv.AppendBool(dst, x)
	case []byte:
		return appendString(dst, base64.StdEncoding.EncodeToString(x))
	case time.Time:
		return appendString(dst, FormatTimestamp(x))
	case string:
		if f.Type == TypeJSON {
			var buf bytes.Buffer
			if json.Compact(&buf, []byte(x)) == nil {
				return append(dst, buf.Bytes()...)
			}
		}

		return appendString(dst, x)
	}

	b, _ := json.Marshal(v)

	return append(dst, b...)
}

func appendString(dst []byte, s string) []byte {
	b, _ := json.Marshal(s)

	return append(dst, b...)
}
