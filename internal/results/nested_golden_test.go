package results

import (
	"encoding/json"
	"strings"
	"testing"
)

// githubNestedFields models bigquery-public-data.samples.github_nested with
// deep STRUCTs (actor.attributes, repository, payload.pull_request.head.repo)
// and REPEATED RECORDs (payload.pages, payload.shas) plus >100 leaf columns.
func githubNestedFields() []Field {
	extra := make([]Field, 0, 95)
	for i := range 95 {
		extra = append(extra, Field{Name: "attr_" + string(rune('a'+i%26)) + "_" + string(rune('0'+i/26)), Type: TypeString})
	}

	repoFields := []Field{
		{Name: "id", Type: TypeInt64},
		{Name: "name", Type: TypeString},
		{Name: "url", Type: TypeString},
		{Name: "owner", Type: TypeString},
		{Name: "fork", Type: TypeBool},
		{Name: "stargazers", Type: TypeInt64},
	}

	return []Field{
		{Name: "type", Type: TypeString},
		{Name: "public", Type: TypeBool},
		{Name: "url", Type: TypeString},
		{Name: "created_at", Type: TypeString},
		{Name: "actor", Type: TypeString},
		{Name: "actor_attributes", Type: TypeStruct, Fields: append([]Field{
			{Name: "login", Type: TypeString},
			{Name: "email", Type: TypeString},
			{Name: "type", Type: TypeString},
		}, extra...)},
		{Name: "repository", Type: TypeStruct, Fields: repoFields},
		{Name: "payload", Type: TypeStruct, Fields: []Field{
			{Name: "action", Type: TypeString},
			{Name: "pages", Type: TypeStruct, Repeated: true, Fields: []Field{
				{Name: "page_name", Type: TypeString},
				{Name: "action", Type: TypeString},
				{Name: "sha", Type: TypeString},
			}},
			{Name: "shas", Type: TypeStruct, Repeated: true, Fields: []Field{
				{Name: "sha", Type: TypeString},
				{Name: "author_email", Type: TypeString},
				{Name: "message", Type: TypeString},
			}},
			{Name: "pull_request", Type: TypeStruct, Fields: []Field{
				{Name: "number", Type: TypeInt64},
				{Name: "title", Type: TypeString},
				{Name: "head", Type: TypeStruct, Fields: []Field{
					{Name: "ref", Type: TypeString},
					{Name: "repo", Type: TypeStruct, Fields: repoFields},
				}},
			}},
		}},
	}
}

// gaSessionsFields models bigquery-public-data.google_analytics_sample.ga_sessions_20170801
// with 3-level REPEATED RECORD nesting (hits -> product -> customDimensions) and >100 leaves.
func gaSessionsFields() []Field {
	extraTotals := make([]Field, 0, 95)
	for i := range 95 {
		extraTotals = append(extraTotals, Field{Name: "metric_" + string(rune('a'+i%26)) + "_" + string(rune('0'+i/26)), Type: TypeInt64})
	}

	return []Field{
		{Name: "fullVisitorId", Type: TypeString},
		{Name: "visitId", Type: TypeInt64},
		{Name: "totals", Type: TypeStruct, Fields: append([]Field{
			{Name: "visits", Type: TypeInt64},
			{Name: "hits", Type: TypeInt64},
			{Name: "pageviews", Type: TypeInt64},
		}, extraTotals...)},
		{Name: "device", Type: TypeStruct, Fields: []Field{
			{Name: "browser", Type: TypeString},
			{Name: "isMobile", Type: TypeBool},
		}},
		{Name: "customDimensions", Type: TypeStruct, Repeated: true, Fields: []Field{
			{Name: "index", Type: TypeInt64},
			{Name: "value", Type: TypeString},
		}},
		{Name: "hits", Type: TypeStruct, Repeated: true, Fields: []Field{
			{Name: "hitNumber", Type: TypeInt64},
			{Name: "type", Type: TypeString},
			{Name: "page", Type: TypeStruct, Fields: []Field{
				{Name: "pagePath", Type: TypeString},
				{Name: "pageTitle", Type: TypeString},
			}},
			{Name: "product", Type: TypeStruct, Repeated: true, Fields: []Field{
				{Name: "productSKU", Type: TypeString},
				{Name: "v2ProductName", Type: TypeString},
				{Name: "customDimensions", Type: TypeStruct, Repeated: true, Fields: []Field{
					{Name: "index", Type: TypeInt64},
					{Name: "value", Type: TypeString},
				}},
			}},
		}},
	}
}

func TestNestedGoldenGitHubNestedAndGASessions(t *testing.T) {
	t.Run("github_nested_deep_structs_and_null_struct", func(t *testing.T) {
		fields := githubNestedFields()
		leaves := Leaves(fields)

		if len(leaves) <= 100 {
			t.Fatalf("expected >100 leaf columns, got %d", len(leaves))
		}

		actorAttrs := make([]any, 98)
		actorAttrs[0], actorAttrs[1], actorAttrs[2] = "octocat", "octo@example.com", "User"
		for i := 3; i < len(actorAttrs); i++ {
			actorAttrs[i] = "val"
		}

		row0 := []any{
			"PullRequestEvent", true, "https://github.com/org/repo/pull/1", "2012-03-11T00:00:00Z", "octocat",
			actorAttrs,
			[]any{int64(42), "repo", "https://github.com/org/repo", "org", false, int64(100)},
			[]any{
				"opened",
				[]any{
					[]any{"Home", "created", "aaa111"},
					[]any{"FAQ", "edited", "bbb222"},
				},
				[]any{},
				[]any{
					int64(7), "Fix nested fields",
					[]any{"fix/nested", []any{int64(43), "forked", "https://github.com/octo/repo", "octo", true, int64(5)}},
				},
			},
		}

		// Row 1: NULL nested repository & pull_request structs + empty pages + 1 sha.
		row1 := []any{
			"PushEvent", true, "https://github.com/org/repo/commit/ccc", "2012-03-11T01:00:00Z", "octocat",
			nil,
			nil,
			[]any{
				nil,
				[]any{},
				[]any{[]any{"ccc333", "dev@example.com", "commit msg"}},
				nil,
			},
		}

		if got := Lines(fields, row0); got != 2 {
			t.Fatalf("row0 lines = %d, want 2", got)
		}

		if got := Lines(fields, row1); got != 1 {
			t.Fatalf("row1 lines = %d, want 1", got)
		}

		m := &MemoryStore{}
		g := NewGrid(fields, m)
		AppendRows(g, m, [][]any{row0, row1})
		g.Finish(nil)

		// Verify deep nested leaf values and NULL struct expansion.
		headRepoCol := -1
		repoNameCol := -1
		pageNameCol := -1
		for i, c := range leaves {
			switch c.Name {
			case "payload.pull_request.head.repo.name":
				headRepoCol = i
			case "repository.name":
				repoNameCol = i
			case "payload.pages.page_name":
				pageNameCol = i
			}
		}

		if headRepoCol < 0 || repoNameCol < 0 || pageNameCol < 0 {
			t.Fatalf("missing expected nested leaf columns: head=%d repo=%d page=%d", headRepoCol, repoNameCol, pageNameCol)
		}

		c, _, err := g.Cell(0, headRepoCol)
		if err != nil || c.Kind != Value || c.V != "forked" {
			t.Fatalf("head.repo.name cell = %+v, err=%v", c, err)
		}

		c2, _, err := g.Cell(1, pageNameCol)
		if err != nil || c2.Kind != Value || c2.V != "FAQ" {
			t.Fatalf("pages[1].page_name cell = %+v, err=%v", c2, err)
		}

		// Row 1 starts at line 2; repository.name must be explicit Null because repository struct is nil.
		cNull, _, err := g.Cell(2, repoNameCol)
		if err != nil || cNull.Kind != Null {
			t.Fatalf("row1 null struct leaf = %+v, err=%v", cNull, err)
		}

		assertRowJSONAndTree(t, fields, row0, leaves[pageNameCol], 1, "payload.pages[1].page_name")
		assertRowJSONAndTree(t, fields, row1, leaves[repoNameCol], 0, "repository.name")
	})

	t.Run("ga_sessions_3_level_repeated_record", func(t *testing.T) {
		fields := gaSessionsFields()
		leaves := Leaves(fields)

		if len(leaves) <= 100 {
			t.Fatalf("expected >100 leaf columns, got %d", len(leaves))
		}

		totals := make([]any, 98)
		totals[0], totals[1], totals[2] = int64(1), int64(2), int64(2)

		row := []any{
			"1234567890", int64(1501591568),
			totals,
			[]any{"Chrome", false},
			[]any{[]any{int64(4), "EMEA"}},
			[]any{
				// Hit 0: 2 products; product[0] has 2 customDimensions, product[1] has 0 -> 3 lines total.
				[]any{
					int64(1), "PAGE",
					[]any{"/home", "Home"},
					[]any{
						[]any{"SKU-1", "Tee", []any{
							[]any{int64(1), "blue"},
							[]any{int64(2), "cotton"},
						}},
						[]any{"SKU-2", "Hat", []any{}},
					},
				},
				// Hit 1: empty product array -> 1 line.
				[]any{
					int64(2), "EVENT",
					[]any{"/cart", "Cart"},
					[]any{},
				},
			},
		}

		if got := Lines(fields, row); got != 4 {
			t.Fatalf("3-level repeated record lines = %d, want 4", got)
		}

		m := &MemoryStore{}
		g := NewGrid(fields, m)
		AppendRows(g, m, [][]any{row})
		g.Finish(nil)

		var skuCol, cdValCol, hitNumCol int
		for i, c := range leaves {
			switch c.Name {
			case "hits.product.productSKU":
				skuCol = i
			case "hits.product.customDimensions.value":
				cdValCol = i
			case "hits.hitNumber":
				hitNumCol = i
			}
		}

		checks := []struct {
			line int64
			col  int
			kind CellKind
			val  any
		}{
			{0, cdValCol, Value, "blue"},
			{1, cdValCol, Value, "cotton"},
			{2, skuCol, Value, "SKU-2"},
			{2, cdValCol, Blank, nil},
			{3, hitNumCol, Value, int64(2)},
			{3, skuCol, Blank, nil},
		}

		for _, tc := range checks {
			cell, _, err := g.Cell(tc.line, tc.col)
			if err != nil {
				t.Fatalf("Cell(%d, %d) err = %v", tc.line, tc.col, err)
			}

			if cell.Kind != tc.kind || cell.V != tc.val {
				t.Errorf("Cell(%d, %d) = %+v, want kind=%v val=%v", tc.line, tc.col, cell, tc.kind, tc.val)
			}
		}

		assertRowJSONAndTree(t, fields, row, leaves[cdValCol], 1, "hits[0].product[0].customDimensions[1].value")
		assertRowJSONAndTree(t, fields, row, leaves[skuCol], 2, "hits[0].product[1].productSKU")
	})
}

func assertRowJSONAndTree(t *testing.T, fields []Field, row []any, col Column, sub int, wantPath string) {
	t.Helper()

	raw := AppendRowJSON(nil, fields, row)
	if !json.Valid(raw) {
		t.Fatalf("AppendRowJSON invalid JSON: %s", raw)
	}

	s := string(raw)
	spans := JSONSpans(s)

	pos := 0
	for _, sp := range spans {
		if sp.Start != pos || sp.End <= sp.Start || sp.End > len(s) {
			t.Fatalf("bad JSONSpan %+v at pos %d", sp, pos)
		}

		pos = sp.End
	}

	if pos != len(s) {
		t.Fatalf("JSONSpans ended at %d, want %d", pos, len(s))
	}

	tree := Tree(fields, row)
	if len(tree) != len(fields) {
		t.Fatalf("Tree len = %d, want %d", len(tree), len(fields))
	}

	gotPath := ColumnPath(fields, row, col, sub)
	if gotPath != wantPath {
		t.Errorf("ColumnPath(%s, sub=%d) = %q, want %q", col.Name, sub, gotPath, wantPath)
	}

	// Sanity-check that leaf name prefixes match tree root field names.
	if !strings.HasPrefix(gotPath, strings.Split(col.Name, ".")[0]) {
		t.Errorf("path %q unrelated to column %q", gotPath, col.Name)
	}
}
