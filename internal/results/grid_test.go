package results

import (
	"errors"
	"sync"
	"testing"
)

func buildGrid(t *testing.T, n int) (*Grid, int64) {
	t.Helper()

	m := &MemoryStore{}
	g := NewGrid(ordersFields(), m)

	rows := make([][]any, 0, n)

	var lines int64

	for i := range n {
		var r []any

		switch i % 3 {
		case 0:
			r = order(int64(i), "a", "g", item("x", 1, "t1", "t2"), item("y", 2)) // 3 lines
		case 1:
			r = order(int64(i), "b", nil) // 1 line
		default:
			r = order(int64(i), "c", "s", item("z", 3)) // 1 line
		}

		lines += int64(Lines(ordersFields(), r))
		rows = append(rows, r)
	}

	AppendRows(g, m, rows)
	g.Finish(nil)

	return g, lines
}

func TestGridLocateAcrossChunks(t *testing.T) {
	const n = 3*ChunkRows + 17

	g, lines := buildGrid(t, n)

	rows, gl, complete := g.Size()
	if rows != n || gl != lines || !complete {
		t.Fatalf("size = %d rows %d lines complete=%v; want %d/%d", rows, gl, complete, n, lines)
	}

	// Walk every line and check rows advance monotonically with correct subs.
	var prev Pos

	for line := range lines {
		p, err := g.Locate(line)
		if err != nil {
			t.Fatal(err)
		}

		sameRow := p.Row == prev.Row && p.Sub == prev.Sub+1
		nextRow := p.Row == prev.Row+1 && p.Sub == 0 && prev.Last()

		if line > 0 && !sameRow && !nextRow {
			t.Fatalf("line %d: %+v after %+v", line, p, prev)
		}

		want := 1
		if p.Row%3 == 0 {
			want = 3
		}

		if p.Lines != want {
			t.Fatalf("row %d lines = %d, want %d", p.Row, p.Lines, want)
		}

		prev = p
	}

	if prev.Row != n-1 || !prev.Last() {
		t.Fatalf("last line maps to %+v", prev)
	}

	if _, err := g.Locate(lines); !errors.Is(err, ErrOutOfRange) {
		t.Errorf("past end: %v", err)
	}
}

func TestGridCellsAndRows(t *testing.T) {
	g, _ := buildGrid(t, 10)

	// Row 0 occupies lines 0-2: items x (tags t1,t2) then y.
	c, p, err := g.Cell(1, 5) // items.tags, second line
	if err != nil || c.Kind != Value || c.V != "t2" || p.Row != 0 || p.Sub != 1 {
		t.Fatalf("cell(1,5) = %+v %+v %v", c, p, err)
	}

	c, _, _ = g.Cell(2, 3) // items.sku on third line
	if c.V != "y" {
		t.Errorf("cell(2,3) = %+v", c)
	}

	c, _, _ = g.Cell(2, 0) // order_id continuation
	if c.Kind != Blank {
		t.Errorf("continuation = %+v", c)
	}

	c, p, _ = g.Cell(3, 2) // row 1: customer.tier is NULL
	if c.Kind != Null || p.Row != 1 {
		t.Errorf("null tier = %+v %+v", c, p)
	}

	r, err := g.Row(4)
	if err != nil || r[0] != int64(4) {
		t.Fatalf("Row(4) = %v %v", r, err)
	}

	// Rows 0, 1, 2 take 3, 1 and 1 lines, so row 3 starts at line 5.
	if first, err := g.FirstLine(3); err != nil || first != 5 {
		t.Errorf("FirstLine(3) = %d, %v", first, err)
	}
}

func TestGridConcurrentIngestAndRead(t *testing.T) {
	m := &MemoryStore{}
	g := NewGrid(ordersFields(), m)

	var wg sync.WaitGroup

	wg.Go(func() {
		for i := range 50 {
			rows := make([][]any, 100)
			for j := range rows {
				rows[j] = order(int64(i*100+j), "n", "t", item("s", 1, "a", "b"))
			}

			AppendRows(g, m, rows)
		}

		g.Finish(nil)
	})

	wg.Go(func() {
		for {
			_, lines, complete := g.Size()
			if lines > 0 {
				if _, _, err := g.Cell(lines-1, 0); err != nil {
					t.Error(err)

					return
				}
			}

			if complete {
				return
			}
		}
	})

	wg.Wait()

	if rows, lines, _ := g.Size(); rows != 5000 || lines != 10000 {
		t.Errorf("size = %d/%d", rows, lines)
	}
}

func TestLRUEvicts(t *testing.T) {
	c := newLRU[int, string](2)
	c.put(1, "a")
	c.put(2, "b")
	c.get(1)
	c.put(3, "c")

	if _, ok := c.get(2); ok {
		t.Error("2 should be evicted")
	}

	if v, ok := c.get(1); !ok || v != "a" {
		t.Error("1 should survive")
	}
}
