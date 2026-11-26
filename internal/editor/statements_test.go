package editor

import (
	"testing"

	"github.com/paulourio/bqlang/lsp"
)

func stmt(text string, sl, sc, el, ec int) lsp.StatementInfo {
	return lsp.StatementInfo{
		Text:  text,
		Range: lsp.Range{Start: lsp.Position{Line: sl, Character: sc}, End: lsp.Position{Line: el, Character: ec}},
	}
}

func TestStatementAt(t *testing.T) {
	// "SELECT 1;\n\nSELECT 2; SELECT 3"
	sts := []lsp.StatementInfo{
		stmt("SELECT 1", 0, 0, 0, 8),
		stmt("SELECT 2", 2, 0, 2, 8),
		stmt("SELECT 3", 2, 10, 2, 18),
	}

	tests := []struct {
		line, char int
		want       string
	}{
		{0, 0, "SELECT 1"},
		{0, 4, "SELECT 1"},
		{0, 9, "SELECT 1"},  // after the semicolon
		{1, 0, "SELECT 1"},  // blank line between statements
		{2, 3, "SELECT 2"},  //
		{2, 9, "SELECT 2"},  // on "; "
		{2, 12, "SELECT 3"}, //
		{9, 0, "SELECT 3"},  // past the end
	}

	for _, tt := range tests {
		got, ok := StatementAt(sts, tt.line, tt.char)
		if !ok || got.Text != tt.want {
			t.Errorf("StatementAt(%d:%d) = %q, want %q", tt.line, tt.char, got.Text, tt.want)
		}
	}

	if _, ok := StatementAt(nil, 0, 0); ok {
		t.Error("empty input should report not found")
	}

	// Leading whitespace before the first statement picks the first one.
	if got, _ := StatementAt([]lsp.StatementInfo{stmt("SELECT 1", 3, 2, 3, 10)}, 0, 0); got.Text != "SELECT 1" {
		t.Errorf("leading cursor = %q", got.Text)
	}
}
