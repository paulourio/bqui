package results

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// ordersFields is the schema from the M0 discussion: a struct, a repeated
// struct containing a repeated scalar, and scalars.
func ordersFields() []Field {
	return []Field{
		{Name: "order_id", Type: TypeInt64},
		{Name: "customer", Type: TypeStruct, Fields: []Field{
			{Name: "name", Type: TypeString},
			{Name: "tier", Type: TypeString},
		}},
		{Name: "items", Type: TypeStruct, Repeated: true, Fields: []Field{
			{Name: "sku", Type: TypeString},
			{Name: "qty", Type: TypeInt64},
			{Name: "tags", Type: TypeString, Repeated: true},
		}},
	}
}

func order(id int64, name string, tier any, items ...[]any) []any {
	its := make([]any, 0, len(items))
	for _, it := range items {
		its = append(its, it)
	}

	return []any{id, []any{name, tier}, its}
}

func item(sku string, qty int64, tags ...string) []any {
	ts := make([]any, 0, len(tags))
	for _, t := range tags {
		ts = append(ts, t)
	}

	return []any{sku, qty, ts}
}

func TestLeavesFlattenNestedColumns(t *testing.T) {
	var names []string
	for _, c := range Leaves(ordersFields()) {
		names = append(names, c.Name)
	}

	want := "order_id customer.name customer.tier items.sku items.qty items.tags"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("leaves = %s", got)
	}

	cols := Leaves(ordersFields())
	if cols[0].Repeated || !cols[3].Repeated || !cols[5].Repeated || !cols[0].Numeric() || cols[3].Numeric() {
		t.Errorf("column flags wrong: %+v", cols)
	}
}

// render returns the grid as text: one line per visual line, cells joined
// by "|", "-" for blank and "∅" for null.
func render(fields []Field, row []any) string {
	cells := Render(fields, row)
	lines := len(cells[0])

	var b strings.Builder

	for l := range lines {
		for c := range cells {
			if c > 0 {
				b.WriteByte('|')
			}

			switch cell := cells[c][l]; cell.Kind {
			case Blank:
				b.WriteByte('-')
			case Null:
				b.WriteString("∅")
			case Value:
				fmt.Fprint(&b, cell.V)
			}
		}

		b.WriteByte('\n')
	}

	return b.String()
}

func TestRenderConsoleLayout(t *testing.T) {
	tests := []struct {
		name string
		row  []any
		want string
	}{
		{
			name: "array of struct of array stacks sub-rows",
			row:  order(1001, "Ana", "gold", item("A-1", 2, "x", "y"), item("B-7", 1), item("C-3", 5, "z")),
			want: "1001|Ana|gold|A-1|2|x\n" +
				"-|-|-|-|-|y\n" +
				"-|-|-|B-7|1|-\n" +
				"-|-|-|C-3|5|z\n",
		},
		{
			name: "null struct field and single item",
			row:  order(1002, "Bruno", nil, item("A-1", 1)),
			want: "1002|Bruno|∅|A-1|1|-\n",
		},
		{
			name: "empty array is blank, one line",
			row:  order(1003, "Carla", "silver"),
			want: "1003|Carla|silver|-|-|-\n",
		},
		{
			name: "null struct shows null in every leaf",
			row:  []any{int64(1004), nil, []any{}},
			want: "1004|∅|∅|-|-|-\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := render(ordersFields(), tt.row); got != tt.want {
				t.Errorf("layout mismatch\n got:\n%s\nwant:\n%s", got, tt.want)
			}

			if n := Lines(ordersFields(), tt.row); n != strings.Count(tt.want, "\n") {
				t.Errorf("Lines = %d", n)
			}
		})
	}
}

func TestRenderSiblingArraysAndNestedStructs(t *testing.T) {
	fields := []Field{
		{Name: "a", Type: TypeInt64, Repeated: true},
		{Name: "b", Type: TypeString, Repeated: true},
		{Name: "s", Type: TypeStruct, Fields: []Field{
			{Name: "inner", Type: TypeStruct, Fields: []Field{{Name: "v", Type: TypeInt64}}},
		}},
	}

	row := []any{[]any{int64(1), int64(2), int64(3)}, []any{"x"}, []any{[]any{int64(9)}}}
	want := "1|x|9\n2|-|-\n3|-|-\n"

	if got := render(fields, row); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestLayoutAgreesWithReference fuzzes random nested rows and checks that
// Render's line count matches Lines and that every value appears exactly once.
func TestLayoutAgreesWithReference(t *testing.T) {
	fields := ordersFields()
	seed := uint64(42)

	rnd := func(n uint64) uint64 {
		seed = seed*6364136223846793005 + 1442695040888963407

		return (seed >> 33) % n
	}

	for range 2000 {
		var items []any
		for range rnd(5) {
			var tags []any
			for range rnd(4) {
				tags = append(tags, "t")
			}

			items = append(items, []any{"s", int64(1), tags})
		}

		row := []any{int64(rnd(100)), []any{"n", nil}, items}
		if rnd(10) == 0 {
			row[1] = nil
		}

		cells := Render(fields, row)
		if len(cells[0]) != Lines(fields, row) {
			t.Fatalf("Render lines %d != Lines %d for %v", len(cells[0]), Lines(fields, row), row)
		}

		values := 0
		for _, col := range cells {
			for _, c := range col {
				if c.Kind == Value {
					values++
				}
			}
		}

		want := 2 // order_id + customer.name
		if row[1] == nil {
			want = 1
		}

		for _, it := range items {
			want += 2 + len(it.([]any)[2].([]any))
		}

		if values != want {
			t.Fatalf("values = %d, want %d for %v", values, want, row)
		}
	}
}

func TestFormatValue(t *testing.T) {
	cases := map[string]string{
		FormatValue(TypeInt64, int64(-42)):                                      "-42",
		FormatValue(TypeFloat64, 3.14):                                          "3.14",
		FormatValue(TypeFloat64, 1e21):                                          "1e+21",
		FormatValue(TypeFloat64, 1234567.0):                                     "1234567",
		FormatValue(TypeFloat64, math.NaN()):                                    "NaN",
		FormatValue(TypeFloat64, math.Inf(-1)):                                  "-Infinity",
		FormatValue(TypeBool, true):                                             "true",
		FormatValue(TypeBytes, []byte{1, 2, 3}):                                 "AQID",
		FormatValue(TypeTimestamp, time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)): "2024-01-02 03:04:05 UTC",
		FormatValue(TypeNumeric, "12.5"):                                        "12.5",
		FormatValue(TypeString, nil):                                            "null",
		FormatCell(TypeString, "a\nb\tc"):                                       "a⏎b⇥c",
	}

	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}

	long := FormatCell(TypeString, strings.Repeat("é", MaxCellRunes+10))
	if !strings.HasSuffix(long, "…") || len([]rune(long)) != MaxCellRunes+1 {
		t.Errorf("truncation: %d runes", len([]rune(long)))
	}
}
