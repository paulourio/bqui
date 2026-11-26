package editor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paulourio/bqlang/bqconfig"
	"github.com/paulourio/bqlang/lsp"
)

const (
	testURI     = "file:///bqui/tab-1.sql"
	waitTimeout = 10 * time.Second
)

// fakePage plays the role of the WKWebView page.
type fakePage struct {
	mu     sync.Mutex
	evals  int
	recv   []Envelope
	notify chan struct{}
}

func newFakePage() *fakePage { return &fakePage{notify: make(chan struct{}, 1024)} }

func (p *fakePage) eval(js string) {
	const prefix, suffix = "window.bqui&&window.bqui.recv(", ");"

	if !strings.HasPrefix(js, prefix) || !strings.HasSuffix(js, suffix) {
		panic("unexpected JS: " + js)
	}

	var batch []Envelope
	if err := json.Unmarshal([]byte(js[len(prefix):len(js)-len(suffix)]), &batch); err != nil {
		panic(err)
	}

	p.mu.Lock()
	p.evals++
	p.recv = append(p.recv, batch...)
	p.mu.Unlock()

	p.notify <- struct{}{}
}

// waitLSP waits for a JSON-RPC message from bqls matching pred.
func (p *fakePage) waitLSP(t *testing.T, pred func(rpcMessage) bool) rpcMessage {
	t.Helper()

	deadline := time.After(waitTimeout)

	for {
		p.mu.Lock()
		for _, e := range p.recv {
			if e.T != "lsp" {
				continue
			}

			var m rpcMessage
			if json.Unmarshal(e.M, &m) == nil && pred(m) {
				p.mu.Unlock()

				return m
			}
		}
		p.mu.Unlock()

		select {
		case <-p.notify:
		case <-deadline:
			t.Fatal("timed out waiting for message from bqls")
		}
	}
}

func lspMsg(t *testing.T, v any) string {
	t.Helper()

	m, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	env, _ := json.Marshal(Envelope{T: "lsp", M: m})

	return string(env)
}

func appMsg(method string, params any) string {
	m, _ := json.Marshal(method)
	p, _ := json.Marshal(params)
	env, _ := json.Marshal(Envelope{T: "app", M: m, P: p})

	return string(env)
}

// runLoop mimics objc.Main: scheduled work runs on later "run-loop turns"
// on a single goroutine, never inline with the producer.
type runLoop struct {
	mu  sync.Mutex
	fns []func()
}

const turn = 5 * time.Millisecond

func newRunLoop(t *testing.T) *runLoop {
	t.Helper()

	l := &runLoop{}
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })

	go func() {
		tick := time.NewTicker(turn)
		defer tick.Stop()

		for {
			select {
			case <-done:
				return
			case <-tick.C:
			}

			l.mu.Lock()
			fns := l.fns
			l.fns = nil
			l.mu.Unlock()

			for _, fn := range fns {
				fn()
			}
		}
	}()

	return l
}

func (l *runLoop) schedule(fn func()) {
	l.mu.Lock()
	l.fns = append(l.fns, fn)
	l.mu.Unlock()
}

type harness struct {
	b     *Bridge
	page  *fakePage
	mu    sync.Mutex
	apps  []string
	diags map[string]int
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	opts := bqconfig.DefaultLSPOptions()
	opts.DefaultProject = "p"
	opts.BigQueryAPIEnabled = false
	opts.CacheDir = t.TempDir()

	h := &harness{page: newFakePage(), diags: make(map[string]int)}

	b, err := New(Options{
		LSP:      lsp.ServerOptions{DefaultLSPOptions: opts},
		Eval:     h.page.eval,
		Schedule: newRunLoop(t).schedule,
		OnApp: func(method string, _ json.RawMessage) {
			h.mu.Lock()
			h.apps = append(h.apps, method)
			h.mu.Unlock()
		},
		OnDiagnostics: func(uri string, n int) {
			h.mu.Lock()
			h.diags[uri] = n
			h.mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = b.Close() })
	h.b = b

	return h
}

// initialize performs the handshake Monaco's LSP client does.
func (h *harness) initialize(t *testing.T) rpcMessage {
	t.Helper()

	h.b.Receive(lspMsg(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"processId": nil, "rootUri": nil, "capabilities": map[string]any{}},
	}))

	// Responses are held back until the page says it is ready.
	time.Sleep(50 * time.Millisecond)

	h.page.mu.Lock()
	early := len(h.page.recv)
	h.page.mu.Unlock()

	if early != 0 {
		t.Fatalf("%d messages delivered before ready", early)
	}

	h.b.Receive(appMsg("ready", map[string]string{"monaco": "test"}))

	init := h.page.waitLSP(t, func(m rpcMessage) bool { return string(m.ID) == "1" })
	h.b.Receive(lspMsg(t, map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}}))

	return init
}

func (h *harness) open(t *testing.T, text string) {
	t.Helper()

	h.b.Receive(lspMsg(t, map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen", "params": map[string]any{
		"textDocument": map[string]any{"uri": testURI, "languageId": "sql", "version": 1, "text": text},
	}}))
}

func TestBridgeHandshakeAndCapabilities(t *testing.T) {
	h := newHarness(t)
	init := h.initialize(t)

	var res struct {
		Capabilities struct {
			Experimental struct {
				Statements bool `json:"bqlsStatements"`
			} `json:"experimental"`
			SemanticTokens json.RawMessage `json:"semanticTokensProvider"`
			Completion     json.RawMessage `json:"completionProvider"`
		} `json:"capabilities"`
	}

	if err := json.Unmarshal(init.Result, &res); err != nil {
		t.Fatal(err)
	}

	if !res.Capabilities.Experimental.Statements || len(res.Capabilities.SemanticTokens) == 0 || len(res.Capabilities.Completion) == 0 {
		t.Fatalf("capabilities = %s", init.Result)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.apps) != 1 || h.apps[0] != "ready" {
		t.Errorf("app messages = %v", h.apps)
	}
}

func TestBridgeDiagnosticsCompletionAndStatements(t *testing.T) {
	h := newHarness(t)
	h.initialize(t)

	h.open(t, "SELECT 1 AS a;\nSELEC broken;\nCREATE TABLE d.t (x INT64)")

	diag := h.page.waitLSP(t, func(m rpcMessage) bool { return m.Method == "textDocument/publishDiagnostics" })
	if !strings.Contains(string(diag.Params), testURI) {
		t.Errorf("diagnostics for wrong uri: %s", diag.Params)
	}

	h.mu.Lock()
	n := h.diags[testURI]
	h.mu.Unlock()

	if n == 0 {
		t.Error("OnDiagnostics did not observe the syntax error")
	}

	h.b.Receive(lspMsg(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/completion", "params": map[string]any{
		"textDocument": map[string]any{"uri": testURI}, "position": map[string]any{"line": 0, "character": 7},
	}}))

	comp := h.page.waitLSP(t, func(m rpcMessage) bool { return string(m.ID) == "2" })
	if comp.Error != nil || len(comp.Result) < 10 {
		t.Errorf("completion = %+v / %s", comp.Error, comp.Result)
	}

	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	sts, err := h.b.Statements(ctx, "FILE:///bqui/TAB-1.sql") // Monaco lowercases; so do we
	if err != nil {
		t.Fatal(err)
	}

	var kinds []string
	for _, s := range sts {
		kinds = append(kinds, string(s.Kind))
	}

	if len(sts) != 3 || kinds[0] != "QUERY" || kinds[2] != "DDL" {
		t.Fatalf("statements = %v", kinds)
	}

	// The bridge's own exchange must never reach the page.
	h.page.mu.Lock()
	defer h.page.mu.Unlock()

	for _, e := range h.page.recv {
		if strings.Contains(string(e.M), ownIDPrefix) {
			t.Fatalf("private response leaked to page: %s", e.M)
		}
	}
}

func TestBridgeBatchesBursts(t *testing.T) {
	h := newHarness(t)
	h.initialize(t)

	h.page.mu.Lock()
	before := h.page.evals
	h.page.mu.Unlock()

	for range 20 {
		if err := h.b.SendApp("setText", map[string]string{"text": "x"}); err != nil {
			t.Fatal(err)
		}
	}

	deadline := time.After(waitTimeout)

	for {
		h.page.mu.Lock()
		got := 0

		for _, e := range h.page.recv {
			if e.T == "app" {
				got++
			}
		}

		evals := h.page.evals - before
		h.page.mu.Unlock()

		if got == 20 {
			if evals > 3 {
				t.Errorf("20 messages took %d evaluateJavaScript calls", evals)
			}

			return
		}

		select {
		case <-h.page.notify:
		case <-deadline:
			t.Fatalf("got %d of 20 app messages", got)
		}
	}
}

func TestBridgeClose(t *testing.T) {
	h := newHarness(t)

	if err := h.b.Close(); err != nil {
		t.Fatal(err)
	}

	if err := h.b.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	if _, err := h.b.Statements(context.Background(), testURI); !errors.Is(err, ErrClosed) {
		t.Fatalf("Statements after Close: %v", err)
	}

	h.b.Receive("not json") // must not panic
}

func TestOpenAssets(t *testing.T) {
	a, err := Open("/")
	if err != nil || !strings.Contains(string(a.Body), "bqui-editor.js") || !strings.HasPrefix(a.ContentType, "text/html") {
		t.Fatalf("index: %v %q", err, a.ContentType)
	}

	if _, terr := Open("/../../etc/passwd"); !errors.Is(terr, ErrNotFound) {
		t.Errorf("traversal: %v", terr)
	}

	if MonacoVersion() == "" {
		t.Skip("monaco assets not generated; run go generate ./internal/editor")
	}

	loader, err := Open("/monaco/vs/loader.js")
	if err != nil || len(loader.Body) < 1000 || !strings.HasPrefix(loader.ContentType, "text/javascript") {
		t.Fatalf("loader: %v %d %q", err, len(loader.Body), loader.ContentType)
	}
}
