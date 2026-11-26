//go:build darwin

// Package smoke runs bqui's offscreen UI smoke test: it renders the main
// window in light and dark appearance to PNGs, snapshots the accessibility
// tree, checks structural invariants, and writes report.json.
package smoke

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/ui"
)

const (
	dirPerm  = 0o750
	filePerm = 0o600

	maxDispatchDelay = 2 * time.Second
)

// ErrChecksFailed is returned when at least one smoke check fails.
var ErrChecksFailed = errors.New("smoke: checks failed")

// Check is one named assertion.
type Check struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// Report is written to report.json.
type Report struct {
	Version       string        `json:"version"`
	GoVersion     string        `json:"goVersion"`
	Arch          string        `json:"arch"`
	OSVersion     string        `json:"osVersion"`
	WindowFrame   appkit.Rect   `json:"windowFrame"`
	DispatchDelay time.Duration `json:"dispatchDelayNs"`
	Screenshots   []string      `json:"screenshots"`
	Checks        []Check       `json:"checks"`
	Failed        int           `json:"failed"`
}

func (r *Report) check(name string, ok bool, format string, args ...any) {
	c := Check{Name: name, OK: ok, Detail: fmt.Sprintf(format, args...)}
	if !ok {
		r.Failed++
	}

	r.Checks = append(r.Checks, c)
}

func (r *Report) skip(name, format string, args ...any) {
	r.Checks = append(r.Checks, Check{Name: name, OK: true, Skipped: true, Detail: fmt.Sprintf(format, args...)})
}

// Start must be called from a worker goroutine. It hops to the main thread
// (exercising objc.Main), runs the smoke test, then calls done with the
// result on the main thread.
func Start(s *ui.Shell, dir, version string, done func(error)) {
	queued := time.Now()

	objc.Main(func() {
		rep := &Report{
			Version:       version,
			GoVersion:     runtime.Version(),
			Arch:          runtime.GOARCH,
			OSVersion:     appkit.OSVersion(),
			DispatchDelay: time.Since(queued),
		}

		if err := run(s, dir, rep, done); err != nil {
			done(err)
		}
	})
}

// run performs the synchronous checks, then captures screenshots
// asynchronously and calls done exactly once (unless it returns an error).
func run(s *ui.Shell, dir string, rep *Report, done func(error)) error {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("smoke: %w", err)
	}

	rep.WindowFrame = objc.Send[appkit.Rect](s.Window, "frame")
	rep.check("dispatch", rep.DispatchDelay < maxDispatchDelay, "objc.Main delay %v", rep.DispatchDelay)

	structuralChecks(s, rep)

	ax := appkit.AXTree(s.Window)
	accessibilityChecks(ax, rep)

	if err := writeJSON(filepath.Join(dir, "ax.json"), ax); err != nil {
		return err
	}

	screenshots(s, dir, rep, func(err error) {
		if err == nil {
			err = writeJSON(filepath.Join(dir, "report.json"), rep)
		}

		if err == nil && rep.Failed > 0 {
			err = fmt.Errorf("%w: %d of %d", ErrChecksFailed, rep.Failed, len(rep.Checks))
		}

		done(err)
	})

	return nil
}

func structuralChecks(s *ui.Shell, rep *Report) {
	menu := objc.Msg(s.App, "mainMenu")
	menus := objc.Send[int64](menu, "numberOfItems")
	rep.check("main-menu", menus == 7, "%d top-level menus", menus)

	tb := objc.Msg(s.Window, "toolbar")
	items := 0
	if tb != objc.Nil {
		items = len(objc.ArrayItems(objc.Msg(tb, "items")))
	}

	rep.check("toolbar", items >= 8, "%d toolbar items", items)

	panes := len(objc.ArrayItems(objc.Msg(s.Split, "splitViewItems")))
	rep.check("split-panes", panes == 3, "%d panes (sidebar, content, inspector)", panes)

	rows := objc.Send[int64](s.Sidebar.View, "numberOfRows")
	rep.check("sidebar-rows", rows >= 6, "%d visible outline rows", rows)

	rep.check("textkit2", s.Editor.UsesTextKit2(), "editor uses NSTextLayoutManager: %v", s.Editor.UsesTextKit2())

	text := s.Editor.String()
	rep.check("editor-text", len(text) > 0, "%d bytes of SQL", len(text))
}

func accessibilityChecks(ax *appkit.AXNode, rep *Report) {
	for _, role := range []string{"AXOutline", "AXTextArea", "AXSplitGroup", "AXToolbar"} {
		found := ax.Find(func(n *appkit.AXNode) bool { return n.Role == role }) != nil
		rep.check("ax-"+role, found, "role %s present: %v", role, found)
	}
}

// screenshots captures the window per appearance. The WindowServer capture
// shows real Liquid Glass; the offscreen render is a fallback that needs no
// compositor (materials render flat there).
func screenshots(s *ui.Shell, dir string, rep *Report, next func(error)) {
	// The frame view (content view's superview) includes titlebar and toolbar.
	frameView := objc.Msg(objc.Msg(s.Window, "contentView"), "superview")
	looks := []struct{ name, appearance string }{
		{"light", "NSAppearanceNameAqua"},
		{"dark", "NSAppearanceNameDarkAqua"},
	}

	var step func(i int)

	step = func(i int) {
		if i == len(looks) {
			objc.Msg(s.Window, "setAppearance:", objc.Nil)
			next(nil)

			return
		}

		look := looks[i]
		objc.Msg(s.Window, "setAppearance:", objc.Msg(objc.Cls("NSAppearance"), "appearanceNamed:", objc.String(look.appearance)))
		objc.Msg(frameView, "layoutSubtreeIfNeeded")
		objc.Msg(s.Window, "displayIfNeeded")

		// Let the WindowServer composite the new appearance before capturing.
		afterMain(settleDelay, func() {
			name := "window-" + look.name + ".png"
			err := appkit.WriteWindowPNG(s.Window, filepath.Join(dir, name))

			switch {
			case errors.Is(err, appkit.ErrCaptureBlank):
				rep.skip("capture-"+look.name, "%v", err)
			default:
				rep.check("capture-"+look.name, err == nil, "WindowServer capture: %v", errText(err))
			}

			if err == nil {
				rep.Screenshots = append(rep.Screenshots, name)
			}

			offscreen := "offscreen-" + look.name + ".png"
			if err := appkit.WritePNG(frameView, filepath.Join(dir, offscreen)); err != nil {
				next(err)

				return
			}

			rep.Screenshots = append(rep.Screenshots, offscreen)
			step(i + 1)
		})
	}

	step(0)
}

const settleDelay = 600 * time.Millisecond

func afterMain(d time.Duration, fn func()) {
	go func() {
		time.Sleep(d)
		objc.Main(fn)
	}()
}

func errText(err error) string {
	if err == nil {
		return "ok"
	}

	return err.Error()
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("smoke: %w", err)
	}

	if err := os.WriteFile(path, append(data, '\n'), filePerm); err != nil {
		return fmt.Errorf("smoke: %w", err)
	}

	return nil
}
