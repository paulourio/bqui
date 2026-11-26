// bqui editor page: hosts Monaco and connects its built-in LSP client to the
// in-process bqls server through the native bridge (internal/editor/bridge.go).
//
// Messages to Go:   webkit.messageHandlers.bqui.postMessage(JSON.stringify(envelope))
// Messages from Go: window.bqui.recv([envelope, ...])
// Envelope: {t: "lsp", m: <JSON-RPC message>} | {t: "app", m: <method>, p: <params>}
(function () {
  'use strict';

  var handler = window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.bqui;
  var DEFAULT_URI = 'file:///bqui/tab-1.sql';

  function post(env) {
    if (handler) handler.postMessage(JSON.stringify(env));
  }

  function app(method, params) {
    post({ t: 'app', m: method, p: params === undefined ? null : params });
  }

  function fmt(v) {
    if (v instanceof Error) return v.stack || v.message;
    if (typeof v === 'string') return v;
    try { return JSON.stringify(v); } catch (_) { return String(v); }
  }

  // Forward console output and errors to Go (visible in bqui's log / smoke).
  ['log', 'info', 'warn', 'error'].forEach(function (level) {
    var orig = console[level].bind(console);
    console[level] = function () {
      try { app('console', { level: level, msg: Array.prototype.map.call(arguments, fmt).join(' ') }); } catch (_) { /* ignore */ }
      orig.apply(null, arguments);
    };
  });
  window.addEventListener('error', function (e) {
    app('console', { level: 'error', msg: e.message + ' @ ' + e.filename + ':' + e.lineno });
  });
  window.addEventListener('unhandledrejection', function (e) {
    app('console', { level: 'error', msg: 'unhandled rejection: ' + fmt(e.reason) });
  });

  // A Worker-shaped port: Monaco's createTransportToWorker() only needs
  // postMessage and addEventListener('message').
  var lspListeners = [];
  var port = {
    postMessage: function (msg) { post({ t: 'lsp', m: msg }); },
    addEventListener: function (type, fn) { if (type === 'message') lspListeners.push(fn); },
    removeEventListener: function (type, fn) {
      lspListeners = lspListeners.filter(function (f) { return f !== fn; });
    },
  };

  var state = { editor: null };

  function modelURI(model) {
    // Monaco's LSP client lowercases URIs; report them the same way.
    return model.uri.toString(true).toLowerCase();
  }

  function runPayload(atCursor) {
    var ed = state.editor;
    var model = ed.getModel();
    var sel = ed.getSelection();
    var pos = ed.getPosition();
    return {
      uri: modelURI(model),
      version: model.getVersionId(),
      text: model.getValue(),
      selection: sel && !sel.isEmpty() ? model.getValueInRange(sel) : '',
      cursor: { line: pos.lineNumber - 1, character: pos.column - 1 },
      atCursor: !!atCursor,
    };
  }

  function handleApp(method, p) {
    var ed = state.editor;
    if (!ed) return;
    switch (method) {
      case 'setText':
        ed.getModel().setValue(p.text || '');
        break;
      case 'setTheme':
        monaco.editor.setTheme(p.dark ? 'bqui-dark' : 'bqui-light');
        break;
      case 'focus':
        ed.focus();
        break;
      case 'setOptions':
        ed.updateOptions(p || {});
        break;
      case 'insertText': {
        var sel = ed.getSelection();
        ed.executeEdits('bqui', [{ range: sel, text: p.text, forceMoveMarkers: true }]);
        ed.focus();
        break;
      }
      case 'appendText': {
        // Append a statement after the current text (or replace an empty
        // buffer), select it and reveal it. Undoable as one edit.
        var model = ed.getModel();
        var text = p.text || '';
        var current = model.getValue();
        if (current.trim() === '') {
          model.pushEditOperations([], [{ range: model.getFullModelRange(), text: text }], function () { return null; });
          ed.setSelection(model.getFullModelRange());
        } else {
          var last = model.getLineCount();
          var end = new monaco.Range(last, model.getLineMaxColumn(last), last, model.getLineMaxColumn(last));
          var sep = current.endsWith('\n') ? '\n' : '\n\n';
          model.pushEditOperations([], [{ range: end, text: sep + text }], function () { return null; });
          var startLine = model.getLineCount() - text.split('\n').length + 1;
          ed.setSelection(new monaco.Range(startLine, 1, model.getLineCount(), model.getLineMaxColumn(model.getLineCount())));
        }
        ed.revealRangeInCenterIfOutsideViewport(ed.getSelection());
        ed.focus();
        break;
      }
      default:
        console.warn('bqui: unknown app message', method);
    }
  }

  window.bqui = {
    recv: function (batch) {
      for (var i = 0; i < batch.length; i++) {
        var e = batch[i];
        if (e.t === 'lsp') {
          for (var j = 0; j < lspListeners.length; j++) lspListeners[j]({ data: e.m });
        } else if (e.t === 'app') {
          handleApp(e.m, e.p);
        }
      }
    },
    requestRun: function (atCursor) { if (state.editor) app('run', runPayload(atCursor)); },
    format: function () { if (state.editor) state.editor.getAction('editor.action.formatDocument').run(); },
    action: function (id) {
      var a = state.editor && state.editor.getAction(id);
      if (a) { state.editor.focus(); a.run(); } else { console.warn('bqui: unknown editor action', id); }
    },
    // selfTest is used by the smoke harness: it exercises completion, markers
    // and semantic tokens end to end and reports what Monaco actually shows.
    selfTest: function (sql, line, column) {
      var ed = state.editor;
      var model = ed.getModel();
      model.setValue(sql);
      ed.setPosition({ lineNumber: line, column: column });
      ed.focus();
      setTimeout(function () {
        ed.trigger('selftest', 'editor.action.triggerSuggest', {});
        setTimeout(function () {
          var rows = document.querySelectorAll('.suggest-widget .monaco-list-row');
          var labels = Array.prototype.slice.call(rows, 0, 5).map(function (r) { return r.getAttribute('aria-label') || r.textContent; });
          var markers = monaco.editor.getModelMarkers({ resource: model.uri });
          var semantic = document.querySelectorAll('.view-lines span[class*="mtk"]').length;
          app('selftest', {
            suggestions: rows.length,
            labels: labels,
            markers: markers.length,
            firstMarker: markers.length ? markers[0].message : '',
            spans: semantic,
          });
          ed.trigger('selftest', 'hideSuggestWidget', {});
        }, 2500);
      }, 1500);
    },
  };

  function defineThemes() {
    // Rules for bqls semantic tokens (see bqlang lsp/PROTOCOL.md §4); keys are
    // "type" or "type.modifier". Built-in Monarch "sql" tokens remain the
    // fallback before semantic tokens arrive.
    monaco.editor.defineTheme('bqui-light', {
      base: 'vs', inherit: true,
      rules: [
        { token: 'keyword', foreground: '0000ff' },
        { token: 'namespace', foreground: '267f99' },
        { token: 'class', foreground: '267f99' },
        { token: 'type', foreground: '267f99' },
        { token: 'function', foreground: '795e26' },
        { token: 'function.defaultLibrary', foreground: '795e26' },
        { token: 'variable', foreground: '001080' },
        { token: 'property', foreground: '001080' },
        { token: 'parameter', foreground: '001080', fontStyle: 'italic' },
        { token: 'property.partitioned', foreground: '001080', fontStyle: 'underline' },
        { token: 'property.clustered', foreground: '001080', fontStyle: 'italic' },
        { token: 'property.pseudo', foreground: 'af00db' },
        { token: 'variable.pseudo', foreground: 'af00db' },
        { token: 'string', foreground: 'a31515' },
        { token: 'number', foreground: '098658' },
        { token: 'comment', foreground: '008000', fontStyle: 'italic' },
        { token: 'macro', foreground: 'af00db' },
        { token: 'enumMember', foreground: '0070c1' },
      ],
      colors: {},
    });
    monaco.editor.defineTheme('bqui-dark', {
      base: 'vs-dark', inherit: true,
      rules: [
        { token: 'keyword', foreground: '569cd6' },
        { token: 'namespace', foreground: '4ec9b0' },
        { token: 'class', foreground: '4ec9b0' },
        { token: 'type', foreground: '4ec9b0' },
        { token: 'function', foreground: 'dcdcaa' },
        { token: 'function.defaultLibrary', foreground: 'dcdcaa' },
        { token: 'variable', foreground: '9cdcfe' },
        { token: 'property', foreground: '9cdcfe' },
        { token: 'parameter', foreground: '9cdcfe', fontStyle: 'italic' },
        { token: 'property.partitioned', foreground: '9cdcfe', fontStyle: 'underline' },
        { token: 'property.clustered', foreground: '9cdcfe', fontStyle: 'italic' },
        { token: 'property.pseudo', foreground: 'c586c0' },
        { token: 'variable.pseudo', foreground: 'c586c0' },
        { token: 'string', foreground: 'ce9178' },
        { token: 'number', foreground: 'b5cea8' },
        { token: 'comment', foreground: '6a9955', fontStyle: 'italic' },
        { token: 'macro', foreground: 'c586c0' },
        { token: 'enumMember', foreground: '4fc1ff' },
      ],
      colors: { 'editor.background': '#1e1e1e' },
    });
  }

  function bootError(msg) {
    var el = document.getElementById('boot-error');
    el.style.display = 'block';
    el.textContent = 'Editor failed to load: ' + msg;
    app('bootError', { message: msg });
  }

  if (typeof require === 'undefined') {
    bootError('Monaco loader missing (run go generate ./internal/editor)');
    return;
  }

  require.config({ paths: { vs: 'monaco/vs' } });
  require(['vs/editor/editor.main'], function () {
    defineThemes();

    var dark = window.matchMedia('(prefers-color-scheme: dark)');
    var model = monaco.editor.createModel('', 'sql', monaco.Uri.parse(DEFAULT_URI));
    var ed = monaco.editor.create(document.getElementById('editor'), {
      model: model,
      theme: dark.matches ? 'bqui-dark' : 'bqui-light',
      automaticLayout: true,
      fontFamily: 'ui-monospace, "SF Mono", Menlo, monospace',
      fontSize: 13,
      tabSize: 2,
      insertSpaces: true,
      minimap: { enabled: true },
      'semanticHighlighting.enabled': true,
      bracketPairColorization: { enabled: true },
      scrollBeyondLastLine: false,
      fixedOverflowWidgets: true,
      padding: { top: 8 },
      renderLineHighlight: 'all',
    });
    state.editor = ed;

    dark.addEventListener('change', function (e) {
      monaco.editor.setTheme(e.matches ? 'bqui-dark' : 'bqui-light');
    });

    // Monaco's own LSP client drives completion, hover, signature help,
    // diagnostics, semantic tokens, formatting, code lenses, symbols, ...
    new monaco.lsp.MonacoLspClient(monaco.lsp.createTransportToWorker(port));

    var K = monaco.KeyMod, C = monaco.KeyCode;
    ed.addCommand(K.CtrlCmd | C.Enter, function () { window.bqui.requestRun(false); });
    ed.addCommand(K.CtrlCmd | K.Shift | C.Enter, function () { window.bqui.requestRun(true); });
    ed.addCommand(K.CtrlCmd | K.Shift | C.KeyF, function () { window.bqui.format(); });

    model.onDidChangeContent(function () {
      app('dirty', { uri: modelURI(model), version: model.getVersionId() });
    });

    app('ready', { uri: modelURI(model) });
  }, function (err) {
    bootError(fmt(err));
  });
})();
