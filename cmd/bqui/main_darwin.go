//go:build darwin

// Command bqui is a native macOS BigQuery client written in pure Go.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"google.golang.org/api/option"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/bqstorage"
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

	settings, settingsPath := loadSettings(*fake, *smokeDir != "")

	var shell *ui.Shell

	backends, err := newBackends(*fake, func() string { return shell.BillingProject() })
	if err != nil {
		fatal(err)
	}

	objc.WithPool(func() {
		var err error

		shell, err = ui.NewShell(ui.Options{
			Deterministic: *smokeDir != "",
			Inspectable:   *inspect,
			Backend:       backends.metadata,
			Queries:       backends.queries,
			Storage:       backends.storage,
			Jobs:          backends.jobs,
			Live:          !*fake,
			Settings:      settings,
			SettingsPath:  settingsPath,
			SpillDir:      spillDir(),
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

type backends struct {
	metadata app.Backend
	queries  app.QueryBackend
	storage  app.StorageOpener
	jobs     app.JobsBackend
}

// newBackends wires BigQuery (REST + Storage Read API) or the fixture fake.
// billing is read when a Storage Read session opens (any goroutine).
func newBackends(fake bool, billing func() string) (backends, error) {
	if fake {
		f := &app.FakeBackend{Latency: fakeLatency}

		return backends{metadata: f, queries: f, jobs: f}, nil
	}

	ctx := context.Background()

	client, err := bq.New(ctx, bq.Options{})
	if err != nil {
		return backends{}, err
	}

	sc, err := bqstorage.New(ctx, option.WithTokenSource(client.Tokens()))
	if err != nil {
		return backends{}, err
	}

	return backends{metadata: client, queries: client, jobs: client, storage: app.NewStorageOpener(sc, billing)}, nil
}

const staleSpill = 24 * time.Hour

// spillDir returns the result spill directory, removing spill files left by
// crashed sessions.
func spillDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}

	dir := filepath.Join(base, "bqui", "results")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > staleSpill {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}

	return dir
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
