package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestZipBundleKeepsLayoutAndModes(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "bqui-bin")

	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	app := filepath.Join(dir, "bqui.app")
	if err := build(exe, app, "", bundleInfo{Name: "bqui", Exe: "bqui", ID: "x.y", Version: "1.0.0", MinOS: "26.0"}); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "bqui.zip")
	if err := zipBundle(app, out); err != nil {
		t.Fatal(err)
	}

	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	modes := map[string]os.FileMode{}
	for _, f := range zr.File {
		modes[f.Name] = f.Mode()
	}

	if m, ok := modes["bqui.app/Contents/MacOS/bqui"]; !ok || m&0o111 == 0 {
		t.Errorf("executable missing or not executable: %v (%v)", m, modes)
	}

	if _, ok := modes["bqui.app/Contents/Info.plist"]; !ok {
		t.Errorf("Info.plist missing: %v", modes)
	}

	if _, err := os.Stat(out + ".partial"); !os.IsNotExist(err) {
		t.Error("partial zip left behind")
	}
}

func TestShortVersion(t *testing.T) {
	for in, want := range map[string]string{"1.2.3": "1.2.3", "0.4.0-rc1": "0.4.0", "190d644-dirty": "0.0.0", "1234567": "0.0.0", "dev": "0.0.0", "2": "2", "1.2.3-4-gabcdef0": "1.2.3"} {
		if got := (bundleInfo{Version: in}).Short(); got != want {
			t.Errorf("Short(%q) = %q, want %q", in, got, want)
		}
	}
}
