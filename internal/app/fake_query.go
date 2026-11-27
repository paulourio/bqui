package app

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/bq"
)

// Fake query behavior: every SELECT returns deterministic nested "orders"
// rows (LIMIT n picks the count, default 1000), "SELEC" is a syntax error,
// and CREATE/INSERT/... are statements without results.
const (
	fakeDefaultRows = 1000
	fakeMaxRows     = 500_000
	fakeFirstPage   = 1000
	fakeRunTime     = 600 * time.Millisecond
	fakeStages      = 3
)

var (
	fakeLimit = regexp.MustCompile(`(?i)\bLIMIT\s+(\d+)`)
	fakeJobs  int64
)

var _ QueryBackend = (*FakeBackend)(nil)

// FakeOrdersSchema is the schema of fake query results.
func FakeOrdersSchema() *bigquery.TableSchema {
	return &bigquery.TableSchema{Fields: []*bigquery.TableFieldSchema{
		{Name: "order_id", Type: "INTEGER", Mode: "REQUIRED"},
		{Name: "created_at", Type: "TIMESTAMP"},
		{Name: "customer", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{
			{Name: "name", Type: "STRING"},
			{Name: "tier", Type: "STRING"},
		}},
		{Name: "items", Type: "RECORD", Mode: "REPEATED", Fields: []*bigquery.TableFieldSchema{
			{Name: "sku", Type: "STRING"},
			{Name: "qty", Type: "INTEGER"},
			{Name: "price", Type: "NUMERIC"},
			{Name: "tags", Type: "STRING", Mode: "REPEATED"},
		}},
		{Name: "total", Type: "FLOAT"},
	}}
}

// Query implements QueryBackend.
func (f *FakeBackend) Query(ctx context.Context, req bq.QueryRequest) (*bq.QueryResult, error) {
	fakeJobs++
	job := bq.JobRef{Project: FakeProject, JobID: fmt.Sprintf("job_fake_%d", fakeJobs), Location: "US"}

	if req.OnJob != nil {
		req.OnJob(job)
	}

	sql := strings.TrimSpace(req.SQL)
	upper := strings.ToUpper(sql)

	// Simulate execution with per-stage progress; honor cancellation.
	for stage := 1; stage <= fakeStages; stage++ {
		select {
		case <-time.After(fakeRunTime / fakeStages):
		case <-ctx.Done():
			return nil, ctx.Err()
		}

		if req.OnProgress != nil {
			req.OnProgress(fakeJob(job, stage))
		}
	}

	if strings.Contains(upper, "SELEC ") || strings.HasPrefix(upper, "SELEC\n") {
		return nil, fmt.Errorf("%w: Syntax error: Unexpected identifier \"SELEC\" at [1:1]", bq.ErrQueryFailed)
	}

	res := &bq.QueryResult{Job: &job, JobDetails: fakeJob(job, fakeStages), SlotMs: 1234, BytesProc: 2 << 20, BytesBilled: 10 << 20}

	for _, kw := range []string{"CREATE", "INSERT", "UPDATE", "DELETE", "MERGE", "DROP", "ALTER"} {
		if strings.HasPrefix(upper, kw) {
			res.Statement = kw

			return res, nil
		}
	}

	n := fakeDefaultRows
	if m := fakeLimit.FindStringSubmatch(sql); m != nil {
		n, _ = strconv.Atoi(m[1])
	}

	n = min(n, fakeMaxRows)
	first := min(n, fakeFirstPage)

	res.Statement = "SELECT"
	res.Schema = FakeOrdersSchema()
	res.TotalRows = uint64(n)
	res.Rows = fakeOrderRows(0, first)

	if first < n {
		res.PageToken = fmt.Sprintf("%d/%d", first, n)
	}

	return res, nil
}

// ResultPage implements QueryBackend.
func (f *FakeBackend) ResultPage(ctx context.Context, job bq.JobRef, token string, maxRows int64) (*bigquery.GetQueryResultsResponse, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}

	// Tokens are "start/total".
	var start, total int
	if _, err := fmt.Sscanf(token, "%d/%d", &start, &total); err != nil || start < 0 || total > fakeMaxRows {
		return nil, fmt.Errorf("fake: bad page token %q for %s", token, job.JobID)
	}

	end := min(start+int(maxRows), total)
	resp := &bigquery.GetQueryResultsResponse{
		JobComplete: true, Schema: FakeOrdersSchema(), Rows: fakeOrderRows(start, end-start),
		TotalRows: uint64(total), //nolint:gosec // bounded above
	}

	if end < total {
		resp.PageToken = fmt.Sprintf("%d/%d", end, total)
	}

	return resp, nil
}

// CancelJob implements QueryBackend.
func (f *FakeBackend) CancelJob(context.Context, bq.JobRef) error { return nil }

func fakeJob(ref bq.JobRef, done int) *bigquery.Job {
	plan := make([]*bigquery.ExplainQueryStage, fakeStages)
	for i := range plan {
		status := "PENDING"
		if i < done {
			status = "COMPLETE"
		}

		plan[i] = &bigquery.ExplainQueryStage{Id: int64(i), Name: fmt.Sprintf("S%02d", i), Status: status}
	}

	return &bigquery.Job{
		JobReference: &bigquery.JobReference{ProjectId: ref.Project, JobId: ref.JobID, Location: ref.Location},
		Status:       &bigquery.JobStatus{State: "RUNNING"},
		Statistics:   &bigquery.JobStatistics{TotalSlotMs: int64(done) * 400, Query: &bigquery.JobStatistics2{QueryPlan: plan}},
	}
}

// fakeOrderRows encodes rows [start, start+n) exactly as the REST API does.
func fakeOrderRows(start, n int) []*bigquery.TableRow {
	tiers := []string{"gold", "silver", ""}
	names := []string{"Ana", "Bruno", "Carla", "Diego", "Elisa"}
	skus := []string{"A-1", "B-7", "C-3", "D-9"}

	rows := make([]*bigquery.TableRow, 0, n)

	for id := start; id < start+n; id++ {
		var tier any = tiers[id%len(tiers)]
		if tier == "" {
			tier = nil
		}

		var items []any

		for k := range id % 4 { // 0..3 items, so some rows have an empty array
			var tags []any
			for t := range (id + k) % 3 {
				tags = append(tags, map[string]any{"v": fmt.Sprintf("tag%d", t)})
			}

			items = append(items, map[string]any{"v": map[string]any{"f": []any{
				map[string]any{"v": skus[(id+k)%len(skus)]},
				map[string]any{"v": strconv.Itoa(k + 1)},
				map[string]any{"v": fmt.Sprintf("%d.%02d", 10+k, id%100)},
				map[string]any{"v": tags},
			}}})
		}

		created := int64(1_704_067_200_000_000) + int64(id)*3_600_000_000

		rows = append(rows, &bigquery.TableRow{F: []*bigquery.TableCell{
			{V: strconv.Itoa(id + 1)},
			{V: strconv.FormatInt(created, 10)},
			{V: map[string]any{"f": []any{map[string]any{"v": names[id%len(names)]}, map[string]any{"v": tier}}}},
			{V: items},
			{V: strconv.FormatFloat(float64(id%1000)*1.25, 'f', -1, 64)},
		}})
	}

	return rows
}
