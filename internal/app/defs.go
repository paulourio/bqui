package app

import (
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/paulourio/bqui/internal/bq"
)

// ObjectAt is a BigQuery object reference found in SQL text.
type ObjectAt struct {
	Ref bq.TableRef
	// Start and End are the reference's UTF-16 columns on its line (LSP and
	// Monaco positions).
	Line, Start, End int
}

// tableKeywords precede a table name ("FROM x", "JOIN x", ...).
var tableKeywords = map[string]bool{
	"FROM": true, "JOIN": true, "UPDATE": true, "INTO": true, "TABLE": true,
	"MERGE": true, "USING": true, "VIEW": true, "TRUNCATE": true,
}

// ObjectRefAt finds the table, view, routine or model referenced at a
// position (0-based line, UTF-16 character). dataset.table resolves in
// defProject (the billing project); a bare name in defDataset
// ("project.dataset"). It accepts backticked names and project.dataset.table
// anywhere, and dataset.table or a bare name only after FROM, JOIN and
// similar keywords, so column references such as t.col are not mistaken
// for tables. Bare names that are CTEs are skipped.
func ObjectRefAt(text string, line, character int, defProject, defDataset string) (ObjectAt, bool) {
	lines := strings.Split(text, "\n")
	if line < 0 || line >= len(lines) {
		return ObjectAt{}, false
	}

	src := []rune(lines[line])
	col := runeIndex(src, character)

	start, end, quoted, ok := nameSpan(src, col)
	if !ok {
		return ObjectAt{}, false
	}

	raw := string(src[start:end])
	parts := splitName(raw)

	if !quoted && len(parts) < 3 && !afterTableKeyword(lines, line, start) {
		return ObjectAt{}, false
	}

	var ref bq.TableRef

	switch len(parts) {
	case 3:
		ref = bq.TableRef{Project: parts[0], Dataset: parts[1], Table: parts[2]}
	case 2:
		ref = bq.TableRef{Project: defProject, Dataset: parts[0], Table: parts[1]}
	case 1:
		project, dataset, ok := strings.Cut(defDataset, ".")
		if !ok || isCTE(text, parts[0]) {
			return ObjectAt{}, false
		}

		ref = bq.TableRef{Project: project, Dataset: dataset, Table: parts[0]}
	default:
		return ObjectAt{}, false
	}

	if ref.Project == "" || ref.Dataset == "" || ref.Table == "" {
		return ObjectAt{}, false
	}

	return ObjectAt{Ref: ref, Line: line, Start: utf16Len(src[:start]), End: utf16Len(src[:end])}, true
}

// nameSpan finds the (possibly backticked, dotted) name around col.
func nameSpan(src []rune, col int) (start, end int, quoted, ok bool) {
	isName := func(r rune) bool {
		return r == '_' || r == '-' || r == '.' || r == '`' || unicode.IsLetter(r) || unicode.IsDigit(r)
	}

	if col > len(src) {
		return 0, 0, false, false
	}

	// Allow the cursor right after the name.
	if col == len(src) || !isName(src[col]) {
		if col == 0 || !isName(src[col-1]) {
			return 0, 0, false, false
		}

		col--
	}

	start, end = col, col+1
	for start > 0 && isName(src[start-1]) {
		start--
	}

	for end < len(src) && isName(src[end]) {
		end++
	}

	// Trim stray dots (a trailing "." while typing, "x.").
	for start < end && src[start] == '.' {
		start++
	}

	for end > start && src[end-1] == '.' {
		end--
	}

	name := string(src[start:end])

	return start, end, strings.Contains(name, "`"), name != "" && name != "`"
}

// splitName splits `a.b`.c / `a`.`b`.`c` / a.b.c into parts.
func splitName(raw string) []string {
	var parts []string

	for p := range strings.SplitSeq(strings.ReplaceAll(raw, "`", ""), ".") {
		if p != "" {
			parts = append(parts, p)
		}
	}

	return parts
}

// afterTableKeyword reports whether the previous word before the name is a
// table keyword (looking back across lines).
func afterTableKeyword(lines []string, line, start int) bool {
	before := string([]rune(lines[line])[:start])

	for l := line; ; l-- {
		fields := strings.FieldsFunc(before, func(r rune) bool {
			return r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})

		if len(fields) > 0 {
			return tableKeywords[strings.ToUpper(fields[len(fields)-1])]
		}

		if l == 0 {
			return false
		}

		before = lines[l-1]
	}
}

// isCTE reports whether name is defined as a CTE ("name AS (").
func isCTE(text, name string) bool {
	up := strings.ToUpper(text)
	n := strings.ToUpper(name)

	for i := strings.Index(up, n); i >= 0; {
		rest := strings.TrimLeft(up[i+len(n):], " \t\r\n")
		if strings.HasPrefix(rest, "AS") {
			if strings.HasPrefix(strings.TrimLeft(rest[2:], " \t\r\n"), "(") {
				return true
			}
		}

		next := strings.Index(up[i+1:], n)
		if next < 0 {
			break
		}

		i += next + 1
	}

	return false
}

func runeIndex(src []rune, utf16Col int) int {
	n := 0

	for i, r := range src {
		if n >= utf16Col {
			return i
		}

		n += len(utf16.Encode([]rune{r}))
	}

	return len(src)
}

func utf16Len(rs []rune) int { return len(utf16.Encode(rs)) }
