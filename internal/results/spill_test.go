package results

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// jsonDecode stands in for Arrow decoding: blobs are JSON arrays of rows.
func jsonDecode(data []byte) ([][]any, error) {
	var raw [][]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	// JSON numbers decode as float64; that is fine for layout tests.
	return raw, nil
}

func TestSpillStoreRoundTripAndCleanup(t *testing.T) {
	fields := []Field{{Name: "id", Type: TypeInt64}, {Name: "tags", Type: TypeString, Repeated: true}}

	s, err := NewSpillStore(t.TempDir(), jsonDecode)
	if err != nil {
		t.Fatal(err)
	}

	g := NewGrid(fields, s)

	blobs := []string{
		`[[1, ["a","b"]], [2, []]]`, // 2 + 1 lines
		`[[3, ["c"]]]`,              // 1 line
	}

	for _, b := range blobs {
		if err = AppendSpilled(g, s, []byte(b)); err != nil {
			t.Fatal(err)
		}
	}

	g.Finish(nil)

	rows, lines, _ := g.Size()
	if rows != 3 || lines != 4 {
		t.Fatalf("size = %d rows / %d lines", rows, lines)
	}

	c, p, err := g.Cell(1, 1)
	if err != nil || c.V != "b" || p.Row != 0 || p.Sub != 1 {
		t.Fatalf("cell(1,1) = %+v %+v %v", c, p, err)
	}

	c, p, _ = g.Cell(3, 1)
	if c.V != "c" || p.Row != 2 {
		t.Fatalf("cell(3,1) = %+v %+v", c, p)
	}

	if s.Bytes() != int64(len(blobs[0])+len(blobs[1])) {
		t.Errorf("spill size = %d", s.Bytes())
	}

	name := s.f.Name()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("spill file not removed: %v", err)
	}

	if _, err := s.Load(0); !errors.Is(err, ErrClosed) {
		t.Errorf("Load after Close: %v", err)
	}
}

func TestSpillStoreRejectsBadBlob(t *testing.T) {
	s, err := NewSpillStore(t.TempDir(), jsonDecode)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.Append([]byte("{nope")); err == nil {
		t.Fatal("expected decode error")
	}

	if s.Bytes() != 0 {
		t.Error("bad blob must not be written")
	}
}
