# bqui

A native macOS BigQuery client in pure Go: AppKit through
[purego](https://github.com/ebitengine/purego), with no cgo, and one binary. The editor is
Monaco, backed by the in-process [bqls](https://github.com/paulourio/bqlang) language
server.

**Features**
- **Explorer:** projects, datasets, tables, views, routines and models, with schemas, details and previews. Previews read through the Storage Read API at no query cost.
- **Editor:** completion, hover, diagnostics, formatting, and dry-run cost estimates from bqls. You can run the selection, the statement at the cursor, or the whole script.
- **Results:** a console-style grid. Nested and repeated fields expand into sub-rows and never into JSON cells. There is also a memory-safe JSON view and a value inspector. Large results stream through the Storage Read API and spill to disk.
- **Jobs:**
  - Your jobs or the whole project's.
  - Job details, execution details (stages), and an execution graph.
  - Local run history with full-text search.
- **Export:** CSV (console-style lines or JSON cells), JSON Lines, Parquet and Arrow IPC.
- **Workspace:**
  - Saved queries are `.sql` files in `~/Documents/bqui/queries`.
  - The editor autosaves, and survives a crash.
  - BigQuery session mode (temp tables and variables persist between runs).
- **Themes:** built-in themes, TOML themes and VS Code theme import.

## Install

Download `bqui-<version>-darwin-arm64.zip` from CI artifacts or releases, unzip it, and move
`bqui.app` to `/Applications`. The build is ad-hoc signed, not notarized, so clear the
quarantine flag once:

```sh
xattr -dr com.apple.quarantine /Applications/bqui.app
```

You need macOS 26 (Tahoe) and Google credentials. Use Application Default Credentials
(`gcloud auth application-default login`) or a logged-in `gcloud`.

## Where things live

| What | Path |
|---|---|
| Settings (billing project, pinned projects, theme, query guards) | `~/Library/Application Support/bqui/settings.json` |
| Editor state and run history (SQLite) | `~/Library/Application Support/bqui/bqui.db` |
| Saved queries | `~/Documents/bqui/queries/*.sql` |
| Themes | `~/Library/Application Support/bqui/themes/*.toml` |
| Result spill files | `~/Library/Caches/bqui/results` |
| Logs and crash reports | `~/Library/Logs/bqui` |

**Settings (⌘,):**
- Default location and default dataset.
- Maximum bytes billed: BigQuery enforces it, and runs fail instead of billing more.
- bqls cost warning threshold.
- Result spill limit.

## Build and test

Everything cross-compiles from Linux with `CGO_ENABLED=0`:

```sh
go test ./...                     # app, results, plan, export, store, theme, … (Linux)
make lint                         # golangci-lint for linux, darwin/arm64, darwin/amd64
make release-arm64                # dist/bqui-<version>-darwin-arm64.zip
make vm-smoke                     # scripted UI smoke test in the macOS VM (fixture backend)
BQUI_LIVE_PROJECT=my-project go test -tags live ./internal/app/   # against BigQuery
```

`bqui -fake` runs against recorded fixtures, with no credentials and no network.

Builds and CI download only Go modules, through `GOPROXY`. Monaco and the Fira Code Nerd Font
are vendored under `internal/editor/web`. `make update-assets` refreshes them, and you run it
by hand only.

See [docs/ROADMAP.md](docs/ROADMAP.md) for what is done, what was deferred, and the known
issues.
