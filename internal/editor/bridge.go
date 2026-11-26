package editor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/paulourio/bqlang/lsp"
)

// Envelope is one message between the page and Go.
//
//	page → Go: {"t":"lsp","m":<JSON-RPC message>} or {"t":"app","m":"run","p":{...}}
//	Go → page: the same shapes, delivered in batches to window.bqui.recv([...]).
type Envelope struct {
	T string          `json:"t"`
	M json.RawMessage `json:"m"`
	P json.RawMessage `json:"p,omitempty"`
}

// Options configures a Bridge.
type Options struct {
	// LSP configures the in-process bqls server.
	LSP lsp.ServerOptions
	// Eval delivers a JavaScript snippet to the page. It is only called from
	// functions passed to Schedule.
	Eval func(js string)
	// Schedule runs fn on the UI thread (objc.Main on macOS).
	Schedule func(fn func())
	// OnApp receives app-level messages from the page ("ready", "run", ...).
	// It is called on the caller of Receive (the UI thread).
	OnApp func(method string, params json.RawMessage)
	// OnCost receives bqls/queryCost notifications (background goroutine).
	OnCost func(params json.RawMessage)
	// OnDiagnostics observes textDocument/publishDiagnostics (background goroutine).
	OnDiagnostics func(uri string, count int)
	Logger        *slog.Logger
}

// Bridge relays LSP JSON-RPC between Monaco's built-in LSP client and an
// in-process bqls server, and carries app messages both ways.
type Bridge struct {
	opts Options
	log  *slog.Logger
	srv  *lsp.Server

	toServer   *io.PipeWriter
	fromServer *io.PipeReader
	writeMu    sync.Mutex
	serveDone  chan error

	nextID  atomic.Int64
	pendMu  sync.Mutex
	pending map[string]chan rpcMessage

	outMu     sync.Mutex
	outbox    []Envelope
	scheduled bool
	ready     bool
	closed    atomic.Bool
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ErrClosed is returned after Close.
var ErrClosed = errors.New("editor: bridge closed")

// errRPC wraps JSON-RPC error responses.
var errRPC = errors.New("editor: rpc error")

const ownIDPrefix = "bqui:"

// New starts the bqls server and returns a Bridge.
func New(opts Options) (*Bridge, error) {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	if opts.LSP.Logger == nil {
		opts.LSP.Logger = log
	}

	srv, err := lsp.NewServer(opts.LSP)
	if err != nil {
		return nil, fmt.Errorf("editor: start bqls: %w", err)
	}

	sr, cw := io.Pipe() // server reads what we write
	cr, sw := io.Pipe() // we read what the server writes

	b := &Bridge{
		opts: opts, log: log, srv: srv,
		toServer: cw, fromServer: cr,
		serveDone: make(chan error, 1),
		pending:   make(map[string]chan rpcMessage),
	}

	go func() {
		err := srv.Serve(context.Background(), sr, sw)
		_ = sw.Close()
		b.serveDone <- err
	}()

	go b.readServer()

	return b, nil
}

// Close stops the server. It is safe to call more than once.
func (b *Bridge) Close() error {
	if b.closed.Swap(true) {
		return nil
	}

	_ = b.toServer.Close()
	err := <-b.serveDone
	_ = b.fromServer.Close()

	if cerr := b.srv.Close(); err == nil {
		err = cerr
	}

	b.failPending()

	return err
}

// Receive handles one message posted by the page (a JSON Envelope).
func (b *Bridge) Receive(raw string) {
	var env Envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		b.log.Warn("editor: bad message from page", slog.Any("error", err))

		return
	}

	switch env.T {
	case "lsp":
		if err := b.writeServer(env.M); err != nil {
			b.log.Warn("editor: forward to bqls", slog.Any("error", err))
		}
	case "app":
		var method string
		_ = json.Unmarshal(env.M, &method)

		if method == "ready" {
			b.markReady()
		}

		if b.opts.OnApp != nil {
			b.opts.OnApp(method, env.P)
		}
	default:
		b.log.Warn("editor: unknown envelope type", slog.String("t", env.T))
	}
}

// SendApp queues an app message for the page.
func (b *Bridge) SendApp(method string, params any) error {
	m, err := json.Marshal(method)
	if err != nil {
		return err
	}

	p, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("editor: marshal %s params: %w", method, err)
	}

	b.enqueue(Envelope{T: "app", M: m, P: p})

	return nil
}

// Statements asks bqls for the statement boundaries of a document. uri must be
// the model URI as Monaco reports it (Monaco's LSP client lowercases URIs).
func (b *Bridge) Statements(ctx context.Context, uri string) ([]lsp.StatementInfo, error) {
	var res lsp.StatementsResult

	err := b.Call(ctx, "bqls/statements", lsp.StatementsParams{TextDocument: lsp.TextDocumentIdentifier{URI: strings.ToLower(uri)}}, &res)

	return res.Statements, err
}

// Call sends a request to bqls on the bridge's own ID space and decodes the
// result into out. Monaco never sees these exchanges.
func (b *Bridge) Call(ctx context.Context, method string, params, out any) error {
	if b.closed.Load() {
		return ErrClosed
	}

	id := strconv.Quote(ownIDPrefix + strconv.FormatInt(b.nextID.Add(1), 10))
	ch := make(chan rpcMessage, 1)

	b.pendMu.Lock()
	b.pending[id] = ch
	b.pendMu.Unlock()

	defer func() {
		b.pendMu.Lock()
		delete(b.pending, id)
		b.pendMu.Unlock()
	}()

	p, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("editor: marshal %s: %w", method, err)
	}

	msg, err := json.Marshal(rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method, Params: p})
	if err != nil {
		return err
	}

	if err := b.writeServer(msg); err != nil {
		return err
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return ErrClosed
		}

		if resp.Error != nil {
			return fmt.Errorf("%w: %s: %d %s", errRPC, method, resp.Error.Code, resp.Error.Message)
		}

		if out != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, out)
		}

		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *Bridge) writeServer(body []byte) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()

	if b.closed.Load() {
		return ErrClosed
	}

	if _, err := fmt.Fprintf(b.toServer, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}

	_, err := b.toServer.Write(body)

	return err
}

// readServer routes every frame bqls writes.
func (b *Bridge) readServer() {
	br := bufio.NewReader(b.fromServer)

	for {
		body, err := readFrame(br)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
				b.log.Warn("editor: read bqls", slog.Any("error", err))
			}

			b.failPending()

			return
		}

		b.route(body)
	}
}

func (b *Bridge) route(body []byte) {
	var msg rpcMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		b.log.Warn("editor: bad frame from bqls", slog.Any("error", err))

		return
	}

	if msg.Method == "" && strings.HasPrefix(string(msg.ID), `"`+ownIDPrefix) {
		b.pendMu.Lock()
		ch := b.pending[string(msg.ID)]
		b.pendMu.Unlock()

		if ch != nil {
			ch <- msg
		}

		return
	}

	switch msg.Method {
	case "bqls/queryCost":
		if b.opts.OnCost != nil {
			b.opts.OnCost(msg.Params)
		}

		return // Monaco has no use for it
	case "textDocument/publishDiagnostics":
		if b.opts.OnDiagnostics != nil {
			var p struct {
				URI         string            `json:"uri"`
				Diagnostics []json.RawMessage `json:"diagnostics"`
			}

			if json.Unmarshal(msg.Params, &p) == nil {
				b.opts.OnDiagnostics(p.URI, len(p.Diagnostics))
			}
		}
	}

	b.enqueue(Envelope{T: "lsp", M: body})
}

func (b *Bridge) failPending() {
	b.pendMu.Lock()
	defer b.pendMu.Unlock()

	for id, ch := range b.pending {
		close(ch)
		delete(b.pending, id)
	}
}

// enqueue adds env to the outbox and schedules one flush per burst.
func (b *Bridge) enqueue(env Envelope) {
	b.outMu.Lock()
	b.outbox = append(b.outbox, env)
	schedule := b.ready && !b.scheduled
	b.scheduled = b.scheduled || schedule
	b.outMu.Unlock()

	if schedule {
		b.opts.Schedule(b.flush)
	}
}

func (b *Bridge) markReady() {
	b.outMu.Lock()
	b.ready = true
	schedule := len(b.outbox) > 0 && !b.scheduled
	b.scheduled = b.scheduled || schedule
	b.outMu.Unlock()

	if schedule {
		b.opts.Schedule(b.flush)
	}
}

// flush runs on the UI thread and delivers the whole outbox in one call.
func (b *Bridge) flush() {
	b.outMu.Lock()
	batch := b.outbox
	b.outbox = nil
	b.scheduled = false
	b.outMu.Unlock()

	if len(batch) == 0 {
		return
	}

	data, err := json.Marshal(batch)
	if err != nil {
		b.log.Warn("editor: marshal batch", slog.Any("error", err))

		return
	}

	b.opts.Eval("window.bqui&&window.bqui.recv(" + string(data) + ");")
}

const maxFrame = 64 << 20

var errFrame = errors.New("editor: bad LSP frame")

func readFrame(br *bufio.Reader) ([]byte, error) {
	length := -1

	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}

		if v, ok := strings.CutPrefix(line, "Content-Length:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 0 || n > maxFrame {
				return nil, fmt.Errorf("%w: content length %q", errFrame, v)
			}

			length = n
		}
	}

	if length < 0 {
		return nil, fmt.Errorf("%w: missing Content-Length", errFrame)
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(br, body); err != nil {
		return nil, err
	}

	return body, nil
}
