package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	bigquery "google.golang.org/api/bigquery/v2"
)

// testdata/job.json is a real 13-stage plan (public shakespeare data),
// with identities scrubbed.
func fixture(t *testing.T) *Plan {
	t.Helper()

	b, err := os.ReadFile("testdata/job.json")
	if err != nil {
		t.Fatal(err)
	}

	var j bigquery.Job
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatal(err)
	}

	p := FromJob(&j)
	if p == nil {
		t.Fatal("no plan")
	}

	return p
}

func TestFromJobRealPlan(t *testing.T) {
	p := fixture(t)

	if len(p.Stages) != 13 {
		t.Fatalf("stages = %d", len(p.Stages))
	}

	join, ok := p.Stage(9)
	if !ok || !strings.HasPrefix(join.Name, "S09: Join") || len(join.Inputs) != 2 {
		t.Fatalf("join stage = %+v", join)
	}

	if len(join.Steps) == 0 || len(StepText(join)) != len(join.Steps) {
		t.Errorf("steps = %+v", join.Steps)
	}

	if p.TotalSlotMs() <= 0 || p.MaxSlotMs() <= 0 || len(p.Timeline) == 0 {
		t.Errorf("totals: slot %d max %d timeline %d", p.TotalSlotMs(), p.MaxSlotMs(), len(p.Timeline))
	}

	if FromJob(&bigquery.Job{}) != nil {
		t.Error("job without statistics must have no plan")
	}
}

func TestLayoutRealPlan(t *testing.T) {
	p := fixture(t)
	l := p.Layout()

	if len(l.Nodes) != 13 || len(l.Edges) != 12 {
		t.Fatalf("nodes %d edges %d", len(l.Nodes), len(l.Edges))
	}

	out := l.Nodes[13] // S0D: Output
	if out.Layer != len(l.Layers)-1 {
		t.Errorf("output on layer %d of %d", out.Layer, len(l.Layers))
	}

	for _, e := range l.Edges {
		if l.Nodes[e.From].Layer >= l.Nodes[e.To].Layer || e.A.Y >= e.B.Y {
			t.Errorf("edge %d→%d does not flow downward", e.From, e.To)
		}
	}

	// No overlapping nodes; all inside the canvas.
	for _, a := range l.Nodes {
		if a.X < 0 || a.Y < 0 || a.X+NodeWidth > l.Width || a.Y+NodeHeight > l.Height {
			t.Errorf("node %d outside canvas", a.ID)
		}

		for _, b := range l.Nodes {
			if a.ID < b.ID && a.Layer == b.Layer && (a.X < b.X+NodeWidth && b.X < a.X+NodeWidth) {
				t.Errorf("nodes %d and %d overlap", a.ID, b.ID)
			}
		}
	}

	if x := p.Crossings(l.Layers); x != 0 {
		t.Errorf("real plan should lay out without crossings, got %d", x)
	}
}

// synthetic builds a layered DAG whose natural ID order is maximally crossed.
func synthetic(stages int) *Plan {
	p := &Plan{byID: map[int64]int{}, Insights: map[int64][]string{}}

	const width = 10

	for i := range stages {
		s := Stage{ID: int64(i), Name: fmt.Sprintf("S%02d", i), SlotMs: int64(i * 10)}

		if i >= width {
			// Connect to the mirrored node of the previous layer.
			prev := (i/width-1)*width + (width - 1 - i%width)
			s.Inputs = []int64{int64(prev)}
		}

		p.byID[s.ID] = len(p.Stages)
		p.Stages = append(p.Stages, s)
	}

	return p
}

func TestLayoutReducesCrossingsAndIsFast(t *testing.T) {
	p := synthetic(200)

	var natural [][]int64
	for i := 0; i < 200; i += 10 {
		var row []int64
		for j := range 10 {
			row = append(row, int64(i+j))
		}

		natural = append(natural, row)
	}

	start := time.Now()
	l := p.Layout()
	elapsed := time.Since(start)

	if before, after := p.Crossings(natural), p.Crossings(l.Layers); after >= before || after != 0 {
		t.Errorf("crossings %d → %d", before, after)
	}

	if elapsed > 50*time.Millisecond {
		t.Errorf("200-stage layout took %v", elapsed)
	}
}

func TestFlags(t *testing.T) {
	p := &Plan{byID: map[int64]int{}, Insights: map[int64][]string{1: {"slot contention"}}}
	s := Stage{ID: 1, ComputeAvgMs: 100, ComputeMaxMs: 900, SpilledBytes: 1 << 20, SlotMs: 10}
	p.Stages = []Stage{s, {ID: 2, SlotMs: 1}}

	got := strings.Join(p.Flags(s), ", ")
	for _, want := range []string{"slot contention", "compute skew 9.0×", "shuffle spilled", "% of slot time"} {
		if !strings.Contains(got, want) {
			t.Errorf("flags %q missing %q", got, want)
		}
	}
}
