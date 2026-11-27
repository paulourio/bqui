//go:build darwin

package smoke

import (
	"encoding/json"
	"strings"

	"github.com/paulourio/bqui/internal/ui"
)

// copyResult captures the results grid's JSON copy items.
type copyResult struct {
	RowJSON, NDJSON   string
	HasRow, HasNDJSON bool
}

// copyFlow reads what the results menu copies for the first rows of the
// current result.
func copyFlow(s *ui.Shell) copyResult {
	var r copyResult

	onMain(func() {
		r.RowJSON, r.HasRow = s.Results.CopyText("Copy Row as JSON", 0, []int{0})
		r.NDJSON, r.HasNDJSON = s.Results.CopyText("Copy Rows as NDJSON", 0, []int{0, 1})
	})

	return r
}

func copyChecks(rep *Report, r copyResult) {
	var obj map[string]any

	indented := json.Unmarshal([]byte(r.RowJSON), &obj) == nil && strings.Contains(r.RowJSON, "\n  \"")
	rep.check("results-copy-row-json", r.HasRow && indented, "menu item %v, indented object %v: %.60q", r.HasRow, indented, r.RowJSON)

	lines := strings.Split(strings.TrimSuffix(r.NDJSON, "\n"), "\n")
	compact := len(lines) == 2

	for _, l := range lines {
		compact = compact && json.Valid([]byte(l)) && !strings.Contains(l, "\n")
	}

	rep.check("results-copy-ndjson", r.HasNDJSON && compact, "menu item %v, %d compact lines: %.60q", r.HasNDJSON, len(lines), r.NDJSON)
}
