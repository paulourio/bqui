// Command fetchmonaco downloads the pinned monaco-editor npm tarball,
// verifies its sha512 integrity, and extracts the AMD "min/vs" build into
// gzip-compressed files that internal/editor embeds. It needs no Node.js.
//
// Language services bqui never uses (TypeScript, CSS, HTML workers) are
// skipped; Monaco only loads them when a model of that language exists.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
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

// Pinned release. Bump all three together, then run `go generate ./internal/editor`.
const (
	version   = "0.57.0"
	tarball   = "https://registry.npmjs.org/monaco-editor/-/monaco-editor-" + version + ".tgz"
	integrity = "sha512-5BkI9KGoqrNvBGUe15/QlZq3OooZ8WLg1AxTpaqHRCP3HNpzPPZKE2EDz8M7c+VRmCeUw1Brp4cx/PWm3kI/5A=="

	srcPrefix   = "package/min/vs/"
	maxTarball  = 64 << 20
	maxFile     = 32 << 20
	stampFile   = "VERSION"
	dirPerm     = 0o755
	filePerm    = 0o644
	httpTimeout = 2 * time.Minute
)

// skipped are path prefixes (relative to min/vs/) left out of the bundle.
var skipped = []string{
	"language/typescript/", "language/css/", "language/html/",
	"assets/ts.worker-", "assets/css.worker-", "assets/html.worker-",
}

var errIntegrity = errors.New("fetchmonaco: integrity mismatch")

func main() {
	out := flag.String("out", "web/monaco/vs", "output directory")
	force := flag.Bool("force", false, "re-download even if the stamp matches")
	flag.Parse()

	if err := run(*out, *force); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(out string, force bool) error {
	if !force {
		if b, err := os.ReadFile(filepath.Join(out, stampFile)); err == nil && strings.TrimSpace(string(b)) == version {
			return nil
		}
	}

	data, err := download(tarball)
	if err != nil {
		return err
	}

	if err = verify(data, integrity); err != nil {
		return err
	}

	// Only ever replace a directory that is clearly ours.
	if filepath.Base(filepath.Clean(out)) != "vs" || filepath.Base(filepath.Dir(filepath.Clean(out))) != "monaco" {
		return fmt.Errorf("fetchmonaco: refusing to replace %q (expected a .../monaco/vs directory)", out)
	}

	if err = os.RemoveAll(out); err != nil {
		return fmt.Errorf("fetchmonaco: %w", err)
	}

	n, size, err := extract(data, out)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "fetchmonaco: monaco-editor %s: %d files, %.1f MiB compressed\n", version, n, float64(size)/(1<<20))

	return os.WriteFile(filepath.Join(out, stampFile), []byte(version+"\n"), filePerm)
}

func download(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("fetchmonaco: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetchmonaco: download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetchmonaco: download: HTTP %d", resp.StatusCode)
	}

	return io.ReadAll(io.LimitReader(resp.Body, maxTarball))
}

func verify(data []byte, want string) error {
	sum := sha512.Sum512(data)
	if got := "sha512-" + base64.StdEncoding.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("%w: got %s", errIntegrity, got)
	}

	return nil
}

func extract(data []byte, out string) (files int, compressed int64, err error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return 0, 0, fmt.Errorf("fetchmonaco: %w", err)
	}

	tr := tar.NewReader(zr)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files, compressed, nil
		}

		if err != nil {
			return files, compressed, fmt.Errorf("fetchmonaco: tar: %w", err)
		}

		rel, ok := keep(hdr)
		if !ok {
			continue
		}

		n, err := writeGzip(filepath.Join(out, filepath.FromSlash(rel)+".gz"), io.LimitReader(tr, maxFile))
		if err != nil {
			return files, compressed, err
		}

		files++
		compressed += n
	}
}

func keep(hdr *tar.Header) (string, bool) {
	if hdr.Typeflag != tar.TypeReg || !strings.HasPrefix(hdr.Name, srcPrefix) {
		return "", false
	}

	rel := path.Clean(strings.TrimPrefix(hdr.Name, srcPrefix))
	if rel == "." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) || strings.HasSuffix(rel, ".d.ts") {
		return "", false
	}

	for _, p := range skipped {
		if strings.HasPrefix(rel, p) {
			return "", false
		}
	}

	return rel, true
}

func writeGzip(dst string, r io.Reader) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dst), dirPerm); err != nil {
		return 0, fmt.Errorf("fetchmonaco: %w", err)
	}

	var buf bytes.Buffer

	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return 0, fmt.Errorf("fetchmonaco: %w", err)
	}

	if _, err := io.Copy(zw, r); err != nil {
		return 0, fmt.Errorf("fetchmonaco: %s: %w", dst, err)
	}

	if err := zw.Close(); err != nil {
		return 0, fmt.Errorf("fetchmonaco: %w", err)
	}

	if err := os.WriteFile(dst, buf.Bytes(), filePerm); err != nil {
		return 0, fmt.Errorf("fetchmonaco: %w", err)
	}

	return int64(buf.Len()), nil
}
