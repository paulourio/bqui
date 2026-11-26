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
	"strings"
	"time"

	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/ui"
)

const (
	dirPerm  = 0o750
	filePerm = 0o600

	maxDispatchDelay = 2 * time.Second

	editorReadyTimeout = 45 * time.Second // no GPU in the QEMU VM: Monaco boots slowly
	selfTestTimeout    = 20 * time.Second
	runTimeout         = 10 * time.Second
	pollInterval       = 100 * time.Millisecond
)

// selfTestSQL has a valid statement, a syntax error, and a cursor position
// (line 3, column 8) right after "SELECT " where completion must offer items.
const selfTestSQL = "SELECT 1 AS one;\nSELEC broken;\nSELECT \n"

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

// Start must be called from a worker goroutine. It waits for the Monaco
// editor, runs its self-test, hops to the main thread (exercising
// objc.Main), runs the remaining checks, then calls done on the main thread.
func Start(s *ui.Shell, dir, version string, done func(error)) {
	bootStart := time.Now()
	ready := s.Editor.WaitReady(editorReadyTimeout)
	bootTime := time.Since(bootStart)

	var (
		selfTest json.RawMessage
		stErr    error
	)

	var runStatus string

	if ready {
		selfTest, stErr = s.Editor.RunSelfTest(selfTestSQL, 3, 8, selfTestTimeout)
		runStatus = runAtCursor(s)
	}

	queued := time.Now()

	objc.Main(func() {
		rep := &Report{
			Version:       version,
			GoVersion:     runtime.Version(),
			Arch:          runtime.GOARCH,
			OSVersion:     appkit.OSVersion(),
			DispatchDelay: time.Since(queued),
		}

		editorChecks(s, rep, ready, bootTime, selfTest, stErr)

		if ready {
			// Cursor is on line 3 ("SELECT "), the third statement.
			ok := strings.Contains(runStatus, "statement") && strings.Contains(runStatus, ": SELECT")
			rep.check("editor-run-at-cursor", ok, "status: %q", runStatus)
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
}

// runAtCursor triggers "Run Statement at Cursor" (page → Go → bqls
// statements → shell) and waits for the results status to change.
func runAtCursor(s *ui.Shell) string {
	statusCh := make(chan string, 1)
	read := func() string {
		objc.Main(func() { statusCh <- s.Status() })

		return <-statusCh
	}

	before := read()

	objc.Main(func() { s.Editor.RequestRun(true) })

	deadline := time.Now().Add(runTimeout)
	for time.Now().Before(deadline) {
		if st := read(); st != before {
			return st
		}

		time.Sleep(pollInterval)
	}

	return before
}

func editorChecks(s *ui.Shell, rep *Report, ready bool, boot time.Duration, selfTest json.RawMessage, stErr error) {
	consoleErrs, bootErr := s.Editor.Diagnostics()

	rep.check("editor-ready", ready, "Monaco ready after %v (boot error: %q)", boot.Round(time.Millisecond), bootErr)

	if !ready {
		return
	}

	var st struct {
		Suggestions int      `json:"suggestions"`
		Labels      []string `json:"labels"`
		Markers     int      `json:"markers"`
		FirstMarker string   `json:"firstMarker"`
		Spans       int      `json:"spans"`
	}

	if stErr == nil {
		stErr = json.Unmarshal(selfTest, &st)
	}

	rep.check("editor-selftest", stErr == nil, "self-test: %v", errText(stErr))
	rep.check("editor-completion", st.Suggestions > 0, "%d suggestions, first: %s", st.Suggestions, strings.Join(st.Labels, " | "))
	rep.check("editor-diagnostics", st.Markers > 0, "%d markers, first: %q", st.Markers, st.FirstMarker)
	rep.check("editor-tokens", st.Spans > 0, "%d colored token spans", st.Spans)

	first := ""
	if len(consoleErrs) > 0 {
		first = consoleErrs[0]
	}

	rep.check("editor-console", len(consoleErrs) == 0, "%d console errors; first: %q", len(consoleErrs), first)
}

func accessibilityChecks(ax *appkit.AXNode, rep *Report) {
	for _, role := range []string{"AXOutline", "AXSplitGroup", "AXToolbar"} {
		found := ax.Find(func(n *appkit.AXNode) bool { return n.Role == role }) != nil
		rep.check("ax-"+role, found, "role %s present: %v", role, found)
	}

	// The editor WKWebView is an AXGroup host as the first pane of the inner
	// (editor | results) split; its web content lives in WebKit's process.
	inner := ax.Find(func(n *appkit.AXNode) bool {
		return n.Role == "AXSplitGroup" && len(n.Children) > 0 && n.Children[0].Role == "AXGroup"
	})
	rep.check("ax-editor-host", inner != nil, "editor web view host present: %v", inner != nil)
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
