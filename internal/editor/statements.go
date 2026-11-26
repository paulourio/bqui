package editor

import "github.com/paulourio/bqlang/lsp"

// StatementAt picks the statement containing the cursor (0-based UTF-16
// line/character). When the cursor is between statements (e.g. after a
// trailing semicolon) it picks the nearest statement that starts before it,
// and before the first statement it picks the first one.
func StatementAt(sts []lsp.StatementInfo, line, char int) (lsp.StatementInfo, bool) {
	if len(sts) == 0 {
		return lsp.StatementInfo{}, false
	}

	before := func(a, b lsp.Position) bool {
		return a.Line < b.Line || (a.Line == b.Line && a.Character <= b.Character)
	}

	cur := lsp.Position{Line: line, Character: char}
	best := sts[0]

	for _, s := range sts {
		if !before(s.Range.Start, cur) {
			break
		}

		best = s

		if before(cur, s.Range.End) {
			return s, true
		}
	}

	return best, true
}
