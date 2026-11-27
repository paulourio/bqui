package app

import (
	"strings"
	"testing"

	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/plan"
)

func TestStageSectionsFromRecordedPlan(t *testing.T) {
	var j bigquery.Job
	if err := readFixture("plan.job", &j); err != nil {
		t.Fatal(err)
	}

	p := plan.FromJob(&j)
	join, _ := p.Stage(9)
	secs := StageSections(p, join)

	if !strings.Contains(section(secs, "Stage")["Inputs"], "Build") || section(secs, "Steps") == nil {
		t.Errorf("sections = %+v", secs)
	}

	// The fake replays the same plan with progressive statuses.
	half := plan.FromJob(fakeJob(bq.JobRef{Project: "p", JobID: "j"}, 1))
	done, running := 0, 0

	for _, s := range half.Stages {
		switch s.Status {
		case "COMPLETE":
			done++
		case "RUNNING":
			running++
		}
	}

	if len(half.Stages) != 13 || done != 4 || running != 1 {
		t.Errorf("progress replay: %d stages, %d complete, %d running", len(half.Stages), done, running)
	}

	// Nil plan must not panic (GAP-11).
	nilSecs := StageSections(nil, plan.Stage{
		ID: 1, Name: "S01: Join", Status: "COMPLETE",
		Inputs: []int64{0}, ComputeAvgMs: 100, ComputeMaxMs: 900,
	})
	if section(nilSecs, "Stage")["Name"] != "S01: Join" {
		t.Errorf("StageSections(nil) = %+v", nilSecs)
	}
}
