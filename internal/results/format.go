package results

import (
	"encoding/base64"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxCellRunes caps the characters a grid cell shows; the value inspector
// shows the full value.
const MaxCellRunes = 500

// FormatValue renders a value the way the BigQuery console grid does.
func FormatValue(typ string, v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return FormatFloat(x)
	case bool:
		return strconv.FormatBool(x)
	case []byte:
		return base64.StdEncoding.EncodeToString(x)
	case time.Time:
		return FormatTimestamp(x)
	case []any:
		return "[" + strconv.Itoa(len(x)) + " items]"
	}

	_ = typ

	return ""
}

// FormatCell renders a value for a single-line grid cell: newlines and tabs
// become visible symbols and long values are truncated with an ellipsis.
func FormatCell(typ string, v any) string {
	s := FormatValue(typ, v)
	if strings.ContainsAny(s, "\n\r\t") {
		s = strings.NewReplacer("\r\n", "⏎", "\n", "⏎", "\r", "⏎", "\t", "⇥").Replace(s)
	}

	if utf8.RuneCountInString(s) > MaxCellRunes {
		r := []rune(s)
		s = string(r[:MaxCellRunes]) + "…"
	}

	return s
}

// FormatFloat renders FLOAT64 like the console: shortest round-trip digits,
// plain notation for ordinary magnitudes, NaN and ±Infinity spelled out.
func FormatFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}

	if a := math.Abs(f); a != 0 && (a < 1e-6 || a >= 1e21) {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}

	return strconv.FormatFloat(f, 'f', -1, 64)
}

// FormatTimestamp renders a TIMESTAMP like the console:
// "2024-01-02 03:04:05.123456 UTC", omitting a zero fraction.
func FormatTimestamp(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02 15:04:05") + " UTC"
	}

	return t.Format("2006-01-02 15:04:05.000000") + " UTC"
}
