# What bqui needs from bqlang

**Audience:** an agent working in `~/dev/bqlang` (module `github.com/paulourio/bqlang`).
**Requester:** `bqui` (`~/dev/bqui`, module `github.com/paulourio/bqui`). This is a native macOS BigQuery client in pure Go (`CGO_ENABLED=0`, AppKit through purego) that embeds bqls **in-process** for its SQL editor and shares bqlang's metadata catalog with its dataset browser. See `~/dev/bqui/docs/ROADMAP.md`, milestone M0.

bqui will import bqlang through a `go.work` (`use ../bqlang`) during development, and later through a tagged release.

---

## 0. Before anything else: protect the uncommitted work

As of 2026-11-26, `~/dev/bqlang` has a lot of uncommitted work:

- **Untracked:** `internal/`, `cmd/bqls/`, `e2e/`. This is the entire LSP, catalog and analysis implementation, with no git history.
- **Modified:** `README.md`, `go.mod`, `go.sum`, `cmd/bqfmt/*`, `formatter/*`, `formatter/testdata/property_graph.toml`

Rules:
1. **Do not** run `git checkout`, `git stash`, `git clean`, `git reset`, or any move or rename until that work is committed. **Ask the owner** how to commit it (one commit, or several by area). Do not commit it on your own initiative.
2. After it's committed, create branch `feat/public-api` from that commit. Do all of the following work there.
3. Use `git mv` for the package moves so the history follows the files.

---

## 1. Promote three packages from `internal/` to public

| From | To | Why bqui needs it |
|---|---|---|
| `internal/lsp` | `lsp` | Run the server in-process over `io.Pipe` |
| `internal/catalog` | `catalog` | Share the metadata store and REST client with the dataset browser, and reuse the types |
| `internal/bqconfig` | `bqconfig` | `catalog.NewStore` and `lsp.ServerOptions` take `*bqconfig.LSPOptions` |

- `analysis`, `syntax`, `funcdocs`, `editors` **stay internal**. bqui only reaches them through LSP. (Public packages in the same module can still import `internal/...`.)
- Update every importer: `cmd/bqls`, `cmd/bqfmt`, `internal/analysis`, `e2e`, and the tests.
- Review exported identifiers in the three promoted packages. Anything that exists only for internal plumbing should be unexported or documented. Every exported symbol needs a doc comment, because they are now public API.
- The README gets a short "Library usage: embedding bqls" section (see §2 for the example).

**Acceptance:** `go build ./...`, `go test ./...` and `golangci-lint run` pass. `GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build ./...` and the same command with `GOARCH=amd64` succeed. `cmd/bqls` behaves exactly as before.

---

## 2. `lsp`: embeddable server

Current: `NewServer(ServerOptions)` and `(*Server).Serve(ctx, r, w)` (`internal/lsp/server.go:44,201,291`), with options `ConfigPath`, `Logger`, `Version` and `StoreOptions`.

### 2.1 New `ServerOptions` fields

```go
type ServerOptions struct {
    ConfigPath   string
    Logger       *slog.Logger
    Version      string
    StoreOptions []catalog.StoreOption

    // Store, when non-nil, is used as the default catalog store instead of
    // creating one. The server must NOT Close it. Documents whose directory
    // resolves to a different .bqfmt.toml may still get their own stores, as today.
    Store *catalog.Store

    // TokenProvider, when non-nil, replaces the default ADC→gcloud chain for
    // every RestClient the server creates (metadata fetches and dry runs).
    TokenProvider catalog.TokenProvider

    // DefaultLSPOptions, when non-nil, replaces bqconfig.DefaultLSPOptions()
    // as the base configuration when no .bqfmt.toml is found. bqui uses it to
    // pass default_project / default_dataset / bigquery_api_enabled from its
    // own preferences without writing a file.
    DefaultLSPOptions *bqconfig.LSPOptions
}
```

### 2.2 Requirements for embedding

- `Serve` **returns** on the LSP `exit` notification, on context cancellation or on reader EOF. It never calls `os.Exit` and never writes to `os.Stdout` or `os.Stderr`; all logging goes through `Logger`.
- `Close()` is idempotent and safe to call after `Serve` returns. It leaves an injected `Store` open.
- There is no package-level mutable state, so two servers in one process (in tests) don't interfere.
- `initializationOptions` and `workspace/didChangeConfiguration` accept the same keys as the `[lsp]` TOML table (this already exists in `client_config.go`). Document the accepted JSON shape in the package doc.

Intended use from bqui:

```go
cr, sw := io.Pipe() // client reads what the server writes
sr, cw := io.Pipe() // server reads what the client writes
srv, _ := lsp.NewServer(lsp.ServerOptions{Store: shared, TokenProvider: tp, Logger: log})
go srv.Serve(ctx, sr, sw)
// bqui's JSON-RPC client uses (cr, cw)
```

### 2.3 New request `bqls/statements`

bqui needs statement boundaries for "Run statement at cursor" (Cmd-Shift-Enter) and to split a script into per-statement runs.

- **Request:** `{ "textDocument": { "uri": string } }`
- **Response:** `{ "statements": [ { "range": Range, "kind": string, "text": string } ] }`
  - `range` uses LSP UTF-16 positions and covers the full statement, excluding the trailing `;` and surrounding whitespace or comments.
  - `kind` is a coarse class from the existing statement classification: `QUERY`, `DML`, `DDL`, `SCRIPT` (DECLARE/SET/IF/LOOP/BEGIN…), `CALL`, `EXPORT`, `LOAD`, `OTHER`.
  - `text` is the exact source slice. Jinja templates stay as written.
- It works on documents that fail to parse: the result is best-effort, falling back to splitting on top-level `;` using the existing scanner, so it never errors.
- Announce it under `capabilities.experimental.bqlsStatements = true` in `initialize`.

### 2.4 Document the existing custom protocol

In the package doc or `lsp/PROTOCOL.md`, document the shape of `bqls/queryCost` (notification) and `bqls/refreshCost` (request), plus the semantic token types and modifiers legend (`partitioned`, `clustered`, `pseudo`, …). bqui maps these to theme colors.

**Acceptance:** a new e2e test runs `NewServer` + `Serve` over `io.Pipe` in-process and covers:
- `initialize` → `didOpen` → `semanticTokens/full` → `bqls/statements` → `shutdown` → `exit`
- `Serve` returns nil
- an injected `Store` is still usable after `Close`

---

## 3. `catalog`: a store shared by the LSP and the UI

### 3.1 Multiple update subscribers

`WithOnUpdate` / `Store.OnUpdate` currently hold **one** callback, and the LSP server sets it, so a shared store would lose bqui's callback (or the LSP's). Add:

```go
// Subscribe registers fn to run after each async table fetch completes
// (success or failure). It returns a function that removes the subscription.
// fn runs on a background goroutine with the store mutex released; it must
// not call Close synchronously.
func (s *Store) Subscribe(fn func(TableRef)) (unsubscribe func())
```

Keep `WithOnUpdate` and `OnUpdate` working, implemented on top of `Subscribe`, and switch the LSP server to `Subscribe`.

### 3.2 Default auth exposed with selectable scopes

bqui needs the same ADC → `gcloud` fallback chain (`defaultTokenCache` in `internal/catalog/bqrest.go`), but for the scopes it chooses. Its jobs, Storage Read API and Dataform calls need `https://www.googleapis.com/auth/bigquery` and `https://www.googleapis.com/auth/cloud-platform`.

```go
// NewDefaultTokenProvider returns a cached ADC→gcloud token provider for the
// given OAuth scopes (default: bigquery.readonly).
func NewDefaultTokenProvider(scopes ...string) TokenProvider

// DescribeCredentials reports, best-effort, which credential the default chain
// resolves to, for UI display, e.g. "authorized_user (ADC)", "service_account
// sa@proj.iam (ADC)" or "gcloud". It returns "" if unknown.
func DescribeCredentials(ctx context.Context) (string, error)
```

Shape it however fits the code; the requirement is that bqui can get a `TokenProvider` for arbitrary scopes, using the same caching and fallback behavior as today. Do not change the current defaults that `bqls` uses.

### 3.3 Public API bqui will use (keep these exported and stable)

- **Types:** `TableRef` (`FullName`, `IsComplete`, `WithDefaults`), `DatasetRef`, `TableSummary`, `TableType`, `TableMeta` (+ `Clone`, `FindColumn`, `FindField`, `IsPartitionColumn`, `ClusterRank`, `EffectivePartitioning`, `EffectiveRequirePartitionFilter`), `ColumnMeta` (`SQLType`, `EffectiveMode`), `FlexInt64`
- **Store:** `NewStore`, `Close`, `GetTable`, `WaitForTable`, `IsPending`, `PutTable`, `ListProjects`, `DiscoverDatasets`, `DiscoverTables`, `DefaultProject`, `DefaultDataset`, `Subscribe`
- **REST:** `NewRestClient`, `WithBaseURL`, `WithHTTPClient`, `WithTimeout`, `WithTokenProvider`, `FetchTable`, `ListDatasets`, `ListTables`
- **Errors:** `ErrTableNotFound`, `ErrUnauthorized`, `ErrInvalidTableRef`, `ErrAPIError`, `ErrDryRunRejected`
- **Helper:** `DecodeTableJSON`

### 3.4 Concurrency guarantee

Document, and cover with a `-race` test, that one `*Store` can be used at the same time by an LSP `Server` and by unrelated goroutines (bqui's browser) calling `DiscoverTables`, `GetTable` and `WaitForTable`.

**Acceptance:**
- `go test -race ./catalog/...` covers `Subscribe` and unsubscribe.
- A shared-store test runs an LSP server plus a concurrent browser goroutine.
- A token-provider scope test uses a fake ADC or gcloud.

---

## 4. `bqconfig`

- Keep exported: `LSPOptions`, `DefaultLSPOptions`, `LSPDefinedFields`, `Loader`, `NewLoader`, `LoadedConfig`.
- Add `func (o *LSPOptions) Validate() error` if one doesn't already exist, so bqui can validate preferences before passing them in.

---

## 5. Out of scope (don't do these)

- No new BigQuery features (query execution, jobs, Storage Read API). bqui implements those itself.
- No change to `bqfmt` and `bqls` CLI behavior, flags or output.
- No cgo, and no new non-pure-Go dependencies.
- Don't make `analysis`, `syntax` or `funcdocs` public.

## 6. Finishing

- Run `go test -race ./...`, `golangci-lint run`, and the darwin arm64/amd64 `CGO_ENABLED=0` builds.
- Add a CHANGELOG entry under a new minor version (public packages `lsp`, `catalog`, `bqconfig`; `bqls/statements`; `Store.Subscribe`; `NewDefaultTokenProvider`).
- Leave the branch `feat/public-api` ready for review. **Do not tag, push or merge** without the owner's approval.
- Report back: the final exported API of `lsp` (the `ServerOptions` fields), the `bqls/statements` JSON example, and the auth constructor's signature. bqui will code against those.
