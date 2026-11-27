package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDocCostKeepsEstimateWithErrors(t *testing.T) {
	// bqls replaces the summary with "Invalid SQL" when a statement has an
	// error, but its code lenses still show both statements' estimates.
	raw := `{"summary":"Invalid SQL (Ln 3)","status":"error","formattedBytes":"2.00 MiB","formattedCost":"<$0.01",
		"errorCount":1,"errors":[{"line":3,"column":8,"source":"semantic","message":"Unrecognized name: wrod [at 1:8]"}],
		"statements":[{"label":"Est. 1.00 MiB (<$0.01)","source":"dry_run"},{"label":"Est. ≤ 1.00 MiB (<$0.01, offline)","source":"offline"}]}`

	var c DocCost
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}

	if got := c.Estimate(); got != "Est. ≤ 2.00 MiB (<$0.01, offline)" {
		t.Errorf("estimate = %q", got)
	}

	if !c.Errored() || c.ErrorBadge() != "1 error" || c.ErrorTitle() != "1 error in this query" {
		t.Errorf("errors: %v %q %q", c.Errored(), c.ErrorBadge(), c.ErrorTitle())
	}

	if detail, more := c.ErrorDetail(); detail != "Line 3, column 8: Unrecognized name: wrod" || more != "" {
		t.Errorf("detail = %q more %q", detail, more)
	}
}

func TestDocCostSummaries(t *testing.T) {
	one := DocCost{Summary: "Est. 5 B", Statements: []CostStatement{{Label: "Est. 5 B ($0.00)", Source: "dry_run"}}}
	if one.Estimate() != "Est. 5 B ($0.00)" || one.Errored() || one.ErrorBadge() != "" {
		t.Errorf("one = %q %v", one.Estimate(), one.Errored())
	}

	dry := DocCost{
		FormattedBytes: "3 KiB", FormattedCost: "$0.00", ExceedsThreshold: true,
		Statements: []CostStatement{{Source: "dry_run"}, {Source: "dry_run"}},
	}
	if dry.Estimate() != "⚠ Est. 3 KiB ($0.00)" {
		t.Errorf("dry = %q", dry.Estimate())
	}

	// Nothing to estimate: no estimate, only the error marker.
	bad := DocCost{Summary: "Invalid SQL", ErrorCount: 7}
	for i := range 7 {
		bad.Errors = append(bad.Errors, CostError{Line: i + 1, Message: "Syntax error"})
	}

	if bad.Estimate() != "" || bad.ErrorBadge() != "7 errors" || bad.ErrorTitle() != "7 errors in this query" {
		t.Errorf("bad = %q %q %q", bad.Estimate(), bad.ErrorBadge(), bad.ErrorTitle())
	}

	detail, more := bad.ErrorDetail()
	if strings.Count(detail, "\n") != maxListedErrors-1 || !strings.HasPrefix(detail, "Line 1: Syntax error") || more != "and 2 more" {
		t.Errorf("detail = %q more %q", detail, more)
	}

	if (DocCost{Summary: "—"}).Estimate() != "—" {
		t.Error("empty document summary not kept")
	}
}
