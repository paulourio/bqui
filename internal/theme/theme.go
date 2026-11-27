// Package theme models bqui themes: app appearance, editor colors and
// per-token styles (bqls semantic token types), grid, JSON and plan colors,
// and fonts, each color with light and dark variants. Themes are TOML files;
// VS Code color themes can be imported.
package theme

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Appearance selects the app appearance.
type Appearance string

// Appearances.
const (
	System Appearance = "system"
	Light  Appearance = "light"
	Dark   Appearance = "dark"
)

// Theme is one complete theme.
type Theme struct {
	Name       string     `toml:"name"`
	Appearance Appearance `toml:"appearance"`
	Fonts      Fonts      `toml:"fonts"`
	Light      Palette    `toml:"light"`
	Dark       Palette    `toml:"dark"`

	// Path is where the theme was loaded from ("" for built-ins).
	Path string `toml:"-"`
}

// Fonts sets font sizes (the editor font family is embedded).
type Fonts struct {
	EditorSize float64 `toml:"editor_size,omitempty"`
	Ligatures  *bool   `toml:"ligatures,omitempty"`
	GridSize   float64 `toml:"grid_size,omitempty"`
}

// Palette is one appearance's colors. Colors are "#rrggbb" or "#rrggbbaa".
type Palette struct {
	Editor EditorColors     `toml:"editor"`
	Tokens map[string]Style `toml:"tokens"`
	Grid   GridColors       `toml:"grid"`
	JSON   JSONColors       `toml:"json"`
	Plan   PlanColors       `toml:"plan"`
}

// EditorColors are Monaco workbench colors.
type EditorColors struct {
	Background    string `toml:"background,omitempty"`
	Foreground    string `toml:"foreground,omitempty"`
	Selection     string `toml:"selection,omitempty"`
	LineHighlight string `toml:"line_highlight,omitempty"`
	Cursor        string `toml:"cursor,omitempty"`
	LineNumber    string `toml:"line_number,omitempty"`
}

// Style is a token style. Keys are bqls semantic token types, optionally
// with one modifier ("property.partitioned").
type Style struct {
	Color     string `toml:"color,omitempty"`
	Bold      bool   `toml:"bold,omitempty"`
	Italic    bool   `toml:"italic,omitempty"`
	Underline bool   `toml:"underline,omitempty"`
}

// GridColors style result cells ("" = system colors).
type GridColors struct {
	Null   string `toml:"null,omitempty"`
	Number string `toml:"number,omitempty"`
}

// JSONColors style the JSON results view.
type JSONColors struct {
	Key     string `toml:"key,omitempty"`
	String  string `toml:"string,omitempty"`
	Number  string `toml:"number,omitempty"`
	Literal string `toml:"literal,omitempty"` // true, false, null
}

// PlanColors style the execution graph heat scale.
type PlanColors struct {
	Hot string `toml:"hot,omitempty"`
}

// TokenTypes are the bqls semantic token types (and the modifier
// combinations bqui styles).
var TokenTypes = []string{
	"keyword", "namespace", "class", "type", "function", "function.defaultLibrary",
	"variable", "property", "parameter", "property.partitioned", "property.clustered",
	"property.pseudo", "variable.pseudo", "string", "number", "comment", "macro", "enumMember",
}

// Palette returns the palette for an appearance.
func (t *Theme) Palette(dark bool) Palette {
	if dark {
		return t.Dark
	}

	return t.Light
}

// Validate checks names and colors.
func (t *Theme) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("theme: name is required")
	}

	switch t.Appearance {
	case "", System, Light, Dark:
	default:
		return fmt.Errorf("theme %s: appearance must be system, light or dark, not %q", t.Name, t.Appearance)
	}

	for _, v := range []struct {
		name string
		p    Palette
	}{{"light", t.Light}, {"dark", t.Dark}} {
		for key, c := range v.p.colors() {
			if c == "" {
				continue
			}

			if _, err := ParseColor(c); err != nil {
				return fmt.Errorf("theme %s: %s.%s: %w", t.Name, v.name, key, err)
			}
		}
	}

	return nil
}

// colors lists every color of a palette by key (validation).
func (p Palette) colors() map[string]string {
	m := map[string]string{
		"editor.background": p.Editor.Background, "editor.foreground": p.Editor.Foreground,
		"editor.selection": p.Editor.Selection, "editor.line_highlight": p.Editor.LineHighlight,
		"editor.cursor": p.Editor.Cursor, "editor.line_number": p.Editor.LineNumber,
		"grid.null": p.Grid.Null, "grid.number": p.Grid.Number,
		"json.key": p.JSON.Key, "json.string": p.JSON.String, "json.number": p.JSON.Number, "json.literal": p.JSON.Literal,
		"plan.hot": p.Plan.Hot,
	}

	for k, s := range p.Tokens {
		m["tokens."+k] = s.Color
	}

	return m
}

// RGBA is a parsed color with components in [0, 1].
type RGBA struct{ R, G, B, A float64 }

// ParseColor parses #rgb, #rrggbb or #rrggbbaa.
func ParseColor(s string) (RGBA, error) {
	h := strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}

	if len(h) == 6 {
		h += "ff"
	}

	if len(h) != 8 || !strings.HasPrefix(strings.TrimSpace(s), "#") {
		return RGBA{}, fmt.Errorf("bad color %q (want #rrggbb)", s)
	}

	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return RGBA{}, fmt.Errorf("bad color %q (want #rrggbb)", s)
	}

	const maxByte = 255.0

	return RGBA{
		R: float64(v>>24&0xff) / maxByte, G: float64(v>>16&0xff) / maxByte,
		B: float64(v>>8&0xff) / maxByte, A: float64(v&0xff) / maxByte,
	}, nil
}

// merge fills unset fields of t from base (imports and partial user themes
// inherit the default theme).
func (t *Theme) merge(base *Theme) {
	if t.Appearance == "" {
		t.Appearance = base.Appearance
	}

	if t.Fonts.EditorSize == 0 {
		t.Fonts.EditorSize = base.Fonts.EditorSize
	}

	if t.Fonts.GridSize == 0 {
		t.Fonts.GridSize = base.Fonts.GridSize
	}

	if t.Fonts.Ligatures == nil {
		t.Fonts.Ligatures = base.Fonts.Ligatures
	}

	t.Light = mergePalette(t.Light, base.Light)
	t.Dark = mergePalette(t.Dark, base.Dark)
}

func mergePalette(p, base Palette) Palette {
	fill := func(dst *string, src string) {
		if *dst == "" {
			*dst = src
		}
	}

	fill(&p.Editor.Background, base.Editor.Background)
	fill(&p.Editor.Foreground, base.Editor.Foreground)
	fill(&p.Editor.Selection, base.Editor.Selection)
	fill(&p.Editor.LineHighlight, base.Editor.LineHighlight)
	fill(&p.Editor.Cursor, base.Editor.Cursor)
	fill(&p.Editor.LineNumber, base.Editor.LineNumber)
	fill(&p.Grid.Null, base.Grid.Null)
	fill(&p.Grid.Number, base.Grid.Number)
	fill(&p.JSON.Key, base.JSON.Key)
	fill(&p.JSON.String, base.JSON.String)
	fill(&p.JSON.Number, base.JSON.Number)
	fill(&p.JSON.Literal, base.JSON.Literal)
	fill(&p.Plan.Hot, base.Plan.Hot)

	tokens := maps.Clone(base.Tokens)
	if tokens == nil {
		tokens = map[string]Style{}
	}

	maps.Copy(tokens, p.Tokens)
	p.Tokens = tokens

	return p
}

// Load reads a theme file; missing values inherit the default theme.
func Load(path string) (*Theme, error) {
	var t Theme

	if _, err := toml.DecodeFile(path, &t); err != nil {
		return nil, fmt.Errorf("theme: %s: %w", filepath.Base(path), err)
	}

	if t.Name == "" {
		t.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	t.merge(Default())
	t.Path = path

	if err := t.Validate(); err != nil {
		return nil, err
	}

	return &t, nil
}

// Save writes a theme as TOML (atomically).
func Save(t *Theme, path string) error {
	if err := t.Validate(); err != nil {
		return err
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(t); err != nil {
		return fmt.Errorf("theme: encode: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return fmt.Errorf("theme: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), filePerm); err != nil {
		return fmt.Errorf("theme: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("theme: %w", err)
	}

	return nil
}

const (
	dirPerm  = 0o750
	filePerm = 0o600
	themeExt = ".toml"
)

// FileName returns a file name for a theme name ("My Theme" →
// "my-theme.toml").
func FileName(name string) string {
	var b strings.Builder

	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}

	n := strings.Trim(b.String(), "-")
	if n == "" {
		n = "theme"
	}

	return n + themeExt
}

// List returns the built-in themes followed by the valid themes in dir
// (sorted by name). Invalid files are reported in errs, not fatal.
func List(dir string) (themes []*Theme, errs []error) {
	themes = Builtins()

	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return themes, []error{err}
	}

	var user []*Theme

	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), themeExt) {
			continue
		}

		t, err := Load(filepath.Join(dir, e.Name()))
		if err != nil {
			errs = append(errs, err)

			continue
		}

		user = append(user, t)
	}

	slices.SortFunc(user, func(a, b *Theme) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })

	return append(themes, user...), errs
}

// Find returns the theme with a name (case-insensitive) or nil.
func Find(themes []*Theme, name string) *Theme {
	for _, t := range themes {
		if strings.EqualFold(t.Name, name) {
			return t
		}
	}

	return nil
}
