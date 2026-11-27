//go:build e2e

package e2e_test

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOfflineCrossPlatformBuildAndVet(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	// Compile cmd/bquictl (native) and cmd/bqui across linux + darwin/arm64 + darwin/amd64.
	bquictlBin := buildBinary(t, "./cmd/bquictl", "bquictl", "", "")
	bquiLinuxBin := buildBinary(t, "./cmd/bqui", "bqui-linux", "linux", "amd64")
	_ = buildBinary(t, "./cmd/bqui", "bqui-darwin-arm64", "darwin", "arm64")
	_ = buildBinary(t, "./cmd/bqui", "bqui-darwin-amd64", "darwin", "amd64")
	bundleBin := buildBinary(t, "./tools/bundle", "bundle", "", "")

	if stdout, stderr, rc := runSubprocess(t, "", bquictlBin, nil, "-version"); rc != 0 || !strings.HasPrefix(stdout, "bquictl ") {
		t.Fatalf("bquictl -version rc=%d stdout=%q stderr=%q", rc, stdout, stderr)
	}

	if stdout, stderr, rc := runSubprocess(t, "", bquiLinuxBin, nil); rc != 2 || stdout != "" || !strings.Contains(stderr, "only runs on macOS") {
		t.Fatalf("bqui linux stub rc=%d stdout=%q stderr=%q", rc, stdout, stderr)
	}

	if _, stderr, rc := runSubprocess(t, "", bundleBin, nil); rc != 2 || !strings.Contains(stderr, "-bin is required") {
		t.Fatalf("bundle missing -bin rc=%d stderr=%q", rc, stderr)
	}

	// Verify go vet ./... across Linux, darwin/arm64, and darwin/amd64 with CGO_ENABLED=0.
	targets := []struct {
		goos, goarch string
	}{
		{"linux", "amd64"},
		{"darwin", "arm64"},
		{"darwin", "amd64"},
	}

	for _, target := range targets {
		env := []string{"CGO_ENABLED=0", "GOOS=" + target.goos, "GOARCH=" + target.goarch}
		if _, stderr, rc := runSubprocess(t, root, "go", env, "vet", "./..."); rc != 0 {
			t.Fatalf("go vet ./... (%s/%s) rc=%d stderr=%s", target.goos, target.goarch, rc, stderr)
		}
	}

	// Also verify compiling the e2e test binary itself for darwin/arm64 into TempDir.
	outTest := filepath.Join(t.TempDir(), "e2e-arm64.test")
	if _, stderr, rc := runSubprocess(t, root, "go", []string{"CGO_ENABLED=0", "GOOS=darwin", "GOARCH=arm64"}, "test", "-tags", "e2e", "-c", "-o", outTest, "./internal/e2e"); rc != 0 {
		t.Fatalf("go test -tags e2e -c darwin/arm64 rc=%d stderr=%s", rc, stderr)
	}
}
