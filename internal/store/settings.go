// Package store persists bqui's local state. Settings are a small JSON file
// in the user config directory (~/Library/Application Support/bqui on macOS).
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

const (
	dirPerm      = 0o700
	filePerm     = 0o600
	settingsFile = "settings.json"
	appDir       = "bqui"
)

// DefaultPinned is pinned on first launch so the explorer is never empty.
const DefaultPinned = "bigquery-public-data"

// Settings are user preferences that survive restarts.
type Settings struct {
	// BillingProject runs jobs and is bqls' default project.
	BillingProject string `json:"billingProject,omitempty"`
	// PinnedProjects are shown in the explorer, in order.
	PinnedProjects []string `json:"pinnedProjects"`
	// Theme is the chosen theme's name ("" = Default).
	Theme string `json:"theme,omitempty"`
	// Query preferences.
	Prefs Preferences `json:"preferences"`
	// Starred datasets ("project.dataset"), for quick filtering.
	Starred []string `json:"starredDatasets,omitempty"`
	// StarredOnly shows only starred datasets in the explorer.
	StarredOnly bool `json:"starredOnly,omitempty"`
}

// ToggleStar stars or unstars a dataset and reports whether it is now
// starred.
func (s *Settings) ToggleStar(project, dataset string) bool {
	key := project + "." + dataset

	if i := slices.Index(s.Starred, key); i >= 0 {
		s.Starred = slices.Delete(s.Starred, i, i+1)

		return false
	}

	s.Starred = append(s.Starred, key)
	slices.Sort(s.Starred)

	return true
}

// DefaultThemesDir is ~/Library/Application Support/bqui/themes (or the OS
// equivalent).
func DefaultThemesDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("store: %w", err)
	}

	return filepath.Join(dir, appDir, "themes"), nil
}

// Pin adds project to the pinned list (no-op if present). It reports
// whether the list changed.
func (s *Settings) Pin(project string) bool {
	if project == "" || slices.Contains(s.PinnedProjects, project) {
		return false
	}

	s.PinnedProjects = append(s.PinnedProjects, project)

	return true
}

// Unpin removes project from the pinned list. It reports whether the list changed.
func (s *Settings) Unpin(project string) bool {
	n := len(s.PinnedProjects)
	s.PinnedProjects = slices.DeleteFunc(s.PinnedProjects, func(p string) bool { return p == project })

	return len(s.PinnedProjects) != n
}

// DefaultPath returns the settings file location.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("store: %w", err)
	}

	return filepath.Join(dir, appDir, settingsFile), nil
}

// LoadSettings reads settings from path. A missing file yields defaults.
func LoadSettings(path string) (*Settings, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Settings{PinnedProjects: []string{DefaultPinned}}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("store: read settings: %w", err)
	}

	var s Settings
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("store: parse %s: %w", path, err)
	}

	return &s, nil
}

// Save writes settings atomically (temp file + rename).
func (s *Settings) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return fmt.Errorf("store: %w", err)
	}

	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.json")
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}

	defer func() { _ = os.Remove(tmp.Name()) }() // best effort; gone after rename

	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("store: %w", err)
	}

	if err := tmp.Chmod(filePerm); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("store: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: %w", err)
	}

	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("store: %w", err)
	}

	return nil
}
