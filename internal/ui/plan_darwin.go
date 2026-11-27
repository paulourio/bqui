//go:build darwin

package ui

import (
	"strconv"
	"strings"
	"time"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/plan"
)

const noPlan = "No query plan: the query ran without a job (short query optimization), or this is a preview."

// stepBase offsets step handles from stage handles in the details outline.
const stepBase = 1 << 32

// PlanPane is "Execution details": one row per stage, steps as children.
type PlanPane struct {
	View    objc.ID
	Outline *appkit.Outline

	s    *Shell
	plan *plan.Plan
	note objc.ID
}

func newPlanPane(s *Shell) *PlanPane {
	p := &PlanPane{s: s}
	p.Outline = appkit.NewOutline(planSource{p}, appkit.OutlineOptions{
		Columns: []appkit.Column{
			{ID: "stage", Title: "Stage", Width: 200, MinWidth: 80},
			{ID: "status", Title: "Status", Width: 80},
			{ID: "duration", Title: "Duration", Width: 80},
			{ID: "wait", Title: "Wait avg/max", Width: 110},
			{ID: "read", Title: "Read avg/max", Width: 110},
			{ID: "compute", Title: "Compute avg/max", Width: 120},
			{ID: "write", Title: "Write avg/max", Width: 110},
			{ID: "in", Title: "Records in", Width: 100},
			{ID: "out", Title: "Records out", Width: 100},
			{ID: "shuffle", Title: "Shuffle", Width: 90},
			{ID: "slot", Title: "Slot time", Width: 90},
			{ID: "flags", Title: "Flags", Width: 260},
		},
		Alternating: true,
		OnSelect: func(item uint64) {
			if item != 0 && item < stepBase && p.plan != nil {
				p.s.ShowStage(p.plan, int64(item-1))
			}
		},
	})
	p.note = appkit.SecondaryLabel(noPlan)

	p.View = appkit.NewView()
	appkit.AddFilling(p.View, p.Outline.Scroll, 0)
	appkit.AddCentered(p.View, p.note)
	appkit.SetHidden(p.Outline.Scroll, true)

	return p
}

// SetPlan shows a plan (nil shows the placeholder).
func (p *PlanPane) SetPlan(pl *plan.Plan) {
	p.plan = pl
	appkit.SetHidden(p.Outline.Scroll, pl == nil)
	appkit.SetHidden(p.note, pl != nil)
	p.Outline.Reload(0)
}

// Rows reports visible rows (smoke checks).
func (p *PlanPane) Rows() int64 { return p.Outline.Rows() }

type planSource struct{ p *PlanPane }

func (s planSource) Children(parent uint64) []uint64 {
	pl := s.p.plan
	if pl == nil {
		return nil
	}

	if parent == 0 {
		out := make([]uint64, len(pl.Stages))
		for i, st := range pl.Stages {
			out[i] = uint64(st.ID) + 1 //nolint:gosec // stage IDs are small and non-negative
		}

		return out
	}

	if parent >= stepBase {
		return nil
	}

	st, _ := pl.Stage(int64(parent - 1))
	out := make([]uint64, len(st.Steps))

	for i := range st.Steps {
		out[i] = stepBase + parent<<8 + uint64(i)
	}

	return out
}

func (s planSource) Expandable(item uint64) bool {
	return item < stepBase && len(s.Children(item)) > 0
}

func (s planSource) stage(item uint64) (plan.Stage, bool) {
	if s.p.plan == nil || item == 0 || item >= stepBase {
		return plan.Stage{}, false
	}

	return s.p.plan.Stage(int64(item - 1))
}

func (s planSource) Cell(item uint64) appkit.OutlineCell {
	if item >= stepBase {
		stage := (item - stepBase) >> 8
		st, _ := s.stage(stage)
		i := int((item - stepBase) & 0xff)

		if i < len(st.Steps) {
			return appkit.OutlineCell{Title: st.Steps[i].Kind, Secondary: true}
		}

		return appkit.OutlineCell{}
	}

	st, _ := s.stage(item)

	return appkit.OutlineCell{Title: st.Name, Symbol: statusSymbol(st.Status)}
}

func (s planSource) CellText(item uint64, column string) appkit.CellText {
	if item >= stepBase {
		if column != "status" {
			return appkit.CellText{}
		}

		st, _ := s.stage((item - stepBase) >> 8)
		i := int((item - stepBase) & 0xff)

		if i < len(st.Steps) {
			text := strings.Join(st.Steps[i].Substeps, "; ")

			return appkit.CellText{Text: text, Mono: true, Secondary: true, ToolTip: strings.Join(st.Steps[i].Substeps, "\n")}
		}

		return appkit.CellText{}
	}

	st, ok := s.stage(item)
	if !ok {
		return appkit.CellText{}
	}

	pair := func(a, b int64) appkit.CellText {
		return appkit.CellText{Text: msShort(a) + " / " + msShort(b), Right: true}
	}

	switch column {
	case "status":
		return appkit.CellText{Text: strings.ToLower(st.Status), Secondary: true}
	case "duration":
		return appkit.CellText{Text: msShort(st.DurationMs()), Right: true}
	case "wait":
		return pair(st.WaitAvgMs, st.WaitMaxMs)
	case "read":
		return pair(st.ReadAvgMs, st.ReadMaxMs)
	case "compute":
		return pair(st.ComputeAvgMs, st.ComputeMaxMs)
	case "write":
		return pair(st.WriteAvgMs, st.WriteMaxMs)
	case "in":
		return appkit.CellText{Text: app.Count(uint64(st.RecordsRead)), Right: true} //nolint:gosec // non-negative
	case "out":
		return appkit.CellText{Text: app.Count(uint64(st.RecordsWritten)), Right: true} //nolint:gosec // non-negative
	case "shuffle":
		return appkit.CellText{Text: app.Bytes(st.ShuffleBytes), Right: true}
	case "slot":
		return appkit.CellText{Text: msShort(st.SlotMs), Right: true}
	case "flags":
		return appkit.CellText{Text: strings.Join(s.p.plan.Flags(st), " · ")}
	}

	return appkit.CellText{}
}

func statusSymbol(status string) string {
	switch status {
	case "COMPLETE":
		return "checkmark.circle"
	case "RUNNING":
		return "circle.dotted"
	case "FAILED":
		return "xmark.octagon"
	}

	return "circle"
}

func msShort(v int64) string {
	if v < 1000 {
		return strconv.FormatInt(v, 10) + " ms"
	}

	return strconv.FormatFloat((time.Duration(v)*time.Millisecond).Seconds(), 'f', 1, 64) + " s"
}

// GraphPane is "Execution graph": the stage DAG drawn natively.
type GraphPane struct {
	View   objc.ID
	Canvas objc.ID

	s        *Shell
	plan     *plan.Plan
	layout   plan.Layout
	selected int64
	note     objc.ID
}

func newGraphPane(s *Shell) *GraphPane {
	g := &GraphPane{s: s, selected: -1}
	g.Canvas = appkit.NewCanvas(g)
	scroll := appkit.MagnifyingScroll(g.Canvas, 0.2, 2.0)
	g.note = appkit.SecondaryLabel(noPlan)

	g.View = appkit.NewView()
	appkit.AddFilling(g.View, scroll, 0)
	appkit.AddCentered(g.View, g.note)

	return g
}

// SetPlan lays out and shows a plan (nil shows the placeholder).
func (g *GraphPane) SetPlan(pl *plan.Plan) {
	g.plan = pl
	appkit.SetHidden(g.note, pl != nil)

	if pl == nil {
		g.layout = plan.Layout{}
		appkit.SetFrameSize(g.Canvas, 1, 1)
	} else {
		g.layout = pl.Layout()
		appkit.SetFrameSize(g.Canvas, g.layout.Width, g.layout.Height)
	}

	appkit.SetNeedsDisplay(g.Canvas)
}

// Nodes reports how many nodes are laid out (smoke checks).
func (g *GraphPane) Nodes() int { return len(g.layout.Nodes) }

// SelectStage selects a node as if clicked (smoke checks).
func (g *GraphPane) SelectStage(id int64) {
	if n, ok := g.layout.Nodes[id]; ok {
		g.MouseDown(appkit.Point{X: n.X + 1, Y: n.Y + 1})
	}
}

// Draw implements appkit.CanvasHandler.
func (g *GraphPane) Draw(appkit.Rect) {
	if g.plan == nil {
		return
	}

	edge := appkit.SystemColor("tertiaryLabelColor")
	for _, e := range g.layout.Edges {
		appkit.StrokeCurve(appkit.Point(e.A), appkit.Point(e.B), 1.5, edge)
	}

	base := appkit.SystemColor("controlBackgroundColor")
	hot := appkit.RGB(0.95, 0.45, 0.15, 1)
	border := appkit.SystemColor("separatorColor")
	accent := appkit.SystemColor("controlAccentColor")
	label := appkit.SystemColor("labelColor")
	secondary := appkit.SystemColor("secondaryLabelColor")
	title := appkit.SystemFont(12, appkit.WeightSemibold, false)
	small := appkit.SystemFont(10.5, appkit.WeightRegular, true)
	maxSlot := float64(max(g.plan.MaxSlotMs(), 1))

	for _, st := range g.plan.Stages {
		n := g.layout.Nodes[st.ID]
		r := appkit.MakeRect(n.X, n.Y, plan.NodeWidth, plan.NodeHeight)

		// Heat: share of the busiest stage's slot time.
		appkit.FillRoundedRect(r, 8, appkit.Blend(base, hot, 0.7*float64(st.SlotMs)/maxSlot))

		width, color := 1.0, border
		if st.ID == g.selected {
			width, color = 2.5, accent
		}

		appkit.StrokeRoundedRect(r, 8, width, color)

		appkit.DrawText(st.Name, appkit.MakeRect(n.X+10, n.Y+7, plan.NodeWidth-20, 16), title, label)
		appkit.DrawText(msShort(st.SlotMs)+" slot · "+app.Count(uint64(st.RecordsWritten))+" rows", //nolint:gosec // non-negative
			appkit.MakeRect(n.X+10, n.Y+25, plan.NodeWidth-20, 14), small, secondary)

		if flags := g.plan.Flags(st); len(flags) > 0 {
			appkit.DrawText("⚠︎ "+flags[0], appkit.MakeRect(n.X+10, n.Y+39, plan.NodeWidth-20, 14), small, hot)
		}
	}
}

// MouseDown implements appkit.CanvasHandler: selects the node under p.
func (g *GraphPane) MouseDown(p appkit.Point) {
	g.selected = -1

	for id, n := range g.layout.Nodes {
		if p.X >= n.X && p.X <= n.X+plan.NodeWidth && p.Y >= n.Y && p.Y <= n.Y+plan.NodeHeight {
			g.selected = id
		}
	}

	appkit.SetNeedsDisplay(g.Canvas)

	if g.selected >= 0 {
		g.s.ShowStage(g.plan, g.selected)
	}
}
