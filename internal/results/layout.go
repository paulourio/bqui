package results

// CellKind distinguishes what a grid cell shows.
type CellKind uint8

// Cell kinds.
const (
	// Blank is a continuation line: the value lives on an earlier line, or a
	// sibling array is shorter, or the array is empty.
	Blank CellKind = iota
	// Value holds a non-NULL value.
	Value
	// Null is an explicit SQL NULL.
	Null
)

// Cell is one rendered grid cell.
type Cell struct {
	Kind CellKind
	V    any
}

// MaxLinesPerRow caps how many visual lines one logical row may take; rows
// beyond it are truncated (the value inspector shows them in full).
const MaxLinesPerRow = 10_000

// Lines returns how many visual lines a row occupies in the console layout:
//
//	lines(scalar)   = 1
//	lines(STRUCT)   = max(lines(children)), 1 if NULL
//	lines(REPEATED) = Σ lines(element), 1 if empty
func Lines(fields []Field, row []any) int {
	n := 1

	for i, f := range fields {
		var v any
		if i < len(row) {
			v = row[i]
		}

		n = max(n, fieldLines(f, v))
	}

	return min(n, MaxLinesPerRow)
}

func fieldLines(f Field, v any) int {
	if !f.Repeated {
		return elemLines(f, v)
	}

	items, _ := v.([]any)
	if len(items) == 0 {
		return 1
	}

	n := 0
	for _, it := range items {
		n += elemLines(f, it)
	}

	return n
}

func elemLines(f Field, v any) int {
	if !IsStructType(f.Type) || len(f.Fields) == 0 || v == nil {
		return 1
	}

	s, _ := v.([]any)
	n := 1

	for i, c := range f.Fields {
		var cv any
		if i < len(s) {
			cv = s[i]
		}

		n = max(n, fieldLines(c, cv))
	}

	return n
}

// Render lays out one row: cells[col][line] for every leaf column and each
// of the row's lines. Values sit on the line where their element starts;
// everything else is Blank.
func Render(fields []Field, row []any) [][]Cell {
	lines := Lines(fields, row)
	cols := 0

	for _, f := range fields {
		cols += leafCount(f)
	}

	cells := make([][]Cell, cols)
	backing := make([]Cell, cols*lines)

	for c := range cells {
		cells[c] = backing[c*lines : (c+1)*lines]
	}

	r := renderer{cells: cells, lines: lines}
	col := 0

	for i, f := range fields {
		var v any
		if i < len(row) {
			v = row[i]
		}

		r.field(f, v, col, 0)
		col += leafCount(f)
	}

	return cells
}

type renderer struct {
	cells [][]Cell
	lines int
}

func (r *renderer) set(col, line int, c Cell) {
	if line < r.lines {
		r.cells[col][line] = c
	}
}

// field renders f's value starting at line and returns the lines it used.
func (r *renderer) field(f Field, v any, col, line int) int {
	if !f.Repeated {
		return r.elem(f, v, col, line)
	}

	items, _ := v.([]any)
	if len(items) == 0 {
		return 1 // empty array: blank leaves
	}

	cur := line
	for _, it := range items {
		if cur >= r.lines {
			break
		}

		cur += r.elem(f, it, col, cur)
	}

	return cur - line
}

func (r *renderer) elem(f Field, v any, col, line int) int {
	if !IsStructType(f.Type) || len(f.Fields) == 0 {
		if v == nil {
			r.set(col, line, Cell{Kind: Null})
		} else {
			r.set(col, line, Cell{Kind: Value, V: v})
		}

		return 1
	}

	if v == nil { // NULL struct: every leaf shows null
		for c := range leafCount(f) {
			r.set(col+c, line, Cell{Kind: Null})
		}

		return 1
	}

	s, _ := v.([]any)
	used, c := 1, col

	for i, sub := range f.Fields {
		var sv any
		if i < len(s) {
			sv = s[i]
		}

		used = max(used, r.field(sub, sv, c, line))
		c += leafCount(sub)
	}

	return used
}
