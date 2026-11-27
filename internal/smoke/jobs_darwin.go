//go:build darwin

package smoke

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/ui"
)

// jobTreeResult captures the Job History tree (M16).
type jobTreeResult struct {
	Listed, AllMine   bool
	Tooltip           string
	DocTitle, DocSQL  string
	JobSQL            string
	TypedIgnored      bool
	SavedTitle        string
	SavedHasSQL       bool
	EditableAfterSave bool
	Shot, Err         string
}

func jobTreeFlow(s *ui.Shell, dir string) jobTreeResult {
	var r jobTreeResult

	onMain(func() { s.SelectSidebar(ui.SidebarWorkbooks) })
	waitMain(loadTimeout, func() bool { return !s.Workbooks.JobsLoading() && len(s.Workbooks.Jobs()) > 0 })

	var job app.JobRow

	onMain(func() {
		jobs := s.Workbooks.Jobs()
		r.Listed, r.AllMine = len(jobs) > 0, true

		for _, j := range jobs {
			if j.User != "you@example.com" {
				r.AllMine = false
			}

			if job.Ref.JobID == "" && strings.TrimSpace(j.SQL) != "" {
				job = j
			}
		}

		r.Tooltip = app.JobTooltip(job)
	})

	if job.Ref.JobID == "" {
		r.Err = "no job with SQL listed"

		return r
	}

	// Open read-only: typing is ignored and nothing is autosaved.
	onMain(func() { s.OpenJobDoc(job) })
	waitMain(loadTimeout, func() bool { return s.Workspace.Tab().SQL == job.SQL })
	onMain(func() {
		r.DocTitle, r.DocSQL, r.JobSQL = s.Workspace.Title(), s.Workspace.Tab().SQL, job.SQL
		s.Editor.InsertText("-- typed")
	})
	time.Sleep(settleDelay)

	r.TypedIgnored = s.Workspace.Tab().SQL == job.SQL

	onMain(func() {
		r.Shot = "job-history.png"
		if err := appkit.WriteWindowPNG(s.Window, filepath.Join(dir, r.Shot)); err != nil {
			r.Shot = "✕ " + err.Error()
		}
	})

	// Save to Workbook: an editable copy in the selected workbook.
	onMain(func() {
		s.SaveJobDocToWorkbook()
		r.SavedTitle = s.Workspace.Title()
		r.SavedHasSQL = strings.Contains(s.Workspace.Tab().SQL, strings.TrimSpace(job.SQL))
		s.Editor.InsertText("-- now editable\n")
	})
	r.EditableAfterSave = waitMain(loadTimeout, func() bool { return strings.Contains(s.Workspace.Tab().SQL, "-- now editable") })
	onMain(func() { s.SelectSidebar(ui.SidebarExplorer) })

	return r
}

func jobTreeChecks(rep *Report, r jobTreeResult) {
	rep.check("jobs-list", r.Err == "" && r.Listed && r.AllMine && strings.Contains(r.Tooltip, " · ") && strings.Contains(r.Tooltip, "\n\n"),
		"listed %v, only mine %v, tooltip %q %s", r.Listed, r.AllMine, firstLines(r.Tooltip, 3), r.Err)
	rep.check("jobs-open-readonly", strings.HasPrefix(r.DocTitle, "Job History / ") && r.DocSQL == r.JobSQL && r.TypedIgnored && !strings.HasPrefix(r.Shot, "✕"),
		"title %q, SQL matches %v, typing ignored %v, %s", r.DocTitle, r.DocSQL == r.JobSQL, r.TypedIgnored, r.Shot)
	rep.check("jobs-save-to-workbook", strings.Contains(r.SavedTitle, " / Untitled-") && r.SavedHasSQL && r.EditableAfterSave,
		"saved as %q, has SQL %v, editable %v", r.SavedTitle, r.SavedHasSQL, r.EditableAfterSave)
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")

	return strings.Join(lines[:min(n, len(lines))], " ⏎ ")
}
