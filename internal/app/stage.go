package app

import (
	"strings"
	"time"

	"github.com/paulourio/bqui/internal/plan"
)

// StageSections renders one plan stage for the inspector.
func StageSections(p *plan.Plan, s plan.Stage) []Section {
	info := Section{Title: "Stage"}
	info.add("Name", s.Name)
	info.add("Status", s.Status)
	info.add("Inputs", stageNames(p, s.Inputs))

	if d := s.DurationMs(); d > 0 {
		info.add("Duration", ms(d))
	}

	info.add("Slot time", ms(s.SlotMs))

	if s.Parallel > 0 {
		info.add("Parallel inputs", CountInt(s.Completed)+" of "+CountInt(s.Parallel)+" completed")
	}

	flags := Section{Title: "Flags"}
	for _, f := range p.Flags(s) {
		flags.add("⚠︎", f)
	}

	timing := Section{Title: "Time per worker (avg / max)"}
	timing.add("Wait", ms(s.WaitAvgMs)+" / "+ms(s.WaitMaxMs))
	timing.add("Read", ms(s.ReadAvgMs)+" / "+ms(s.ReadMaxMs))
	timing.add("Compute", ms(s.ComputeAvgMs)+" / "+ms(s.ComputeMaxMs))
	timing.add("Write", ms(s.WriteAvgMs)+" / "+ms(s.WriteMaxMs))

	data := Section{Title: "Data"}
	data.add("Records read", CountInt(s.RecordsRead))
	data.add("Records written", CountInt(s.RecordsWritten))
	data.add("Shuffle output", bytesOrZero(s.ShuffleBytes))

	if s.SpilledBytes > 0 {
		data.add("Spilled to disk", Bytes(s.SpilledBytes))
	}

	steps := Section{Title: "Steps"}
	for _, st := range s.Steps {
		steps.mono(st.Kind, strings.Join(st.Substeps, "\n"))
	}

	return nonEmpty(info, flags, timing, data, steps)
}

func stageNames(p *plan.Plan, ids []int64) string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		if s, ok := p.Stage(id); ok {
			names = append(names, s.Name)
		}
	}

	return strings.Join(names, ", ")
}

func ms(v int64) string {
	return (time.Duration(v) * time.Millisecond).String()
}
