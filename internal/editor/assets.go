// Package editor hosts the Monaco-based SQL editor: embedded web assets and
// the bridge that relays LSP traffic between Monaco's built-in LSP client and
// the in-process bqls server. Nothing here imports AppKit.
package editor

import (
	"bytes"
	"compress/gzip"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"path"
	"strings"
)

// web holds index.html, bqui-editor.js, the gzip-compressed Monaco build
// under monaco/ and the editor font under fonts/. The third-party files are
// vendored (committed) so builds never download anything outside GOPROXY;
// `make update-assets` refreshes them deliberately (see tools/fetchassets).
//
//go:embed all:web
var web embed.FS

// ErrNotFound is returned for unknown asset paths.
var ErrNotFound = errors.New("editor: asset not found")

// Asset is one servable file.
type Asset struct {
	Body        []byte
	ContentType string
}

// Open returns the asset at urlPath (e.g. "/index.html",
// "/monaco/vs/loader.js"), transparently decompressing embedded .gz files.
func Open(urlPath string) (Asset, error) {
	p := path.Clean("/" + urlPath)
	if p == "/" {
		p = "/index.html"
	}

	name := "web" + p

	body, err := web.ReadFile(name)
	if err != nil {
		gz, gzErr := web.ReadFile(name + ".gz")
		if gzErr != nil {
			return Asset{}, fmt.Errorf("%w: %s", ErrNotFound, p)
		}

		if body, err = gunzip(gz); err != nil {
			return Asset{}, fmt.Errorf("editor: %s: %w", p, err)
		}
	}

	return Asset{Body: body, ContentType: contentType(p)}, nil
}

// MonacoVersion returns the vendored Monaco version stamp, or "" if the
// assets are missing.
func MonacoVersion() string { return stamp("web/monaco/VERSION") }

// FontVersion returns the embedded editor font stamp, or "".
func FontVersion() string { return stamp("web/fonts/VERSION") }

func stamp(name string) string {
	b, err := fs.ReadFile(web, name)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(b))
}

func gunzip(b []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	return io.ReadAll(zr)
}

func contentType(p string) string {
	switch path.Ext(p) {
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	case ".json":
		return "application/json"
	case ".ttf":
		return "font/ttf"
	}

	if t := mime.TypeByExtension(path.Ext(p)); t != "" {
		return t
	}

	return "application/octet-stream"
}
