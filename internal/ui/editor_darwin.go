//go:build darwin

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/paulourio/bqlang/bqconfig"
	"github.com/paulourio/bqlang/lsp"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/editor"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/theme"
	"github.com/paulourio/bqui/internal/webkit"
)

const (
	editorScheme     = "bqui"
	editorURL        = "bqui://editor/index.html"
	statementTimeout = 5 * time.Second
	maxConsoleLines  = 200
)

// RunRequest is what the editor asks to run.
type RunRequest struct {
	URI       string `json:"uri"`
	Version   int    `json:"version"`
	Text      string `json:"text"`
	Selection string `json:"selection"`
	Cursor    struct {
		Line      int `json:"line"`
		Character int `json:"character"`
	} `json:"cursor"`
	AtCursor bool `json:"atCursor"`

	// SQL is the text to execute: the selection, the statement at the cursor,
	// or the whole script.
	SQL string `json:"-"`
	// Kind is the bqls statement kind when a single statement was picked.
	Kind string `json:"-"`
}

// EditorOptions configures the editor pane.
type EditorOptions struct {
	LSP         *bqconfig.LSPOptions
	Inspectable bool
	OnRun       func(RunRequest)            // main thread
	OnCost      func(summary string)        // main thread
	OnConsole   func(level, message string) // main thread
	// OnChange receives the text and cursor after edits (debounced; main thread).
	OnChange func(text string, line, character int)
	// ObjectAt resolves a table/view/routine/model reference at a position
	// (Go to Definition); OnOpenObject reveals "project.dataset.name".
	ObjectAt     func(text string, line, character int) (app.ObjectAt, bool)
	OnOpenObject func(ref string)
	Logger       *slog.Logger
}

// EditorPane is the Monaco editor hosted in a WKWebView.
type EditorPane struct {
	Web    *webkit.WebView
	Bridge *editor.Bridge

	opts      EditorOptions
	ready     chan struct{}
	readyOnce sync.Once
	selfTest  chan json.RawMessage
	menuProbe chan json.RawMessage

	mu            sync.Mutex
	uri           string
	changes       int
	objectLookups int
	objectOpens   int
	appliedTheme  string
	consoleErrors []string
	bootError     string
}

// NewEditorPane creates the WebView and starts the in-process bqls bridge.
func NewEditorPane(opts EditorOptions) (*EditorPane, error) {
	p := &EditorPane{opts: opts, ready: make(chan struct{}), selfTest: make(chan json.RawMessage, 1), menuProbe: make(chan json.RawMessage, 1)}

	web, err := webkit.New(webkit.Options{
		Scheme:      editorScheme,
		Serve:       serveEditorAsset,
		MessageName: "bqui",
		OnMessage:   func(body string) { p.Bridge.Receive(body) },
		Inspectable: opts.Inspectable,
	})
	if err != nil {
		return nil, err
	}

	p.Web = web

	lspOpts := opts.LSP
	if lspOpts == nil {
		lspOpts = bqconfig.DefaultLSPOptions()
	}

	bridge, err := editor.New(editor.Options{
		LSP:      lsp.ServerOptions{DefaultLSPOptions: lspOpts, Logger: opts.Logger},
		Eval:     web.Eval,
		Schedule: objc.Main,
		OnApp:    p.onApp,
		OnCost: func(params json.RawMessage) {
			var c struct {
				Summary string `json:"summary"`
				Status  string `json:"status"`
			}

			if json.Unmarshal(params, &c) == nil && opts.OnCost != nil {
				objc.Main(func() { opts.OnCost(c.Summary) })
			}
		},
		Logger: opts.Logger,
	})
	if err != nil {
		return nil, err
	}

	p.Bridge = bridge
	web.Load(editorURL)

	return p, nil
}

func serveEditorAsset(_, path string) webkit.Response {
	a, err := editor.Open(path)
	if err != nil {
		return webkit.Response{Status: http.StatusNotFound, ContentType: "text/plain", Body: []byte(err.Error())}
	}

	return webkit.Response{Status: http.StatusOK, ContentType: a.ContentType, Body: a.Body}
}

// SetText replaces the editor contents (queued until the page is ready).
func (p *EditorPane) SetText(text string) {
	_ = p.Bridge.SendApp("setText", map[string]string{"text": text})
}

// SetTheme compiles a theme for Monaco and applies it (and its fonts).
func (p *EditorPane) SetTheme(t *theme.Theme) {
	lig := t.Fonts.Ligatures == nil || *t.Fonts.Ligatures

	_ = p.Bridge.SendApp("setTheme", map[string]any{
		"light": theme.Monaco(t.Light, false), "dark": theme.Monaco(t.Dark, true),
		"mode": string(t.Appearance), "fontSize": t.Fonts.EditorSize, "ligatures": lig,
	})
}

// AppliedTheme is the Monaco theme the page last applied (smoke checks).
func (p *EditorPane) AppliedTheme() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.appliedTheme
}

// SetTextAt replaces the contents and places the cursor (0-based), without
// reporting a change.
func (p *EditorPane) SetTextAt(text string, line, character int) {
	_ = p.Bridge.SendApp("setText", map[string]any{"text": text, "line": line, "character": character})
}

// Changes counts change reports from the page (smoke checks).
func (p *EditorPane) Changes() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.changes
}

// InsertText inserts text at the cursor (replacing the selection).
func (p *EditorPane) InsertText(text string) {
	_ = p.Bridge.SendApp("insertText", map[string]string{"text": text})
}

// AppendText appends a statement at the end of the buffer and selects it.
func (p *EditorPane) AppendText(text string) {
	_ = p.Bridge.SendApp("appendText", map[string]string{"text": text})
}

// ConfigureLSP sends bqls the options bqui controls. bqls replaces its
// client settings on every notification, so all of them are sent each time.
func (p *EditorPane) ConfigureLSP(o *bqconfig.LSPOptions) {
	settings := map[string]any{
		"default_project": o.DefaultProject, "bigquery_api_enabled": o.BigQueryAPIEnabled,
		"default_dataset": o.DefaultDataset,
	}

	if o.CostWarningThresholdUSD > 0 {
		settings["cost_warning_threshold_usd"] = o.CostWarningThresholdUSD
	}

	_ = p.Bridge.Notify("workspace/didChangeConfiguration", map[string]any{"settings": map[string]any{"bqls": settings}})
}

// RequestRun asks the page for the current text/selection and triggers OnRun.
func (p *EditorPane) RequestRun(atCursor bool) {
	p.Web.Eval(fmt.Sprintf("window.bqui&&window.bqui.requestRun(%t)", atCursor))
}

// Action runs a Monaco editor action by ID (e.g. "actions.find").
func (p *EditorPane) Action(id string) {
	p.Focus()
	// Through the app queue, so the action runs after earlier setText /
	// insertText messages (Eval would overtake them).
	_ = p.Bridge.SendApp("runAction", map[string]string{"id": id})
}

// Format formats the document through bqls (bqfmt).
func (p *EditorPane) Format() {
	p.Web.Eval("window.bqui&&window.bqui.format()")
}

// Focus moves keyboard focus into Monaco.
func (p *EditorPane) Focus() {
	objc.Msg(objc.Msg(p.Web.View, "window"), "makeFirstResponder:", p.Web.View)
	_ = p.Bridge.SendApp("focus", nil)
}

// WaitReady blocks until Monaco is loaded or timeout elapses.
func (p *EditorPane) WaitReady(timeout time.Duration) bool {
	select {
	case <-p.ready:
		return true
	case <-time.After(timeout):
		return false
	}
}

// RunSelfTest makes the page exercise completion, markers and semantic
// tokens, and waits for its report (smoke tests). Call from a worker goroutine.
func (p *EditorPane) RunSelfTest(sql string, line, column int, timeout time.Duration) (json.RawMessage, error) {
	js := fmt.Sprintf("window.bqui.selfTest(%s,%d,%d)", mustJSON(sql), line, column)
	objc.Main(func() { p.Web.Eval(js) })

	select {
	case r := <-p.selfTest:
		return r, nil
	case <-time.After(timeout):
		return nil, errors.New("editor self-test timed out")
	}
}

// ProbeContextMenu opens the editor's context menu and returns how it is
// styled (smoke checks). The menu stays open until CloseContextMenu.
func (p *EditorPane) ProbeContextMenu(timeout time.Duration) (json.RawMessage, error) {
	objc.Main(func() { p.Web.Eval("window.bqui.menuProbe()") })

	select {
	case r := <-p.menuProbe:
		return r, nil
	case <-time.After(timeout):
		return nil, errors.New("editor context menu probe timed out")
	}
}

// CloseContextMenu dismisses the editor's context menu.
func (p *EditorPane) CloseContextMenu() { p.Web.Eval("window.bqui.closeMenu()") }

// Diagnostics returns console errors and the boot error seen so far.
func (p *EditorPane) Diagnostics() (consoleErrors []string, bootError string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.consoleErrors...), p.bootError
}

// Close stops bqls.
func (p *EditorPane) Close() error { return p.Bridge.Close() }

func (p *EditorPane) onApp(method string, params json.RawMessage) {
	switch method {
	case "ready":
		var r struct {
			URI string `json:"uri"`
		}

		_ = json.Unmarshal(params, &r)

		p.mu.Lock()
		p.uri = r.URI
		p.mu.Unlock()
		p.readyOnce.Do(func() { close(p.ready) })
	case "run":
		var req RunRequest
		if err := json.Unmarshal(params, &req); err == nil {
			p.resolveRun(req)
		}
	case "changed":
		var c struct {
			Text      string `json:"text"`
			Line      int    `json:"line"`
			Character int    `json:"character"`
		}

		if json.Unmarshal(params, &c) == nil {
			p.mu.Lock()
			p.changes++
			p.mu.Unlock()

			if p.opts.OnChange != nil {
				p.opts.OnChange(c.Text, c.Line, c.Character)
			}
		}
	case "objectAt":
		p.mu.Lock()
		p.objectLookups++
		p.mu.Unlock()
		p.resolveObject(params)
	case "openObject":
		var o struct {
			Ref string `json:"ref"`
		}

		p.mu.Lock()
		p.objectOpens++
		p.mu.Unlock()

		if json.Unmarshal(params, &o) == nil && p.opts.OnOpenObject != nil {
			p.opts.OnOpenObject(o.Ref)
		}
	case "themeApplied":
		var t struct {
			Theme string `json:"theme"`
		}

		if json.Unmarshal(params, &t) == nil {
			p.mu.Lock()
			p.appliedTheme = t.Theme
			p.mu.Unlock()
		}
	case "readClipboard":
		// The page asks for the pasteboard instead of reading it itself:
		// WebKit guards navigator.clipboard reads with its own Paste button.
		var r struct {
			ID int `json:"id"`
		}

		if json.Unmarshal(params, &r) == nil {
			_ = p.Bridge.SendApp("clipboardText", map[string]any{"id": r.ID, "text": appkit.PasteboardString()})
		}
	case "menuProbe":
		select {
		case p.menuProbe <- params:
		default:
		}
	case "selftest":
		select {
		case p.selfTest <- params:
		default:
		}
	case "console":
		p.console(params)
	case "bootError":
		var e struct {
			Message string `json:"message"`
		}

		_ = json.Unmarshal(params, &e)

		p.mu.Lock()
		p.bootError = e.Message
		p.mu.Unlock()
		fmt.Fprintln(os.Stderr, "bqui editor: boot error:", e.Message)
	}
}

func (p *EditorPane) console(params json.RawMessage) {
	var c struct {
		Level string `json:"level"`
		Msg   string `json:"msg"`
	}

	if json.Unmarshal(params, &c) != nil {
		return
	}

	if c.Level == "error" {
		p.mu.Lock()
		if len(p.consoleErrors) < maxConsoleLines {
			p.consoleErrors = append(p.consoleErrors, c.Msg)
		}
		p.mu.Unlock()
	}

	if p.opts.OnConsole != nil {
		p.opts.OnConsole(c.Level, c.Msg)
	}
}

// resolveRun decides what to execute. Statement-at-cursor needs a bqls round
// trip, so it runs on a worker goroutine and hops back to the main thread.
func (p *EditorPane) resolveRun(req RunRequest) {
	switch {
	case req.Selection != "":
		req.SQL = req.Selection
	case !req.AtCursor:
		req.SQL = req.Text
	default:
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), statementTimeout)
			defer cancel()

			sts, err := p.Bridge.Statements(ctx, req.URI)
			if st, ok := editor.StatementAt(sts, req.Cursor.Line, req.Cursor.Character); err == nil && ok {
				req.SQL, req.Kind = st.Text, string(st.Kind)
			} else {
				req.SQL = req.Text
			}

			objc.Main(func() { p.deliverRun(req) })
		}()

		return
	}

	p.deliverRun(req)
}

func (p *EditorPane) deliverRun(req RunRequest) {
	if p.opts.OnRun != nil {
		p.opts.OnRun(req)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)

	return string(b)
}

// resolveObject answers the page's definition lookup.
func (p *EditorPane) resolveObject(params json.RawMessage) {
	var q struct {
		ID        int    `json:"id"`
		Text      string `json:"text"`
		Line      int    `json:"line"`
		Character int    `json:"character"`
	}

	if json.Unmarshal(params, &q) != nil {
		return
	}

	reply := map[string]any{"id": q.ID}

	if p.opts.ObjectAt != nil {
		if o, ok := p.opts.ObjectAt(q.Text, q.Line, q.Character); ok {
			reply["ref"] = o.Ref.Project + "." + o.Ref.Dataset + "." + o.Ref.Table
			reply["line"], reply["start"], reply["end"] = o.Line, o.Start, o.End
		}
	}

	_ = p.Bridge.SendApp("objectAt", reply)
}

// GoToDefinition runs Monaco's Go to Definition at the cursor.
func (p *EditorPane) GoToDefinition() { p.Action("editor.action.revealDefinition") }

// ObjectRequests counts definition lookups and opens from the page (smoke).
func (p *EditorPane) ObjectRequests() (lookups, opens int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.objectLookups, p.objectOpens
}

// SetReadOnly makes the editor read-only (job documents) or editable.
func (p *EditorPane) SetReadOnly(on bool) {
	opts := map[string]any{"readOnly": on}
	if on {
		opts["readOnlyMessage"] = map[string]string{"value": "This is a job's SQL (read-only). Use File ▸ Save Job SQL to Workbook to edit a copy."}
	}

	_ = p.Bridge.SendApp("setOptions", opts)
}
