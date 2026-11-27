//go:build darwin

package smoke

import (
	"strings"
	"time"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/ui"
)

// pagingResult captures on-demand fetching, auto-fit, Add Project and the
// inspector's editor actions.
type pagingResult struct {
	FirstRows, AfterScroll int64
	FirstMore              bool
	Status                 string
	ColWidth, HeaderWidth  float64
	Added                  bool
	ClickEdited            bool   // double-clicking a table changed the buffer
	ActionsShown           bool   // the inspector shows the copy button and actions
	CopyText, WantCopy     string // what the copy button copies, and the table ID
	Inserted, Queried      bool   // Insert Reference / Query edited the buffer
	Focused                bool   // the editor has focus after Insert Reference
	ActErr                 string
	Err                    string
}

func pagingFlow(s *ui.Shell) pagingResult {
	var r pagingResult

	// 30,000 rows page over REST: only the first window loads.
	run := waitRun(s, func() { s.SelectTab(ui.TabResults); s.RunSQL("SELECT order_id, customer FROM orders LIMIT 30000") })
	if run == nil {
		r.Err = "no run"

		return r
	}

	waitMain(loadTimeout, func() bool { _, more := s.Results.LoadedRows(); return more })
	onMain(func() {
		r.FirstRows, r.FirstMore = s.Results.LoadedRows()
		r.Status = s.Status()
		r.ColWidth = s.Results.ColumnWidth(0)
		r.HeaderWidth = appkit.TextWidth("order_id", appkit.FontHeader)
		s.Results.Table.ScrollToEnd()
	})

	// Scrolling near the end fetches the next window.
	waitMain(loadTimeout, func() bool { n, _ := s.Results.LoadedRows(); return n > r.FirstRows })
	onMain(func() { r.AfterScroll, _ = s.Results.LoadedRows() })

	// Add Project (Other…): pinned and shown, then removed again.
	onMain(func() {
		s.AddProject("smoke-extra-project")
		_, r.Added = s.Explorer.Model.Find("smoke-extra-project", "", "")
		s.UnpinProject("smoke-extra-project")
	})

	// Double-clicking a table shows it but leaves the buffer alone; the
	// inspector's actions edit it and focus the editor.
	m := s.Explorer.Model

	for _, ds := range []string{"", "samples"} {
		var id uint64

		onMain(func() {
			id, _ = m.Find("bigquery-public-data", ds, "")
			m.Expand(id)
			s.Explorer.Outline.Expand(id, false)
		})
		waitMain(loadTimeout, func() bool { return m.Loaded(id) })
	}

	var (
		info   app.ObjectInfo
		before string
	)

	onMain(func() {
		before = s.Workspace.Tab().SQL

		id, ok := m.Find("bigquery-public-data", "samples", "shakespeare")
		if !ok {
			return
		}

		info, _ = m.Info(id)
		s.SelectSidebar(ui.SidebarExplorer)
		objc0(s)
		s.Explorer.Outline.Select(id)
		s.Explorer.Activate(id)
	})

	if info.Name == "" {
		r.ActErr = "shakespeare not in the explorer"

		return r
	}

	waitMain(loadTimeout, func() bool { return s.Inspector.Current().FullID() == info.FullID() })

	time.Sleep(settleDelay)
	onMain(func() {
		r.ClickEdited = s.Workspace.Tab().SQL != before
		copyButton, actions := s.Inspector.ActionViews()
		r.ActionsShown = !objc.Send[bool](copyButton, "isHidden") && !objc.Send[bool](actions, "isHidden")
		r.CopyText, r.WantCopy = s.Inspector.CopyText(), info.FullID()
		s.Inspector.InsertCurrent()
	})

	r.Inserted = waitMain(loadTimeout, func() bool { return strings.Contains(s.Workspace.Tab().SQL, info.SQLName()) })
	onMain(func() {
		r.Focused = s.EditorFocused()
		s.Inspector.QueryCurrent()
	})

	r.Queried = waitMain(loadTimeout, func() bool { return strings.Contains(s.Workspace.Tab().SQL, "FROM "+info.SQLName()) })

	return r
}

// objc0 moves focus to the explorer first, so the editor focus check
// proves that Insert Reference moves it.
func objc0(s *ui.Shell) { s.Explorer.Outline.Focus() }

func pagingChecks(rep *Report, r pagingResult) {
	rep.check("results-paging", r.Err == "" && r.FirstMore && r.FirstRows >= app.DefaultFetchWindow && r.FirstRows < 30000 &&
		strings.Contains(r.Status, "of 30,000 rows loaded") && r.AfterScroll > r.FirstRows,
		"first window %d (more %v), after scrolling %d; status %q %s", r.FirstRows, r.FirstMore, r.AfterScroll, r.Status, r.Err)
	rep.check("results-autofit", r.ColWidth >= r.HeaderWidth, "order_id column %.0fpt (header text %.0fpt)", r.ColWidth, r.HeaderWidth)
	rep.check("explorer-add-project", r.Added, "added %v", r.Added)
	rep.check("explorer-doubleclick-no-insert", r.ActErr == "" && !r.ClickEdited, "buffer changed %v %s", r.ClickEdited, r.ActErr)
	rep.check("inspector-actions", r.ActionsShown && r.CopyText == r.WantCopy && r.Inserted && r.Queried && r.Focused,
		"shown %v, copies %q (want %q), inserted %v, queried %v, editor focused %v",
		r.ActionsShown, r.CopyText, r.WantCopy, r.Inserted, r.Queried, r.Focused)
}
