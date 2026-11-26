//go:build darwin

// Package webkit binds the small slice of WebKit bqui needs to host the
// Monaco editor: a WKWebView, a Go-implemented custom URL scheme handler,
// a script message handler (page → Go), and JavaScript evaluation (Go → page).
package webkit

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/objc"
)

const frameworkPath = "/System/Library/Frameworks/WebKit.framework/WebKit"

var (
	loadOnce sync.Once
	errLoad  error
)

// Load dlopens WebKit. It is idempotent and called by New.
func Load() error {
	loadOnce.Do(func() {
		if _, err := purego.Dlopen(frameworkPath, purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
			errLoad = fmt.Errorf("webkit: dlopen: %w", err)
		}
	})

	return errLoad
}

// Response is what a scheme handler returns for one request.
type Response struct {
	Status      int
	ContentType string
	Body        []byte
}

// Options configures a WebView.
type Options struct {
	// Scheme is the custom URL scheme served by Serve (e.g. "bqui").
	Scheme string
	// Serve answers every request for Scheme. It runs on the main thread.
	Serve func(host, path string) Response
	// MessageName is the window.webkit.messageHandlers.<name> channel.
	MessageName string
	// OnMessage receives string bodies posted by the page (main thread).
	OnMessage func(body string)
	// Inspectable enables the Safari Web Inspector (debug builds).
	Inspectable bool
}

// WebView is a configured WKWebView.
type WebView struct {
	View objc.ID
	opts Options
}

var (
	schemeOwners  objc.Registry[*WebView]
	messageOwners objc.Registry[*WebView]

	schemeHandlerClass = objc.LazyClass("BQUISchemeHandler", "NSObject",
		[]string{"WKURLSchemeHandler"}, func() []objc.Method {
			return []objc.Method{
				{Sel: "webView:startURLSchemeTask:", Fn: func(self objc.ID, _ objc.SEL, _, task objc.ID) {
					if w, ok := schemeOwners.Get(self); ok {
						objc.WithPool(func() { w.serveTask(task) })
					}
				}},
				{Sel: "webView:stopURLSchemeTask:", Fn: func(objc.ID, objc.SEL, objc.ID, objc.ID) {
					// Responses are written synchronously in start; nothing to cancel.
				}},
			}
		})

	messageHandlerClass = objc.LazyClass("BQUIScriptMessageHandler", "NSObject",
		[]string{"WKScriptMessageHandler"}, func() []objc.Method {
			return []objc.Method{
				{Sel: "userContentController:didReceiveScriptMessage:", Fn: func(self objc.ID, _ objc.SEL, _, msg objc.ID) {
					w, ok := messageOwners.Get(self)
					if !ok || w.opts.OnMessage == nil {
						return
					}

					body := objc.Msg(msg, "body")
					if body == objc.Nil || !objc.Send[bool](body, "isKindOfClass:", objc.Cls("NSString")) {
						return
					}

					w.opts.OnMessage(objc.GoString(body))
				}},
			}
		})
)

// New creates a WKWebView with the custom scheme and message channel wired.
func New(opts Options) (*WebView, error) {
	if err := Load(); err != nil {
		return nil, err
	}

	w := &WebView{opts: opts}
	cfg := objc.New("WKWebViewConfiguration")

	if opts.Scheme != "" && opts.Serve != nil {
		sh := objc.Msg(objc.Msg(schemeHandlerClass(), "alloc"), "init")
		schemeOwners.Put(sh, w)
		objc.Msg(cfg, "setURLSchemeHandler:forURLScheme:", sh, objc.String(opts.Scheme))
	}

	if opts.MessageName != "" {
		mh := objc.Msg(objc.Msg(messageHandlerClass(), "alloc"), "init")
		messageOwners.Put(mh, w)
		objc.Msg(objc.Msg(cfg, "userContentController"), "addScriptMessageHandler:name:", mh, objc.String(opts.MessageName))
	}

	w.View = objc.Msg(objc.Msg(objc.Cls("WKWebView"), "alloc"), "initWithFrame:configuration:", appkit.MakeRect(0, 0, 100, 100), cfg)
	objc.Release(cfg)

	// Avoid a white flash before the page paints (supported KVC key on macOS).
	objc.Msg(w.View, "setValue:forKey:", objc.Msg(objc.Cls("NSNumber"), "numberWithBool:", false), objc.String("drawsBackground"))

	if opts.Inspectable && objc.RespondsTo(w.View, "setInspectable:") {
		objc.Msg(w.View, "setInspectable:", true)
	}

	return w, nil
}

// Load navigates to url (e.g. "bqui://editor/index.html").
func (w *WebView) Load(url string) {
	u := objc.Msg(objc.Cls("NSURL"), "URLWithString:", objc.String(url))
	objc.Msg(w.View, "loadRequest:", objc.Msg(objc.Cls("NSURLRequest"), "requestWithURL:", u))
}

// Eval runs JavaScript in the page and ignores the result. Main thread only.
func (w *WebView) Eval(js string) {
	objc.Msg(w.View, "evaluateJavaScript:completionHandler:", objc.String(js), objc.Nil)
}

func (w *WebView) serveTask(task objc.ID) {
	url := objc.Msg(objc.Msg(task, "request"), "URL")
	host := objc.GoString(objc.Msg(url, "host"))
	path := objc.GoString(objc.Msg(url, "path"))

	res := w.opts.Serve(host, path)
	if res.Status == 0 {
		res.Status = http.StatusOK
	}

	headers := objc.Msg(objc.Cls("NSMutableDictionary"), "dictionary")
	set := func(k, v string) { objc.Msg(headers, "setObject:forKey:", objc.String(v), objc.String(k)) }

	if res.ContentType != "" {
		set("Content-Type", res.ContentType)
	}

	set("Content-Length", strconv.Itoa(len(res.Body)))
	set("Cache-Control", "no-cache")

	resp := objc.Msg(objc.Msg(objc.Cls("NSHTTPURLResponse"), "alloc"),
		"initWithURL:statusCode:HTTPVersion:headerFields:", url, int64(res.Status), objc.String("HTTP/1.1"), headers)
	defer objc.Release(resp)

	objc.Msg(task, "didReceiveResponse:", resp)

	if len(res.Body) > 0 {
		data := objc.Msg(objc.Cls("NSData"), "dataWithBytes:length:", unsafe.Pointer(&res.Body[0]), uint64(len(res.Body)))
		objc.Msg(task, "didReceiveData:", data)
	}

	objc.Msg(task, "didFinish")
}
