//go:build darwin

package ui

import (
	"strings"
	"time"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/objc"
)

// Job History tree item IDs (after the BigQuery Studio section).
const (
	jobsGroupID = studioGroupID + 10
	jobsEmptyID = studioGroupID + 11
	jobsMoreID  = studioGroupID + 12
	jobBase     = uint64(1) << 41
)

// jobTree is the Workbooks pane's Job History section: the user's own
// jobs in the billing project, newest first, one page at a time.
type jobTree struct {
	model *app.History
	shown []app.JobRow
}

// initJobs creates the Job History model (nil when there is no jobs
// backend).
func (p *WorkbooksPane) initJobs() {
	if p.s.opts.Jobs == nil {
		return
	}

	p.jobs.model = app.NewHistory(p.s.opts.Jobs, objc.Main, p.rebuild)
}

// ReloadJobs refetches the first page of the user's jobs.
func (p *WorkbooksPane) ReloadJobs() {
	if p.jobs.model != nil && p.s.BillingProject() != "" {
		p.jobs.model.Reload(p.s.BillingProject(), false)
	}
}

// filterJobs applies the tree filter to the loaded jobs (rebuild).
func (p *WorkbooksPane) filterJobs() {
	if p.jobs.model == nil {
		return
	}

	p.jobs.shown = app.FilterJobRows(p.jobs.model.Rows(), p.filter)
}

// Jobs returns the listed jobs (smoke checks).
func (p *WorkbooksPane) Jobs() []app.JobRow { return p.jobs.shown }

// JobsLoading reports a Job History fetch in flight.
func (p *WorkbooksPane) JobsLoading() bool { return p.jobs.model != nil && p.jobs.model.Loading() }

func (p *WorkbooksPane) job(id uint64) (app.JobRow, bool) {
	if id <= jobBase || id >= studioGroupID || id-jobBase > uint64(len(p.jobs.shown)) {
		return app.JobRow{}, false
	}

	return p.jobs.shown[id-jobBase-1], true
}

func (p *WorkbooksPane) jobChildren() []uint64 {
	if len(p.jobs.shown) == 0 {
		return []uint64{jobsEmptyID}
	}

	ids := make([]uint64, 0, len(p.jobs.shown)+1)
	for i := range p.jobs.shown {
		ids = append(ids, jobBase+1+uint64(i))
	}

	if p.jobs.model.HasMore() {
		ids = append(ids, jobsMoreID)
	}

	return ids
}

// jobCell renders Job History rows; ok is false for other items.
func (p *WorkbooksPane) jobCell(id uint64) (appkit.OutlineCell, bool) {
	switch id {
	case jobsGroupID:
		return appkit.OutlineCell{Title: app.JobHistoryTitle, Group: true, ToolTip: "Your jobs in " + p.s.BillingProject()}, true
	case jobsEmptyID:
		msg := emptyText(p.jobs.model.Err(), p.filter, "No jobs yet")
		if p.jobs.model.Loading() {
			msg = "Loading…"
		}

		return appkit.OutlineCell{Title: msg, Secondary: true, ToolTip: msg}, true
	case jobsMoreID:
		title := "Load older jobs…"
		if p.jobs.model.Loading() {
			title = "Loading…"
		}

		return appkit.OutlineCell{Title: title, Secondary: true}, true
	}

	r, ok := p.job(id)
	if !ok {
		return appkit.OutlineCell{}, false
	}

	return appkit.OutlineCell{Title: r.Ref.JobID, Symbol: jobSymbol(r.State), ToolTip: app.JobTooltip(r)}, true
}

func jobSymbol(state string) string {
	switch state {
	case "error":
		return "xmark.circle"
	case "running", "pending":
		return "clock"
	}

	return "checkmark.circle"
}

// jobActivate handles double-click/select on Job History items; it reports
// whether id belonged to the section.
func (p *WorkbooksPane) jobActivate(id uint64) bool {
	if id == jobsMoreID {
		if !p.jobs.model.Loading() {
			p.jobs.model.LoadMore()
			p.rebuild()
		}

		return true
	}

	r, ok := p.job(id)
	if ok {
		p.s.OpenJobDoc(r)
	}

	return ok
}

func (p *WorkbooksPane) jobMenu(id uint64) ([]appkit.MenuItem, bool) {
	r, ok := p.job(id)
	if !ok {
		if id == jobsGroupID || id == jobsEmptyID || id == jobsMoreID {
			return []appkit.MenuItem{{Title: "Refresh Job History", Run: p.ReloadJobs}}, true
		}

		return nil, false
	}

	return []appkit.MenuItem{
		{Title: "Open SQL", Run: func() { p.s.OpenJobDoc(r) }},
		{Title: "Save to Workbook", Run: func() { p.s.OpenJobDoc(r); p.s.SaveJobDocToWorkbook() }},
		{},
		{Title: "Show Results", Run: func() { p.s.SelectTab(TabResults); p.s.Results.Show(p.s.Runner.OpenJob(r.Ref)) }},
		{Title: "Show Details", Run: func() { p.s.Inspector.ShowJob(r.Ref) }},
		{},
		{Title: "Copy Job ID", Run: func() { appkit.CopyToPasteboard(r.Ref.JobID) }},
		{Title: "Copy SQL", Run: func() { appkit.CopyToPasteboard(r.SQL) }},
		{Title: "Open in BigQuery Console", Run: func() { appkit.OpenURL(jobConsoleURL(r)) }},
		{},
		{Title: "Refresh Job History", Run: p.ReloadJobs},
	}, true
}

// --- Shell actions ---------------------------------------------------------

// OpenJobDoc shows a job's SQL read-only in the editor, with its details in
// the inspector and Job information tab.
func (s *Shell) OpenJobDoc(r app.JobRow) {
	if strings.TrimSpace(r.SQL) == "" {
		s.status("Job " + r.Ref.JobID + " has no SQL (" + strings.ToLower(r.Type) + " job).")

		return
	}

	t := s.Workspace.Tab()
	if t.File == "" && t.Remote != "" && !s.keepChanges() {
		return
	}

	if err := s.Workspace.OpenJobDoc(r.Ref, r.SQL); err != nil {
		s.status("✕ " + err.Error())

		return
	}

	s.Editor.SetTextAt(r.SQL, 0, 0)
	s.updateTitle()
	s.Inspector.ShowJob(r.Ref)
	s.Job.ShowJob(r.Ref)
	s.Runs.Reload()
	s.status("Job " + r.Ref.JobID + " (read-only). File ▸ Save Job SQL to Workbook to edit it.")
}

// SaveJobDocToWorkbook copies the open job document into a new query in
// the selected workbook and opens it for editing.
func (s *Shell) SaveJobDocToWorkbook() {
	if _, ok := s.Workspace.JobDoc(); !ok {
		s.status("Open a job from Job History first.")

		return
	}

	workbook := s.Workbooks.SelectedWorkbook()
	if workbook == "" {
		if w, err := s.Workspace.Library().CreateWorkbook("", time.Now()); err == nil {
			workbook = w.Name
		}
	}

	q, err := s.Workspace.SaveJobDocTo(workbook)
	if err != nil {
		s.status("✕ " + err.Error())

		return
	}

	s.Editor.SetTextAt(s.Workspace.Tab().SQL, 0, 0)
	s.updateTitle()
	s.SelectSidebar(SidebarWorkbooks)
	s.Workbooks.rebuild()
	s.Runs.Reload()
	s.status("Saved as " + q.Workbook + " / " + q.Name + ".")
}

// syncReadOnly makes the editor read-only while a job document is open.
func (s *Shell) syncReadOnly() {
	_, ro := s.Workspace.JobDoc()
	if ro != s.readOnly {
		s.readOnly = ro
		s.Editor.SetReadOnly(ro)
	}
}
