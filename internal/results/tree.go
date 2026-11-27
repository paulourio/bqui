package results

import (
	"strconv"
	"unicode/utf8"
)

// MaxInspectorRunes caps a single value shown in the value inspector.
const MaxInspectorRunes = 64 << 10

// ValueNode is one node of a row's value tree (value inspector).
type ValueNode struct {
	Name     string // field name or "[i]"
	Path     string // dotted path with indexes, e.g. "items[2].sku"
	Type     string // GoogleSQL type ("ARRAY<STRUCT>", "STRING", ...)
	Display  string // formatted value or a summary ("3 items", "4 fields")
	Null     bool
	Children []ValueNode
}

// Tree builds the value tree of one row.
func Tree(fields []Field, row []any) []ValueNode {
	out := make([]ValueNode, 0, len(fields))

	for i, f := range fields {
		var v any
		if i < len(row) {
			v = row[i]
		}

		out = append(out, fieldNode(f, f.Name, f.Name, v))
	}

	return out
}

func fieldNode(f Field, name, path string, v any) ValueNode {
	if !f.Repeated {
		return elemNode(f, name, path, v)
	}

	items, _ := v.([]any)
	n := ValueNode{Name: name, Path: path, Type: "ARRAY<" + typeName(f) + ">", Display: plural(len(items), "item")}

	for i, it := range items {
		idx := "[" + strconv.Itoa(i) + "]"
		n.Children = append(n.Children, elemNode(f, idx, path+idx, it))
	}

	return n
}

func elemNode(f Field, name, path string, v any) ValueNode {
	n := ValueNode{Name: name, Path: path, Type: typeName(f)}

	if v == nil {
		n.Display, n.Null = "null", true

		return n
	}

	if f.Type == TypeStruct {
		s, _ := v.([]any)
		n.Display = plural(len(f.Fields), "field")

		for i, c := range f.Fields {
			var cv any
			if i < len(s) {
				cv = s[i]
			}

			n.Children = append(n.Children, fieldNode(c, c.Name, path+"."+c.Name, cv))
		}

		return n
	}

	n.Display = FormatValue(f.Type, v)
	if utf8.RuneCountInString(n.Display) > MaxInspectorRunes {
		n.Display = string([]rune(n.Display)[:MaxInspectorRunes]) + "… (truncated; copy the value for all of it)"
	}

	return n
}

func typeName(f Field) string {
	if f.Type == TypeRange && f.RangeElem != "" {
		return "RANGE<" + f.RangeElem + ">"
	}

	return f.Type
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}

	return strconv.Itoa(n) + " " + unit + "s"
}

// ColumnPath returns the value-tree path of a leaf column for the row's
// visual line sub, e.g. "items[1].tags[0]" — used to reveal the clicked
// cell in the inspector. It returns the leaf's dotted name when the line
// does not map to an element.
func ColumnPath(fields []Field, row []any, col Column, sub int) string {
	path, ok := locate(fields, row, col.Path, sub)
	if !ok {
		return col.Name
	}

	return path
}

// locate walks the leaf path, picking the array element that starts at or
// before line sub.
func locate(fields []Field, vals []any, path []int, sub int) (string, bool) {
	if len(path) == 0 {
		return "", false
	}

	f := fields[path[0]]

	var v any
	if path[0] < len(vals) {
		v = vals[path[0]]
	}

	name := f.Name

	if f.Repeated {
		if i, it, rel, ok := elementAt(f, v, sub); ok {
			name = name + "[" + strconv.Itoa(i) + "]"
			v, sub = it, rel
		}
	}

	if len(path) == 1 {
		return name, true
	}

	s, _ := v.([]any)

	rest, ok := locate(f.Fields, s, path[1:], sub)
	if !ok {
		return name, true
	}

	return name + "." + rest, true
}

// elementAt finds the array element whose lines cover sub, and sub relative
// to that element.
func elementAt(f Field, v any, sub int) (index int, elem any, rel int, ok bool) {
	items, _ := v.([]any)
	line := 0

	for i, it := range items {
		n := elemLines(f, it)
		if sub < line+n {
			return i, it, sub - line, true
		}

		line += n
	}

	return 0, nil, 0, false
}
