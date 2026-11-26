// Command fetchassets downloads the pinned third-party web assets that
// internal/editor embeds, verifies their published digests, and writes the
// needed files gzip-compressed. It needs no Node.js.
//
//   - Monaco editor (MIT): the AMD "min/vs" build. Language services bqui
//     never uses (TypeScript, CSS, HTML workers) are skipped; Monaco only
//     loads them when a model of that language exists.
//   - FiraCode Nerd Font Mono (SIL OFL 1.1, Nerd Fonts glyphs under their own
//     licenses): Regular and Bold, which keep Fira Code's ligatures (calt).
//
// License and notice files are extracted next to the assets.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// asset is one pinned download. Bump version, url and digest together, then
// run `go generate ./internal/editor`.
type asset struct {
	name    string
	version string
	url     string
	digest  string // "sha512-<base64>" (npm integrity) or "sha256:<hex>" (GitHub)
	format  string // "tgz" or "zip"
	dir     string // output directory under -web
	// pick maps an archive entry to its output path (relative to dir).
	pick func(entry string) (string, bool)
}

var assets = []asset{
	{
		name:    "monaco-editor",
		version: "0.57.0",
		url:     "https://registry.npmjs.org/monaco-editor/-/monaco-editor-0.57.0.tgz",
		digest:  "sha512-5BkI9KGoqrNvBGUe15/QlZq3OooZ8WLg1AxTpaqHRCP3HNpzPPZKE2EDz8M7c+VRmCeUw1Brp4cx/PWm3kI/5A==",
		format:  "tgz",
		dir:     "monaco",
		pick:    pickMonaco,
	},
	{
		name:    "FiraCode Nerd Font Mono",
		version: "nerd-fonts v3.5.1",
		url:     "https://github.com/ryanoasis/nerd-fonts/releases/download/v3.5.1/FiraCode.zip",
		digest:  "sha256:239395baf60c89b2eaf4862b6b09db0ef95605cd3e8eef51c00345822a81a665",
		format:  "zip",
		dir:     "fonts",
		pick:    pickFont,
	},
}

// monacoSkipped are path prefixes (relative to min/vs/) left out of the bundle.
var monacoSkipped = []string{
	"language/typescript/", "language/css/", "language/html/",
	"assets/ts.worker-", "assets/css.worker-", "assets/html.worker-",
}

func pickMonaco(entry string) (string, bool) {
	switch entry {
	case "package/LICENSE":
		return "LICENSE", true
	case "package/ThirdPartyNotices.txt":
		return "ThirdPartyNotices.txt", true
	}

	const prefix = "package/min/vs/"
	if !strings.HasPrefix(entry, prefix) || strings.HasSuffix(entry, ".d.ts") {
		return "", false
	}

	rel := strings.TrimPrefix(entry, prefix)
	for _, p := range monacoSkipped {
		if strings.HasPrefix(rel, p) {
			return "", false
		}
	}

	return "vs/" + rel, true
}

func pickFont(entry string) (string, bool) {
	switch entry {
	case "FiraCodeNerdFontMono-Regular.ttf", "FiraCodeNerdFontMono-Bold.ttf":
		return entry, true
	case "LICENSE":
		return "LICENSE", true // SIL OFL 1.1 for Fira Code
	case "README.md":
		return "NERD_FONTS_README.md", true // lists the icon sets and their licenses
	}

	return "", false
}

const (
	maxDownload = 64 << 20
	maxFile     = 32 << 20
	stampFile   = "VERSION"
	dirPerm     = 0o755
	filePerm    = 0o644
	httpTimeout = 3 * time.Minute
)

var (
	errDigest  = errors.New("fetchassets: digest mismatch")
	errUnsafe  = errors.New("fetchassets: refusing to replace directory")
	errMissing = errors.New("fetchassets: expected files missing from archive")
)

func main() {
	web := flag.String("web", "web", "the embedded web directory (internal/editor/web)")
	force := flag.Bool("force", false, "re-download even if the stamps match")
	flag.Parse()

	for _, a := range assets {
		if err := fetch(a, *web, *force); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func fetch(a asset, web string, force bool) error {
	out := filepath.Join(web, a.dir)
	stamp := a.version + " " + a.digest

	if !force {
		if b, err := os.ReadFile(filepath.Join(out, stampFile)); err == nil && strings.TrimSpace(string(b)) == stamp {
			return nil
		}
	}

	data, err := download(a.url)
	if err != nil {
		return err
	}

	if err = verify(data, a.digest); err != nil {
		return fmt.Errorf("%s: %w", a.name, err)
	}

	// Only ever replace a directory that is clearly ours: web/<dir>.
	if filepath.Base(filepath.Clean(web)) != "web" {
		return fmt.Errorf("%w %q (expected .../web/%s)", errUnsafe, out, a.dir)
	}

	if err = os.RemoveAll(out); err != nil {
		return fmt.Errorf("fetchassets: %w", err)
	}

	n, size, err := extract(a, data, out)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "fetchassets: %s %s: %d files, %.1f MiB compressed\n", a.name, a.version, n, float64(size)/(1<<20))

	return os.WriteFile(filepath.Join(out, stampFile), []byte(stamp+"\n"), filePerm)
}

func download(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("fetchassets: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetchassets: download %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetchassets: download %s: HTTP %d", url, resp.StatusCode)
	}

	return io.ReadAll(io.LimitReader(resp.Body, maxDownload))
}

func verify(data []byte, want string) error {
	var got string

	switch {
	case strings.HasPrefix(want, "sha512-"):
		sum := sha512.Sum512(data)
		got = "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
	case strings.HasPrefix(want, "sha256:"):
		sum := sha256.Sum256(data)
		got = "sha256:" + hex.EncodeToString(sum[:])
	default:
		return fmt.Errorf("%w: unsupported digest %q", errDigest, want)
	}

	if got != want {
		return fmt.Errorf("%w: got %s", errDigest, got)
	}

	return nil
}

type entryFunc func(name string, r io.Reader) error

func extract(a asset, data []byte, out string) (files int, compressed int64, err error) {
	write := func(name string, r io.Reader) error {
		rel, ok := a.pick(name)
		if !ok {
			return nil
		}

		rel = path.Clean(rel)
		if rel == "." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
			return nil
		}

		n, werr := writeGzip(filepath.Join(out, filepath.FromSlash(rel)+".gz"), io.LimitReader(r, maxFile))
		if werr != nil {
			return werr
		}

		files++
		compressed += n

		return nil
	}

	switch a.format {
	case "tgz":
		err = walkTarGz(data, write)
	case "zip":
		err = walkZip(data, write)
	default:
		err = fmt.Errorf("fetchassets: unknown format %q", a.format)
	}

	if err == nil && files == 0 {
		err = fmt.Errorf("%w: %s", errMissing, a.name)
	}

	return files, compressed, err
}

func walkTarGz(data []byte, fn entryFunc) error {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("fetchassets: %w", err)
	}

	tr := tar.NewReader(zr)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("fetchassets: tar: %w", err)
		}

		if hdr.Typeflag == tar.TypeReg {
			if err := fn(hdr.Name, tr); err != nil {
				return err
			}
		}
	}
}

func walkZip(data []byte, fn entryFunc) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("fetchassets: zip: %w", err)
	}

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("fetchassets: zip %s: %w", f.Name, err)
		}

		err = fn(f.Name, rc)
		_ = rc.Close()

		if err != nil {
			return err
		}
	}

	return nil
}

func writeGzip(dst string, r io.Reader) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dst), dirPerm); err != nil {
		return 0, fmt.Errorf("fetchassets: %w", err)
	}

	var buf bytes.Buffer

	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return 0, fmt.Errorf("fetchassets: %w", err)
	}

	if _, err := io.Copy(zw, r); err != nil {
		return 0, fmt.Errorf("fetchassets: %s: %w", dst, err)
	}

	if err := zw.Close(); err != nil {
		return 0, fmt.Errorf("fetchassets: %w", err)
	}

	if err := os.WriteFile(dst, buf.Bytes(), filePerm); err != nil {
		return 0, fmt.Errorf("fetchassets: %w", err)
	}

	return int64(buf.Len()), nil
}
