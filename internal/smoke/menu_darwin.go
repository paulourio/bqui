//go:build darwin

package smoke

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/ui"
)

// menuResult captures how Monaco's context menu renders.
type menuResult struct {
	Found      bool   `json:"found"`
	Shadow     bool   `json:"shadow"`
	Background string `json:"background"`
	Color      string `json:"color"`
	MenuVar    string `json:"menuVar"`
	HostClass  string `json:"hostClass"`
	InEditor   bool   `json:"inEditor"`
	Items      int    `json:"items"`
	Shot       string `json:"-"`
	Err        string `json:"-"`
	Pasted     bool   `json:"-"` // the menu's Paste action inserted the pasteboard text
}

// pasteMarker is put on the pasteboard for the Paste check.
const pasteMarker = "bqui_smoke_paste_marker"

func menuFlow(s *ui.Shell, dir string) menuResult {
	var r menuResult

	raw, err := s.Editor.ProbeContextMenu(loadTimeout)
	if err != nil {
		r.Err = err.Error()

		return r
	}

	_ = json.Unmarshal(raw, &r)

	time.Sleep(settleDelay)
	onMain(func() {
		r.Shot = "editor-context-menu.png"
		if err := appkit.WriteWindowPNG(s.Window, filepath.Join(dir, r.Shot)); err != nil {
			r.Shot = "✕ " + err.Error()
		}

		s.Editor.CloseContextMenu()
	})

	// Monaco's Paste (the context-menu item) must paste the pasteboard
	// right away, without WebKit's own Paste confirmation in between.
	onMain(func() {
		appkit.CopyToPasteboard(pasteMarker)
		s.Editor.Action("editor.action.clipboardPasteAction")
	})

	r.Pasted = waitMain(loadTimeout, func() bool { return strings.Contains(s.Workspace.Tab().SQL, pasteMarker) })

	return r
}

// opaque reports whether a CSS color has a visible background.
func opaque(css string) bool {
	css = strings.ReplaceAll(css, " ", "")

	return css != "" && css != "transparent" && !strings.HasSuffix(css, ",0)") && css != "rgba(0,0,0,0)"
}

func menuChecks(rep *Report, r menuResult) {
	rep.check("editor-context-menu", r.Err == "" && r.Found && r.Items > 0 && opaque(r.Background) && r.MenuVar != "",
		"found %v (%d items, shadow %v, host %q, in editor %v), background %q, --vscode-menu-background %q, %s %s",
		r.Found, r.Items, r.Shadow, r.HostClass, r.InEditor, r.Background, r.MenuVar, r.Shot, r.Err)
	rep.check("editor-menu-paste", r.Pasted, "context-menu Paste inserted the pasteboard text: %v", r.Pasted)
}
