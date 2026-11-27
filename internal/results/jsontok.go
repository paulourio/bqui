package results

// JSONKind classifies JSON text for syntax coloring.
type JSONKind int

// JSON token kinds.
const (
	JSONPunct JSONKind = iota // braces, brackets, colons, commas, spaces
	JSONKey
	JSONString
	JSONNumber
	JSONLiteral // true, false, null
)

// JSONSpan is a run of text of one kind: s[Start:End].
type JSONSpan struct {
	Kind       JSONKind
	Start, End int
}

// JSONSpans splits JSON text (possibly truncated) into colorable runs.
// Adjacent punctuation merges into one span; invalid input degrades to
// punctuation rather than failing.
func JSONSpans(s string) []JSONSpan {
	var out []JSONSpan

	for i := 0; i < len(s); {
		kind, end := jsonToken(s, i)

		if n := len(out); kind == JSONPunct && n > 0 && out[n-1].Kind == JSONPunct && out[n-1].End == i {
			out[n-1].End = end
		} else {
			out = append(out, JSONSpan{Kind: kind, Start: i, End: end})
		}

		i = end
	}

	return out
}

// jsonToken classifies the token starting at s[i] and returns its end.
func jsonToken(s string, i int) (JSONKind, int) {
	switch c := s[i]; {
	case c == '"':
		end := stringEnd(s, i)
		if k := skipSpace(s, end); k < len(s) && s[k] == ':' {
			return JSONKey, end
		}

		return JSONString, end
	case c == '-' || c >= '0' && c <= '9':
		j := i + 1
		for j < len(s) && isNumberByte(s[j]) {
			j++
		}

		return JSONNumber, j
	case c >= 'a' && c <= 'z':
		j := i + 1
		for j < len(s) && s[j] >= 'a' && s[j] <= 'z' {
			j++
		}

		switch s[i:j] {
		case "true", "false", "null":
			return JSONLiteral, j
		}

		return JSONPunct, j
	}

	return JSONPunct, i + 1
}

// stringEnd returns the index after the string literal starting at s[i]
// (or len(s) if it is truncated).
func stringEnd(s string, i int) int {
	j := i + 1
	for j < len(s) && s[j] != '"' {
		if s[j] == '\\' {
			j++
		}
		j++
	}

	return min(j+1, len(s))
}

func skipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}

	return i
}

func isNumberByte(c byte) bool {
	return c >= '0' && c <= '9' || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-'
}
