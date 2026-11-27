//go:build darwin

// Command bqui is a native macOS BigQuery client written in pure Go.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"google.golang.org/api/option"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/bqstorage"
	"github.com/paulourio/bqui/internal/logs"
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

	// AppKit's terminate calls exit(): deferred calls never run, so the
	// terminate hook closes the logs.
	closeLogs := func() {}

	if *smokeDir == "" {
		if dir, err := logs.DefaultDir(); err == nil {
			if c, err := logs.Setup(dir); err == nil {
				closeLogs = c
			}
		}

		slog.Default().Info("bqui", "version", version, "fake", *fake)
	}

	settings, settingsPath := loadSettings(*fake, *smokeDir != "")

	workspace, closeDB := openWorkspace(*fake, *smokeDir)
	defer closeDB()

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
			Workspace:     workspace,
			ThemesDir:     themesDir(*smokeDir),
		})
		if err != nil {
			fatal(err)
		}

		appkit.SetDelegate(shell.App, &appkit.AppDelegate{
			WillTerminate: func() {
				_ = workspace.Flush()
				closeDB()
				closeLogs()
			},
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

// openWorkspace opens the local database and saved-queries folder. Smoke
// runs use a fresh database and folder under the smoke directory; fake runs
// keep nothing.
func openWorkspace(fake bool, smokeDir string) (*app.Workspace, func()) {
	var dbPath, queries string

	switch {
	case smokeDir != "":
		state := filepath.Join(smokeDir, "state")
		_ = os.RemoveAll(state)
		dbPath, queries = filepath.Join(state, "bqui.db"), filepath.Join(state, "queries")
	case !fake:
		var err error
		if dbPath, err = store.DefaultDBPath(); err != nil {
			fmt.Fprintln(os.Stderr, "bqui:", err)
		}

		if queries, err = store.DefaultQueriesDir(); err != nil {
			fmt.Fprintln(os.Stderr, "bqui:", err)
		}
	default:
		queries, _ = store.DefaultQueriesDir()
	}

	var db *store.DB

	if dbPath != "" {
		var err error
		if db, err = store.Open(dbPath); err != nil {
			fmt.Fprintln(os.Stderr, "bqui: local database unavailable, nothing will be restored:", err)
		}
	}

	w, err := app.OpenWorkspace(app.WorkspaceOptions{DB: db, QueriesDir: queries, InitialSQL: ui.SampleSQL})
	if err != nil {
		fmt.Fprintln(os.Stderr, "bqui: restoring the workspace failed:", err)

		w, _ = app.OpenWorkspace(app.WorkspaceOptions{QueriesDir: queries, InitialSQL: ui.SampleSQL})
	}

	var once sync.Once

	return w, func() {
		once.Do(func() {
			if db != nil {
				_ = db.Close()
			}
		})
	}
}

// themesDir is the user themes folder (a fresh one for smoke runs, which
// openWorkspace has already cleared).
func themesDir(smokeDir string) string {
	if smokeDir != "" {
		return filepath.Join(smokeDir, "state", "themes")
	}

	dir, err := store.DefaultThemesDir()
	if err != nil {
		return ""
	}

	return dir
}
