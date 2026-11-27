//go:build darwin

package smoke

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/ui"
)

// activeResult captures how the Workbooks tree marks the query open in the
// editor.
type activeResult struct {
	Open               string // path of the open query
	MarkedWithFolder   string // marked query while its folder is selected
	FolderSelected     bool
	Other, MarkedOther string // another query, and the mark after opening it
	Shot, Err          string
}

func activeFlow(s *ui.Shell, dir string) activeResult {
	var r activeResult

	marked := func() string {
		q, _ := s.Workbooks.ActiveQuery()

		return q.Path
	}

	q, ok := s.Workspace.CurrentQuery()
	if !ok {
		r.Err = "no open query"

		return r
	}

	r.Open = q.Path

	// Select the open query's workbook folder: the query stays marked.
	onMain(func() {
		s.SelectSidebar(ui.SidebarWorkbooks)
		s.Workbooks.Reload()

		if id, ok := s.Workbooks.ID(filepath.Dir(q.Path)); ok {
			s.Workbooks.Outline.Focus()
			r.FolderSelected = s.Workbooks.Outline.Select(id)
		}
	})
	time.Sleep(settleDelay)
	onMain(func() {
		r.MarkedWithFolder = marked()

		r.Shot = "workbooks-active.png"
		if err := appkit.WriteWindowPNG(s.Window, filepath.Join(dir, r.Shot)); err != nil {
			r.Shot = "✕ " + err.Error()
		}

		// Open another query of the workbook: the mark follows.
		for _, name := range s.Workbooks.Queries(q.Workbook) {
			if p := filepath.Join(filepath.Dir(q.Path), name+".sql"); p != q.Path {
				r.Other = p
				s.OpenLibraryQuery(p)

				break
			}
		}

		r.MarkedOther = marked()

		if r.Other != "" {
			s.OpenLibraryQuery(q.Path)
		}
	})

	return r
}

func activeChecks(rep *Report, r activeResult) {
	rep.check("workbooks-active-kept", r.Err == "" && r.FolderSelected && r.MarkedWithFolder == r.Open && !strings.HasPrefix(r.Shot, "✕"),
		"folder selected %v, marked %q (open %q), %s %s", r.FolderSelected, r.MarkedWithFolder, r.Open, r.Shot, r.Err)
	rep.check("workbooks-active-follows", r.Other != "" && r.MarkedOther == r.Other,
		"opened %q, marked %q", r.Other, r.MarkedOther)
}
