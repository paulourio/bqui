//go:build darwin

package smoke

import (
	"path/filepath"
	"strings"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/ui"
)

// attrResult captures the schema's Attr column for a table with keys.
type attrResult struct {
	ID, AccountID     string // Attr text of users.id and users.account_id
	IDTip, AccountTip string // their tooltips
	Shot, Err         string
}

func attrFlow(s *ui.Shell, dir string) attrResult {
	var r attrResult

	info := app.ObjectInfo{Kind: app.KindTable, Project: app.FakeProject, Dataset: "analytics", Name: "users"}

	onMain(func() { s.Inspector.Show(info) })

	if !waitMain(loadTimeout, func() bool { return s.Inspector.Current().Name == "users" && s.Inspector.TableLoaded() }) {
		r.Err = "users did not load"

		return r
	}

	onMain(func() {
		s.Inspector.SelectSegment(0)
		layoutWindow(s.Window)
	})
	pumpMainLoop()

	onMain(func() {
		schema := s.Inspector.SchemaOutline()

		cell := func(name string) (string, string) {
			g := appkit.OutlineRowGeometry(schema, findOutlineRow(schema, "name", name), "attr")

			return g.Text, g.ToolTip
		}

		r.ID, r.IDTip = cell("id")
		r.AccountID, r.AccountTip = cell("account_id")

		r.Shot = "inspector-attr.png"
		if err := appkit.WriteWindowPNG(s.Window, filepath.Join(dir, r.Shot)); err != nil {
			r.Shot = "✕ " + err.Error()
		}
	})

	return r
}

func attrChecks(rep *Report, r attrResult) {
	rep.check("schema-attr-keys", r.Err == "" && r.ID == "R+PK" && r.AccountID == "FK" && !strings.HasPrefix(r.Shot, "✕"),
		"id %q, account_id %q, %s %s", r.ID, r.AccountID, r.Shot, r.Err)
	rep.check("schema-attr-tooltip", strings.Contains(r.IDTip, "primary key") && strings.Contains(r.IDTip, "REQUIRED") &&
		strings.Contains(r.AccountTip, "fk_account → "+app.FakeProject+".analytics.accounts.id"),
		"id %q, account_id %q", r.IDTip, r.AccountTip)
}
