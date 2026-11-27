package bq

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	bigquery "google.golang.org/api/bigquery/v2"
)

// ErrQueryFailed wraps errors BigQuery reports for a finished job.
var ErrQueryFailed = errors.New("bq: query failed")

const (
	jobCreationOptional = "JOB_CREATION_OPTIONAL"
	jobCreationRequired = "JOB_CREATION_REQUIRED"

	defaultInlineRows = 1000
	firstWait         = 10 * time.Second
	pollWait          = 10 * time.Second
)

// QueryRequest describes one query run.
type QueryRequest struct {
	SQL            string
	Project        string // billing project; defaults to the client's
	Location       string
	DefaultDataset *DatasetRef
	DryRun         bool
	MaxBytesBilled int64
	Labels         map[string]string
	// MaxInlineRows caps rows returned in the first page (0 = 1000).
	MaxInlineRows int64
	// RequireJob forces a job (needed for query plans of fast queries).
	RequireJob bool
	// Session mode: CreateSession starts a session; SessionID joins one.
	CreateSession bool
	SessionID     string
	// OnProgress, if set, receives job snapshots while the query runs.
	OnProgress func(*bigquery.Job)
	// OnJob, if set, receives the job reference as soon as it is known (to
	// allow cancellation while the query runs).
	OnJob func(JobRef)
}

// QueryResult is the outcome of a completed query.
type QueryResult struct {
	Job         *JobRef // nil when BigQuery answered without creating a job
	QueryID     string
	Schema      *bigquery.TableSchema
	TotalRows   uint64
	Rows        []*bigquery.TableRow // first page, inline
	PageToken   string               // more rows via getQueryResults
	Destination *TableRef            // anonymous or explicit result table
	CacheHit    bool
	DryRun      bool
	Statement   string // SELECT, INSERT, CREATE_TABLE, SCRIPT, ...
	BytesProc   int64
	BytesBilled int64
	SlotMs      int64
	DMLAffected int64
	SessionID   string
	JobDetails  *bigquery.Job // full job (statistics, plan) when a job exists
	Elapsed     time.Duration
	// CreationReason explains why a job was created in optional mode.
	CreationReason string
}

// Query runs a query to completion and returns its first page of rows.
func (c *Client) Query(ctx context.Context, req QueryRequest) (*QueryResult, error) {
	project, err := c.projectOr(req.Project)
	if err != nil {
		return nil, err
	}

	started := time.Now()

	resp, err := c.svc.Jobs.Query(project, c.queryRequest(req)).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("bq: query: %w", err)
	}

	res := fromQueryResponse(resp)
	res.DryRun = req.DryRun

	if res.Job != nil && req.OnJob != nil {
		req.OnJob(*res.Job)
	}

	if err := errorsOf(resp.Errors); err != nil && resp.JobComplete {
		return res, err
	}

	if !res.DryRun && !resp.JobComplete {
		if err := c.waitForResults(ctx, req, res); err != nil {
			return res, err
		}
	}

	if res.Job != nil && !res.DryRun {
		if err := c.attachJob(ctx, res); err != nil {
			return res, err
		}
	}

	res.Elapsed = time.Since(started)

	return res, nil
}

func (c *Client) queryRequest(req QueryRequest) *bigquery.QueryRequest {
	legacy := false
	rows := req.MaxInlineRows

	if rows <= 0 {
		rows = defaultInlineRows
	}

	mode := jobCreationOptional
	if req.RequireJob || req.CreateSession || req.SessionID != "" {
		mode = jobCreationRequired
	}

	qr := &bigquery.QueryRequest{
		Query:              req.SQL,
		UseLegacySql:       &legacy,
		JobCreationMode:    mode,
		TimeoutMs:          firstWait.Milliseconds(),
		MaxResults:         rows,
		Location:           firstNonEmpty(req.Location, c.location),
		DryRun:             req.DryRun,
		MaximumBytesBilled: req.MaxBytesBilled,
		Labels:             req.Labels,
		CreateSession:      req.CreateSession,
		RequestId:          requestID(),
		FormatOptions:      &bigquery.DataFormatOptions{UseInt64Timestamp: true},
	}

	if req.DefaultDataset != nil {
		qr.DefaultDataset = &bigquery.DatasetReference{ProjectId: req.DefaultDataset.Project, DatasetId: req.DefaultDataset.Dataset}
	}

	if req.SessionID != "" {
		qr.ConnectionProperties = []*bigquery.ConnectionProperty{{Key: "session_id", Value: req.SessionID}}
	}

	return qr
}

func fromQueryResponse(resp *bigquery.QueryResponse) *QueryResult {
	res := &QueryResult{
		Job:         jobRefFrom(resp.JobReference),
		QueryID:     resp.QueryId,
		Schema:      resp.Schema,
		TotalRows:   resp.TotalRows,
		Rows:        resp.Rows,
		PageToken:   resp.PageToken,
		CacheHit:    resp.CacheHit,
		Statement:   resp.StatementType,
		BytesProc:   resp.TotalBytesProcessed,
		BytesBilled: resp.TotalBytesBilled,
		SlotMs:      resp.TotalSlotMs,
		DMLAffected: resp.NumDmlAffectedRows,
	}

	if resp.SessionInfo != nil {
		res.SessionID = resp.SessionInfo.SessionId
	}

	if resp.JobCreationReason != nil {
		res.CreationReason = resp.JobCreationReason.Code
	}

	return res
}

// waitForResults long-polls getQueryResults until the job completes.
func (c *Client) waitForResults(ctx context.Context, req QueryRequest, res *QueryResult) error {
	if res.Job == nil {
		return fmt.Errorf("%w: incomplete query without a job reference", ErrQueryFailed)
	}

	for {
		call := c.svc.Jobs.GetQueryResults(res.Job.Project, res.Job.JobID).
			TimeoutMs(pollWait.Milliseconds()).
			MaxResults(firstPositive(req.MaxInlineRows, defaultInlineRows)).
			FormatOptionsUseInt64Timestamp(true).
			Context(ctx)
		if res.Job.Location != "" {
			call = call.Location(res.Job.Location)
		}

		qr, err := call.Do()
		if err != nil {
			return fmt.Errorf("bq: get query results %s: %w", res.Job.JobID, err)
		}

		if qr.JobComplete {
			if err := errorsOf(qr.Errors); err != nil {
				return err
			}

			res.Schema, res.Rows, res.PageToken = qr.Schema, qr.Rows, qr.PageToken
			res.TotalRows, res.CacheHit = qr.TotalRows, qr.CacheHit
			res.BytesProc, res.DMLAffected = qr.TotalBytesProcessed, qr.NumDmlAffectedRows

			return nil
		}

		if req.OnProgress != nil {
			if job, err := c.GetJob(ctx, *res.Job); err == nil {
				req.OnProgress(job)
			}
		}

		if err := ctx.Err(); err != nil {
			return fmt.Errorf("bq: wait for %s: %w", res.Job.JobID, err)
		}
	}
}

// attachJob loads full job metadata (statistics, plan, destination).
func (c *Client) attachJob(ctx context.Context, res *QueryResult) error {
	job, err := c.GetJob(ctx, *res.Job)
	if err != nil {
		return err
	}

	res.JobDetails = job

	if job.Status != nil && job.Status.ErrorResult != nil {
		return fmt.Errorf("%w: %s", ErrQueryFailed, job.Status.ErrorResult.Message)
	}

	if job.Configuration != nil && job.Configuration.Query != nil && job.Configuration.Query.DestinationTable != nil {
		d := job.Configuration.Query.DestinationTable
		res.Destination = &TableRef{Project: d.ProjectId, Dataset: d.DatasetId, Table: d.TableId}
	}

	if st := job.Statistics; st != nil {
		res.SlotMs = st.TotalSlotMs

		if q := st.Query; q != nil {
			res.Statement = q.StatementType
			res.BytesBilled = q.TotalBytesBilled
			res.CacheHit = q.CacheHit
		}

		if st.SessionInfo != nil && res.SessionID == "" {
			res.SessionID = st.SessionInfo.SessionId
		}
	}

	return nil
}

// ResultPage fetches a further page of a job's results.
func (c *Client) ResultPage(ctx context.Context, job JobRef, pageToken string, maxRows int64) (*bigquery.GetQueryResultsResponse, error) {
	call := c.svc.Jobs.GetQueryResults(job.Project, job.JobID).
		PageToken(pageToken).
		MaxResults(firstPositive(maxRows, defaultInlineRows)).
		FormatOptionsUseInt64Timestamp(true).
		Context(ctx)
	if job.Location != "" {
		call = call.Location(job.Location)
	}

	resp, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("bq: result page %s: %w", job.JobID, err)
	}

	return resp, nil
}

func errorsOf(errs []*bigquery.ErrorProto) error {
	if len(errs) == 0 {
		return nil
	}

	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msg := e.Message
		if e.Location != "" {
			msg += " (" + e.Location + ")"
		}

		msgs = append(msgs, msg)
	}

	return fmt.Errorf("%w: %s", ErrQueryFailed, strings.Join(msgs, "; "))
}

func firstPositive(v, def int64) int64 {
	if v > 0 {
		return v
	}

	return def
}

// requestID returns a random RFC 4122 v4 UUID so retried jobs.query calls
// are idempotent.
func requestID() string {
	var b [16]byte

	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
