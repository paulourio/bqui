//go:build darwin

package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/results"
	"github.com/paulourio/bqui/internal/theme"
)

const (
	appearanceAqua     = "NSAppearanceNameAqua"
	appearanceDarkAqua = "NSAppearanceNameDarkAqua"
	// AppleInterfaceThemeChangedNotification is posted (distributed) when
	// the system switches between light and dark.
	interfaceThemeChanged = "AppleInterfaceThemeChangedNotification"
	gridRowPadding        = 10.0
	minGridRowHeight      = 22.0
	themesDirPerm         = 0o750
)

// loadThemes reads built-in and user themes.
func (s *Shell) loadThemes() {
	var errs []error

	s.Themes, errs = theme.List(s.opts.ThemesDir)
	s.themeErrs = errs

	for _, err := range errs {
		fmt.Fprintln(os.Stderr, "bqui:", err)
	}
}

// ApplyTheme switches the theme by name (unknown names use Default) and
// restyles every pane live.
func (s *Shell) ApplyTheme(name string) {
	t := theme.Find(s.Themes, name)
	if t == nil {
		t = theme.Find(s.Themes, theme.DefaultName)
	}

	s.Theme = t

	var appearance objc.ID

	switch t.Appearance {
	case theme.Light:
		appearance = objc.Msg(objc.Cls("NSAppearance"), "appearanceNamed:", objc.String(appearanceAqua))
	case theme.Dark:
		appearance = objc.Msg(objc.Cls("NSAppearance"), "appearanceNamed:", objc.String(appearanceDarkAqua))
	case theme.System, "":
	}

	objc.Msg(s.App, "setAppearance:", appearance)
	s.Editor.SetTheme(t)

	appkit.SetGridFontSize(t.Fonts.GridSize)

	h := max(minGridRowHeight, t.Fonts.GridSize+gridRowPadding)
	s.Results.Table.SetRowHeight(h)
	s.JSON.Table.SetRowHeight(h)

	s.restyle()

	if s.settings.Theme != t.Name {
		s.settings.Theme = t.Name
		s.saveSettings()
	}
}

// Dark reports whether the app currently renders dark.
func (s *Shell) Dark() bool {
	names := objc.Array(objc.String(appearanceAqua), objc.String(appearanceDarkAqua))
	best := objc.Msg(objc.Msg(s.App, "effectiveAppearance"), "bestMatchFromAppearancesWithNames:", names)

	return objc.GoString(best) == appearanceDarkAqua
}

// restyle resolves the theme colors for the current appearance.
func (s *Shell) restyle() {
	if s.Theme == nil {
		return
	}

	p := s.Theme.Palette(s.Dark())

	for _, c := range s.themeColors {
		objc.Release(c)
	}

	s.themeColors = s.themeColors[:0]

	s.Results.NullColor, s.Results.NumberColor = s.color(p.Grid.Null), s.color(p.Grid.Number)
	s.JSON.colors = map[results.JSONKind]objc.ID{
		results.JSONKey: s.color(p.JSON.Key), results.JSONString: s.color(p.JSON.String),
		results.JSONNumber: s.color(p.JSON.Number), results.JSONLiteral: s.color(p.JSON.Literal),
	}
	s.Graph.Hot = s.color(p.Plan.Hot)

	s.Results.Table.Reload()
	s.JSON.Table.Reload()
	appkit.SetNeedsDisplay(s.Graph.Canvas)
}

// color converts a theme color (retained until the next restyle).
func (s *Shell) color(hex string) objc.ID {
	if hex == "" {
		return objc.Nil
	}

	c, err := theme.ParseColor(hex)
	if err != nil {
		return objc.Nil
	}

	id := objc.Retain(appkit.RGB(c.R, c.G, c.B, c.A))
	s.themeColors = append(s.themeColors, id)

	return id
}

// watchAppearance restyles when the system switches light/dark.
func (s *Shell) watchAppearance() {
	center := objc.Msg(objc.Cls("NSDistributedNotificationCenter"), "defaultCenter")
	target := objc.NewTarget(func(objc.ID) {
		// The app's effective appearance updates after the notification.
		objc.Main(s.restyle)
	})

	objc.Msg(center, "addObserver:selector:name:object:", target, objc.Sel(objc.ActionSelector), objc.String(interfaceThemeChanged), objc.Nil)
}

// themeMenu lists themes (with a check on the current one) and actions.
func (s *Shell) themeMenu() appkit.MenuItem {
	items := make([]appkit.MenuItem, 0, len(s.Themes)+6)

	for _, t := range s.Themes {
		name := t.Name
		items = append(items, appkit.MenuItem{
			Title:   name,
			Run:     func() { s.ApplyTheme(name); s.rebuildMenus() },
			Checked: func() bool { return s.Theme != nil && s.Theme.Name == name },
		})
	}

	items = append(items, appkit.MenuItem{},
		appkit.MenuItem{Title: "Import VS Code Theme…", Run: s.chooseVSCodeTheme},
		appkit.MenuItem{Title: "Duplicate Current Theme", Run: s.duplicateTheme},
		appkit.MenuItem{Title: "Reload Themes", Run: func() { s.ReloadThemes() }},
		appkit.MenuItem{Title: "Show Themes Folder", Run: s.showThemesFolder},
	)

	return appkit.MenuItem{Title: "Theme", Submenu: &appkit.Menu{Title: "Theme", Items: items}}
}

// rebuildMenus reinstalls the main menu (theme list or checks changed).
func (s *Shell) rebuildMenus() { appkit.SetMainMenu(s.App, s.menus()) }

// ReloadThemes rereads theme files (after editing them) and reapplies.
func (s *Shell) ReloadThemes() {
	name := theme.DefaultName
	if s.Theme != nil {
		name = s.Theme.Name
	}

	s.loadThemes()
	s.ApplyTheme(name)
	s.rebuildMenus()

	if len(s.themeErrs) > 0 {
		s.status("✕ " + s.themeErrs[0].Error())
	} else {
		s.status(fmt.Sprintf("Loaded %d themes.", len(s.Themes)))
	}
}

func (s *Shell) chooseVSCodeTheme() {
	path, ok := appkit.OpenFile("Import VS Code Theme", "", "json")
	if !ok {
		return
	}

	if _, err := s.ImportVSCodeTheme(path); err != nil {
		s.status("✕ " + err.Error())
	}
}

// ImportVSCodeTheme converts a VS Code theme, saves it to the themes folder
// and applies it.
func (s *Shell) ImportVSCodeTheme(path string) (*theme.Theme, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	t, err := theme.ImportVSCode(data)
	if err != nil {
		return nil, err
	}

	if err := s.saveTheme(t); err != nil {
		return nil, err
	}

	s.status("Imported theme " + t.Name + ".")

	return t, nil
}

func (s *Shell) duplicateTheme() {
	if s.Theme == nil {
		return
	}

	t := *s.Theme
	t.Name += " Copy"

	if err := s.saveTheme(&t); err != nil {
		s.status("✕ " + err.Error())

		return
	}

	s.status("Saved " + t.Path + " — edit it, then View ▸ Theme ▸ Reload Themes.")
}

// saveTheme writes t into the themes folder, reloads the list and applies it.
func (s *Shell) saveTheme(t *theme.Theme) error {
	if s.opts.ThemesDir == "" {
		return errors.New("no themes folder")
	}

	t.Path = filepath.Join(s.opts.ThemesDir, theme.FileName(t.Name))
	if err := theme.Save(t, t.Path); err != nil {
		return err
	}

	s.loadThemes()
	s.ApplyTheme(t.Name)
	s.rebuildMenus()

	return nil
}

func (s *Shell) showThemesFolder() {
	if s.opts.ThemesDir == "" {
		return
	}

	_ = os.MkdirAll(s.opts.ThemesDir, themesDirPerm)
	appkit.RevealInFinder(s.opts.ThemesDir)
}
