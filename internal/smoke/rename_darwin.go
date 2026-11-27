//go:build darwin

package smoke

import (
	"path/filepath"
	"time"

	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/ui"
)

// renameResult captures F2 renaming in the Workbooks tree.
type renameResult struct {
	PromptFocused  bool   // the rename prompt's field has keyboard focus
	PromptSelected uint64 // characters selected in it when it opens
	ArrowMoved     bool   // Down arrow still moves the selection
	QueryAsked     string // prompt title after F2 on a query
	QueryName      string // the open query's name after the rename
	WorkbookAsked  string // prompt title after F2 on a workbook
	WorkbookName   string // the open query's workbook after the rename
	Err            string
}

const (
	keyDownArrow    uint16 = 125 // kVK_DownArrow
	f2Chars                = ""
	downArrowChars         = ""
	renamedQuery           = "renamed with f2"
	renamedWorkbook        = "workbook renamed with f2"
)

func renameFlow(s *ui.Shell) renameResult {
	var r renameResult

	asked := ""
	answer := ""

	onMain(func() {
		s.SetPrompt(func(message, _, _ string) (string, bool) {
			asked = message

			return answer, true
		})
	})
	defer onMain(func() { s.SetPrompt(nil) })

	q, ok := s.Workspace.CurrentQuery()
	if !ok {
		r.Err = "no open query"

		return r
	}

	sel := func(path string) {
		if id, ok := s.Workbooks.ID(path); ok {
			s.Workbooks.Outline.Select(id)
		}
	}

	// Focus the tree on the workbook folder; Down moves to its first query.
	onMain(func() {
		s.SelectSidebar(ui.SidebarWorkbooks)
		s.Workbooks.Reload()
		s.Workbooks.Outline.Focus()
		sel(filepath.Dir(q.Path))
		before := s.Workbooks.Outline.Selected()
		appkit.SendKey(s.Window, keyDownArrow, downArrowChars)
		r.ArrowMoved = s.Workbooks.Outline.Selected() != before
	})

	// F2 on the open query renames it.
	answer = renamedQuery
	onMain(func() {
		s.Workbooks.Outline.Focus()
		sel(q.Path)
		appkit.SendKey(s.Window, appkit.KeyF2, f2Chars)
	})
	waitMain(loadTimeout, func() bool { cq, _ := s.Workspace.CurrentQuery(); return cq.Name == renamedQuery })
	onMain(func() {
		r.QueryAsked = asked
		cq, _ := s.Workspace.CurrentQuery()
		r.QueryName = cq.Name
	})

	// F2 on its workbook renames the folder (the open query follows).
	answer = renamedWorkbook
	time.Sleep(settleDelay)
	onMain(func() {
		cq, _ := s.Workspace.CurrentQuery()
		s.Workbooks.Outline.Focus()
		sel(filepath.Dir(cq.Path))
		appkit.SendKey(s.Window, appkit.KeyF2, f2Chars)
	})
	waitMain(loadTimeout, func() bool { cq, _ := s.Workspace.CurrentQuery(); return cq.Workbook == renamedWorkbook })
	onMain(func() {
		r.WorkbookAsked = asked
		cq, _ := s.Workspace.CurrentQuery()
		r.WorkbookName = cq.Workbook
	})

	// A real prompt, opened from inside a main-thread callback like F2's,
	// focuses its field with the whole name selected.
	onMain(func() { r.PromptFocused, r.PromptSelected = appkit.PromptFocus(renamedQuery) })

	return r
}

func renameChecks(rep *Report, r renameResult) {
	rep.check("rename-prompt-focus", r.PromptFocused && r.PromptSelected == uint64(len(renamedQuery)),
		"field focused %v, %d of %d characters selected", r.PromptFocused, r.PromptSelected, len(renamedQuery))
	rep.check("workbooks-arrow-keys", r.ArrowMoved, "Down arrow moved the selection: %v", r.ArrowMoved)
	rep.check("workbooks-f2-query", r.Err == "" && r.QueryAsked == "Rename Query" && r.QueryName == renamedQuery,
		"prompt %q, query now %q %s", r.QueryAsked, r.QueryName, r.Err)
	rep.check("workbooks-f2-workbook", r.WorkbookAsked == "Rename Workbook" && r.WorkbookName == renamedWorkbook,
		"prompt %q, workbook now %q", r.WorkbookAsked, r.WorkbookName)
}
