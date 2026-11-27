package app

import (
	"context"
	"strings"
	"time"

	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/store"
)

// JobsBackend lists, inspects and cancels jobs.
type JobsBackend interface {
	ListJobs(ctx context.Context, f bq.JobFilter) (*bq.JobPage, error)
	GetJob(ctx context.Context, ref bq.JobRef) (*bigquery.Job, error)
	CancelJob(ctx context.Context, ref bq.JobRef) error
}

var _ JobsBackend = (*bq.Client)(nil)

// JobRow is one line of the job history.
type JobRow struct {
	Ref       bq.JobRef
	State     string // pending, running, done, error
	Type      string // QUERY, LOAD, EXTRACT, COPY
	User      string
	Created   time.Time
	Duration  time.Duration
	Processed int64
	SlotMs    int64
	Statement string
	CacheHit  bool
	SQL       string
	Error     string
}

// Summary returns the query collapsed to one line.
func (r JobRow) Summary() string {
	return strings.Join(strings.Fields(r.SQL), " ")
}

// JobRowFromList converts a jobs.list entry.
func JobRowFromList(j *bigquery.JobListJobs) JobRow {
	if j == nil {
		return JobRow{}
	}

	r := JobRow{User: j.UserEmail, State: strings.ToLower(j.State)}

	if ref := j.JobReference; ref != nil {
		r.Ref = bq.JobRef{Project: ref.ProjectId, JobID: ref.JobId, Location: ref.Location}
	}

	if j.ErrorResult != nil {
		r.State, r.Error = "error", j.ErrorResult.Message
	}

	fillJobRow(&r, j.Configuration, j.Statistics)

	return r
}

// JobRowFromJob converts a full job.
func JobRowFromJob(j *bigquery.Job) JobRow {
	if j == nil {
		return JobRow{}
	}

	r := JobRow{User: j.UserEmail}

	if ref := j.JobReference; ref != nil {
		r.Ref = bq.JobRef{Project: ref.ProjectId, JobID: ref.JobId, Location: ref.Location}
	}

	if st := j.Status; st != nil {
		r.State = strings.ToLower(st.State)
		if st.ErrorResult != nil {
			r.State, r.Error = "error", st.ErrorResult.Message
		}
	}

	fillJobRow(&r, j.Configuration, j.Statistics)

	return r
}

func fillJobRow(r *JobRow, cfg *bigquery.JobConfiguration, st *bigquery.JobStatistics) {
	if cfg != nil {
		r.Type = cfg.JobType
		if cfg.Query != nil {
			r.SQL = cfg.Query.Query
		}
	}

	if st == nil {
		return
	}

	r.Created = time.UnixMilli(st.CreationTime)
	r.SlotMs = st.TotalSlotMs

	if st.EndTime > 0 && st.StartTime > 0 {
		r.Duration = time.Duration(st.EndTime-st.StartTime) * time.Millisecond
	}

	if q := st.Query; q != nil {
		r.Processed, r.Statement, r.CacheHit = q.TotalBytesProcessed, q.StatementType, q.CacheHit
	}
}

// Destination returns the job's result table, if any.
func Destination(j *bigquery.Job) *bq.TableRef {
	if j == nil || j.Configuration == nil || j.Configuration.Query == nil || j.Configuration.Query.DestinationTable == nil {
		return nil
	}

	d := j.Configuration.Query.DestinationTable

	return &bq.TableRef{Project: d.ProjectId, Dataset: d.DatasetId, Table: d.TableId}
}

// JobSections renders the console's "Job information" for a job.
func (f Formatter) JobSections(j *bigquery.Job) []Section {
	if j == nil {
		return nil
	}

	row := JobRowFromJob(j)

	query := Section{Title: "Query"}
	if row.SQL != "" {
		query.mono("SQL", FormatSQL(row.SQL))
	}

	return nonEmpty(f.jobInfo(j, row), jobErrors(j), jobStats(j), jobTables(j), query)
}

func (f Formatter) jobInfo(j *bigquery.Job, row JobRow) Section {
	info := Section{Title: "Job information"}
	info.mono("Job ID", row.Ref.Project+":"+row.Ref.Location+"."+row.Ref.JobID)
	info.add("User", row.User)
	info.add("Location", row.Ref.Location)
	info.add("State", strings.ToUpper(row.State))
	info.add("Job type", row.Type)
	info.add("Statement", row.Statement)

	if st := j.Statistics; st != nil {
		info.add("Created", f.Millis(st.CreationTime))
		info.add("Started", f.Millis(st.StartTime))
		info.add("Ended", f.Millis(st.EndTime))

		if row.Duration > 0 {
			info.add("Duration", row.Duration.Round(time.Millisecond).String())
		}

		info.mono("Parent job", st.ParentJobId)

		if st.NumChildJobs > 0 {
			info.add("Child jobs", Count(uint64(st.NumChildJobs)))
		}

		if st.SessionInfo != nil {
			info.mono("Session", st.SessionInfo.SessionId)
		}

		info.mono("Reservation", st.ReservationId)
	}

	if cfg := j.Configuration; cfg != nil && cfg.Query != nil {
		info.add("Priority", cfg.Query.Priority)
		info.add("Labels", labels(cfg.Labels))

		if cfg.Query.MaximumBytesBilled > 0 {
			info.add("Maximum bytes billed", Bytes(cfg.Query.MaximumBytesBilled))
		}
	}

	return info
}

func jobStats(j *bigquery.Job) Section {
	stats := Section{Title: "Statistics"}

	st := j.Statistics
	if st == nil {
		return stats
	}

	if q := st.Query; q != nil {
		stats.add("Bytes processed", bytesOrZero(q.TotalBytesProcessed))
		stats.add("Bytes billed", bytesOrZero(q.TotalBytesBilled))
		stats.add("Cache hit", yesNo(q.CacheHit))

		for _, c := range []struct {
			k string
			v int64
		}{
			{"Rows affected", q.NumDmlAffectedRows},
			{"Partitions processed", q.TotalPartitionsProcessed},
			{"Stages", int64(len(q.QueryPlan))},
		} {
			if c.v > 0 {
				stats.add(c.k, Count(uint64(c.v)))
			}
		}
	}

	if st.TotalSlotMs > 0 {
		stats.add("Slot time", (time.Duration(st.TotalSlotMs) * time.Millisecond).String())
	}

	return stats
}

func jobTables(j *bigquery.Job) Section {
	tables := Section{Title: "Tables"}

	if d := Destination(j); d != nil {
		tables.mono("Destination", d.String())
	}

	if st := j.Statistics; st != nil && st.Query != nil {
		for _, t := range st.Query.ReferencedTables {
			tables.mono("Referenced", t.ProjectId+"."+t.DatasetId+"."+t.TableId)
		}
	}

	return tables
}

func jobErrors(j *bigquery.Job) Section {
	errs := Section{Title: "Errors"}

	st := j.Status
	if st == nil {
		return errs
	}

	if st.ErrorResult != nil {
		errs.add("Error", errorText(st.ErrorResult))
	}

	for _, e := range st.Errors {
		if st.ErrorResult == nil || e.Message != st.ErrorResult.Message {
			errs.add("Detail", errorText(e))
		}
	}

	return errs
}

func bytesOrZero(n int64) string {
	if n == 0 {
		return "0 B"
	}

	return Bytes(n)
}

func errorText(e *bigquery.ErrorProto) string {
	s := e.Message
	if e.Reason != "" {
		s += " (" + e.Reason + ")"
	}

	if e.Location != "" {
		s += " at " + e.Location
	}

	return s
}

// History is the job-history view model: one project's jobs, newest first,
// loaded page by page. Methods run on the UI thread; fetches go through post.
type History struct {
	be      JobsBackend
	post    func(func())
	changed func()

	project  string
	allUsers bool
	rows     []JobRow
	next     string
	loading  bool
	err      error
	gen      int
}

// HistoryPage is how many jobs one fetch returns.
const HistoryPage = 50

// NewHistory creates an empty history.
func NewHistory(be JobsBackend, post func(func()), changed func()) *History {
	return &History{be: be, post: post, changed: changed}
}

// Reload starts over for a project (allUsers: project-wide history).
func (h *History) Reload(project string, allUsers bool) {
	h.gen++
	h.project, h.allUsers = project, allUsers
	h.rows, h.next, h.err, h.loading = nil, "", nil, false
	h.changed()
	h.fetch()
}

// LoadMore fetches the next page if there is one.
func (h *History) LoadMore() {
	if h.next != "" && !h.loading {
		h.fetch()
	}
}

// Rows returns the loaded jobs.
func (h *History) Rows() []JobRow { return h.rows }

// Loading reports whether a fetch is in flight; HasMore whether more exist.
func (h *History) Loading() bool { return h.loading }

// HasMore reports whether another page exists.
func (h *History) HasMore() bool { return h.next != "" }

// Err returns the last fetch error.
func (h *History) Err() error { return h.err }

// AllUsers reports whether project-wide history is shown.
func (h *History) AllUsers() bool { return h.allUsers }

func (h *History) fetch() {
	if h.project == "" {
		return
	}

	h.loading = true
	gen, project, all, token := h.gen, h.project, h.allUsers, h.next

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()

		page, err := h.be.ListJobs(ctx, bq.JobFilter{Project: project, AllUsers: all, Limit: HistoryPage, PageToken: token})

		h.post(func() {
			if gen != h.gen {
				return
			}

			h.loading, h.err = false, err

			if err == nil {
				for _, j := range page.Jobs {
					h.rows = append(h.rows, JobRowFromList(j))
				}

				h.next = page.NextPageToken
			}

			h.changed()
		})
	}()
}

// LocalUser labels runs from the local history (they may predate jobs.list
// retention, or have no job at all).
const LocalUser = "this Mac"

// JobRowsFromRuns converts local run history to job rows.
func JobRowsFromRuns(runs []store.Run) []JobRow {
	out := make([]JobRow, 0, len(runs))

	for _, r := range runs {
		state := r.State
		if state == "failed" || state == "cancelled" {
			state = "error"
		}

		out = append(out, JobRow{
			Ref:   bq.JobRef{Project: r.Project, JobID: r.JobID, Location: r.Location},
			State: state, Type: "QUERY", User: LocalUser, Created: r.Started, Duration: r.Elapsed,
			Processed: r.Bytes, SQL: r.SQL, Error: r.Error,
		})
	}

	return out
}

// FilterJobRows keeps rows whose SQL, job ID or user contain every word of
// q (case-insensitive).
func FilterJobRows(rows []JobRow, q string) []JobRow {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return rows
	}

	var out []JobRow

	for _, r := range rows {
		hay := strings.ToLower(r.SQL + " " + r.Ref.JobID + " " + r.User)
		match := true

		for _, w := range words {
			if !strings.Contains(hay, w) {
				match = false

				break
			}
		}

		if match {
			out = append(out, r)
		}
	}

	return out
}
