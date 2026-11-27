//go:build darwin

package ui

import (
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/logs"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/store"
)

const (
	prefsWidth   = 520.0
	prefsHeight  = 340.0
	prefsField   = 260.0
	prefsPadding = 20.0
)

// prefsWindow is the Settings window.
type prefsWindow struct {
	window  objc.ID
	fields  []objc.ID // location, dataset, max billed, cost warning, spill, studio region
	message objc.ID
}

// PreferencesWindow is the Settings window, once shown (smoke checks).
func (s *Shell) PreferencesWindow() objc.ID {
	if s.prefs == nil {
		return objc.Nil
	}

	return s.prefs.window
}

// ShowPreferences opens the Settings window (created once).
func (s *Shell) ShowPreferences() {
	if s.prefs == nil {
		s.prefs = s.newPrefsWindow()
	}

	t := s.settings.Prefs.Text()
	for i, v := range []string{t.Location, t.DefaultDataset, t.MaxBytesBilled, t.CostWarningUSD, t.MaxSpillBytes, t.StudioRegion} {
		appkit.SetString(s.prefs.fields[i], v)
	}

	appkit.SetString(s.prefs.message, "")
	objc.Msg(s.prefs.window, "center")
	objc.Msg(s.prefs.window, "makeKeyAndOrderFront:", objc.Nil)
}

func (s *Shell) newPrefsWindow() *prefsWindow {
	p := &prefsWindow{window: appkit.NewPanelWindow(AppName+" Settings", appkit.MakeRect(0, 0, prefsWidth, prefsHeight))}

	labels := []string{"Default location:", "Default dataset:", "Maximum bytes billed:", "Cost warning:", "Result spill limit:", "Studio saved queries:"}
	placeholders := []string{
		"Automatic (where the data lives)", "project.dataset",
		"No limit — e.g. 10 GB", "Off — e.g. 1.50 ($ per query)", "8 GiB (default) — e.g. 20 GB",
		"us-central1 (holds the US multi-region)",
	}

	rows := make([][]objc.ID, 0, len(labels)+2)

	for i, l := range labels {
		f := appkit.TextField(placeholders[i], prefsField)
		p.fields = append(p.fields, f)
		rows = append(rows, []objc.ID{appkit.Label(l, 0), f})
	}

	p.message = appkit.SecondaryLabel("")

	apply := appkit.Button("Apply", func() { s.applyPrefsWindow() })
	appkit.SetDefaultButton(apply)

	logsButton := appkit.Button("Open Logs Folder", func() {
		if dir, err := logs.DefaultDir(); err == nil {
			appkit.RevealInFinder(dir)
		}
	})

	rows = append(rows, []objc.ID{appkit.GridEmptyCell(), p.message}, []objc.ID{logsButton, apply})

	grid := appkit.Grid(rows)
	content := objc.Msg(p.window, "contentView")
	objc.Msg(content, "addSubview:", grid)

	appkit.Activate(
		appkit.Equal(appkit.Anchor(grid, "topAnchor"), appkit.Anchor(content, "topAnchor"), prefsPadding),
		appkit.Equal(appkit.Anchor(grid, "centerXAnchor"), appkit.Anchor(content, "centerXAnchor"), 0),
	)

	return p
}

func (s *Shell) applyPrefsWindow() {
	f := s.prefs.fields
	text := store.PreferenceText{
		Location: appkit.StringValue(f[0]), DefaultDataset: appkit.StringValue(f[1]),
		MaxBytesBilled: appkit.StringValue(f[2]), CostWarningUSD: appkit.StringValue(f[3]), MaxSpillBytes: appkit.StringValue(f[4]),
		StudioRegion: appkit.StringValue(f[5]),
	}

	if err := s.ApplyPreferences(text); err != nil {
		appkit.SetString(s.prefs.message, "✕ "+err.Error())

		return
	}

	appkit.SetString(s.prefs.message, "✓ Saved. Applies to the next run.")
}

// ApplyPreferences validates, saves and applies preferences (runs, bqls,
// spill limit).
func (s *Shell) ApplyPreferences(text store.PreferenceText) error {
	p, err := text.Parse()
	if err != nil {
		return err
	}

	s.settings.Prefs = p
	s.saveSettings()
	s.Runner.SetMaxSpillBytes(p.MaxSpillBytes)
	s.Editor.ConfigureLSP(s.lspOptions())
	s.Saved.ReloadStudio()

	return nil
}
