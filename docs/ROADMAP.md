# bqui — native macOS BigQuery client in pure Go (no cgo)

## Context

`~/dev/bqlang` already provides a pure-Go GoogleSQL toolchain: formatter, `bqls` LSP 3.17 server (semantic tokens, completion, hover, diagnostics, dry-run cost), and a BigQuery metadata catalog with an SQLite (modernc) cache. All of it is pure Go and builds with `CGO_ENABLED=0`, but it lives under `internal/`.

`bqui` (empty repo, `origin = git@git.home.arpa:tea/bqui.git`) will be a **native AppKit app for macOS 26 Tahoe on Apple silicon (M4)**, written in pure Go with no cgo. It is a desktop alternative to the BigQuery console, with:
- a dataset/table browser and metadata views
- a modern SQL editor powered by bqls
- query runs with a console-style results grid that handles deep nesting, plus a JSON view with memory limits
- job history, query plan details and an execution graph
- export through the BigQuery Storage Read API
- local saved queries and sessions, plus sync with BigQuery Studio saved queries
- saveable themes

### Decisions made with the user
| Topic | Decision |
|---|---|
| UI stack | Real **AppKit through `github.com/ebitengine/purego` + `purego/objc`** (no cgo). The only web content is the SQL editor |
| Editor | **Monaco** in a `WKWebView` (assets embedded, served via a custom `bqui://` scheme). Go only relays LSP frames between Monaco and the in-process bqls |
| bqlang reuse | **Promote public packages in bqlang** (`lsp`, `catalog`, `bqconfig`). bqui runs bqls **in-process** over `io.Pipe`, so there is a single binary |
| Auth | ADC first, with gcloud fallback (bqlang's existing chain), plus a `cloud-platform` scope option for Dataform |
| Dev and test | Develop on Gentoo (i9-13900K) and cross-compile. **QEMU macOS Tahoe x86_64 VM** for interactive UI work (no GPU acceleration, so no perf or visual-polish judgments there). **Private GitHub mirror + GitHub Actions arm64 macOS runner** for the arm64 ABI and smoke screenshots |
| Results grid | **BigQuery console style only.** Leaf-path columns `a.b.c`, REPEATED fields shown as stacked sub-rows, at any depth. **Never JSON inside a cell**, including arrays of structs |
| Persistence | Local: SQLite (history, sessions) + `.sql` files (saved queries) + TOML (themes). Plus **BigQuery Studio saved queries** (Dataform) as a second library, with an optional per-query link |
| MVP | Browse + edit + run + grid + job history |

---

## Architecture

```
bqui/
  cmd/bqui/            main (darwin): LockOSThread, NSApplication run loop
  cmd/bquictl/         headless CLI (linux+darwin) that drives the same core: query, export, plan dump, layout dump
  internal/
    objc/              purego objc helpers: typed msgSend, selector cache, class builder, blocks, autorelease pools, main-thread dispatch
    foundation/ appkit/ coregraphics/ coretext/ quartzcore/   thin hand-written bindings (only what we use), darwin-only
    ui/                AppKit views/controllers (darwin build tag): window, sidebar, editor, grid, plan graph, inspectors
    app/               UI-agnostic view-models + command bus (testable on Linux)
    bq/                BigQuery client facade: projects, datasets, tables, routines, models, jobs, query execution
    bqstorage/         Storage Read API sessions + Arrow decoding (arrow-go, pure Go)
    results/           result store (Arrow IPC spill), schema flattener, sub-row layout engine, value formatter, JSON renderer
    plan/              query plan model, DAG layout (Sugiyama), timeline model
    export/            CSV / NDJSON / Parquet / Arrow IPC writers, parallel streams, progress
    webkit/            WKWebView, URL scheme handler, script message handler, JS evaluation (darwin)
    editor/            Monaco host: embedded assets (web/), JS↔Go bridge, LSP relay to in-process bqls (UI-agnostic core)
    store/             SQLite schema/migrations (history, sessions, tabs, links), saved-query folder, settings
    dataform/          BigQuery Studio saved-query sync (Dataform API)
    theme/             theme model, TOML load/save, VS Code theme import, appearance resolution
    smoke/             --smoke-test harness: scripted UI walk, offscreen PNG capture, AX tree dump
  tools/bundle/        Go program: builds bqui.app (Info.plist, .icns, ad-hoc signing via rcodesign), zips artifact
  tools/fetchassets/   Go program: downloads the pinned monaco-editor npm tarball, checks sha512, extracts min/vs
  .github/workflows/macos.yml
  Makefile             build-darwin-arm64, build-darwin-amd64 (QEMU), bundle, vm-deploy, test
```

**Layering rule.** `ui` → `app` → (`bq`, `results`, `plan`, `store`, …). `app` and below never import `objc`/`appkit`, so roughly 80% of the code is unit-tested on Linux. Darwin-only packages carry a `//go:build darwin` tag.

**Threading model.** The main goroutine calls `runtime.LockOSThread()` in `init`, and AppKit runs there. Everything else runs on worker goroutines. UI updates go through a single `objc.Main(func())`, which is a Go-side queue drained by **one** purego callback trampoline registered with `dispatch_async_f`. This matters because `purego.NewCallback` slots are finite (about 2000) and never freed. Method implementations for our own ObjC subclasses (delegates, data sources, NSView subclasses) are registered once per class through `objc.RegisterClass`, never per instance.

**Key dependencies (all pure Go)**
- `github.com/ebitengine/purego` (+`/objc`)
- `cloud.google.com/go/bigquery` (REST surface: jobs, plans, metadata)
- `cloud.google.com/go/bigquery/storage/apiv1` (raw Storage Read, Arrow format)
- `github.com/apache/arrow-go/v18` (ipc, array)
- `github.com/parquet-go/parquet-go` or `arrow-go/parquet/pqarrow`
- `modernc.org/sqlite` (already used by bqlang)
- `monaco-editor@0.57.0` (MIT; static JS/CSS assets embedded with `go:embed`, not a Go dependency)
- `github.com/BurntSushi/toml`
- `google.golang.org/api/dataform/v1`
- `github.com/paulourio/bqlang` v1.2.0 (public `lsp`, `catalog`, `bqconfig`)

The build always uses `CGO_ENABLED=0`, and CI enforces it.

---

## Roadmap

Each milestone lists deliverables, then tests, then the exit criteria (**Exit**).

### M0 — Foundations (bqlang API + repo + build/test loop)

**bqlang changes** (in `~/dev/bqlang`, on a separate branch/PR):
1. Promote `internal/lsp` to `lsp/`, exposing `NewServer(ServerOptions)` and `(*Server).Serve(ctx, r, w)`. These already exist in `internal/lsp/server.go:201,291`. Add `ServerOptions.Store *catalog.Store` (inject a shared store) and `ServerOptions.TokenProvider`.
2. Promote `internal/catalog` to `catalog/`: `TableRef`, `TableMeta`, `ColumnMeta`, `Store`, `RestClient`, `TokenProvider`, `DiscoverDatasets/Tables`.
3. Promote `internal/bqconfig` to `bqconfig/`.
4. Add a scope option to `defaultTokenCache` (`internal/catalog/bqrest.go:27-28,581`) so bqui can request `cloud-platform`.
5. Add custom LSP request `bqls/statements`, which returns statement ranges and kinds (from the existing parser) for "run statement at cursor" and script splitting. Reuse the `textDocument/documentSymbol` machinery.
6. Keep `analysis`, `syntax` and `funcdocs` internal. bqui consumes them only through LSP.
7. Add e2e tests for in-process Serve over `io.Pipe`, following `e2e/harness_test.go`.

**bqui scaffold**
- Module `github.com/paulourio/bqui`, Go 1.26, requires `github.com/paulourio/bqlang v1.2.0` (published; use a local `go.work` only when co-developing).
- `.golangci.yml` copied from bqlang. Makefile targets as above.
- `tools/bundle` builds `bqui.app/Contents/{MacOS/bqui, Info.plist, Resources/AppIcon.icns}`. It sets `LSMinimumSystemVersion=26.0`, `NSHighResolutionCapable` and a bundle ID, then ad-hoc signs with `rcodesign` from Linux.

**Infrastructure**
- Gitea push mirror to a private GitHub repo.
- `macos.yml` runs on `macos-26` (fallback `macos-15`), arm64: `go test ./...`, bundle, `bqui --smoke-test`, then uploads PNG/AX/log artifacts. It triggers on pushes to `master`, on tags, and manually, to save minutes.
- QEMU Tahoe VM: SSH key, a shared folder or `scp` deploy (`make vm-deploy` builds darwin/amd64 and runs `open bqui.app`). Pin P-cores 0–15.

**Exit:** bqui imports `bqlang/lsp`. `CGO_ENABLED=0 GOOS=darwin GOARCH={arm64,amd64} go build` succeeds. CI is green on a hello-world.

### M1 — AppKit binding layer + app shell

**Status: done.** The editor pane is a plain `NSTextView` placeholder until M4 replaces it with Monaco.

**`internal/objc`**
- `Send[T]` typed msgSend wrappers.
- Struct returns: NSRect/CGRect use `objc_msgSend_stret` on **amd64 only**, so the code needs per-arch build files.
- `Sel` cache, `NSString` ↔ Go string, `NSArray`, `NSDictionary`, `NSNumber`.
- Autorelease pool scope, retain/release helpers, ObjC blocks (purego objc block support, or a minimal block ABI).
- Class builder with ivar → Go handle map.

**`foundation`, `appkit`, `coregraphics`, `coretext`:** only the selectors needed. Hand-written with tiny generic helpers. No SDK-wide generator.

**App shell**
- `NSApplication` with a main menu: App, File, Edit, View, Query, Window, Help, with standard key equivalents.
- `NSWindow` with full-size content and a unified `NSToolbar`. It contains project picker, run, stop, format, cost label and a search field.
- `NSSplitViewController` holding:
  - a **sidebar item** (Explorer / Saved / History source list), which picks up Liquid Glass automatically on Tahoe;
  - a **content** area with tabs (`NSTabView` or a custom tab bar) holding editor/results split views;
  - an **inspector item** for metadata and value inspector.
- Window state restoration (frame, splits) saved to `store`.

**Smoke harness (`internal/smoke`)**
- Run with `bqui --smoke-test=script.toml`.
- Opens views, renders each with `bitmapImageRepForCachingDisplayInRect:` + `cacheDisplayInRect:toBitmapImageRep:`, writes PNG files (no TCC permission needed), dumps the AX tree to JSON, and exits non-zero on error.

**Tests**
- ABI tests on both arches: `NSMakeRect` round trip, `frame` struct return, block invocation, callback count budget.
- A fake-backend mode (`BQUI_FAKE=1`) loads fixtures so screenshots are deterministic.

**Exit:** the shell renders in the QEMU VM and on the CI arm64 runner. Screenshots are uploaded. The app has no leaks over 10k open/close cycles (checked with `leaks` from the CLT).

### M2 — BigQuery core + `bquictl`

**`internal/bq`**
- Uses `cloud.google.com/go/bigquery` with an `oauth2.TokenSource` adapter over bqlang's `catalog.TokenProvider` chain.
- Projects: Resource Manager `projects.list` + pinned/starred projects.
- Datasets, tables, views, materialized views, external tables, snapshots, clones, routines, models.
- Datasets and tables are listed through bqlang `catalog.Store` (shared with the LSP, so there is one cache).
- Queries run through `jobs.query` with `jobCreationMode=JOB_CREATION_OPTIONAL`, falling back to `jobs.insert`. Options: params, labels, maximum bytes billed, dry run, priority, location, legacy SQL off, session mode (`create_session`/`session_id`).
- Multi-statement scripts: list child jobs (`parentJobId`) for results per statement.
- Polling with backoff, cancel (`jobs.cancel`), streaming progress into `app` events.

**`internal/bqstorage`:** CreateReadSession (Arrow, `max_stream_count`, selected fields, row restriction), ReadRows on N goroutines, backpressure, and splitting.

**`cmd/bquictl`:** `ls`, `show`, `query`, `jobs`, `plan`, `export`, `layout`. It serves as a dev oracle, and every core feature is exercised on Linux before UI work.

**Tests:** `httptest` fakes for REST, an in-process gRPC fake for Storage Read, and golden JSON fixtures recorded from real responses (sanitized) under `testdata/`.

**Exit:** `bquictl query` and `bquictl export` work against a real sandbox project from Linux.

### M3 — Explorer (dataset/table browser + metadata)

**Status: done.** What shipped, and where it differs from the plan below:
- **Explorer** (`app.Explorer`, UI-agnostic and tested on Linux): pinned projects → datasets → tables, views, materialized views, external tables, snapshots, routines and models, loaded lazily through the `app.Backend` interface (`*bq.Client`, or `app.FakeBackend`). Date-sharded tables are grouped like the console (`ga_sessions_(366)`). Includes a client-side filter, error placeholders with retry, Refresh, and per-kind context menus (Query, Insert Reference, Copy ID, Copy SQL Reference, Open in BigQuery Console, Use as Billing Project, Unpin). Double-click inserts the reference into the editor.
- **Metadata goes in the inspector pane, not in table tabs.** The inspector has Schema and Details tabs; per-object tabs arrive with tabs and sessions (M10).
  - Schema: a nested tree (name, type, mode, key badges, description). Parameterized types are shown, and partition, cluster and policy-tag badges appear in a Key column that is hidden when no field has any.
  - Details: the console's sections for tables, views, materialized views, external tables, snapshots and clones, plus datasets, routines and models. View SQL is formatted with bqfmt. A view whose SQL is hidden from the account says so instead of showing an empty value.
- **Not done:** Preview moves to M5, where it uses the results grid. Partitions (`INFORMATION_SCHEMA.PARTITIONS`) and Delete were not built: Partitions is deferred, and Delete is a destructive action that needs its own confirmation design.
- **Projects:** the toolbar picker lists pinned projects and then every accessible project. The selection is the billing project and also bqls's default project (pushed live through `workspace/didChangeConfiguration`), and it persists in `~/Library/Application Support/bqui/settings.json`.
- **Fake mode:** `--fake` / `BQUI_FAKE=1` (the smoke test turns it on) serves fixtures recorded from `bigquery-public-data`, plus a synthetic `bqui-demo` project with a 2,000-table dataset.
- **Binding-layer finding:** purego callbacks cannot return floating-point values, so delegate methods that return `CGFloat` (such as `heightOfRowByItem:`) cannot be implemented in Go. Use `usesAutomaticRowHeights` and Auto Layout instead.
- **To check on real hardware:**
  - In the QEMU VM, the unselected segment of the inspector's segmented control is invisible on screen, though a CPU render of the same control draws it correctly. This is most likely vibrancy compositing without a GPU.
  - At 1280 points wide, the toolbar overflows and hides the cost label.
- **Exit:** the VM smoke test drives the real UI path (expand through the outline delegate, select to update the inspector, filter). Reloading the 2,000-table dataset takes 71 ms on the main thread in the GPU-less VM.

**Sidebar `NSOutlineView`**
- Hierarchy: Project → Dataset → (Tables | Views | Routines | Models), with lazy children through `DiscoverTables`.
- Type icons from SF Symbols (`NSImage imageWithSystemSymbolName:`), filter field, starred items, refresh, and a context menu: Query, Preview, Copy ID, Copy as `project.dataset.table`, Open in console, Delete (with confirmation).

**Table tab** with sub-tabs, as in the console:
- **Schema:** nested `NSOutlineView` showing name, type, mode, description, policy tags, default value, collation. Partition and cluster columns are badged, reusing `TableMeta.IsPartitionColumn` / `ClusterRank`.
- **Details:** IDs, created/modified, expiration, labels, partitioning and clustering spec, `require_partition_filter`, encryption, row count, logical/physical/long-term bytes, time travel window, and a view or materialized view definition (formatted with bqfmt).
- **Preview:** free via Storage Read with a row limit (or `tabledata.list` for views that can't be read). It is shown in the M5 grid.
- **Partitions:** `INFORMATION_SCHEMA.PARTITIONS` (on demand, with a cost warning).
- **Lineage:** later, optional.

The Dataset tab shows location, default expiration, labels, access and table count. The Routine tab shows the DDL and arguments. The Model tab shows the type, features and training runs.

**Exit:** you can browse a real project with about 1k tables, and the tree stays responsive with no blocking on the main thread.

### M4 — SQL editor: Monaco in WKWebView, powered by bqls

**Status: done.**
- **LSP client:** Monaco 0.57 ships its own `monaco.lsp.MonacoLspClient`. The page hands it a Worker-shaped transport (`createTransportToWorker`), so the planned ~600-line hand-written adapter was not needed. `bqui-editor.js` is ~300 lines: transport, themes, key bindings, run requests and a smoke self-test.
- **Go side:** Go relays LSP frames, intercepts `bqls/queryCost`, and issues its own `bqls/statements` requests in a private ID space.
- **Size:** assets are 2.0 MiB gzipped (the TypeScript, CSS and HTML workers are excluded).
- **Smoke (VM):** Monaco is ready in under 1 s with no GPU; completion, diagnostics, semantic colors and run-at-cursor are verified end to end.
- **Risk:** Monaco's AMD build is deprecated upstream. If a future release drops it, bundle the ESM build with esbuild's Go API inside `tools/fetchassets` (still no Node).

The editor is **Monaco** (MIT, the VS Code editor component) hosted in a `WKWebView`. Monaco gives us multi-cursor, folding, minimap, bracket colorization, find/replace, the command palette, and the completion/hover/signature UI for free. Everything else in the window stays native AppKit. The WebView is still driven from Go through purego, so there is still no cgo.

**Assets**
- Monaco (and the FiraCode Nerd Font Mono editor font) are **vendored**: the gzip-compressed files and their licenses are committed under `internal/editor/web/`. Builds and CI make no network calls except Go module downloads through GOPROXY. `make update-assets` (`tools/fetchassets`, a maintainer-only tool) downloads a pinned release from npm or GitHub, verifies its digest, and rewrites the vendored files for review. A test fails if the committed files drift from the pins. No Node.js is needed.
- Assets are embedded with `go:embed` and served to the WebView by a Go `WKURLSchemeHandler` on `bqui://editor/...`. Nothing is extracted to disk, and nothing loads from the network: the CSP is `default-src bqui:`.
- Monaco's web workers are created from same-origin `bqui://` URLs via `MonacoEnvironment.getWorkerUrl`. The editor falls back to main-thread mode if workers are unavailable.
- Only the languages we need are kept (SQL tokenizer as a fallback, plus JSON), to keep the binary small.

**WebKit bindings (`internal/webkit`, darwin)**
- `WKWebView` and `WKWebViewConfiguration`, plus the URL scheme handler (a Go class implementing `webView:startURLSchemeTask:` / `stopURLSchemeTask:`).
- `WKScriptMessageHandler` (a Go class) for JS → Go messages, and `evaluateJavaScript:completionHandler:` (a purego ObjC block) for Go → JS.
- Navigation is locked down: no external URLs, links open in the default browser, Web Inspector is enabled only in debug builds (`isInspectable`).

**Bridge (`internal/editor`, UI-agnostic, tested on Linux)**
- Go is only a **transport**. The page's JS adapter speaks LSP JSON-RPC, and Go forwards those frames unchanged to the in-process bqls (`bqlang/lsp` over `io.Pipe`) and back. No LSP types are re-modelled in Go.
- App-level messages share the same channel with their own method namespace (`bqui/run`, `bqui/runStatement`, `bqui/cancel`, `bqui/cursor`, `bqui/dirty`, `bqui/setTheme`, `bqui/setText`, `bqui/focus`).
- Go → JS messages are batched per run-loop turn, so a burst of diagnostics or semantic tokens becomes one `evaluateJavaScript` call.

**JS adapter (`internal/editor/web/bqui-editor.js`, ~600 lines, no bundler)**
- Maps LSP ↔ Monaco:
  - `didOpen` and incremental `didChange` from Monaco content-change events (UTF-16 offsets map directly)
  - `publishDiagnostics` → `setModelMarkers`
  - `completion` → `CompletionItemProvider`, `hover` → `HoverProvider` (Markdown rendered natively by Monaco), `signatureHelp` → `SignatureHelpProvider`
  - `definition` → `DefinitionProvider`; table references are intercepted and open the table tab in the native UI
  - `documentSymbol` → outline
  - `formatting` → `DocumentFormattingEditProvider` (bqfmt)
  - `codeLens` → `CodeLensProvider` (cost per statement)
  - `semanticTokens/full/delta` → `DocumentSemanticTokensProvider`, with the bqls legend including the `partitioned`, `clustered` and `pseudo` modifiers
  - `bqls/queryCost` → a `bqui/cost` message to the toolbar label
- Monaco `addCommand` bindings:
  - ⌘↩ runs the selection or the whole script
  - ⇧⌘↩ runs the statement at the cursor (`bqls/statements`)
  - ⌘. cancels
  - ⇧⌘F formats

  Native menu items with the same shortcuts call the same bridge, so menus and keys stay consistent.
- Editor options come from settings and theme: font, ligatures, minimap, word wrap, rulers. Vim mode (`monaco-vim`) is deferred to M13.

**Tabs:** one Monaco instance per window with **one model per query tab**. Switching tabs swaps the model and keeps view state, so it's cheap and preserves undo history.

**Tests**
- Linux: bridge routing and batching tests, plus an end-to-end LSP round trip through the bridge against the in-process bqls (`initialize` → `didOpen` → `completion`).
- macOS smoke: the harness loads the editor, waits for `bqui/ready`, types via `evaluateJavaScript`, asserts that diagnostics and completion items come back, and screenshots the editor.

**Exit:** a 5k-line script edits without visible lag in the VM. Completion, hover, diagnostics, semantic colors, formatting and cost lenses work, both offline (with `schema_dirs`) and online. The binary grows by ≤ 8 MB.

### M5 — Query results: store + console-style grid

**Status: done.** What shipped, and where it differs from the plan below:
- **`internal/results` (Linux-tested):**
  - Flattening to leaf columns and the console sub-row layout at any depth. A randomized check compares the renderer with the line counter.
  - Console value formatting and JSON row encoding.
  - Arrow → value conversion that matches the REST decoder for every type. A live parity test (`-tags live`) proves REST and the Storage Read API produce identical rows, including INTERVAL, RANGE, DATETIME and TIME edge cases probed against BigQuery.
- **Grid:** indexes visual lines per chunk and keeps only per-chunk metadata for the whole result, behind LRU caches of decoded chunks and rendered rows.
- **Storage path:** instead of re-encoding Arrow IPC, the spill file stores the raw LZ4 batches BigQuery sent and decodes them on demand.
- **`app.Runner`:**
  - Picks inline (first page of 5,000 rows), REST paging (script results without a destination; capped at 1M rows) or Storage Read API streaming (spilled, capped at 8 GiB).
  - Reports progress, cancels the BigQuery job on Stop, and throttles UI updates.
  - Table Preview reads 1,000 rows through the Storage Read API, at no query cost.
- **UI:**
  - The grid is a view-based `NSTableView` with one row per visual line, rather than the custom `NSView` planned, which gives native selection, resizing, reordering and accessibility for free.
  - It has a row-number gutter, 1pt logical-row separators, right-aligned numbers, italic `null`, ⌘C copy as TSV, and Copy Rows as JSON or Copy Cell Value.
  - A status line shows running, loading, done/failed/cancelled, plus bytes, slot time and elapsed time.
- **Deferred:**
  - Cell-level selection and find-in-results.
  - The value inspector (M7).
  - The Execution details and Execution graph tabs (M8) and the Job information tab (M6).
  - The 50M-row memory benchmark, which needs real hardware.
- **AppKit findings:** `NSView.tag` is read-only; it can only be set on controls. A separator-style `NSBox` forces a 4pt frame and centers its line, so use a custom box with `separatorColor`.
- **Exit:** the VM smoke test (38 checks) covers a nested 5,000-row result shown as 11,250 lines, gutter and separator geometry, NULL styling, JSON copy, syntax errors, cancel, preview, and run-at-cursor executing only the statement under the cursor.

**Result store (`internal/results`)**
- **Small results** (inline from `jobs.query`): kept in memory as Arrow record batches.
- **Large results** (destination temp table): Storage Read API with parallel streams into **Arrow IPC spill files** at `~/Library/Caches/bqui/results/<jobId>/`. An in-memory **LRU page cache** of record batches has a configurable budget (default 256 MiB). Files are evicted on tab close or by age or size quota.
- Rows are fetched lazily. The grid asks for row windows, and only visible pages plus prefetch are decoded.

**Flattener and layout engine** (pure Go, the heart of the grid)
- The schema becomes leaf columns: `path`, type, and `repeatedDepth` (the chain of REPEATED ancestors). STRUCTs expand recursively, and so do arrays of structs at any depth. **Nothing is shown as JSON in a cell.**
- Line count for a logical row:
  - `lines(scalar) = 1`
  - `lines(struct) = max(lines(fields))`
  - `lines(array) = max(1, Σ lines(elem))`
  - An empty array or NULL counts as 1 blank line.
- For each leaf and line there is either a value, or a "continuation" (blank) entry, so cells merge visually like the console. Sibling arrays of different lengths leave blanks on the shorter ones.
- Row index: block-level prefix sums (1024 rows per block, int64) of line counts, filled in the background during ingest. Mapping a visual line to (row, sub-line) is O(log blocks).
- Arrays with 10k+ elements: cap the expansion at a configurable N lines per row, with a "… 9,876 more" affordance that opens the value inspector.

**Value formatting:** formatted the way the console does, with golden tests per type:
- TIMESTAMP as `YYYY-MM-DD HH:MM:SS.ffffff UTC`; DATETIME, DATE, TIME
- NUMERIC/BIGNUMERIC exact (decimal string)
- FLOAT64 with shortest round-trip, plus `NaN` and `Infinity`
- BYTES as base64
- GEOGRAPHY as WKT, JSON compact, INTERVAL canonical, RANGE `[a, b)`
- NULL in grey italic

**Grid view (`ui/grid`)**
- A custom `NSView` subclass inside `NSScrollView` with a fixed line height, so there are O(1) rows per frame and millions of lines stay virtualized.
- Draws only the dirty rect with CoreText/`NSString drawInRect`.
- Column virtualization for very wide tables (1000+ leaf columns).
- Header: leaf path, type tooltip, resizable and reorderable columns, column auto-width sampled from loaded pages.
- Row-number gutter spans the sub-lines. A full border separates logical rows, and faint dividers separate sub-lines inside repeated columns.
- Selection: cell, range, row, column. Copy as TSV, CSV, JSON or Markdown, bounded by size with a confirmation above the cap.
- Find in loaded results. Double-click a cell to open the value inspector.
- Accessibility: `NSAccessibilityTable` role with rows and cells for VoiceOver and the smoke AX dump.

**Results panel tabs** (as in the console):
- **Results (grid)**, **JSON** (M7), **Execution details** and **Execution graph** (M8), **Job information**
- The status line shows rows, bytes processed and billed, slot time, duration and cache hit.

**Tests**
- Layout engine golden tests: nested, repeated and mixed schemas (array of struct of array), sibling arrays, NULL and empty, giant arrays.
- Fuzz the layout against a reference recursive renderer.
- Memory test: a 50M-row synthetic result scrolled end to end must stay at or below the budget plus a fixed overhead (`bquictl layout --bench`).

**Exit:** the grid renders the console layout for a real nested public dataset (for example `bigquery-public-data.google_analytics_sample`, the GA4 sample). The 50M-row memory test passes on arm64 CI.

### M6 — Job history + job details → **MVP release v0.1**

**Status: done.** What shipped, and where it differs from the plan below:
- **Bottom-pane tabs:** Results, Job information and History. The Execution details and Execution graph tabs join in M8.
- **History:**
  - A My jobs / Project jobs scope over `jobs.list` (projection=full), newest first, loading more pages as you scroll.
  - Columns: state, created, duration, processed ("cached"), statement, user, query and job ID.
  - Double-click opens the SQL in the editor. The context menu offers Run Again, Show Results, Show Details (in the inspector), Cancel Job (while running), Copy Job ID and Open in BigQuery Console.
- **Show Results** (`Runner.OpenJob`) reads the job's destination table through the Storage Read API, falling back to `getQueryResults` paging. Expired temporary tables get a clear message.
- **Job information:** job, user, location, state and timings, parent/child jobs, session, reservation, priority, labels, the byte cap, statistics, destination and referenced tables, errors, and the bqfmt-formatted SQL.
- **Not done:**
  - Filters by state, type, time or label beyond the scope switch.
  - Error locations linked back into the editor.
  - Local run history in SQLite, which moves to M10 with sessions.
- **Release:** no tagged release or signed zip yet; that happens with M13 packaging.

**Jobs list**
- Personal (`jobs.list` with `allUsers=false`) and project-wide (`allUsers=true`), filtered by state, type, time range, label and text.
- Infinite list with columns: status, job ID, user, created, duration, bytes, slot-ms, statement type.

**Job detail tab**
- Configuration (formatted SQL), statistics, errors with line/column linking back into the editor, destination table, referenced tables, reservation, and child jobs.
- Actions: open query in editor, re-run, cancel, view results (if the destination table still exists).

**Local run history:** every run is recorded in SQLite (`store`) with SQL, project, job ID, stats and tab, and is searchable.

**Exit:** MVP tagged. A signed `.app` zip comes out of CI. A manual checklist is completed in the VM and on a rented M4 or the CI screenshots.

### M7 — JSON results view + value inspector

**Status: done.** What shipped, and where it differs from the plan below:
- **JSON tab:** one compact JSON object per row, in the console's encoding, in a virtualized table. Each row is encoded on demand from the grid's chunk cache, so the result is never materialized as one string. Lines are truncated at 4,096 characters for display.
- **Value inspector:** selecting a JSON row, or double-clicking a grid cell, shows the row in the inspector as a typed tree (name, value, type), expanded to the clicked element path. For example, the second visual line of `items.tags` maps to `items[0].tags[1]`. Values are shown in full up to 64 KiB, and the menu offers Copy Value, Copy Path and Insert Path in Editor.
- **Copy All as JSON:** stops at 16 MiB and points to Export (M9), which streams to files.
- **Not done:**
  - Folding inside the JSON tab; the inspector tree serves that role.
  - Syntax colors in the JSON tab; they arrive with themes (M12).
  - Formatted/raw/JSON modes in the inspector; they're covered by its copy actions.

**JSON view**
- A virtualized `NSTextView`-free custom view. Each logical row is rendered lazily as pretty JSON, and only visible rows plus a margin are materialized.
- Code folding per object or array, and syntax colors from the theme.
- Encoding matches the console's JSON output: INT64 as string, NUMERIC as string, BYTES as base64.

**Memory and size limits**
- Per-value truncation at 64 KiB for display, with "Show full value" in the inspector.
- Per-row cap for very large rows.
- The total "Copy all as JSON" or "Open as JSON" is streamed to a temp file (never to a whole in-memory string) when above 16 MiB, with a size estimate and confirmation.

**Value inspector** (inspector pane): a typed tree of the selected cell, row or struct, with formatted, raw and JSON modes, plus copy path and copy value.

**Exit:** a 1M-row nested result in the JSON view stays at or below the memory budget. Copying a 2 GiB result goes to a file, not RAM.

### M8 — Query plan: execution details + execution graph

**Status: done.** What shipped, and where it differs from the plan below:
- **`internal/plan`** (Linux-tested against a real 13-stage plan recorded from public data, scrubbed):
  - A stage model: timings (avg/max), records, shuffle, spill, slot time, steps and the timeline.
  - Flags: BigQuery's performance insights, compute or read skew (max ≥ 3× avg), spill, and dominant slot share.
  - A layered DAG layout: longest-path layers, then barycenter sweeps that keep the fewest crossings. The real plan has 0 crossings, and 200 synthetic stages lay out in under 50 ms.
- **Execution details tab:** a stage outline with status, duration, wait/read/compute/write avg/max, records in and out, shuffle, slot time and flags, with expandable steps.
- **Execution graph tab:** a Go-drawn `NSView` with nodes heat-colored by slot time, curved edges and flags, zoomable by pinch or ⌘-scroll. Clicking a stage shows its full details in the inspector.
- **Live updates:** both tabs update from each job snapshot while the query runs. The fake backend replays the recorded plan stage by stage.
- **Not done:**
  - Brandes–Köpf coordinate assignment; layers are simply centered.
  - A minimap.
  - Selectable heat metrics; slot time is the only one.
  - Edge weights by shuffle bytes.
  - The timeline chart; its samples are parsed but not drawn yet.
- **Known issue:**
  - AppKit logs "reentrant operation in its NSTableView delegate" a few times per smoke run, after asynchronous loads land through `objc.Main` (explorer children, run completion, history).
  - A Go-side guard (`appkit/reentrancy_darwin.go`) confirms no table is mutated from inside our own delegate callbacks.
  - Finding the native trigger needs `lldb` with a symbolic breakpoint, so it waits for the Command Line Tools in the VM or a real Mac.
  - `make vm-smoke` reports the count instead of failing.

**Plan model (`internal/plan`)**
- Built from `statistics.query.queryPlan[]` (`ExplainQueryStage`: id, name, status, inputStages, steps (kind + substeps), wait/read/compute/write avg/max ratios and ms, records read/written, shuffle output bytes and spilled, slotMs, parallel inputs), `timeline[]`, `performanceInsights`, plus BI Engine and search statistics.
- Live updates: poll `jobs.get` while RUNNING, then diff the stages.

**Execution details view:** the console's per-stage table with stacked wait/read/compute/write bars (avg vs max), expandable steps showing READ/WRITE/COMPUTE/FILTER/SORT/AGGREGATE/JOIN/LIMIT/ANALYTIC_FUNCTION substeps, and a timeline chart (completed/active/pending units over time).

**Execution graph**
- Layered DAG layout in pure Go: Sugiyama (cycle removal is not needed, so layering, then crossing minimization by barycenter, then Brandes–Köpf coordinates). Golden tests on layouts run on Linux.
- Rendered in a custom `NSView` with CoreGraphics: pan, zoom and fit.
- Nodes are colored by slot-ms, duration or bytes (selectable heat metric), and edges are weighted by records or bytes shuffled. A minimap is included.
- Clicking a node shows stage details in the inspector, and slow or skewed stages get flags (max/avg ratio, spill, performanceInsights).

**Exit:** 200-stage plans lay out in under 50 ms and render smoothly. A live view of a running job updates.

### M9 — Export via the Storage Read API

**Status: done.** What shipped, and where it differs from the plan below:
- **`internal/export`:**
  - CSV, flattened into console-style lines by default, or with JSON cells as an option. NULLs and blank continuation cells are empty.
  - NDJSON in the console's JSON encoding.
  - Parquet (zstd, nested types preserved) and Arrow IPC (zstd), written straight from Storage Read API Arrow records.
- **`Runner.Export`:**
  - Tables and query results (the destination table) stream through the Storage Read API; in-memory results (the fake backend, or no destination) export CSV/NDJSON from the grid.
  - It writes to `<file>.partial` and renames on success; failures and cancellations leave nothing behind.
  - It checks free disk space against the scan estimate and shows a Storage Read API cost hint.
  - Progress shows rows, bytes, elapsed time and estimated rows, and Stop cancels.
- **Live test** (`-tags live`): `bigquery-public-data.samples.shakespeare` (164,656 rows) exports to Parquet (1.0 MB), Arrow (1.9 MB) and CSV (4.6 MB).
- **UI:** Export Results submenus in the Query menu and the results context menu, and Export Table in the explorer, each with a save panel.
- **Not done:**
  - XLSX.
  - Resume.
  - A large-export confirmation dialog; the cost appears in the status line instead.
  - EXPORT DATA / extract jobs to GCS.
  - Dock badge progress.
  - The 10 GiB memory benchmark, which needs real hardware.

**Sources:** the full table, a table with selected columns plus a row restriction or snapshot time, or query results (the destination temp table).

**Formats**
- CSV (with RFC 4180 options and a flattening choice: console-style sub-lines or JSON-in-cell, allowed **only in CSV export**)
- NDJSON (BigQuery JSON encoding)
- Parquet (nested types preserved)
- Arrow IPC/Feather
- *(Optional)* XLSX via `excelize` (pure Go) with a row cap

**Engine**
- N parallel streams, each with an ordered or unordered writer, and backpressure.
- Progress (rows, bytes, ETA) in a toolbar popover and Dock tile badge.
- Cancel, plus resume through stream offsets for huge exports.
- Disk space pre-check via `statfs`. Large-export cost hint: Storage Read API pricing per TiB, with a confirm above a threshold.

**Alternative for very large tables:** `EXPORT DATA` or an extract job to GCS, then optionally download.

**Exit:** a 10 GiB export finishes with bounded memory (≤ 512 MiB RSS), on arm64 CI or a rental.

### M10 — Local saved queries + sessions

**Status: done (single tab).** What shipped, and where it differs from the plan below:
- **`store.DB`:**
  - SQLite (modernc, WAL) with versioned migrations.
  - Stores tabs (text, cursor, linked file, session mode and ID, last job), small metadata, and local run history with FTS5 search. User input is always quoted, so it is never parsed as FTS syntax.
- **Saved queries:**
  - `.sql` files in `~/Documents/bqui/queries` with a `/* bqui … */` TOML header (title, project, default dataset, labels, link_id for M11).
  - Atomic writes and a stable header order, so the files diff cleanly in git.
- **`app.Workspace`:**
  - Autosave with a debounce, and no explicit flush, so a killed app restores its text and cursor.
  - Dirty tracking against the linked file. If the file is deleted, the text is kept and the link dropped.
  - Open/Save/Save As/New.
  - BigQuery session mode: the first run creates a session, later runs join it, and End Session calls `BQ.ABORT_SESSION`.
  - Every run is recorded locally.
- **UI:**
  - File ▸ New, Open, Save, Save As, with save-changes prompts.
  - The window subtitle and the edited dot show the file state.
  - A sidebar Explorer | Saved switcher. The Saved list has a filter and actions: Open, Run, Show in Finder, Copy Path.
  - Query ▸ BigQuery Session Mode (a checkmark) and End Session.
  - History ▸ This Mac (local runs, full-text search), plus a search field for job lists.
- **Not done:**
  - Multiple editor tabs and named workspaces. The schema is ready, but the UI has one tab.
  - Scroll position and split sizes; splits use AppKit autosave.
  - Watching the folder with FSEvents; the list rereads on show and save.
  - Query parameters in the header.

**Saved queries:** a user-chosen folder (default `~/Documents/bqui/queries`) of `.sql` files with a TOML front-matter comment block (title, project, default dataset, params, labels, link ID). It is watched with FSEvents (via purego, or polling) and shown in the sidebar as **Local**.

**Sessions:** the workspace state lives in SQLite: open tabs, editor text (including unsaved), cursor, scroll, selected project, result references (job IDs), split sizes. It is restored on launch and supports named sessions ("Workspaces"), with crash-safe autosave every few seconds.

**BigQuery session mode:** a per-tab toggle (`create_session` → `session_id`) so temp tables and variables persist across runs, with a session indicator and an end-session action.

**Query history** gets full-text search (SQLite FTS5 in modernc) over SQL and job IDs.

**Exit:** if you kill the app mid-edit and relaunch, everything is restored. The saved folder is git-friendly, with stable formatting.

### M11 — BigQuery Studio saved queries (Dataform)

**Status: done.** What shipped, and where it differs from the plan below:
- **Spike:** findings are in `internal/dataform/README.md`.
  - Each saved query is a repository labeled `single-file-asset-type=sql`, holding one file, `content.sql`. Each save is a commit, and there are no workspaces.
  - The "US" multi-region lives in `us-central1`.
  - A stale `requiredHeadCommitSha` is rejected with 400 "does not match actual HEAD commit SHA", so it can't overwrite.
- **`internal/dataform`:** list, read (text plus head), save (commit against a base head, giving `ErrConflict`), create (private), rename, delete, and a shared check.
  - Errors carry actionable messages: API disabled, wrong region, missing roles.
  - The commit author is the account email from tokeninfo.
  - Offline tests use a fake modeled on the recorded responses. A live test creates, edits, conflicts, renames and deletes its own temporary query.
- **`app` (Workspace and FakeStudio):**
  - Tabs link to a saved query and store the synced head and text; this needed a schema migration.
  - A save commits against the synced head.
  - A linked `.sql` file (`link_id` in its header) opened without a known head is accepted only if the remote text still equals the file's synced text. Otherwise it is a conflict.
- **UI:**
  - The Saved sidebar has two sections, "On this Mac" (a link icon for linked files) and "BigQuery Studio", for the billing project and Studio region.
  - Studio queries can be opened, renamed and deleted.
  - ⌘S saves to the file and/or Studio.
  - File menu ▸ Save to BigQuery Studio… creates a private query and links the open file. Reload from BigQuery Studio takes BigQuery's version.
  - Conflict alert: Overwrite, Cancel, or Open BigQuery Version.
  - A Studio region setting; "US" maps to us-central1.
- **Not done:**
  - A side-by-side diff on conflict (the alert offers a choice instead).
  - Sharing controls; new queries are private.
  - Team folders.
  - Background polling for remote changes; conflicts surface on save.

1. **Spike first.** Create saved queries in the console, then inspect them through `dataform.googleapis.com/v1` (repositories, labels, workspaces, files, commits) and document the actual representation in `internal/dataform/README.md`.
2. Add a **BigQuery** library per project and region in the sidebar: list, open, save (commit), create, rename and delete.
3. **Link a local query to a remote one:** saving pushes to both. On conflict (the remote changed since the last pull, tracked by commit SHA or etag in SQLite), show a side-by-side diff with a choice.
4. Permissions and scope errors produce actionable messages: enable the Dataform API, or the required roles.

**Exit:** a query edited in the console round-trips with bqui with no data loss.

### M12 — Themes

**Status: done.** What shipped, and where it differs from the plan below:
- **`internal/theme`:**
  - TOML themes with light and dark palettes. A palette covers editor colors, one style per bqls token type (and modifiers such as `property.partitioned`), grid null and number colors, JSON key, string, number and literal colors, and the plan heat color. Font sizes and ligatures are included.
  - Partial themes inherit from Default, and colors are validated.
  - Built-ins: Default, Paper, Midnight (dark only), High Contrast.
- **Monaco compile:** semantic rules, plus Monarch `sql` aliases so colors are correct before semantic tokens arrive, plus workbench colors.
- **VS Code import:**
  - Reads JSONC and maps `colors`, `tokenColors` and `semanticTokenColors`.
  - `tokenColors` match TextMate prefixes, preferring the most specific scope.
  - `*.modifier` applies to every token type that carries the modifier.
  - The imported appearance is filled in; the other appearance keeps Default.
- **UI (View ▸ Theme):**
  - Pick a theme (shown with a check), Import VS Code Theme, Duplicate Current Theme, Reload Themes, Show Themes Folder.
  - Changes apply live to app appearance, the editor (colors and font size), the grid (font size, null and number colors), JSON syntax colors, and the execution graph heat color.
  - Colors re-resolve when macOS switches between light and dark.
  - The chosen theme persists in settings.
- **Not done:**
  - A preferences window with live preview and in-app color editing; you edit the TOML and use Reload Themes instead.
  - Vibrancy and accent options.
  - The UI font size.

**Theme model**
- App appearance: system, light or dark; accent; vibrancy on or off.
- Editor: background, foreground, selection, caret, gutter, and a color for each semantic token type plus modifier. The theme compiles to a Monaco theme (`monaco.editor.defineTheme`, applied through `bqui/setTheme`), so editor colors switch live together with the native UI.
- Grid: header, borders, sub-line dividers, null style, selection, number alignment.
- JSON view colors and plan heat palette.
- Fonts: editor, grid, UI size.
- Every color has light and dark variants.

**Storage:** TOML at `~/Library/Application Support/bqui/themes/*.toml`. A preferences window offers live preview, duplicate, save as, import and export.

**Import** VS Code `.json` themes. Monaco uses the same token-rule model, so `tokenColors` and `semanticTokenColors` map almost directly; `colors` map to the native UI's palette. Ship 3–4 built-ins.

**Exit:** switching themes restyles the editor, grid, JSON and graph live without a restart.

### M13 — Hardening and release

**Status: done for what can be verified without Apple hardware.**
- **Settings window (⌘,):**
  - Default location and default dataset.
  - Maximum bytes billed: BigQuery enforces it, and the smoke test checks that a run over the limit fails.
  - bqls cost warning in USD.
  - Result spill limit.
  - Values are validated with field-specific errors.
  - Changes apply live to runs, to bqls (`didChangeConfiguration` always sends every field bqui owns, because bqls replaces client settings), and to the runner.
- **Logs and crashes:**
  - `~/Library/Logs/bqui/bqui.log`, rotated at 5 MiB and keeping 3 files.
  - Go crash output goes to `crash.log` through `debug.SetCrashOutput`.
  - When launched from Finder, stderr is redirected into the log.
  - The logs close on terminate.
- **Packaging:**
  - `make release-arm64` writes `dist/bqui-<version>-darwin-arm64.zip`. Executable modes are kept, and Go's linker ad-hoc signs the darwin/arm64 binary.
  - `CFBundleVersion` is numeric: a tag, or `0.0.0` for development builds, with the full `git describe` in `BQUIBuild`.
  - CI uploads the zip and now fails when smoke checks fail (`open -W` always exits 0).
- **README:** install (quarantine), file locations, and the build and test loop.
- **Not done, because it needs real Apple silicon or an account:**
  - Performance passes (scroll FPS, launch time, memory) and the 10 GiB export and 50M-row memory benchmarks.
  - VoiceOver and keyboard audit.
  - Notarization.
- **Not done (scope):**
  - Keybinding editor.
  - Multiple editor tabs.
  - Update checks.
  - Charts, lineage and Vim mode.
- **Open known issues:**
  - AppKit table reentrancy warnings (M8).
  - An invisible inspector segment label in the VM (glass rendering).
  - Toolbar overflow at 1280 pt.
  - Binary size (about 190 MB, mostly bqlang's wasm2go parser).

- Performance passes on real M4 hardware (rental): scroll FPS, launch time, memory profiles.
- Accessibility audit: VoiceOver and full keyboard navigation.
- Preferences: projects, default location, maximum bytes billed guard, cache sizes, LSP options (mapped to `bqconfig.LSPOptions`), keybindings.
- Crash and log collection to `~/Library/Logs/bqui`.
- Packaging: ad-hoc signed zip by default. *(Optional)* Developer ID signing and notarization through `rcodesign notary-submit` if you get an Apple Developer account. *(Optional)* update checks against Gitea releases.
- Optional extras, not committed: a charts tab, a lineage view, Vim keybindings (`monaco-vim`), Monaco upgrade cadence (bump the pin in `tools/fetchassets` per release, after a smoke run).

---

## Next milestones (planned 2026-11-27)

### M14 — Go to definition from the editor

- **Trigger:** right-click a table reference in the editor and choose **Go to Definition** (Monaco's action, also F12 / ⌘-click).
  - bqls already resolves `textDocument/definition` for tables. bqui intercepts results that point at BigQuery objects instead of opening a document.
- **Result:**
  1. The inspector shows that object's **Schema** tab.
  2. The explorer reveals it: it expands the project and dataset (loading lazily), selects the node and scrolls to it.
  3. A project that isn't pinned is pinned temporarily, or the user is told how to pin it.
- **Same treatment:** views, routines (Details tab) and models.
- **Tests:**
  - Bridge test: a definition result maps to a `TableRef`.
  - Smoke: go to definition on `bigquery-public-data.samples.shakespeare` selects the node and shows the schema.

### M15 — Workbooks: a Library › Workbook › Query tree (replaces the Saved sidebar)

**Goal:** an IDE-like tree. A **library** holds **workbooks**, which are folders, and each workbook holds **queries**, which are `.sql` files.

**Naming:** the words avoid BigQuery and GCP terms (project, session, workspace, repository, folder, notebook).

- **Model:**
  - **Libraries** are top-level namespaces at `~/Documents/bqui/libraries/<name>/`.
    - `bqui` with no flag uses `default`, and `bqui --library work` selects another.
    - A library holds files only. Settings stay global.
  - **Workbooks** are folders inside the library. Folders and files on disk are the source of truth, so the tree is git-friendly and editable in Finder.
    - On first launch bqui creates `workbook-YYYY-MM-DD`.
    - **New Workbook** (sidebar button, ⇧⌘N) creates another, adding `-2`, `-3`… on collisions.
    - Workbooks can be renamed (this renames the folder) and deleted (to the Trash).
  - **Queries:** **New Query** (⌘N) creates `Untitled-1.sql`, `Untitled-2.sql`… in the selected workbook. Queries can be renamed, dragged between workbooks, duplicated and deleted (to the Trash).
- **Display:**
  - The tree shows names without `.sql`, and lists only `.sql` files.
  - Renaming edits the bare name; bqui adds the extension and rejects `/`.
- **Editor binding:**
  - The file selected in the tree is the file shown in the editor. Selecting another file autosaves the current one first, replacing the M10 save prompts.
  - The window subtitle shows `workbook / query`.
  - Restore on launch: the last selected file, cursor and BigQuery session mode, reusing the M10 workspace tables, keyed by file path.
- **BigQuery session mode:** stays a per-query toggle (decided 2026-11-27).
- **Migration:**
  - The existing `~/Documents/bqui/queries/*.sql` move into the workbook `default/imported`.
  - BigQuery Studio saved queries (M11) become a read-only-ish top-level node, "BigQuery Studio", opened and saved as today.
  - The local `link_id` header keeps working.
- **Replaces:** the Saved tab. The sidebar switcher becomes Explorer | Workbooks.
- **Tests:** workbook and query naming and collisions, rename and move on disk, migration from `queries/`, `--library`, and restoring the selection (Linux). Smoke: create a workbook, create a query, rename both, switch queries with autosave.
- **Runs per file:** every run from a file is tracked, so a file's job history is one click away.
  - **Storage:**
    - The local `runs` table gains a `file_id` column. Each file gets a stable ID in its header comment (`id = "…"`), so the link survives renames and moves, including ones made in Finder.
    - Each recorded run keeps the job ID, location, SQL as run (the selection or statement, which may differ from the whole file), state, duration, bytes and row count.
  - **Panel:**
    - A **Runs** tab in the bottom pane lists the current file's runs, newest first. It switches with the selected file, and its count shows in the tab title, for example "Runs (12)".
    - Each row shows state, started, duration, bytes and the first line of the SQL that ran.
    - **Double-click** reopens that job's results and plan (`Runner.OpenJob`). The context menu offers Copy Job ID, Open in Console, Restore this SQL into the editor, and Compare with current text.
  - **Tree hint:** the query's tooltip in the Workbooks tree shows its last run (state and time).
  - **Tests:** a file ID survives rename and move, runs are attributed to the right file, and rows come back newest first (Linux). Smoke: run twice from one file and once from another; the Runs tab shows 2, then 1 after switching files.

### M16 — Job browser

- **Where:** a special top-level node, **Job History**, in the Workbooks tree, listing the user's own BigQuery jobs in the current billing project (decided 2026-11-27) (`jobs.list`, `allUsers=false`, newest first).
- **Rows:** one per job, named by **job ID**, with a state icon and the creation time as secondary text.
- **Lazy listing:**
  - It shows a recent window (for example the last 50, or the last 7 days) and loads the next page when scrolled to the end.
  - It is never listed in full.
  - A filter field searches the loaded jobs. The local run history (FTS) can back the search.
- **Hover:** a tooltip with the first ~10 lines of the SQL, state, duration, bytes processed, statement type and project/location.
- **Double-click:** opens the job's SQL in the editor as a **read-only job document**, a virtual file `Job History/<job id>`. Its job details and results are reachable from the bottom tabs, through the existing Job information and results reopening.
  - Editing turns it into a normal file: **Save to Workbook…** copies the SQL into `Untitled-N.sql` in the chosen workbook.
- **Overlap:** the History bottom tab (M6) stays for project-wide and filtered browsing. This folder is the quick, personal path.
- **Tests:** tooltip text formatting and pagination (Linux, fake jobs). Smoke: hover text, then double-click opens the read-only job SQL.

## Follow-ups (open after M0–M13)

These are pending items, collected from the milestone notes above.

### Verification still owed
- **M11 console round trip (exit criterion):** open `select-replace` in bqui, edit it and save. Edit it again in the BigQuery Studio console, then save once more in bqui. Expect the conflict prompt, not a silent overwrite. Also confirm a bqui save shows up in the console's history.
- **M11 shared queries:** share a saved query in the console and check what its IAM policy looks like. All current samples are private, so `Shared` is unverified for shared ones.
- **Real Apple silicon** (CI runner or a rental):
  - Scroll FPS, launch time and memory profiles.
  - The M5 50M-row memory test and the M9 10 GiB export (≤ 512 MiB RSS).
  - The M7 1M-row JSON view.
  - VoiceOver and keyboard audit.
- **First CI run** with the new `jq` smoke gate and release-zip upload.

### Known issues
- AppKit "reentrant operation in its NSTableView delegate" warnings (6 per smoke run). These need `lldb` with a symbolic breakpoint (M8).
- In the macOS 26 inspector pane, bezeled controls (segmented controls, push buttons) draw only their selected or accent parts, at least in the VM. The inspector now uses `appkit.TabBar` (borderless buttons with colored titles). Check on real hardware whether other bezeled controls there need the same treatment.
- The toolbar overflows at 1280 pt width.
- The binary is about 190 MB, mostly bqlang's wasm2go parser.

### Deferred features
- **Editor:**
  - Multiple editor tabs and named workspaces. The schema is ready, but the UI has one tab.
  - A keybinding editor and Vim mode.
- **BigQuery Studio:**
  - A side-by-side diff on conflict.
  - Sharing controls.
  - Team folders.
  - Background polling for remote changes.
- **Export:**
  - XLSX.
  - Resume.
  - A large-export confirmation.
  - EXPORT DATA / extract jobs to GCS.
  - Dock badge progress.
- **Plan:**
  - Selectable heat metrics.
  - Edge weights by shuffle bytes.
  - The timeline chart.
- **Themes:**
  - A preferences pane with live preview and in-app color editing.
  - Vibrancy and accent options.
  - The UI font size.
- **Saved queries:**
  - Watching the folder with FSEvents.
  - Query parameters in the header.
  - Scroll position in session restore.
- **Release:**
  - Notarization (needs an Apple Developer account).
  - Update checks against Gitea releases.
- **Extras:** a charts tab and a lineage view.

## Critical files

- **bqlang (M0):**
  - `internal/lsp/server.go` (`NewServer`, `Serve`, `ServerOptions`) moves to `lsp/`
  - `internal/catalog/{bqrest.go,store.go,model.go,discovery.go}` moves to `catalog/`
  - `internal/bqconfig/config.go` moves to `bqconfig/`
  - `cmd/bqls/main.go` and the tests get updated imports
- **bqui (new):** `internal/objc/*`, `internal/results/{flatten,layout,format}.go`, `internal/ui/grid/*`, `internal/plan/layout.go`, `internal/webkit/*`, `internal/editor/{bridge.go,web/bqui-editor.js}`, `internal/bqstorage/*`, `tools/bundle/main.go`, `tools/fetchassets/main.go`, `.github/workflows/macos.yml`, `Makefile`

## Code from bqlang to reuse

- `catalog.RestClient` + `defaultTokenCache` (ADC → gcloud) for auth and metadata
- `catalog.Store` (SQLite WAL cache, background fetch, negative backoff), shared with the LSP
- `catalog.TableMeta` helpers: `IsPartitionColumn`, `ClusterRank`, `FindField`, `ColumnMeta.SQLType`
- `formatter.Format` for view definitions and job SQL display
- `lsp.Server` for every editor intelligence feature, including dry-run cost (`bqls/queryCost`)
- The `e2e/harness_test.go` pattern for LSP round-trip tests

## Verification

- **Linux (every change):** `CGO_ENABLED=0 go test ./...` across `app`, `bq`, `bqstorage`, `results`, `plan`, `export`, `store`, `editor` (bridge + LSP relay), `theme`, plus `golangci-lint run`, and `GOOS=darwin GOARCH=arm64` and `amd64` builds to catch build-tag breakage.
- **bquictl against a sandbox GCP project:** `bquictl query`, `export`, `plan --layout-json`, `layout --bench`.
- **QEMU Tahoe VM:** `make vm-deploy`, then hands-on UI checks per milestone. It also runs `bqui --smoke-test` and `leaks`.
- **GitHub Actions arm64 (macos-26):** objc ABI tests, the smoke test producing PNG and AX artifacts, and memory benchmarks.
- **Milestone exit criteria** above are the acceptance gates. The MVP (M6) also needs a manual checklist run on real Apple silicon (CI screenshots plus one rental day).
