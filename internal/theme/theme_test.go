package theme

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinsAreCompleteAndValid(t *testing.T) {
	names := map[string]bool{}

	for _, th := range Builtins() {
		if err := th.Validate(); err != nil {
			t.Fatal(err)
		}

		if names[th.Name] {
			t.Errorf("duplicate theme %s", th.Name)
		}

		names[th.Name] = true

		for _, dark := range []bool{false, true} {
			p := th.Palette(dark)
			for _, tok := range TokenTypes {
				if p.Tokens[tok].Color == "" {
					t.Errorf("%s (dark=%v): token %s has no color", th.Name, dark, tok)
				}
			}

			if p.Editor.Background == "" || p.JSON.Key == "" || p.Plan.Hot == "" {
				t.Errorf("%s (dark=%v): missing colors %+v", th.Name, dark, p)
			}
		}
	}
}

func TestParseColor(t *testing.T) {
	c, err := ParseColor("#ff800080")
	if err != nil || c.R != 1 || c.B != 0 || c.A < 0.5 || c.A > 0.51 {
		t.Fatalf("color = %+v %v", c, err)
	}

	if c, _ := ParseColor("#fff"); c.G != 1 || c.A != 1 {
		t.Errorf("short color = %+v", c)
	}

	for _, bad := range []string{"red", "ff0000", "#ff00", "#gggggg"} {
		if _, err := ParseColor(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestSaveLoadAndList(t *testing.T) {
	dir := t.TempDir()

	// A partial user theme inherits everything else from Default.
	partial := "name = \"Mine\"\nappearance = \"dark\"\n[dark.tokens]\nkeyword = { color = \"#ff0000\", bold = true }\n"
	if err := os.WriteFile(filepath.Join(dir, "mine.toml"), []byte(partial), 0o600); err != nil {
		t.Fatal(err)
	}

	_ = os.WriteFile(filepath.Join(dir, "broken.toml"), []byte("name = \"B\"\n[light.editor]\nbackground = \"blue\"\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o600)

	themes, errs := List(dir)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "background") {
		t.Errorf("errs = %v", errs)
	}

	mine := Find(themes, "mine")
	if mine == nil || len(themes) != len(Builtins())+1 {
		t.Fatalf("themes = %d, mine = %v", len(themes), mine)
	}

	kw := mine.Dark.Tokens["keyword"]
	if kw.Color != "#ff0000" || !kw.Bold || mine.Dark.Tokens["string"].Color != Default().Dark.Tokens["string"].Color {
		t.Errorf("merged tokens = %+v", mine.Dark.Tokens)
	}

	if mine.Fonts.EditorSize != 13 || mine.Light.Editor.Background != "#ffffff" {
		t.Errorf("inherited = %+v", mine)
	}

	// Round trip through Save.
	path := filepath.Join(dir, FileName("Mine Again!"))
	mine.Name = "Mine Again!"

	if err := Save(mine, path); err != nil {
		t.Fatal(err)
	}

	again, err := Load(path)
	if err != nil || again.Dark.Tokens["keyword"] != kw || filepath.Base(path) != "mine-again.toml" {
		t.Fatalf("reloaded = %+v %v", again, err)
	}
}

func TestMonacoCompile(t *testing.T) {
	mt := Monaco(Default().Dark, true)
	if mt.Base != "vs-dark" || mt.Colors["editor.background"] != "#1e1e1e" {
		t.Fatalf("monaco = %+v", mt)
	}

	rules := map[string]MonacoRule{}
	for _, r := range mt.Rules {
		rules[r.Token] = r
	}

	if r := rules["property.partitioned"]; r.Foreground != "9cdcfe" || r.FontStyle != "underline" {
		t.Errorf("partitioned = %+v", r)
	}

	if rules["keyword.sql"].Foreground != "569cd6" || rules[""].Foreground != "d4d4d4" {
		t.Errorf("aliases/default = %+v / %+v", rules["keyword.sql"], rules[""])
	}

	if _, err := json.Marshal(mt); err != nil {
		t.Fatal(err)
	}
}

const vscodeSample = `{
	// A comment, and trailing commas below.
	"name": "Sample Dark",
	"type": "dark",
	"colors": {
		"editor.background": "#101010",
		"editor.foreground": "#EEEEEE", /* inline */
		"editor.selectionBackground": "#334455",
	},
	"tokenColors": [
		{ "scope": "keyword", "settings": { "foreground": "#ff0000" } },
		{ "scope": "keyword.control", "settings": { "foreground": "#00ff00", "fontStyle": "bold" } },
		{ "scope": ["string", "string.quoted.single"], "settings": { "foreground": "#aaaa00" } },
		{ "scope": "comment, punctuation.definition.comment", "settings": { "foreground": "#777777", "fontStyle": "italic" } },
		{ "scope": "source.sql constant.numeric", "settings": { "foreground": "#00aaff" } },
		{ "scope": "entity.name.function", "settings": { "foreground": "#ffff00" } },
		{ "scope": "url", "settings": { "foreground": "http://not-a-color" } },
	],
	"semanticTokenColors": {
		"property.partitioned": { "foreground": "#123456", "underline": true },
		"*.pseudo": "#abcdef",
	},
}`

func TestImportVSCode(t *testing.T) {
	th, err := ImportVSCode([]byte(vscodeSample))
	if err != nil {
		t.Fatal(err)
	}

	d := th.Dark
	checks := map[string]string{
		"name": th.Name, "appearance": string(th.Appearance),
		"bg": d.Editor.Background, "fg": d.Editor.Foreground,
		"keyword": d.Tokens["keyword"].Color, "string": d.Tokens["string"].Color,
		"comment": d.Tokens["comment"].Color, "number": d.Tokens["number"].Color,
		"function": d.Tokens["function"].Color, "partitioned": d.Tokens["property.partitioned"].Color,
		"pseudo": d.Tokens["property.pseudo"].Color, "vpseudo": d.Tokens["variable.pseudo"].Color, "json.string": d.JSON.String,
		"light.bg": th.Light.Editor.Background,
	}
	want := map[string]string{
		"name": "Sample Dark", "appearance": "dark", "bg": "#101010", "fg": "#eeeeee",
		// SQL keywords are keyword.other.sql: "keyword" matches, keyword.control does not.
		"keyword": "#ff0000",
		"string":  "#aaaa00", "comment": "#777777", "number": "#00aaff", "function": "#ffff00",
		"partitioned": "#123456", "pseudo": "#abcdef", "vpseudo": "#abcdef", "json.string": "#aaaa00",
		"light.bg": "#ffffff", // the other appearance keeps Default
	}

	for k, w := range want {
		if checks[k] != w {
			t.Errorf("%s = %q, want %q", k, checks[k], w)
		}
	}

	if d.Tokens["keyword"].Bold || !d.Tokens["comment"].Italic || !d.Tokens["property.partitioned"].Underline {
		t.Errorf("styles = %+v", d.Tokens)
	}

	if _, ok := d.Tokens["pseudo"]; ok {
		t.Error(`"*.pseudo" created a "pseudo" token type`)
	}

	if _, err := ImportVSCode([]byte("{not json")); err == nil {
		t.Error("bad JSON imported")
	}
}

func TestStripJSONCKeepsStrings(t *testing.T) {
	in := `{"a": "x // not a comment, ]", "b": [1, 2,], /* c */ "d": "\"/*"}`

	var v map[string]any
	if err := json.Unmarshal(StripJSONC([]byte(in)), &v); err != nil {
		t.Fatal(err)
	}

	if v["a"] != "x // not a comment, ]" || v["d"] != `"/*` || len(v["b"].([]any)) != 2 {
		t.Errorf("parsed = %v", v)
	}
}
