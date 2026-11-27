package app

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// DocCost is bqls' bqls/queryCost notification for the open document: the
// per-statement estimates (the same ones its code lenses show) and the
// local SQL errors it found.
type DocCost struct {
	Summary          string          `json:"summary"`
	Status           string          `json:"status"`
	FormattedBytes   string          `json:"formattedBytes"`
	FormattedCost    string          `json:"formattedCost"`
	Source           string          `json:"source"`
	ExceedsThreshold bool            `json:"exceedsThreshold"`
	ErrorCount       int             `json:"errorCount"`
	Errors           []CostError     `json:"errors"`
	Statements       []CostStatement `json:"statements"`
}

// CostError is one local SQL error (1-based line and column; 0 = unknown).
type CostError struct {
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Source  string `json:"source"`
	Message string `json:"message"`
}

// CostStatement is one statement's estimate.
type CostStatement struct {
	Label  string `json:"label"`
	Source string `json:"source"`
}

// costSourceDryRun marks estimates BigQuery computed (not offline bounds).
const costSourceDryRun = "dry_run"

// statementPos is the "[at 1:1]" suffix bqls' parser adds: a position
// within the statement, which contradicts the document line shown first.
var statementPos = regexp.MustCompile(`\s*\[at \d+:\d+\]$`)

// maxListedErrors caps the errors spelled out in ErrorDetail.
const maxListedErrors = 5

// Estimate is the document's total estimate, built like bqls builds its
// summary but kept when some statement has an error: bqls then replaces
// the summary with "Invalid SQL" while its code lenses still show each
// statement's estimate, so the toolbar and the editor disagreed.
func (c DocCost) Estimate() string {
	switch len(c.Statements) {
	case 0:
		if c.ErrorCount > 0 {
			return ""
		}

		return c.Summary
	case 1:
		return c.Statements[0].Label
	}

	prefix := "Est."
	if c.ExceedsThreshold {
		prefix = "⚠ Est."
	}

	for _, st := range c.Statements {
		if st.Source != costSourceDryRun {
			return fmt.Sprintf("%s ≤ %s (%s, offline)", prefix, c.FormattedBytes, c.FormattedCost)
		}
	}

	return fmt.Sprintf("%s %s (%s)", prefix, c.FormattedBytes, c.FormattedCost)
}

// Errored reports whether bqls found SQL errors.
func (c DocCost) Errored() bool { return c.ErrorCount > 0 || len(c.Errors) > 0 }

func (c DocCost) errorCount() int { return max(c.ErrorCount, len(c.Errors)) }

// ErrorBadge is the toolbar's short error marker ("1 error", "3 errors").
func (c DocCost) ErrorBadge() string {
	switch n := c.errorCount(); n {
	case 0:
		return ""
	case 1:
		return "1 error"
	default:
		return strconv.Itoa(n) + " errors"
	}
}

// ErrorTitle heads the error bar under the editor.
func (c DocCost) ErrorTitle() string {
	if n := c.errorCount(); n > 1 {
		return "Invalid SQL: " + strconv.Itoa(n) + " errors"
	}

	return "Invalid SQL"
}

// ErrorDetail lists the errors, one per line with their position, and how
// many more were left out.
func (c DocCost) ErrorDetail() (detail, more string) {
	lines := make([]string, 0, min(len(c.Errors), maxListedErrors))

	for i, e := range c.Errors {
		if i == maxListedErrors {
			break
		}

		lines = append(lines, e.Position()+statementPos.ReplaceAllString(e.Message, ""))
	}

	if extra := c.errorCount() - len(lines); extra > 0 {
		more = "and " + strconv.Itoa(extra) + " more"
	}

	return strings.Join(lines, "\n"), more
}

// Position is "Line 2, column 5: " ("" when unknown).
func (e CostError) Position() string {
	switch {
	case e.Line > 0 && e.Column > 0:
		return "Line " + strconv.Itoa(e.Line) + ", column " + strconv.Itoa(e.Column) + ": "
	case e.Line > 0:
		return "Line " + strconv.Itoa(e.Line) + ": "
	}

	return ""
}
