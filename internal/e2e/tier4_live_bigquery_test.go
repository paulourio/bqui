//go:build e2e

package e2e_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/export"
	"github.com/paulourio/bqui/internal/results"
)

func TestE2ELiveGitHubNestedRESTAndStorageAndCLIOracle(t *testing.T) {
	project := requireLiveBQ(t)
	client, sc := newLiveBQClients(t, project)

	const sql = "SELECT url, created_at, type, actor, repository.name, payload.pull_request.base.repo.owner.login, payload.shas, payload.pages FROM `bigquery-public-data.samples.github_nested` WHERE url IS NOT NULL ORDER BY url, created_at LIMIT 50"

	runWith := func(maxInline int64, opener app.StorageOpener) app.RunSnapshot {
		rn := app.NewRunner(app.RunnerOptions{
			Backend:  client,
			Storage:  opener,
			Post:     func(fn func()) { fn() },
			SpillDir: t.TempDir(),
		})
		snap := waitRunDone(t, rn.Start(bq.QueryRequest{SQL: sql, Project: project, RequireJob: true, MaxInlineRows: maxInline}))
		if snap.State != app.RunDone {
			t.Fatalf("run failed (%v): %v", snap.State, snap.Err)
		}

		return snap
	}

	inlineSnap := runWith(1000, nil)
	storageSnap := runWith(5, app.NewStorageOpener(sc, func() string { return project }))

	if inlineSnap.Source != "inline" || storageSnap.Source != "storage" {
		t.Fatalf("sources = %q / %q, want inline / storage", inlineSnap.Source, storageSnap.Source)
	}

	rowsA, linesA, _ := inlineSnap.Grid.Size()
	rowsB, linesB, _ := storageSnap.Grid.Size()
	if rowsA != 50 || rowsB != 50 || linesA != linesB {
		t.Fatalf("size mismatch: inline=(%d,%d) storage=(%d,%d)", rowsA, linesA, rowsB, linesB)
	}

	fields := inlineSnap.Grid.Fields()
	for r := range rowsA {
		ra, err := inlineSnap.Grid.Row(r)
		if err != nil {
			t.Fatal(err)
		}

		rb, err := storageSnap.Grid.Row(r)
		if err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(ra, rb) {
			t.Fatalf("row %d mismatch REST vs storage", r)
		}

		ja := string(results.AppendRowJSON(nil, fields, ra))
		jb := string(results.AppendRowJSON(nil, fields, rb))
		if ja != jb || !json.Valid([]byte(ja)) {
			t.Fatalf("row %d JSON mismatch or invalid", r)
		}
	}

	// CLI oracle check via bquictl query.
	cliBin := buildBinary(t, "./cmd/bquictl", "bquictl", "", "")
	stdout, stderr, rc := runSubprocess(t, "", cliBin, nil, "-project", project, "query", "-rows", "50", sql)
	if rc != 0 {
		t.Fatalf("bquictl query rc=%d stderr=%s", rc, stderr)
	}

	cliLines := strings.Split(strings.TrimSpace(stdout), "\n")
	if int64(len(cliLines)) != rowsA {
		t.Fatalf("bquictl query lines = %d, want %d", len(cliLines), rowsA)
	}

	for i, line := range cliLines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("CLI line %d invalid JSON: %s", i, line)
		}

		var cliObj map[string]any
		if err := json.Unmarshal([]byte(line), &cliObj); err != nil {
			t.Fatalf("unmarshal CLI line %d: %v", i, err)
		}

		ra, err := inlineSnap.Grid.Row(int64(i))
		if err != nil {
			t.Fatal(err)
		}

		var runnerObj map[string]any
		if err := json.Unmarshal(results.AppendRowJSON(nil, fields, ra), &runnerObj); err != nil {
			t.Fatalf("unmarshal Runner row %d: %v", i, err)
		}

		if cliObj["url"] != runnerObj["url"] || cliObj["type"] != runnerObj["type"] {
			t.Fatalf("CLI vs Runner row %d mismatch: cli=(url=%v, type=%v) runner=(url=%v, type=%v)",
				i, cliObj["url"], cliObj["type"], runnerObj["url"], runnerObj["type"])
		}
	}
}

func TestE2ELiveGASessions222WideNestedQuery(t *testing.T) {
	project := requireLiveBQ(t)
	client, sc := newLiveBQClients(t, project)

	const sql = "SELECT fullVisitorId, totals, trafficSource.adwordsClickInfo.targetingCriteria, customDimensions, hits FROM `bigquery-public-data.google_analytics_sample.ga_sessions_20170801` WHERE ARRAY_LENGTH(hits) > 1 ORDER BY fullVisitorId, visitId LIMIT 20"

	rn := app.NewRunner(app.RunnerOptions{
		Backend:  client,
		Storage:  app.NewStorageOpener(sc, func() string { return project }),
		Post:     func(fn func()) { fn() },
		SpillDir: t.TempDir(),
	})
	snap := waitRunDone(t, rn.Start(bq.QueryRequest{SQL: sql, Project: project, RequireJob: true, MaxInlineRows: 5}))
	if snap.State != app.RunDone {
		t.Fatalf("ga_sessions run failed: %v", snap.Err)
	}

	rows, lines, complete := snap.Grid.Size()
	if rows != 20 || lines <= rows || !complete {
		t.Fatalf("ga_sessions size = (%d, %d, %v), want rows=20, lines>20", rows, lines, complete)
	}

	var colNames []string
	for _, c := range snap.Grid.Columns() {
		colNames = append(colNames, c.Name)
	}

	for _, want := range []string{"fullVisitorId", "totals.visits", "hits.hitNumber", "hits.page.pagePath"} {
		if !slices.Contains(colNames, want) {
			t.Errorf("missing expected column %q", want)
		}
	}

	fields := snap.Grid.Fields()
	for r := range rows {
		row, err := snap.Grid.Row(r)
		if err != nil {
			t.Fatal(err)
		}

		js := results.AppendRowJSON(nil, fields, row)
		if !json.Valid(js) {
			t.Fatalf("row %d JSON invalid", r)
		}
	}
}

func TestE2ELiveStorageSpillOver1050RowsNestedAndExport(t *testing.T) {
	project := requireLiveBQ(t)
	client, sc := newLiveBQClients(t, project)

	sql := `
SELECT
  id,
  IF(MOD(id, 10) = 0, NULL, STRUCT(
    CONCAT('grp-', CAST(id AS STRING)) AS label,
    IF(MOD(id, 5) = 0, ARRAY<STRUCT<code STRING, tags ARRAY<STRING>>>[], [
      STRUCT('a' AS code, ['t1', 't2'] AS tags),
      STRUCT('b' AS code, ARRAY<STRING>[] AS tags)
    ]) AS entries
  )) AS nested
FROM UNNEST(GENERATE_ARRAY(1, 1200)) AS id`

	if ds := os.Getenv("BQUI_E2E_DATASET"); ds != "" {
		//nolint:unqueryvet // BQUI_E2E_DATASET table schema is supplied by caller at runtime
		sql = "SELECT * FROM `" + project + "." + ds + "` LIMIT 1200"
	}

	rn := app.NewRunner(app.RunnerOptions{
		Backend:  client,
		Storage:  app.NewStorageOpener(sc, func() string { return project }),
		Post:     func(fn func()) { fn() },
		SpillDir: t.TempDir(),
	})

	snap := waitRunDone(t, rn.Start(bq.QueryRequest{SQL: sql, Project: project, RequireJob: true, MaxInlineRows: 50}))
	if snap.State != app.RunDone || snap.Source != "storage" {
		t.Fatalf("expected storage run done, got state=%v source=%q err=%v", snap.State, snap.Source, snap.Err)
	}

	rows, lines, complete := snap.Grid.Size()
	if rows < 1050 || lines < rows || !complete {
		t.Fatalf("size = (%d, %d, %v), want >= 1050 rows", rows, lines, complete)
	}

	if _, _, err := snap.Grid.Cell(lines-1, 0); err != nil {
		t.Fatalf("random access last cell failed: %v", err)
	}

	outDir := t.TempDir()
	for _, f := range []export.Format{export.CSV, export.NDJSON} {
		outPath := filepath.Join(outDir, "nested."+f.Ext())
		done := make(chan app.ExportProgress, 1)
		rn.Export(app.ExportSource{Grid: snap.Grid}, outPath, f, export.Options{CSVNesting: export.CSVLines}, func(p app.ExportProgress) {
			if p.Done {
				done <- p
			}
		})

		p := <-done
		if p.Err != nil || p.Rows != rows {
			t.Fatalf("%s export failed: %+v", f, p)
		}
	}

	if snap.ResultTable != nil {
		cliBin := buildBinary(t, "./cmd/bquictl", "bquictl", "", "")
		stdout, stderr, rc := runSubprocess(t, "", cliBin, nil, "-project", project, "read", "-rows", "10", snap.ResultTable.String())
		if rc != 0 {
			t.Fatalf("bquictl read rc=%d stderr=%s stdout=%s", rc, stderr, stdout)
		}

		readLines := strings.Split(strings.TrimSpace(stdout), "\n")
		if len(readLines) != 10 {
			t.Fatalf("bquictl read lines = %d, want 10", len(readLines))
		}

		for i, line := range readLines {
			if !json.Valid([]byte(line)) {
				t.Fatalf("bquictl read line %d invalid JSON: %s", i, line)
			}
		}
	}
}
