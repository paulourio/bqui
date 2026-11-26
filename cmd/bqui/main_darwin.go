//go:build darwin

// Command bqui is a native macOS BigQuery client written in pure Go.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/objc"
)

const appName = "bqui"

var version = "dev"

func init() {
	// AppKit must run on the process main thread.
	runtime.LockOSThread()
}

type smokeReport struct {
	Version       string        `json:"version"`
	GoVersion     string        `json:"goVersion"`
	Arch          string        `json:"arch"`
	OSVersion     string        `json:"osVersion"`
	WindowFrame   appkit.Rect   `json:"windowFrame"`
	DispatchDelay time.Duration `json:"dispatchDelayNs"`
	Screenshots   []string      `json:"screenshots"`
}

func main() {
	smokeDir := flag.String("smoke-test", "", "render the UI offscreen into PNGs plus report.json in this directory, then exit")
	flag.Parse()

	if err := objc.Load(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	objc.WithPool(func() { run(*smokeDir) })
}

func run(smokeDir string) {
	app := appkit.SharedApplication()
	appkit.SetMainMenu(app, appName, []appkit.MenuItem{
		{Title: "About " + appName, Action: "orderFrontStandardAboutPanel:"},
		{},
		{Title: "Hide " + appName, Action: "hide:", Key: "h"},
		{},
		{Title: "Quit " + appName, Action: "terminate:", Key: "q"},
	})

	win := appkit.NewWindow(appName, appkit.MakeRect(0, 0, 1100, 700))
	content := objc.Msg(win, "contentView")
	appkit.CenterIn(appkit.NewLabel("bqui · pure Go AppKit · "+runtime.GOARCH, 28), content)
	objc.Msg(win, "center")
	objc.Msg(win, "makeKeyAndOrderFront:", objc.Nil)

	delegate := newAppDelegate(func() {
		if smokeDir != "" {
			go runSmoke(app, win, smokeDir)
		}
	})
	objc.Msg(app, "setDelegate:", delegate)
	objc.Msg(app, "activate")
	objc.Msg(app, "run")
}

func newAppDelegate(onLaunch func()) objc.ID {
	cls, err := objc.RegisterClass("BQUIAppDelegate", "NSObject", []string{"NSApplicationDelegate"}, []objc.Method{
		{Sel: "applicationDidFinishLaunching:", Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) { onLaunch() }},
		{Sel: "applicationShouldTerminateAfterLastWindowClosed:", Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) bool { return true }},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	return objc.Msg(objc.Msg(objc.ID(cls), "alloc"), "init")
}

// runSmoke runs on a worker goroutine and hops to the main thread through
// objc.Main, which also exercises the dispatch trampoline.
func runSmoke(app, win objc.ID, dir string) {
	queued := time.Now()

	objc.Main(func() {
		rep := smokeReport{
			Version:       version,
			GoVersion:     runtime.Version(),
			Arch:          runtime.GOARCH,
			OSVersion:     appkit.OSVersion(),
			WindowFrame:   objc.Send[appkit.Rect](win, "frame"),
			DispatchDelay: time.Since(queued),
		}

		if err := smoke(win, dir, &rep); err != nil {
			fmt.Fprintln(os.Stderr, "smoke:", err)
			os.Exit(1)
		}

		objc.Msg(app, "terminate:", objc.Nil)
	})
}

func smoke(win objc.ID, dir string, rep *smokeReport) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}

	objc.Msg(win, "displayIfNeeded")

	// The frame view is the content view's superview, so the shot includes the titlebar.
	frameView := objc.Msg(objc.Msg(win, "contentView"), "superview")

	shot := filepath.Join(dir, "main-window.png")
	if err := appkit.WritePNG(frameView, shot); err != nil {
		return err
	}

	rep.Screenshots = append(rep.Screenshots, filepath.Base(shot))

	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, "report.json"), append(data, '\n'), 0o600)
}
