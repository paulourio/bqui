package results

import (
	"strings"
	"testing"
)

func TestTreeNestedValues(t *testing.T) {
	row := order(1001, "Ana", nil, item("A-1", 2, "x", "y"), item("B-7", 1))
	tree := Tree(ordersFields(), row)

	if len(tree) != 3 || tree[0].Display != "1001" || tree[0].Type != TypeInt64 {
		t.Fatalf("top = %+v", tree)
	}

	cust := tree[1]
	if cust.Display != "2 fields" || cust.Children[1].Display != "null" || !cust.Children[1].Null || cust.Children[1].Path != "customer.tier" {
		t.Errorf("customer = %+v", cust)
	}

	items := tree[2]
	if items.Type != "ARRAY<STRUCT>" || items.Display != "2 items" || len(items.Children) != 2 {
		t.Fatalf("items = %+v", items)
	}

	tags := items.Children[0].Children[2]
	if tags.Path != "items[0].tags" || tags.Display != "2 items" || tags.Children[1].Display != "y" || tags.Children[1].Path != "items[0].tags[1]" {
		t.Errorf("tags = %+v", tags)
	}
}

func TestTreeTruncatesHugeValues(t *testing.T) {
	fields := []Field{{Name: "s", Type: TypeString}}
	tree := Tree(fields, []any{strings.Repeat("x", MaxInspectorRunes+10)})

	if !strings.Contains(tree[0].Display, "truncated") {
		t.Error("huge value not truncated")
	}
}

func TestColumnPathFollowsSubLines(t *testing.T) {
	fields := ordersFields()
	cols := Leaves(fields)
	// Layout: line 0 A-1/x, line 1 -/y, line 2 B-7, line 3 C-3/z.
	row := order(1, "n", "t", item("A-1", 2, "x", "y"), item("B-7", 1), item("C-3", 5, "z"))

	cases := []struct {
		col  int
		sub  int
		want string
	}{
		{0, 0, "order_id"},
		{2, 0, "customer.tier"},
		{3, 0, "items[0].sku"},
		{5, 1, "items[0].tags[1]"},
		{3, 2, "items[1].sku"},
		{5, 3, "items[2].tags[0]"},
	}

	for _, c := range cases {
		if got := ColumnPath(fields, row, cols[c.col], c.sub); got != c.want {
			t.Errorf("ColumnPath(%s, line %d) = %q, want %q", cols[c.col].Name, c.sub, got, c.want)
		}
	}
}

func TestColumnPathBlankContinuationAndNestedRepeated(t *testing.T) {
	fields := []Field{
		{Name: "long", Type: TypeInt64, Repeated: true},
		{
			Name:     "short",
			Type:     TypeStruct,
			Repeated: true,
			Fields: []Field{
				{Name: "tags", Type: TypeString, Repeated: true},
				{Name: "b", Type: TypeString},
				{Name: "c", Type: TypeString},
			},
		},
		{
			Name: "s",
			Type: TypeStruct,
			Fields: []Field{
				{Name: "tags", Type: TypeString, Repeated: true},
			},
		},
	}

	row := []any{
		[]any{int64(1), int64(2), int64(3)},
		[]any{
			[]any{[]any{"t0"}, "b0", "c0"},
			[]any{[]any{"t1"}, "b1", "c1"},
		},
		[]any{[]any{"only"}},
	}

	cols := Leaves(fields)

	// short.tags on line 2 (Blank continuation past short's 2 elements) must not synthesize [2].
	if got := ColumnPath(fields, row, cols[1], 2); got != "short.tags" {
		t.Errorf("ColumnPath(short.tags, line 2) = %q, want %q", got, "short.tags")
	}

	// Non-repeated struct containing a shorter repeated child on Blank line 2.
	if got := ColumnPath(fields, row, cols[4], 2); got != "s.tags" {
		t.Errorf("ColumnPath(s.tags, line 2) = %q, want %q", got, "s.tags")
	}

	// Empty REPEATED RECORD -> "short.tags" on line 0.
	emptyRow := []any{[]any{int64(1)}, []any{}, []any{[]any{}}}
	if got := ColumnPath(fields, emptyRow, cols[1], 0); got != "short.tags" {
		t.Errorf("ColumnPath(empty short.tags, line 0) = %q, want %q", got, "short.tags")
	}

	// NULL struct element inside REPEATED RECORD -> "short[0].tags" on line 0.
	nullElemRow := []any{[]any{int64(1)}, []any{nil}, []any{[]any{}}}
	if got := ColumnPath(fields, nullElemRow, cols[1], 0); got != "short[0].tags" {
		t.Errorf("ColumnPath(null elem short.tags, line 0) = %q, want %q", got, "short[0].tags")
	}

	// Negative sub line must safely fall back without indexing.
	if got := ColumnPath(fields, row, cols[1], -1); got != "short.tags" {
		t.Errorf("ColumnPath(short.tags, line -1) = %q, want %q", got, "short.tags")
	}
}
