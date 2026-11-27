package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
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

var fakeLimit = regexp.MustCompile(`(?i)\bLIMIT\s+(\d+)`)

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
func (f *FakeBackend) Query(ctx context.Context, req bq.QueryRequest) (res *bq.QueryResult, err error) {
	f.mu.Lock()
	f.nextJob++
	job := bq.JobRef{Project: FakeProject, JobID: fmt.Sprintf("job_fake_%d", f.nextJob), Location: "US"}
	f.mu.Unlock()

	started := time.Now()

	defer func() { f.record(job, req.SQL, started, res, err) }()

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

	res = &bq.QueryResult{Job: &job, JobDetails: fakeJob(job, fakeStages), SlotMs: 1234, BytesProc: 2 << 20, BytesBilled: 10 << 20}

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

	// Tokens are "start/total"; "" starts a finished job's results over.
	var start, total int

	if token == "" {
		f.mu.Lock()
		n, ok := f.totals[job.JobID]
		f.mu.Unlock()

		if !ok {
			return nil, fmt.Errorf("%w: results of %s", ErrFakeNotFound, job.JobID)
		}

		total = n
	} else if _, err := fmt.Sscanf(token, "%d/%d", &start, &total); err != nil || start < 0 || total > fakeMaxRows {
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

// fakeJob replays a real recorded 13-stage plan (fixtures/plan.job.json):
// after `done` of fakeStages progress steps, that share of stages is
// COMPLETE, the next one RUNNING, the rest PENDING.
func fakeJob(ref bq.JobRef, done int) *bigquery.Job {
	var recorded bigquery.Job
	if err := readFixture("plan.job", &recorded); err != nil || recorded.Statistics == nil || recorded.Statistics.Query == nil {
		panic("fake: plan fixture: " + fmt.Sprint(err))
	}

	stats := recorded.Statistics
	plan := stats.Query.QueryPlan
	complete := len(plan) * done / fakeStages

	for i, st := range plan {
		switch {
		case i < complete:
			st.Status = "COMPLETE"
		case i == complete:
			st.Status = "RUNNING"
		default:
			st.Status = "PENDING"
		}
	}

	state := "RUNNING"
	if done >= fakeStages {
		state = "DONE"
	}

	return &bigquery.Job{
		JobReference: &bigquery.JobReference{ProjectId: ref.Project, JobId: ref.JobID, Location: ref.Location},
		Status:       &bigquery.JobStatus{State: state},
		Statistics: &bigquery.JobStatistics{
			TotalSlotMs: stats.TotalSlotMs * int64(done) / fakeStages,
			Query:       &bigquery.JobStatistics2{QueryPlan: plan, Timeline: stats.Query.Timeline, StatementType: "SELECT"},
		},
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

// fakeUser is the account the fake backend runs as.
const fakeUser = "you@example.com"

// record stores a finished fake job for ListJobs/GetJob.
func (f *FakeBackend) record(ref bq.JobRef, sql string, started time.Time, res *bq.QueryResult, err error) {
	j := &bigquery.Job{
		JobReference:  &bigquery.JobReference{ProjectId: ref.Project, JobId: ref.JobID, Location: ref.Location},
		UserEmail:     fakeUser,
		Configuration: &bigquery.JobConfiguration{JobType: "QUERY", Query: &bigquery.JobConfigurationQuery{Query: sql, Priority: "INTERACTIVE"}},
		Status:        &bigquery.JobStatus{State: "DONE"},
		Statistics: &bigquery.JobStatistics{
			CreationTime: started.UnixMilli(), StartTime: started.UnixMilli(), EndTime: time.Now().UnixMilli(),
			Query: &bigquery.JobStatistics2{},
		},
	}

	switch {
	case err != nil && errors.Is(err, context.Canceled):
		j.Status.ErrorResult = &bigquery.ErrorProto{Reason: "stopped", Message: "Job execution was cancelled: User requested cancellation"}
	case err != nil:
		j.Status.ErrorResult = &bigquery.ErrorProto{Reason: "invalidQuery", Message: strings.TrimPrefix(err.Error(), "bq: query failed: ")}
	case res != nil:
		j.Statistics.TotalSlotMs = res.SlotMs
		j.Statistics.Query = &bigquery.JobStatistics2{
			StatementType: res.Statement, TotalBytesProcessed: res.BytesProc, TotalBytesBilled: res.BytesBilled,
			QueryPlan:        fakeJob(ref, fakeStages).Statistics.Query.QueryPlan,
			ReferencedTables: []*bigquery.TableReference{{ProjectId: FakeProject, DatasetId: "analytics", TableId: "events"}},
		}

		if res.Schema != nil {
			j.Configuration.Query.DestinationTable = &bigquery.TableReference{ProjectId: FakeProject, DatasetId: "_anon", TableId: "anon_" + ref.JobID}
			f.setTotal(ref.JobID, int(res.TotalRows)) //nolint:gosec // bounded by fakeMaxRows
		}
	}

	f.mu.Lock()
	f.jobs = append(f.jobs, j)
	f.mu.Unlock()
}

func (f *FakeBackend) setTotal(jobID string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.totals == nil {
		f.totals = make(map[string]int)
	}

	f.totals[jobID] = n
}

// seedJobs adds older jobs by several users so history has content.
func (f *FakeBackend) seedJobs() {
	if f.seeded {
		return
	}

	f.seeded = true
	users := []string{fakeUser, "ana@example.com", "bruno@example.com"}
	base := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

	seed := make([]*bigquery.Job, 0, 120)

	for i := range 120 {
		created := base.Add(time.Duration(i) * 17 * time.Minute)
		j := &bigquery.Job{
			JobReference: &bigquery.JobReference{ProjectId: FakeProject, JobId: fmt.Sprintf("job_seed_%03d", i), Location: "US"},
			UserEmail:    users[i%len(users)],
			Configuration: &bigquery.JobConfiguration{JobType: "QUERY", Query: &bigquery.JobConfigurationQuery{
				Query: fmt.Sprintf("SELECT user_id, COUNT(*) AS n\nFROM `bqui-demo.analytics.events`\nWHERE day = '2026-11-%02d'\nGROUP BY user_id\nLIMIT %d", 1+i%28, 100+i),
			}},
			Status: &bigquery.JobStatus{State: "DONE"},
			Statistics: &bigquery.JobStatistics{
				CreationTime: created.UnixMilli(), StartTime: created.UnixMilli(), EndTime: created.Add(time.Duration(800+i*37) * time.Millisecond).UnixMilli(),
				TotalSlotMs: int64(2000 + i*91),
				Query:       &bigquery.JobStatistics2{StatementType: "SELECT", TotalBytesProcessed: int64(1+i) << 20, CacheHit: i%7 == 0},
			},
		}

		if i%13 == 5 {
			j.Status.ErrorResult = &bigquery.ErrorProto{Reason: "invalidQuery", Message: "Unrecognized name: usr_id at [1:8]"}
		}

		seed = append(seed, j)
	}

	f.jobs = append(seed, f.jobs...)
}

// ListJobs implements JobsBackend (newest first, paged by index tokens).
func (f *FakeBackend) ListJobs(ctx context.Context, filter bq.JobFilter) (*bq.JobPage, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.seedJobs()

	var matched []*bigquery.Job

	for _, j := range slices.Backward(f.jobs) {
		if filter.AllUsers || j.UserEmail == fakeUser {
			matched = append(matched, j)
		}
	}

	start := 0
	if filter.PageToken != "" {
		start, _ = strconv.Atoi(filter.PageToken)
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = HistoryPage
	}

	end := min(start+limit, len(matched))
	page := &bq.JobPage{}

	for _, j := range matched[min(start, end):end] {
		page.Jobs = append(page.Jobs, &bigquery.JobListJobs{
			JobReference: j.JobReference, UserEmail: j.UserEmail, Configuration: j.Configuration,
			Statistics: j.Statistics, Status: j.Status, ErrorResult: j.Status.ErrorResult, State: j.Status.State,
		})
	}

	if end < len(matched) {
		page.NextPageToken = strconv.Itoa(end)
	}

	return page, nil
}

// GetJob implements JobsBackend.
func (f *FakeBackend) GetJob(ctx context.Context, ref bq.JobRef) (*bigquery.Job, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.seedJobs()

	for _, j := range f.jobs {
		if j.JobReference.JobId == ref.JobID {
			return j, nil
		}
	}

	return nil, fmt.Errorf("%w: job %s", ErrFakeNotFound, ref.JobID)
}
