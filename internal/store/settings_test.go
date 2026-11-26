package store

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSettingsDefaultsRoundTripAndPins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")

	s, err := LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(s.PinnedProjects, []string{DefaultPinned}) {
		t.Fatalf("defaults = %+v", s)
	}

	if !s.Pin("ff-homelab") || s.Pin("ff-homelab") || s.Pin("") {
		t.Error("Pin change reporting is wrong")
	}

	s.BillingProject = "ff-homelab"

	if err = s.Save(path); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != filePerm {
		t.Fatalf("perm = %v, %v", info.Mode(), err)
	}

	got, err := LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}

	if got.BillingProject != "ff-homelab" || !slices.Equal(got.PinnedProjects, []string{DefaultPinned, "ff-homelab"}) {
		t.Fatalf("round trip = %+v", got)
	}

	if !got.Unpin(DefaultPinned) || got.Unpin("nope") || len(got.PinnedProjects) != 1 {
		t.Errorf("Unpin = %+v", got.PinnedProjects)
	}

	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestLoadSettingsRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{nope"), filePerm); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadSettings(path); err == nil {
		t.Fatal("expected parse error")
	}
}
