package theme

import (
	"encoding/json"
	"fmt"
	"strings"
)

// vscodeTheme is the subset of a VS Code color theme bqui reads.
type vscodeTheme struct {
	Name                string                     `json:"name"`
	Type                string                     `json:"type"` // dark, light, hc, hcLight
	Colors              map[string]string          `json:"colors"`
	TokenColors         []vscodeTokenColor         `json:"tokenColors"`
	SemanticTokenColors map[string]json.RawMessage `json:"semanticTokenColors"`
}

type vscodeTokenColor struct {
	Scope    json.RawMessage `json:"scope"` // string (comma-separated) or []string
	Settings struct {
		Foreground string `json:"foreground"`
		FontStyle  string `json:"fontStyle"`
	} `json:"settings"`
}

// textMateScopes lists, per bqls token type, the TextMate scopes that best
// describe it (most specific first).
var textMateScopes = map[string][]string{
	"keyword":                 {"keyword.other.sql", "keyword.control", "keyword", "storage.modifier"},
	"namespace":               {"entity.name.namespace", "entity.name.type.module", "entity.name.type"},
	"class":                   {"entity.name.class", "entity.name.type.class", "entity.name.type"},
	"type":                    {"storage.type", "support.type", "entity.name.type"},
	"function":                {"entity.name.function", "support.function"},
	"function.defaultLibrary": {"support.function.builtin", "support.function", "entity.name.function"},
	"variable":                {"variable.other", "variable"},
	"property":                {"variable.other.property", "variable.other.object.property", "support.variable.property", "variable.other", "variable"},
	"parameter":               {"variable.parameter"},
	"string":                  {"string.quoted", "string"},
	"number":                  {"constant.numeric"},
	"comment":                 {"comment.line", "comment"},
	"macro":                   {"entity.name.function.macro", "entity.name.function.preprocessor", "keyword.control.directive"},
	"enumMember":              {"variable.other.enummember", "constant.other.enum", "constant.other"},
}

// ImportVSCode converts a VS Code color theme (JSON with comments) into a
// bqui theme. The theme's own appearance is filled from it; the other
// appearance keeps the default theme's colors.
func ImportVSCode(data []byte) (*Theme, error) {
	var vt vscodeTheme
	if err := json.Unmarshal(StripJSONC(data), &vt); err != nil {
		return nil, fmt.Errorf("theme: VS Code theme: %w", err)
	}

	dark := vt.Type != "light" && vt.Type != "hcLight"

	p := Palette{
		Editor: EditorColors{
			Background:    color(vt.Colors["editor.background"]),
			Foreground:    color(vt.Colors["editor.foreground"]),
			Selection:     color(vt.Colors["editor.selectionBackground"]),
			LineHighlight: color(vt.Colors["editor.lineHighlightBackground"]),
			Cursor:        color(vt.Colors["editorCursor.foreground"]),
			LineNumber:    color(vt.Colors["editorLineNumber.foreground"]),
		},
		Tokens: textMateTokens(vt.TokenColors),
	}

	for key, raw := range vt.SemanticTokenColors {
		s, ok := semanticStyle(raw)
		if !ok {
			continue
		}

		// "*.modifier" styles every token type carrying that modifier.
		if mod, wildcard := strings.CutPrefix(key, "*."); wildcard {
			for _, tok := range TokenTypes {
				if strings.HasSuffix(tok, "."+mod) {
					p.Tokens[tok] = s
				}
			}

			continue
		}

		p.Tokens[key] = s
	}

	// Derive JSON/plan colors from token colors.
	p.JSON = JSONColors{Key: p.Tokens["property"].Color, String: p.Tokens["string"].Color, Number: p.Tokens["number"].Color, Literal: p.Tokens["keyword"].Color}

	name := strings.TrimSpace(vt.Name)
	if name == "" {
		name = "Imported Theme"
	}

	t := &Theme{Name: name, Appearance: Light}
	if dark {
		t.Appearance, t.Dark = Dark, p
	} else {
		t.Light = p
	}

	t.merge(Default())

	if err := t.Validate(); err != nil {
		return nil, err
	}

	return t, nil
}

// textMateTokens picks, for each bqls token type, the most specific
// tokenColors rule whose selector prefixes one of its scopes.
func textMateTokens(rules []vscodeTokenColor) map[string]Style {
	out := map[string]Style{}

	for token, scopes := range textMateScopes {
		bestRank, bestLen := len(scopes), -1

		for _, r := range rules {
			if r.Settings.Foreground == "" && r.Settings.FontStyle == "" {
				continue
			}

			for _, sel := range selectors(r.Scope) {
				// Prefer earlier (more specific) target scopes, then longer selectors.
				rank := matchRank(scopes, sel)
				if rank < bestRank || rank == bestRank && rank < len(scopes) && len(sel) > bestLen {
					bestRank, bestLen = rank, len(sel)
					out[token] = styleOf(r.Settings.Foreground, r.Settings.FontStyle)
				}
			}
		}
	}

	return out
}

// matchRank is the index of the first scope the selector matches (TextMate
// prefix semantics), or len(scopes) when none does.
func matchRank(scopes []string, sel string) int {
	for rank, scope := range scopes {
		if scope == sel || strings.HasPrefix(scope, sel+".") {
			return rank
		}
	}

	return len(scopes)
}

func selectors(raw json.RawMessage) []string {
	var list []string

	var one string
	if json.Unmarshal(raw, &one) == nil {
		list = strings.Split(one, ",")
	} else {
		_ = json.Unmarshal(raw, &list)
	}

	out := list[:0]

	for _, s := range list {
		// Descendant selectors ("meta.x keyword") only use their last part.
		if f := strings.Fields(s); len(f) > 0 {
			out = append(out, f[len(f)-1])
		}
	}

	return out
}

func semanticStyle(raw json.RawMessage) (Style, bool) {
	var fg string
	if json.Unmarshal(raw, &fg) == nil {
		return styleOf(fg, ""), color(fg) != ""
	}

	var obj struct {
		Foreground string `json:"foreground"`
		FontStyle  string `json:"fontStyle"`
		Bold       bool   `json:"bold"`
		Italic     bool   `json:"italic"`
		Underline  bool   `json:"underline"`
	}

	if json.Unmarshal(raw, &obj) != nil {
		return Style{}, false
	}

	s := styleOf(obj.Foreground, obj.FontStyle)
	s.Bold = s.Bold || obj.Bold
	s.Italic = s.Italic || obj.Italic
	s.Underline = s.Underline || obj.Underline

	return s, s.Color != "" || s.Bold || s.Italic || s.Underline
}

func styleOf(fg, fontStyle string) Style {
	return Style{
		Color:     color(fg),
		Bold:      strings.Contains(fontStyle, "bold"),
		Italic:    strings.Contains(fontStyle, "italic"),
		Underline: strings.Contains(fontStyle, "underline"),
	}
}

// color normalizes a VS Code color, dropping values bqui cannot parse.
func color(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	if _, err := ParseColor(c); err != nil {
		return ""
	}

	return c
}

// StripJSONC removes // and /* */ comments and trailing commas outside
// strings, turning JSON-with-comments into JSON.
func StripJSONC(in []byte) []byte {
	return dropTrailingCommas(dropComments(in))
}

// scanString copies the string literal starting at in[i] ('"') and returns
// the index of its closing quote.
func scanString(out, in []byte, i int) ([]byte, int) {
	j := i + 1
	for j < len(in) && in[j] != '"' {
		if in[j] == '\\' {
			j++
		}
		j++
	}

	end := min(j+1, len(in))

	return append(out, in[i:end]...), end - 1
}

func dropComments(in []byte) []byte {
	out := make([]byte, 0, len(in))

	for i := 0; i < len(in); i++ {
		switch c := in[i]; {
		case c == '"':
			out, i = scanString(out, in, i)
		case c == '/' && i+1 < len(in) && in[i+1] == '/':
			for i < len(in) && in[i] != '\n' {
				i++
			}

			if i < len(in) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(in) && in[i+1] == '*':
			i += 2
			for i+1 < len(in) && (in[i] != '*' || in[i+1] != '/') {
				i++
			}

			i++
			out = append(out, ' ')
		default:
			out = append(out, c)
		}
	}

	return out
}

func dropTrailingCommas(in []byte) []byte {
	out := make([]byte, 0, len(in))

	for i := 0; i < len(in); i++ {
		switch c := in[i]; c {
		case '"':
			out, i = scanString(out, in, i)
		case ',':
			j := i + 1
			for j < len(in) && strings.ContainsRune(" \t\r\n", rune(in[j])) {
				j++
			}

			if j < len(in) && (in[j] == '}' || in[j] == ']') {
				continue
			}

			out = append(out, c)
		default:
			out = append(out, c)
		}
	}

	return out
}
