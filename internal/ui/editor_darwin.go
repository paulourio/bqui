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

	"github.com/paulourio/bqui/internal/editor"
	"github.com/paulourio/bqui/internal/objc"
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
	Logger      *slog.Logger
}

// EditorPane is the Monaco editor hosted in a WKWebView.
type EditorPane struct {
	Web    *webkit.WebView
	Bridge *editor.Bridge

	opts      EditorOptions
	ready     chan struct{}
	readyOnce sync.Once
	selfTest  chan json.RawMessage

	mu            sync.Mutex
	uri           string
	consoleErrors []string
	bootError     string
}

// NewEditorPane creates the WebView and starts the in-process bqls bridge.
func NewEditorPane(opts EditorOptions) (*EditorPane, error) {
	p := &EditorPane{opts: opts, ready: make(chan struct{}), selfTest: make(chan json.RawMessage, 1)}

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

// InsertText inserts text at the cursor (replacing the selection).
func (p *EditorPane) InsertText(text string) {
	_ = p.Bridge.SendApp("insertText", map[string]string{"text": text})
}

// AppendText appends a statement at the end of the buffer and selects it.
func (p *EditorPane) AppendText(text string) {
	_ = p.Bridge.SendApp("appendText", map[string]string{"text": text})
}

// SetDefaultProject points bqls (completion, dry runs) at a project.
func (p *EditorPane) SetDefaultProject(project string, apiEnabled bool) {
	_ = p.Bridge.Notify("workspace/didChangeConfiguration", map[string]any{
		"settings": map[string]any{"bqls": map[string]any{
			"default_project": project, "bigquery_api_enabled": apiEnabled,
		}},
	})
}

// RequestRun asks the page for the current text/selection and triggers OnRun.
func (p *EditorPane) RequestRun(atCursor bool) {
	p.Web.Eval(fmt.Sprintf("window.bqui&&window.bqui.requestRun(%t)", atCursor))
}

// Action runs a Monaco editor action by ID (e.g. "actions.find").
func (p *EditorPane) Action(id string) {
	p.Focus()
	p.Web.Eval("window.bqui&&window.bqui.action(" + mustJSON(id) + ")")
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
