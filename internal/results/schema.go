// Package results models query results for display: the schema, flattening
// into leaf columns, the console-style sub-row layout of repeated fields,
// value formatting, and chunked row sources with bounded memory. It is
// UI-agnostic and tested on Linux.
package results

import (
	"strings"

	bigquery "google.golang.org/api/bigquery/v2"
)

// Normalized GoogleSQL type names.
const (
	TypeInt64      = "INT64"
	TypeFloat64    = "FLOAT64"
	TypeBool       = "BOOL"
	TypeString     = "STRING"
	TypeBytes      = "BYTES"
	TypeNumeric    = "NUMERIC"
	TypeBigNumeric = "BIGNUMERIC"
	TypeTimestamp  = "TIMESTAMP"
	TypeDate       = "DATE"
	TypeTime       = "TIME"
	TypeDatetime   = "DATETIME"
	TypeGeography  = "GEOGRAPHY"
	TypeJSON       = "JSON"
	TypeInterval   = "INTERVAL"
	TypeRange      = "RANGE"
	TypeStruct     = "STRUCT"
)

// Field is one result field. Values follow the bq.DecodeRows conventions:
// a STRUCT value is []any in field order, a repeated value is []any of
// elements (never nil for an empty array), NULL is nil.
type Field struct {
	Name      string
	Type      string // normalized (see Type* constants)
	Repeated  bool
	Fields    []Field // STRUCT children
	RangeElem string  // element type of RANGE
}

// FieldsFromREST converts a REST schema.
func FieldsFromREST(fs []*bigquery.TableFieldSchema) []Field {
	out := make([]Field, 0, len(fs))

	for _, f := range fs {
		nf := Field{Name: f.Name, Type: NormalizeType(f.Type), Repeated: f.Mode == "REPEATED"}
		if len(f.Fields) > 0 {
			nf.Fields = FieldsFromREST(f.Fields)
		}

		if f.RangeElementType != nil {
			nf.RangeElem = NormalizeType(f.RangeElementType.Type)
		}

		out = append(out, nf)
	}

	return out
}

// NormalizeType maps legacy REST names to GoogleSQL names.
func NormalizeType(t string) string {
	switch strings.ToUpper(t) {
	case "INTEGER":
		return TypeInt64
	case "FLOAT":
		return TypeFloat64
	case "BOOLEAN":
		return TypeBool
	case "RECORD":
		return TypeStruct
	default:
		return strings.ToUpper(t)
	}
}

// Column is one leaf column of the flattened grid.
type Column struct {
	Name     string // dotted path, e.g. "items.sku"
	Type     string
	Path     []int // field index at each nesting level
	Repeated bool  // the leaf itself or an ancestor is repeated
}

// Numeric reports whether values right-align.
func (c Column) Numeric() bool {
	switch c.Type {
	case TypeInt64, TypeFloat64, TypeNumeric, TypeBigNumeric:
		return true
	}

	return false
}

// Leaves flattens fields into leaf columns, depth-first. STRUCT fields
// (repeated or not) expand into their children; nothing is shown as JSON.
func Leaves(fields []Field) []Column {
	var out []Column

	var walk func(fs []Field, prefix string, path []int, repeated bool)

	walk = func(fs []Field, prefix string, path []int, repeated bool) {
		for i, f := range fs {
			p := append(append([]int(nil), path...), i)
			name := prefix + f.Name
			rep := repeated || f.Repeated

			if f.Type == TypeStruct && len(f.Fields) > 0 {
				walk(f.Fields, name+".", p, rep)

				continue
			}

			out = append(out, Column{Name: name, Type: f.Type, Path: p, Repeated: rep})
		}
	}

	walk(fields, "", nil, false)

	return out
}

// leafCount returns how many leaf columns f occupies.
func leafCount(f Field) int {
	if f.Type != TypeStruct || len(f.Fields) == 0 {
		return 1
	}

	n := 0
	for _, c := range f.Fields {
		n += leafCount(c)
	}

	return n
}
