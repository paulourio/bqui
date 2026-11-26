package bq

import (
	"context"
	"fmt"
	"time"

	bigquery "google.golang.org/api/bigquery/v2"
)

// JobRef identifies a job.
type JobRef struct {
	Project  string `json:"projectId"`
	JobID    string `json:"jobId"`
	Location string `json:"location,omitempty"`
}

func jobRefFrom(r *bigquery.JobReference) *JobRef {
	if r == nil || r.JobId == "" {
		return nil
	}

	return &JobRef{Project: r.ProjectId, JobID: r.JobId, Location: r.Location}
}

// JobFilter selects jobs for ListJobs.
type JobFilter struct {
	Project     string
	AllUsers    bool
	States      []string // "pending", "running", "done"
	MinCreation time.Time
	MaxCreation time.Time
	ParentJobID string
	Limit       int // 0 means one page of 100
	PageToken   string
}

// JobPage is one page of jobs.list (projection=full).
type JobPage struct {
	Jobs          []*bigquery.JobListJobs
	NextPageToken string
}

const defaultJobPage = 100

// ListJobs returns one page of jobs, newest first.
func (c *Client) ListJobs(ctx context.Context, f JobFilter) (*JobPage, error) {
	project, err := c.projectOr(f.Project)
	if err != nil {
		return nil, err
	}

	limit := f.Limit
	if limit <= 0 {
		limit = defaultJobPage
	}

	call := c.svc.Jobs.List(project).Projection("full").AllUsers(f.AllUsers).MaxResults(int64(limit)).Context(ctx)
	if len(f.States) > 0 {
		call = call.StateFilter(f.States...)
	}

	if !f.MinCreation.IsZero() {
		call = call.MinCreationTime(uint64(f.MinCreation.UnixMilli()))
	}

	if !f.MaxCreation.IsZero() {
		call = call.MaxCreationTime(uint64(f.MaxCreation.UnixMilli()))
	}

	if f.ParentJobID != "" {
		call = call.ParentJobId(f.ParentJobID)
	}

	if f.PageToken != "" {
		call = call.PageToken(f.PageToken)
	}

	resp, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("bq: list jobs %s: %w", project, err)
	}

	return &JobPage{Jobs: resp.Jobs, NextPageToken: resp.NextPageToken}, nil
}

// GetJob returns full job metadata, including statistics.query.queryPlan.
func (c *Client) GetJob(ctx context.Context, ref JobRef) (*bigquery.Job, error) {
	call := c.svc.Jobs.Get(ref.Project, ref.JobID).Context(ctx)
	if loc := firstNonEmpty(ref.Location, c.location); loc != "" {
		call = call.Location(loc)
	}

	job, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("bq: get job %s: %w", ref.JobID, err)
	}

	return job, nil
}

// CancelJob requests cancellation; BigQuery cancels asynchronously.
func (c *Client) CancelJob(ctx context.Context, ref JobRef) error {
	call := c.svc.Jobs.Cancel(ref.Project, ref.JobID).Context(ctx)
	if loc := firstNonEmpty(ref.Location, c.location); loc != "" {
		call = call.Location(loc)
	}

	if _, err := call.Do(); err != nil {
		return fmt.Errorf("bq: cancel job %s: %w", ref.JobID, err)
	}

	return nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}

	return ""
}
