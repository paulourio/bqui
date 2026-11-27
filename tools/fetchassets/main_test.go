package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVendoredAssetsMatchPins fails when the committed assets under
// internal/editor/web drift from the pins above (run make update-assets).
func TestVendoredAssetsMatchPins(t *testing.T) {
	web := filepath.Join("..", "..", "internal", "editor", "web")
	if _, err := os.Stat(filepath.Join(web, "index.html")); err != nil {
		t.Skip("source tree not available (test binary run outside the repo)")
	}

	for _, a := range assets {
		b, err := os.ReadFile(filepath.Join(web, a.dir, stampFile))
		if err != nil {
			t.Fatalf("%s: vendored stamp missing: %v", a.name, err)
		}

		if got, want := strings.TrimSpace(string(b)), a.version+" "+a.digest; got != want {
			t.Errorf("%s: vendored %q, pinned %q; run make update-assets", a.name, got, want)
		}
	}
}

func TestPickers(t *testing.T) {
	cases := []struct {
		pick  func(string) (string, bool)
		entry string
		want  string
		ok    bool
	}{
		{pickMonaco, "package/min/vs/loader.js", "vs/loader.js", true},
		{pickMonaco, "package/LICENSE", "LICENSE", true},
		{pickMonaco, "package/min/vs/language/typescript/ts.worker.js", "", false},
		{pickMonaco, "package/esm/vs/editor/editor.api.js", "", false},
		{pickFont, "FiraCodeNerdFontMono-Regular.ttf", "FiraCodeNerdFontMono-Regular.ttf", true},
		{pickFont, "FiraCodeNerdFont-Regular.ttf", "", false},
		{pickFont, "README.md", "NERD_FONTS_README.md", true},
	}

	for _, c := range cases {
		if got, ok := c.pick(c.entry); got != c.want || ok != c.ok {
			t.Errorf("pick(%q) = %q, %v", c.entry, got, ok)
		}
	}
}
