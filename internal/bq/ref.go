package bq

import (
	"errors"
	"fmt"
	"strings"
)

// ErrBadRef is returned for malformed table references.
var ErrBadRef = errors.New("bq: invalid table reference")

// TableRef identifies a table, view or other table-like resource.
type TableRef struct {
	Project string `json:"projectId"`
	Dataset string `json:"datasetId"`
	Table   string `json:"tableId"`
}

// String returns project.dataset.table, backtick-quoted parts left unquoted.
func (r TableRef) String() string {
	return r.Project + "." + r.Dataset + "." + r.Table
}

// SQL returns the reference quoted for use in GoogleSQL.
func (r TableRef) SQL() string {
	return "`" + r.String() + "`"
}

// ParseTableRef parses "project.dataset.table", "project:dataset.table",
// "dataset.table" (with defaultProject), and backtick-quoted forms. Domain-
// scoped projects ("example.com:proj") must use the full three-part form.
func ParseTableRef(s, defaultProject string) (TableRef, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "`", ""))
	if s == "" {
		return TableRef{}, fmt.Errorf("%w: empty", ErrBadRef)
	}

	// Legacy "project:dataset.table" form.
	if i := strings.LastIndex(s, ":"); i >= 0 {
		proj, rest := s[:i], s[i+1:]

		parts := strings.Split(rest, ".")
		if len(parts) != 2 {
			return TableRef{}, fmt.Errorf("%w: %q", ErrBadRef, s)
		}

		// "example.com:proj.dataset.table" is not legal here; "example.com:proj:..." neither.
		return validRef(TableRef{Project: proj, Dataset: parts[0], Table: parts[1]}, s)
	}

	parts := strings.Split(s, ".")
	switch len(parts) {
	case 2:
		if defaultProject == "" {
			return TableRef{}, fmt.Errorf("%w: %q needs a project", ErrBadRef, s)
		}

		return validRef(TableRef{Project: defaultProject, Dataset: parts[0], Table: parts[1]}, s)
	case 3:
		return validRef(TableRef{Project: parts[0], Dataset: parts[1], Table: parts[2]}, s)
	default:
		return TableRef{}, fmt.Errorf("%w: %q", ErrBadRef, s)
	}
}

func validRef(r TableRef, raw string) (TableRef, error) {
	if r.Project == "" || r.Dataset == "" || r.Table == "" {
		return TableRef{}, fmt.Errorf("%w: %q", ErrBadRef, raw)
	}

	return r, nil
}

// DatasetRef identifies a dataset.
type DatasetRef struct {
	Project string `json:"projectId"`
	Dataset string `json:"datasetId"`
}

// ParseDatasetRef parses "project.dataset", "project:dataset" or "dataset".
func ParseDatasetRef(s, defaultProject string) (DatasetRef, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "`", ""))

	var r DatasetRef

	switch {
	case strings.Contains(s, ":"):
		i := strings.LastIndex(s, ":")
		r = DatasetRef{Project: s[:i], Dataset: s[i+1:]}
	case strings.Count(s, ".") == 1:
		p, d, _ := strings.Cut(s, ".")
		r = DatasetRef{Project: p, Dataset: d}
	case !strings.Contains(s, "."):
		r = DatasetRef{Project: defaultProject, Dataset: s}
	}

	if r.Project == "" || r.Dataset == "" || strings.Contains(r.Dataset, ".") {
		return DatasetRef{}, fmt.Errorf("%w: dataset %q", ErrBadRef, s)
	}

	return r, nil
}
