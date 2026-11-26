//go:build darwin

// Command bqui is a native macOS BigQuery client written in pure Go.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/smoke"
	"github.com/paulourio/bqui/internal/store"
	"github.com/paulourio/bqui/internal/ui"
)

var version = "dev"

const fakeLatency = 150 * time.Millisecond

func init() {
	// AppKit must run on the process main thread.
	runtime.LockOSThread()
}

func main() {
	smokeDir := flag.String("smoke-test", "", "run the UI smoke test, write PNGs, ax.json and report.json here, then exit (implies -fake)")
	inspect := flag.Bool("inspect", version == "dev", "enable the Safari Web Inspector for the editor")
	fake := flag.Bool("fake", os.Getenv("BQUI_FAKE") == "1", "serve recorded fixture metadata instead of BigQuery")
	hold := flag.Duration("smoke-hold", 0, "keep the window open this long after a passing smoke test (for screen captures)")
	flag.Parse()

	if *smokeDir != "" {
		*fake = true
	}

	if err := objc.Load(); err != nil {
		fatal(err)
	}

	backend, err := newBackend(*fake)
	if err != nil {
		fatal(err)
	}

	settings, settingsPath := loadSettings(*fake, *smokeDir != "")

	objc.WithPool(func() {
		shell, err := ui.NewShell(ui.Options{
			Deterministic: *smokeDir != "",
			Inspectable:   *inspect,
			Backend:       backend,
			Live:          !*fake,
			Settings:      settings,
			SettingsPath:  settingsPath,
		})
		if err != nil {
			fatal(err)
		}

		appkit.SetDelegate(shell.App, &appkit.AppDelegate{
			DidFinishLaunching: func() {
				shell.ResolveBillingProject()

				if *smokeDir == "" {
					return
				}

				go smoke.Start(shell, *smokeDir, version, func(err error) {
					if err != nil {
						fatal(err)
					}

					time.AfterFunc(*hold, func() { objc.Main(func() { appkit.Terminate(shell.App) }) })
				})
			},
		})

		appkit.Run(shell.App)
	})
}

func newBackend(fake bool) (app.Backend, error) {
	if fake {
		return &app.FakeBackend{Latency: fakeLatency}, nil
	}

	return bq.New(context.Background(), bq.Options{})
}

// loadSettings reads the user's settings. Fake and smoke runs use an
// in-memory copy so they never touch real preferences.
func loadSettings(fake, smoke bool) (*store.Settings, string) {
	if fake || smoke {
		return &store.Settings{PinnedProjects: []string{app.FakeProject, store.DefaultPinned}, BillingProject: app.FakeProject}, ""
	}

	path, err := store.DefaultPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "bqui:", err)

		return &store.Settings{PinnedProjects: []string{store.DefaultPinned}}, ""
	}

	s, err := store.LoadSettings(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bqui:", err)

		return &store.Settings{PinnedProjects: []string{store.DefaultPinned}}, ""
	}

	return s, path
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "bqui:", err)
	os.Exit(1)
}
