package app

import "testing"

func TestObjectRefAt(t *testing.T) {
	sql := "WITH recent AS (SELECT a FROM `bigquery-public-data.samples.shakespeare`)\n" +
		"SELECT r.word, u.name\n" +
		"FROM recent AS r\n" +
		"JOIN analytics.users AS u ON u.id = r.id\n" +
		"JOIN p2.ds2.t2 USING (id)\n" +
		"JOIN\n  events\n" +
		"JOIN `proj`.`ds`.`tbl` ON TRUE"

	cases := []struct {
		name            string
		line, character int
		want            string // project.dataset.table, "" = none
		start, end      int
	}{
		{"backticked full name", 0, 40, "bigquery-public-data.samples.shakespeare", 30, 72},
		{"column ref is not a table", 1, 8, "", 0, 0},
		{"CTE is not a table", 2, 7, "", 0, 0},
		{"dataset.table after JOIN uses default project", 3, 10, "my-proj.analytics.users", 5, 20},
		{"alias.column after ON", 3, 26, "", 0, 0},
		{"three parts anywhere", 4, 8, "p2.ds2.t2", 5, 14},
		{"bare name on the next line after JOIN", 6, 4, "other-proj.def.events", 2, 8},
		{"cursor right after the name", 6, 8, "other-proj.def.events", 2, 8},
		{"per-part backticks", 7, 12, "proj.ds.tbl", 5, 22},
		{"keyword itself", 3, 1, "", 0, 0},
	}

	for _, c := range cases {
		got, ok := ObjectRefAt(sql, c.line, c.character, "my-proj", "other-proj.def")

		name := ""
		if ok {
			name = got.Ref.Project + "." + got.Ref.Dataset + "." + got.Ref.Table
		}

		if name != c.want || ok && (got.Start != c.start || got.End != c.end) {
			t.Errorf("%s: got %q [%d,%d), want %q [%d,%d)", c.name, name, got.Start, got.End, c.want, c.start, c.end)
		}
	}

	if _, ok := ObjectRefAt("SELECT a FROM t", 0, 14, "p", ""); ok {
		t.Error("bare name without default dataset resolved")
	}

	if _, ok := ObjectRefAt("x", 5, 0, "p", "d"); ok {
		t.Error("line out of range resolved")
	}

	// Domain-scoped project references (GAP-9).
	domSQL := "SELECT a FROM `example.com:proj.d.t`\n" +
		"JOIN example.com:proj.d2.t2 ON TRUE\n" +
		"JOIN example.com:proj:d3.t3 ON TRUE\n" +
		"JOIN bare_tbl ON TRUE\n" +
		"JOIN `example.com:proj.d` ON TRUE\n" +
		"JOIN `example.com:proj` ON TRUE"

	domCases := []struct {
		name            string
		line, character int
		want            string
		start, end      int
	}{
		{"backticked domain-scoped", 0, 20, "example.com:proj.d.t", 14, 36},
		{"unquoted domain-scoped", 1, 15, "example.com:proj.d2.t2", 5, 27},
		{"legacy colon domain-scoped", 2, 15, "example.com:proj.d3.t3", 5, 27},
		{"bare table with domain-scoped default dataset", 3, 7, "example.com:proj.def.bare_tbl", 5, 13},
		{"incomplete 2-part domain-scoped is rejected", 4, 15, "", 0, 0},
		{"project-only domain-scoped is rejected", 5, 15, "", 0, 0},
	}

	for _, c := range domCases {
		got, ok := ObjectRefAt(domSQL, c.line, c.character, "defp", "example.com:proj.def")

		name := ""
		if ok {
			name = got.Ref.Project + "." + got.Ref.Dataset + "." + got.Ref.Table
		}

		if name != c.want || ok && (got.Start != c.start || got.End != c.end) {
			t.Errorf("%s: got %q [%d,%d), want %q [%d,%d)", c.name, name, got.Start, got.End, c.want, c.start, c.end)
		}
	}
}
