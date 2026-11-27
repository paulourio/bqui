//go:build darwin

package smoke

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/ui"
)

// sqlErrorsResult captures the window title split and how SQL errors are
// shown: the toolbar badge, the error bar and the gutter dots.
type sqlErrorsResult struct {
	Title, Subtitle  string
	WantTitle        string
	WantSubtitle     string
	Cost             string
	BarShown         bool
	BarTitle, Detail string
	GutterErrors     int
	ClearedBar       bool
	ClearedGutter    bool
	ClearedCost      string
	Shot, Err        string
}

const (
	sqlErrorsBad  = "SELECT 1 AS ok;\nSELEC broken FROM nowhere;\n"
	sqlErrorsGood = "SELECT 1 AS ok;\n"
)

func sqlErrorsFlow(s *ui.Shell, dir string) sqlErrorsResult {
	var r sqlErrorsResult

	onMain(func() {
		r.Title = objc.GoString(objc.Msg(s.Window, "title"))
		r.Subtitle = objc.GoString(objc.Msg(s.Window, "subtitle"))

		if q, ok := s.Workspace.CurrentQuery(); ok {
			r.WantTitle, r.WantSubtitle = q.Workbook, q.Name
		}

		s.Editor.SetText(sqlErrorsBad)
	})

	shown := waitMain(loadTimeout, func() bool { return s.SQLErrors.Visible() && s.Editor.GutterErrors() > 0 })
	if !shown {
		r.Err = "no error bar or gutter dot"
	}

	time.Sleep(settleDelay)
	onMain(func() {
		r.Cost = s.CostText()
		r.BarShown = s.SQLErrors.Visible()
		r.BarTitle, r.Detail, _ = s.SQLErrors.Text()
		r.GutterErrors = s.Editor.GutterErrors()

		r.Shot = "sql-errors.png"
		if err := appkit.WriteWindowPNG(s.Window, filepath.Join(dir, r.Shot)); err != nil {
			r.Shot = "✕ " + err.Error()
		}

		s.Editor.SetText(sqlErrorsGood)
	})

	r.ClearedBar = waitMain(loadTimeout, func() bool { return !s.SQLErrors.Visible() })
	r.ClearedGutter = waitMain(loadTimeout, func() bool { return s.Editor.GutterErrors() == 0 })
	onMain(func() { r.ClearedCost = s.CostText() })

	return r
}

func sqlErrorsChecks(rep *Report, r sqlErrorsResult) {
	rep.check("window-title-workbook", r.WantTitle != "" && r.Title == r.WantTitle && strings.HasPrefix(r.Subtitle, r.WantSubtitle),
		"title %q subtitle %q (want %q / %q)", r.Title, r.Subtitle, r.WantTitle, r.WantSubtitle)
	rep.check("sql-errors-shown", r.Err == "" && r.BarShown && r.BarTitle == "1 error in this query" &&
		strings.Contains(r.Detail, "Line 2") && r.GutterErrors == 1 && strings.Contains(r.Cost, "1 error") && !strings.HasPrefix(r.Shot, "✕"),
		"bar %v %q %q, gutter error lines %d, toolbar %q, %s %s", r.BarShown, r.BarTitle, r.Detail, r.GutterErrors, r.Cost, r.Shot, r.Err)
	rep.check("sql-errors-cleared", r.ClearedBar && r.ClearedGutter && !strings.Contains(r.ClearedCost, "error"),
		"bar hidden %v, gutter cleared %v, toolbar %q", r.ClearedBar, r.ClearedGutter, r.ClearedCost)
}
