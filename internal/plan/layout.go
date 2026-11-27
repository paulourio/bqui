package plan

import (
	"slices"
	"sort"
)

// Node geometry (points).
const (
	NodeWidth  = 190.0
	NodeHeight = 58.0
	HGap       = 36.0
	VGap       = 56.0
	Margin     = 24.0
	sweeps     = 12
)

// Point is a layout coordinate (origin top-left, y grows downward).
type Point struct{ X, Y float64 }

// Node is a laid-out stage.
type Node struct {
	ID           int64
	Layer, Order int
	X, Y         float64 // top-left corner
}

// Edge connects an input stage (From) to a consumer (To).
type Edge struct {
	From, To int64
	A, B     Point // bottom-center of From → top-center of To
}

// Layout is a layered drawing of the stage DAG, inputs on top.
type Layout struct {
	Nodes         map[int64]*Node
	Edges         []Edge
	Width, Height float64
	Layers        [][]int64
}

// Layout places stages in layers by longest path from the inputs, orders
// each layer by barycenter sweeps to reduce edge crossings, then centers
// layers horizontally.
func (p *Plan) Layout() Layout {
	l := Layout{Nodes: make(map[int64]*Node)}
	if p == nil || len(p.Stages) == 0 {
		return l
	}

	layer := p.layers()

	maxLayer := 0
	for _, s := range p.Stages {
		maxLayer = max(maxLayer, layer[s.ID])
	}

	l.Layers = make([][]int64, maxLayer+1)
	for _, s := range p.Stages {
		l.Layers[layer[s.ID]] = append(l.Layers[layer[s.ID]], s.ID)
	}

	p.order(l.Layers)

	widest := 0
	for _, ids := range l.Layers {
		widest = max(widest, len(ids))
	}

	l.Width = 2*Margin + float64(widest)*NodeWidth + float64(max(widest-1, 0))*HGap
	l.Height = 2*Margin + float64(len(l.Layers))*NodeHeight + float64(len(l.Layers)-1)*VGap

	for li, ids := range l.Layers {
		rowW := float64(len(ids))*NodeWidth + float64(max(len(ids)-1, 0))*HGap
		x0 := (l.Width - rowW) / 2

		for oi, id := range ids {
			l.Nodes[id] = &Node{
				ID: id, Layer: li, Order: oi,
				X: x0 + float64(oi)*(NodeWidth+HGap),
				Y: Margin + float64(li)*(NodeHeight+VGap),
			}
		}
	}

	for _, s := range p.Stages {
		to := l.Nodes[s.ID]

		for _, in := range s.Inputs {
			from, ok := l.Nodes[in]
			if !ok {
				continue
			}

			l.Edges = append(l.Edges, Edge{
				From: in, To: s.ID,
				A: Point{from.X + NodeWidth/2, from.Y + NodeHeight},
				B: Point{to.X + NodeWidth/2, to.Y},
			})
		}
	}

	return l
}

// layers assigns layer = 1 + max(layer of inputs); inputs to missing stages
// and cycles (never seen in BigQuery plans) are ignored.
func (p *Plan) layers() map[int64]int {
	layer := make(map[int64]int, len(p.Stages))
	visiting := make(map[int64]bool)

	var visit func(id int64) int

	visit = func(id int64) int {
		if l, ok := layer[id]; ok {
			return l
		}

		if visiting[id] {
			return 0
		}

		visiting[id] = true
		l := 0

		if s, ok := p.Stage(id); ok {
			for _, in := range s.Inputs {
				if _, exists := p.byID[in]; exists {
					l = max(l, visit(in)+1)
				}
			}
		}

		visiting[id] = false
		layer[id] = l

		return l
	}

	for _, s := range p.Stages {
		visit(s.ID)
	}

	return layer
}

// order reorders each layer in place by alternating barycenter sweeps,
// keeping the ordering with the fewest crossings.
func (p *Plan) order(layers [][]int64) {
	consumers := make(map[int64][]int64)

	for _, s := range p.Stages {
		for _, in := range s.Inputs {
			consumers[in] = append(consumers[in], s.ID)
		}
	}

	best := cloneLayers(layers)
	bestX := p.Crossings(layers)

	for sweep := range sweeps {
		if sweep%2 == 0 {
			for li := 1; li < len(layers); li++ {
				sortBy(layers[li], layers[li-1], func(id int64) []int64 { s, _ := p.Stage(id); return s.Inputs })
			}
		} else {
			for li := len(layers) - 2; li >= 0; li-- {
				sortBy(layers[li], layers[li+1], func(id int64) []int64 { return consumers[id] })
			}
		}

		if x := p.Crossings(layers); x < bestX {
			best, bestX = cloneLayers(layers), x
		}
	}

	for i := range layers {
		copy(layers[i], best[i])
	}
}

// sortBy orders ids by the mean position of their neighbors in ref.
func sortBy(ids, ref []int64, neighbors func(int64) []int64) {
	pos := make(map[int64]float64, len(ref))
	for i, id := range ref {
		pos[id] = float64(i)
	}

	bary := make(map[int64]float64, len(ids))

	for i, id := range ids {
		sum, n := 0.0, 0

		for _, nb := range neighbors(id) {
			if x, ok := pos[nb]; ok {
				sum += x
				n++
			}
		}

		if n == 0 {
			bary[id] = float64(i) // keep unconnected nodes where they are
		} else {
			bary[id] = sum / float64(n)
		}
	}

	sort.SliceStable(ids, func(a, b int) bool { return bary[ids[a]] < bary[ids[b]] })
}

func cloneLayers(layers [][]int64) [][]int64 {
	out := make([][]int64, len(layers))
	for i, l := range layers {
		out[i] = slices.Clone(l)
	}

	return out
}

// Crossings counts edge crossings between consecutive layers for an
// ordering (edges spanning several layers are counted at their endpoints'
// relative order).
func (p *Plan) Crossings(layers [][]int64) int {
	pos := make(map[int64][2]int)

	for li, ids := range layers {
		for oi, id := range ids {
			pos[id] = [2]int{li, oi}
		}
	}

	type seg struct{ a, b, la int }

	var segs []seg

	for _, s := range p.Stages {
		to, ok := pos[s.ID]
		if !ok {
			continue
		}

		for _, in := range s.Inputs {
			if from, ok := pos[in]; ok {
				segs = append(segs, seg{from[1], to[1], from[0]})
			}
		}
	}

	n := 0

	for i := range segs {
		for j := i + 1; j < len(segs); j++ {
			if segs[i].la != segs[j].la {
				continue
			}

			if (segs[i].a-segs[j].a)*(segs[i].b-segs[j].b) < 0 {
				n++
			}
		}
	}

	return n
}
