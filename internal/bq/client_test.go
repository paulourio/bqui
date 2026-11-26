package bq

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	bigquery "google.golang.org/api/bigquery/v2"
	"google.golang.org/api/option"
)

// fakeBQ is a minimal BigQuery REST v2 server for tests.
type fakeBQ struct {
	t        *testing.T
	polls    atomic.Int32
	lastReq  atomic.Pointer[bigquery.QueryRequest]
	jobError string
}

func (f *fakeBQ) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/bigquery/v2")

	switch {
	case r.Method == http.MethodPost && path == "/projects/p/queries":
		var req bigquery.QueryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			f.t.Errorf("decode query request: %v", err)
		}

		f.lastReq.Store(&req)
		writeJSON(w, bigquery.QueryResponse{
			JobComplete:  false,
			JobReference: &bigquery.JobReference{ProjectId: "p", JobId: "job_1", Location: "US"},
			QueryId:      "q1",
		})
	case r.Method == http.MethodGet && path == "/projects/p/queries/job_1":
		if r.URL.Query().Get("location") != "US" {
			f.t.Errorf("getQueryResults without location: %s", r.URL.RawQuery)
		}

		if r.URL.Query().Get("formatOptions.useInt64Timestamp") != "true" {
			f.t.Errorf("getQueryResults without int64 timestamps: %s", r.URL.RawQuery)
		}

		if f.polls.Add(1) < 2 {
			writeJSON(w, bigquery.GetQueryResultsResponse{JobComplete: false})

			return
		}

		writeJSON(w, bigquery.GetQueryResultsResponse{
			JobComplete: true,
			Schema:      &bigquery.TableSchema{Fields: []*bigquery.TableFieldSchema{{Name: "n", Type: "INTEGER"}}},
			Rows:        []*bigquery.TableRow{{F: []*bigquery.TableCell{{V: "7"}}}},
			TotalRows:   1,
		})
	case r.Method == http.MethodGet && path == "/projects/p/jobs/job_1":
		job := bigquery.Job{
			JobReference: &bigquery.JobReference{ProjectId: "p", JobId: "job_1", Location: "US"},
			Status:       &bigquery.JobStatus{State: "DONE"},
			Configuration: &bigquery.JobConfiguration{Query: &bigquery.JobConfigurationQuery{
				DestinationTable: &bigquery.TableReference{ProjectId: "p", DatasetId: "_anon", TableId: "anon_1"},
			}},
			Statistics: &bigquery.JobStatistics{
				TotalSlotMs: 42,
				Query:       &bigquery.JobStatistics2{StatementType: "SELECT", TotalBytesBilled: 10485760},
			},
		}

		if f.jobError != "" {
			job.Status.ErrorResult = &bigquery.ErrorProto{Message: f.jobError}
		}

		writeJSON(w, job)
	case r.Method == http.MethodGet && path == "/projects/p/datasets":
		if r.URL.Query().Get("pageToken") == "" {
			writeJSON(w, bigquery.DatasetList{
				Datasets:      []*bigquery.DatasetListDatasets{{DatasetReference: &bigquery.DatasetReference{ProjectId: "p", DatasetId: "a"}, Location: "US"}},
				NextPageToken: "next",
			})

			return
		}

		writeJSON(w, bigquery.DatasetList{
			Datasets: []*bigquery.DatasetListDatasets{{DatasetReference: &bigquery.DatasetReference{ProjectId: "p", DatasetId: "b"}, Location: "EU"}},
		})
	case r.Method == http.MethodGet && path == "/projects/p/jobs":
		q := r.URL.Query()
		if q.Get("projection") != "full" || q.Get("allUsers") != "true" || q.Get("stateFilter") != "running" {
			f.t.Errorf("jobs.list query = %s", r.URL.RawQuery)
		}

		writeJSON(w, bigquery.JobList{Jobs: []*bigquery.JobListJobs{{Id: "p:US.job_1", State: "RUNNING"}}, NextPageToken: "tok"})
	default:
		http.Error(w, `{"error":{"code":404,"message":"not found: `+r.Method+" "+path+`"}}`, http.StatusNotFound)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newTestClient(t *testing.T, f *fakeBQ) *Client {
	t.Helper()

	f.t = t
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	c, err := New(context.Background(), Options{
		Project: "p",
		TokenProvider: func(context.Context) (string, error) {
			return "test-token", nil
		},
		ClientOptions: []option.ClientOption{
			option.WithEndpoint(srv.URL + "/bigquery/v2/"),
			option.WithHTTPClient(srv.Client()),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func TestQueryPollsUntilCompleteAndAttachesJob(t *testing.T) {
	f := &fakeBQ{}
	c := newTestClient(t, f)

	var progress int

	res, err := c.Query(context.Background(), QueryRequest{
		SQL:            "SELECT 7 AS n",
		MaxBytesBilled: 1 << 30,
		SessionID:      "sess-1",
		OnProgress:     func(*bigquery.Job) { progress++ },
	})
	if err != nil {
		t.Fatal(err)
	}

	req := f.lastReq.Load()
	if req.UseLegacySql == nil || *req.UseLegacySql {
		t.Error("legacy SQL must be explicitly disabled")
	}

	if req.JobCreationMode != jobCreationRequired {
		t.Errorf("session queries must require a job, got %q", req.JobCreationMode)
	}

	if len(req.ConnectionProperties) != 1 || req.ConnectionProperties[0].Value != "sess-1" {
		t.Errorf("session id not sent: %+v", req.ConnectionProperties)
	}

	if req.RequestId == "" || req.MaximumBytesBilled != 1<<30 {
		t.Errorf("request id / max bytes: %+v", req)
	}

	if f.polls.Load() != 2 || progress != 1 {
		t.Errorf("polls = %d, progress = %d", f.polls.Load(), progress)
	}

	rows, err := DecodeRows(res.Schema.Fields, res.Rows)
	if err != nil || len(rows) != 1 || rows[0][0] != int64(7) {
		t.Fatalf("rows = %v, %v", rows, err)
	}

	if res.Destination == nil || res.Destination.Table != "anon_1" || res.Statement != "SELECT" || res.SlotMs != 42 {
		t.Errorf("job details not attached: %+v", res)
	}
}

func TestQueryReportsJobError(t *testing.T) {
	c := newTestClient(t, &fakeBQ{jobError: "Syntax error at [1:8]"})

	_, err := c.Query(context.Background(), QueryRequest{SQL: "SELECT"})
	if !errors.Is(err, ErrQueryFailed) || !strings.Contains(err.Error(), "Syntax error") {
		t.Fatalf("err = %v", err)
	}
}

func TestQueryDefaultsToOptionalJobCreation(t *testing.T) {
	f := &fakeBQ{}
	c := newTestClient(t, f)

	if _, err := c.Query(context.Background(), QueryRequest{SQL: "SELECT 1"}); err != nil {
		t.Fatal(err)
	}

	if got := f.lastReq.Load().JobCreationMode; got != jobCreationOptional {
		t.Errorf("JobCreationMode = %q", got)
	}
}

func TestListDatasetsFollowsPages(t *testing.T) {
	c := newTestClient(t, &fakeBQ{})

	ds, err := c.ListDatasets(context.Background(), "", false)
	if err != nil {
		t.Fatal(err)
	}

	if len(ds) != 2 || ds[0].Ref.Dataset != "a" || ds[1].Location != "EU" {
		t.Fatalf("datasets = %+v", ds)
	}
}

func TestListJobsFilter(t *testing.T) {
	c := newTestClient(t, &fakeBQ{})

	page, err := c.ListJobs(context.Background(), JobFilter{AllUsers: true, States: []string{"running"}})
	if err != nil {
		t.Fatal(err)
	}

	if len(page.Jobs) != 1 || page.NextPageToken != "tok" {
		t.Fatalf("page = %+v", page)
	}
}

func TestNotFoundAndNoProject(t *testing.T) {
	c := newTestClient(t, &fakeBQ{})

	_, err := c.GetTable(context.Background(), TableRef{"p", "d", "missing"})
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false", err)
	}

	empty := &Client{svc: c.svc}
	if _, err := empty.ListDatasets(context.Background(), "", false); !errors.Is(err, ErrNoProject) {
		t.Errorf("err = %v, want ErrNoProject", err)
	}
}

func TestRequestIDIsUUIDv4(t *testing.T) {
	id := requestID()
	if len(id) != 36 || id[14] != '4' || !strings.ContainsRune("89ab", rune(id[19])) {
		t.Errorf("requestID = %q", id)
	}

	if id == requestID() {
		t.Error("request IDs must differ")
	}
}
