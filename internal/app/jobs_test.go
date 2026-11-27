package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/paulourio/bqui/internal/bq"
)

func TestHistoryPagesAndFilters(t *testing.T) {
	be := &FakeBackend{}
	l := newUILoop(t)

	changed := make(chan struct{}, 64)
	h := NewHistory(be, l.post, func() { changed <- struct{}{} })

	waitIdle := func() {
		t.Helper()

		deadline := time.After(5 * time.Second)

		for {
			var busy bool

			l.do(func() { busy = h.Loading() })

			if !busy {
				return
			}

			select {
			case <-changed:
			case <-deadline:
				t.Fatal("history did not load")
			}
		}
	}

	// Personal history: 40 of the 120 seeded jobs, one page.
	l.do(func() { h.Reload(FakeProject, false) })
	waitIdle()

	var (
		rows []JobRow
		more bool
	)

	l.do(func() { rows, more = h.Rows(), h.HasMore() })

	if len(rows) != 40 || more {
		t.Fatalf("personal history = %d rows (more=%v)", len(rows), more)
	}

	for _, r := range rows {
		if r.User != fakeUser {
			t.Fatalf("personal history has %s", r.User)
		}
	}

	if !rows[0].Created.After(rows[len(rows)-1].Created) {
		t.Error("history must be newest first")
	}

	// Project history: 120 jobs by three users, paged by HistoryPage.
	l.do(func() { h.Reload(FakeProject, true) })
	waitIdle()
	l.do(func() { rows, more = h.Rows(), h.HasMore() })

	if len(rows) != HistoryPage || !more {
		t.Fatalf("first project page = %d (more=%v)", len(rows), more)
	}

	for more {
		l.do(h.LoadMore)
		waitIdle()
		l.do(func() { rows, more = h.Rows(), h.HasMore() })
	}

	users := map[string]bool{}
	for _, r := range rows {
		users[r.User] = true
	}

	if len(rows) != 120 || len(users) != 3 {
		t.Errorf("project history = %d rows by %v", len(rows), users)
	}
}

func TestJobRecordingDetailsAndOpenJob(t *testing.T) {
	be := &FakeBackend{}
	rn := NewRunner(RunnerOptions{Backend: be, Metadata: be, Jobs: be, Post: func(fn func()) { fn() }, SpillDir: t.TempDir()})

	s := wait(t, rn.Start(bq.QueryRequest{SQL: "SELECT * FROM orders LIMIT 1500"}))
	if s.State != RunDone || s.Job == nil {
		t.Fatalf("run = %+v", s)
	}

	failed := wait(t, rn.Start(bq.QueryRequest{SQL: "SELEC nope"}))

	page, err := be.ListJobs(context.Background(), bq.JobFilter{Limit: 2})
	if err != nil || len(page.Jobs) != 2 {
		t.Fatalf("ListJobs = %v %v", page, err)
	}

	newest := JobRowFromList(page.Jobs[0])
	if newest.Ref.JobID != failed.Job.JobID || newest.State != "error" || !strings.Contains(newest.Error, "Syntax error") {
		t.Errorf("newest = %+v", newest)
	}

	job, err := be.GetJob(context.Background(), *s.Job)
	if err != nil {
		t.Fatal(err)
	}

	secs := testFmt.JobSections(job)
	if section(secs, "Statistics")["Bytes processed"] != "2.00 MiB" || !strings.Contains(section(secs, "Query")["SQL"], "LIMIT 1500") ||
		section(secs, "Tables")["Destination"] == "" {
		t.Errorf("sections = %+v", secs)
	}

	opened := wait(t, rn.OpenJob(*s.Job))
	if opened.State != RunDone || opened.Grid == nil || opened.Kind != "job" {
		t.Fatalf("OpenJob = %+v", opened)
	}

	if rows, _, _ := opened.Grid.Size(); rows != 1500 {
		t.Errorf("reopened rows = %d", rows)
	}

	if f := wait(t, rn.OpenJob(*failed.Job)); f.State != RunFailed {
		t.Errorf("OpenJob of failed job = %v", f.State)
	}
}

func TestJobSectionsNil(t *testing.T) {
	if got := testFmt.JobSections(nil); got != nil {
		t.Errorf("JobSections(nil) = %+v, want nil", got)
	}

	if got := JobRowFromList(nil); got != (JobRow{}) {
		t.Errorf("JobRowFromList(nil) = %+v, want zero", got)
	}

	if got := JobRowFromJob(nil); got != (JobRow{}) {
		t.Errorf("JobRowFromJob(nil) = %+v, want zero", got)
	}
}
