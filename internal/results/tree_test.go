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
