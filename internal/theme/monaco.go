package theme

import (
	"slices"
	"strings"
)

// MonacoRule is one monaco.editor.ITokenThemeRule.
type MonacoRule struct {
	Token      string `json:"token"`
	Foreground string `json:"foreground,omitempty"` // rrggbb (no '#')
	FontStyle  string `json:"fontStyle,omitempty"`
}

// MonacoTheme is the monaco.editor.IStandaloneThemeData for one palette.
type MonacoTheme struct {
	Base    string            `json:"base"` // vs or vs-dark
	Inherit bool              `json:"inherit"`
	Rules   []MonacoRule      `json:"rules"`
	Colors  map[string]string `json:"colors"`
}

// Monaco compiles a palette into Monaco theme data. Token keys map to
// semantic token rules ("type" and "type.modifier"); the matching Monarch
// "sql" tokens are styled too, so colors are right before semantic tokens
// arrive.
func Monaco(p Palette, dark bool) MonacoTheme {
	mt := MonacoTheme{Base: "vs", Inherit: true, Colors: map[string]string{}}
	if dark {
		mt.Base = "vs-dark"
	}

	keys := make([]string, 0, len(p.Tokens))
	for k := range p.Tokens {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	for _, k := range keys {
		r := rule(k, p.Tokens[k])
		mt.Rules = append(mt.Rules, r)

		for _, alias := range monarchAliases[k] {
			a := r
			a.Token = alias
			mt.Rules = append(mt.Rules, a)
		}
	}

	if p.Editor.Foreground != "" {
		// The empty token is the default text style.
		mt.Rules = append([]MonacoRule{{Token: "", Foreground: hex(p.Editor.Foreground)}}, mt.Rules...)
	}

	set := func(key, color string) {
		if color != "" {
			mt.Colors[key] = color
		}
	}

	set("editor.background", p.Editor.Background)
	set("editor.foreground", p.Editor.Foreground)
	set("editor.selectionBackground", p.Editor.Selection)
	set("editor.lineHighlightBackground", p.Editor.LineHighlight)
	set("editorCursor.foreground", p.Editor.Cursor)
	set("editorLineNumber.foreground", p.Editor.LineNumber)
	set("editorGutter.background", p.Editor.Background)
	set("minimap.background", p.Editor.Background)

	return mt
}

// monarchAliases are Monaco's built-in "sql" tokenizer tokens.
var monarchAliases = map[string][]string{
	"keyword":  {"keyword.sql", "operator.sql"},
	"string":   {"string.sql"},
	"number":   {"number.sql"},
	"comment":  {"comment.sql"},
	"type":     {"predefined.sql"},
	"variable": {"identifier.sql"},
}

func rule(token string, s Style) MonacoRule {
	r := MonacoRule{Token: token, Foreground: hex(s.Color)}

	var styles []string

	if s.Bold {
		styles = append(styles, "bold")
	}

	if s.Italic {
		styles = append(styles, "italic")
	}

	if s.Underline {
		styles = append(styles, "underline")
	}

	r.FontStyle = strings.Join(styles, " ")

	return r
}

// hex strips '#' and any alpha (Monaco token colors are rrggbb).
func hex(c string) string {
	h := strings.TrimPrefix(c, "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}

	if len(h) > 6 {
		h = h[:6]
	}

	return strings.ToLower(h)
}
