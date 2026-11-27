// Package plan models a BigQuery query plan for the "Execution details"
// and "Execution graph" views: stages, steps, timings, insights, and a
// layered layout of the stage DAG. It is UI-agnostic and tested on Linux.
package plan

import (
	"fmt"
	"slices"
	"strings"

	bigquery "google.golang.org/api/bigquery/v2"
)

// Step is one operation inside a stage (READ, COMPUTE, JOIN, ...).
type Step struct {
	Kind     string
	Substeps []string
}

// Stage is one plan stage.
type Stage struct {
	ID     int64
	Name   string
	Status string
	Inputs []int64

	WaitAvgMs, WaitMaxMs       int64
	ReadAvgMs, ReadMaxMs       int64
	ComputeAvgMs, ComputeMaxMs int64
	WriteAvgMs, WriteMaxMs     int64

	RecordsRead, RecordsWritten int64
	ShuffleBytes, SpilledBytes  int64
	SlotMs                      int64
	Parallel, Completed         int64
	StartMs, EndMs              int64

	Steps []Step
}

// DurationMs is the stage's wall-clock time.
func (s Stage) DurationMs() int64 {
	if s.EndMs > s.StartMs {
		return s.EndMs - s.StartMs
	}

	return 0
}

// Sample is one point of the job timeline.
type Sample struct {
	ElapsedMs, Active, Completed, Pending, SlotMs int64
}

// Plan is a job's query plan.
type Plan struct {
	Stages   []Stage
	Timeline []Sample
	// Insights are BigQuery's performance insights, keyed by stage ID.
	Insights map[int64][]string
	byID     map[int64]int
}

// FromJob extracts the plan of a query job (nil when the job has none).
func FromJob(j *bigquery.Job) *Plan {
	if j == nil || j.Statistics == nil || j.Statistics.Query == nil || len(j.Statistics.Query.QueryPlan) == 0 {
		return nil
	}

	q := j.Statistics.Query
	p := &Plan{Insights: make(map[int64][]string), byID: make(map[int64]int)}

	for _, s := range q.QueryPlan {
		st := Stage{
			ID: s.Id, Name: s.Name, Status: s.Status, Inputs: slices.Clone([]int64(s.InputStages)),
			WaitAvgMs: s.WaitMsAvg, WaitMaxMs: s.WaitMsMax, ReadAvgMs: s.ReadMsAvg, ReadMaxMs: s.ReadMsMax,
			ComputeAvgMs: s.ComputeMsAvg, ComputeMaxMs: s.ComputeMsMax, WriteAvgMs: s.WriteMsAvg, WriteMaxMs: s.WriteMsMax,
			RecordsRead: s.RecordsRead, RecordsWritten: s.RecordsWritten,
			ShuffleBytes: s.ShuffleOutputBytes, SpilledBytes: s.ShuffleOutputBytesSpilled,
			SlotMs: s.SlotMs, Parallel: s.ParallelInputs, Completed: s.CompletedParallelInputs,
			StartMs: s.StartMs, EndMs: s.EndMs,
		}

		for _, step := range s.Steps {
			st.Steps = append(st.Steps, Step{Kind: step.Kind, Substeps: slices.Clone(step.Substeps)})
		}

		p.byID[st.ID] = len(p.Stages)
		p.Stages = append(p.Stages, st)
	}

	for _, t := range q.Timeline {
		p.Timeline = append(p.Timeline, Sample{
			ElapsedMs: t.ElapsedMs, Active: t.ActiveUnits, Completed: t.CompletedUnits,
			Pending: t.PendingUnits, SlotMs: t.TotalSlotMs,
		})
	}

	if pi := q.PerformanceInsights; pi != nil {
		for _, in := range pi.StagePerformanceStandaloneInsights {
			p.Insights[in.StageId] = append(p.Insights[in.StageId], standaloneInsight(in)...)
		}

		for _, in := range pi.StagePerformanceChangeInsights {
			p.Insights[in.StageId] = append(p.Insights[in.StageId], "slower than previous runs")
		}
	}

	return p
}

func standaloneInsight(in *bigquery.StagePerformanceStandaloneInsight) []string {
	var out []string

	if in.SlotContention {
		out = append(out, "slot contention")
	}

	if in.InsufficientShuffleQuota {
		out = append(out, "insufficient shuffle quota")
	}

	if in.PartitionSkew != nil {
		out = append(out, "partition skew")
	}

	if len(in.HighCardinalityJoins) > 0 {
		out = append(out, "high-cardinality join")
	}

	return out
}

// Stage returns a stage by ID.
func (p *Plan) Stage(id int64) (Stage, bool) {
	i, ok := p.byID[id]
	if !ok {
		return Stage{}, false
	}

	return p.Stages[i], true
}

// Skew thresholds for flags.
const (
	skewRatio   = 3.0
	skewMinMs   = 200
	slowShareOf = 0.5
)

// Flags explains what stands out in a stage: BigQuery's insights, compute
// or read skew (max ≫ avg), shuffle spill to disk, and dominating slot time.
func (p *Plan) Flags(s Stage) []string {
	flags := slices.Clone(p.Insights[s.ID])

	if skewed(s.ComputeAvgMs, s.ComputeMaxMs) {
		flags = append(flags, fmt.Sprintf("compute skew %.1f×", ratio(s.ComputeMaxMs, s.ComputeAvgMs)))
	}

	if skewed(s.ReadAvgMs, s.ReadMaxMs) {
		flags = append(flags, fmt.Sprintf("read skew %.1f×", ratio(s.ReadMaxMs, s.ReadAvgMs)))
	}

	if s.SpilledBytes > 0 {
		flags = append(flags, "shuffle spilled to disk")
	}

	if total := p.TotalSlotMs(); total > 0 && float64(s.SlotMs) > slowShareOf*float64(total) && len(p.Stages) > 1 {
		flags = append(flags, fmt.Sprintf("%.0f%% of slot time", 100*float64(s.SlotMs)/float64(total)))
	}

	return flags
}

func skewed(avg, maxMs int64) bool {
	return maxMs >= skewMinMs && avg > 0 && float64(maxMs) >= skewRatio*float64(avg)
}

func ratio(a, b int64) float64 {
	if b == 0 {
		return 0
	}

	return float64(a) / float64(b)
}

// TotalSlotMs sums slot time over stages.
func (p *Plan) TotalSlotMs() int64 {
	var t int64
	for _, s := range p.Stages {
		t += s.SlotMs
	}

	return t
}

// MaxSlotMs is the largest per-stage slot time (heat-map scale).
func (p *Plan) MaxSlotMs() int64 {
	var m int64
	for _, s := range p.Stages {
		m = max(m, s.SlotMs)
	}

	return m
}

// StepText renders a stage's steps as lines ("READ: $1, $2 FROM ...").
func StepText(s Stage) []string {
	out := make([]string, 0, len(s.Steps))
	for _, st := range s.Steps {
		out = append(out, st.Kind+": "+strings.Join(st.Substeps, "; "))
	}

	return out
}
