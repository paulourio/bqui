package theme

// DefaultName is the theme used when none is chosen.
const DefaultName = "Default"

// Default is bqui's own theme (the colors the editor shipped with).
func Default() *Theme {
	return &Theme{
		Name:       DefaultName,
		Appearance: System,
		Fonts:      Fonts{EditorSize: 13, GridSize: 12, Ligatures: new(true)},
		Light: Palette{
			Editor: EditorColors{
				Background: "#ffffff", Foreground: "#1f1f1f", Selection: "#add6ff",
				LineHighlight: "#f3f3f3", Cursor: "#000000", LineNumber: "#9e9e9e",
			},
			Tokens: map[string]Style{
				"keyword": {Color: "#0000ff"}, "namespace": {Color: "#267f99"}, "class": {Color: "#267f99"},
				"type": {Color: "#267f99"}, "function": {Color: "#795e26"}, "function.defaultLibrary": {Color: "#795e26"},
				"variable": {Color: "#001080"}, "property": {Color: "#001080"}, "parameter": {Color: "#001080", Italic: true},
				"property.partitioned": {Color: "#001080", Underline: true}, "property.clustered": {Color: "#001080", Italic: true},
				"property.pseudo": {Color: "#af00db"}, "variable.pseudo": {Color: "#af00db"},
				"string": {Color: "#a31515"}, "number": {Color: "#098658"}, "comment": {Color: "#008000", Italic: true},
				"macro": {Color: "#af00db"}, "enumMember": {Color: "#0070c1"},
			},
			Grid: GridColors{Null: "#8e8e93", Number: ""},
			JSON: JSONColors{Key: "#0451a5", String: "#a31515", Number: "#098658", Literal: "#0000ff"},
			Plan: PlanColors{Hot: "#f27326"},
		},
		Dark: Palette{
			Editor: EditorColors{
				Background: "#1e1e1e", Foreground: "#d4d4d4", Selection: "#264f78",
				LineHighlight: "#2a2a2a", Cursor: "#aeafad", LineNumber: "#858585",
			},
			Tokens: map[string]Style{
				"keyword": {Color: "#569cd6"}, "namespace": {Color: "#4ec9b0"}, "class": {Color: "#4ec9b0"},
				"type": {Color: "#4ec9b0"}, "function": {Color: "#dcdcaa"}, "function.defaultLibrary": {Color: "#dcdcaa"},
				"variable": {Color: "#9cdcfe"}, "property": {Color: "#9cdcfe"}, "parameter": {Color: "#9cdcfe", Italic: true},
				"property.partitioned": {Color: "#9cdcfe", Underline: true}, "property.clustered": {Color: "#9cdcfe", Italic: true},
				"property.pseudo": {Color: "#c586c0"}, "variable.pseudo": {Color: "#c586c0"},
				"string": {Color: "#ce9178"}, "number": {Color: "#b5cea8"}, "comment": {Color: "#6a9955", Italic: true},
				"macro": {Color: "#c586c0"}, "enumMember": {Color: "#4fc1ff"},
			},
			Grid: GridColors{Null: "#8e8e93"},
			JSON: JSONColors{Key: "#9cdcfe", String: "#ce9178", Number: "#b5cea8", Literal: "#569cd6"},
			Plan: PlanColors{Hot: "#f27326"},
		},
	}
}

// paper is a warm, low-contrast theme.
func paper() *Theme {
	t := &Theme{
		Name: "Paper",
		Light: Palette{
			Editor: EditorColors{Background: "#fbf7ef", Foreground: "#3b3a36", Selection: "#e8dcc0", LineHighlight: "#f3ecdd", Cursor: "#6b4f2a", LineNumber: "#b3aa98"},
			Tokens: map[string]Style{
				"keyword": {Color: "#8f3f71", Bold: true}, "type": {Color: "#4a6d7c"}, "namespace": {Color: "#4a6d7c"}, "class": {Color: "#4a6d7c"},
				"function": {Color: "#7a5a12"}, "function.defaultLibrary": {Color: "#7a5a12"},
				"variable": {Color: "#3b3a36"}, "property": {Color: "#2f5d50"}, "parameter": {Color: "#2f5d50", Italic: true},
				"property.partitioned": {Color: "#2f5d50", Underline: true}, "property.clustered": {Color: "#2f5d50", Italic: true},
				"string": {Color: "#9a4b2a"}, "number": {Color: "#5a6e1f"}, "comment": {Color: "#9c9483", Italic: true},
			},
			JSON: JSONColors{Key: "#2f5d50", String: "#9a4b2a", Number: "#5a6e1f", Literal: "#8f3f71"},
			Plan: PlanColors{Hot: "#c0562b"},
		},
		Dark: Palette{
			Editor: EditorColors{Background: "#2b2925", Foreground: "#e6dfcf", Selection: "#4a4335", LineHighlight: "#34312b", Cursor: "#e0c48a", LineNumber: "#7d7566"},
			Tokens: map[string]Style{
				"keyword": {Color: "#d6a0c4", Bold: true}, "type": {Color: "#8fb7c6"}, "namespace": {Color: "#8fb7c6"}, "class": {Color: "#8fb7c6"},
				"function": {Color: "#e0c48a"}, "function.defaultLibrary": {Color: "#e0c48a"},
				"variable": {Color: "#e6dfcf"}, "property": {Color: "#9fcfb8"}, "parameter": {Color: "#9fcfb8", Italic: true},
				"property.partitioned": {Color: "#9fcfb8", Underline: true}, "property.clustered": {Color: "#9fcfb8", Italic: true},
				"string": {Color: "#e39a76"}, "number": {Color: "#b8cc7a"}, "comment": {Color: "#8c8473", Italic: true},
			},
			JSON: JSONColors{Key: "#9fcfb8", String: "#e39a76", Number: "#b8cc7a", Literal: "#d6a0c4"},
			Plan: PlanColors{Hot: "#e0864f"},
		},
	}
	t.merge(Default())

	return t
}

// midnight is a dark-only, blue-black theme.
func midnight() *Theme {
	dark := Palette{
		Editor: EditorColors{Background: "#0f1420", Foreground: "#c8d3f5", Selection: "#2d3f76", LineHighlight: "#18203a", Cursor: "#82aaff", LineNumber: "#4b5a86"},
		Tokens: map[string]Style{
			"keyword": {Color: "#c099ff"}, "type": {Color: "#65bcff"}, "namespace": {Color: "#65bcff"}, "class": {Color: "#65bcff"},
			"function": {Color: "#82aaff"}, "function.defaultLibrary": {Color: "#82aaff"},
			"variable": {Color: "#c8d3f5"}, "property": {Color: "#86e1fc"}, "parameter": {Color: "#ffc777", Italic: true},
			"property.partitioned": {Color: "#86e1fc", Underline: true}, "property.clustered": {Color: "#86e1fc", Italic: true},
			"string": {Color: "#c3e88d"}, "number": {Color: "#ff966c"}, "comment": {Color: "#636da6", Italic: true},
		},
		JSON: JSONColors{Key: "#86e1fc", String: "#c3e88d", Number: "#ff966c", Literal: "#c099ff"},
		Plan: PlanColors{Hot: "#ff757f"},
	}
	t := &Theme{Name: "Midnight", Appearance: Dark, Light: dark, Dark: dark}
	t.merge(Default())

	return t
}

// highContrast maximizes legibility in both appearances.
func highContrast() *Theme {
	t := &Theme{
		Name: "High Contrast",
		Light: Palette{
			Editor: EditorColors{Background: "#ffffff", Foreground: "#000000", Selection: "#0f4a85", LineHighlight: "#eeeeee", Cursor: "#000000", LineNumber: "#292929"},
			Tokens: map[string]Style{
				"keyword": {Color: "#0f4a85", Bold: true}, "type": {Color: "#185e73"}, "function": {Color: "#5e2cbc"},
				"property": {Color: "#001080"}, "variable": {Color: "#001080"}, "string": {Color: "#a31515"},
				"number": {Color: "#096d48"}, "comment": {Color: "#515151", Italic: true},
			},
			Grid: GridColors{Null: "#515151"},
			JSON: JSONColors{Key: "#001080", String: "#a31515", Number: "#096d48", Literal: "#0f4a85"},
			Plan: PlanColors{Hot: "#b5200d"},
		},
		Dark: Palette{
			Editor: EditorColors{Background: "#000000", Foreground: "#ffffff", Selection: "#264f78", LineHighlight: "#1a1a1a", Cursor: "#ffffff", LineNumber: "#ffffff"},
			Tokens: map[string]Style{
				"keyword": {Color: "#569cd6", Bold: true}, "type": {Color: "#4ec9b0"}, "function": {Color: "#dcdcaa"},
				"property": {Color: "#9cdcfe"}, "variable": {Color: "#9cdcfe"}, "string": {Color: "#ce9178"},
				"number": {Color: "#b5cea8"}, "comment": {Color: "#7ca668", Italic: true},
			},
			Grid: GridColors{Null: "#bdbdbd"},
			JSON: JSONColors{Key: "#9cdcfe", String: "#ce9178", Number: "#b5cea8", Literal: "#569cd6"},
			Plan: PlanColors{Hot: "#ff6b4a"},
		},
	}
	t.merge(Default())

	return t
}

// Builtins returns the themes that ship with bqui.
func Builtins() []*Theme {
	return []*Theme{Default(), paper(), midnight(), highContrast()}
}
