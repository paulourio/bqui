// Command bquictl is a headless CLI over bqui's core packages. It is the
// development oracle for everything the GUI does, and runs on Linux.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/paulourio/bqlang/catalog"
	bigquery "google.golang.org/api/bigquery/v2"
	"google.golang.org/api/option"

	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/bqstorage"
)

var version = "dev"

type globals struct {
	project  string
	location string
	asJSON   bool
	stdout   io.Writer
	stderr   io.Writer
}

type command struct {
	name, usage string
	run         func(ctx context.Context, g *globals, c *bq.Client, args []string) error
}

func commands() []command {
	return []command{
		{"whoami", "show the resolved credentials and default project", runWhoami},
		{"projects", "list projects usable with BigQuery", runProjects},
		{"ls", "ls [project | project.dataset]: list datasets or tables", runLs},
		{"show", "show project.dataset.table: table metadata as JSON", runShow},
		{"query", "query [-dry] [-max-bytes N] [-rows N] [-job] <sql | ->: run a query", runQuery},
		{"jobs", "jobs [-all] [-n N] [-state pending|running|done]: list recent jobs", runJobs},
		{"job", "job <job_id>: job details including the query plan summary", runJob},
		{"read", "read [-rows N] [-fields a,b] [-where expr] [-streams N] <table>: Storage Read API as NDJSON", runRead},
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)

	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	g := &globals{stdout: stdout, stderr: stderr}
	fs := flag.NewFlagSet("bquictl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&g.project, "project", "", "billing/default project (default: $GOOGLE_CLOUD_PROJECT or gcloud config)")
	fs.StringVar(&g.location, "location", "", "job location (default: let BigQuery decide)")
	fs.BoolVar(&g.asJSON, "json", false, "print JSON instead of tables")
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.Usage = func() { usage(stderr, fs) }

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *showVersion {
		fmt.Fprintln(stdout, "bquictl", version)

		return 0
	}

	if fs.NArg() == 0 {
		fs.Usage()

		return 2
	}

	var cmd *command

	for _, c := range commands() {
		if c.name == fs.Arg(0) {
			cmd = &c

			break
		}
	}

	if cmd == nil {
		fmt.Fprintf(stderr, "bquictl: unknown command %q\n", fs.Arg(0))
		fs.Usage()

		return 2
	}

	if g.project == "" {
		g.project = bq.DefaultProject(ctx)
	}

	client, err := bq.New(ctx, bq.Options{Project: g.project, Location: g.location})
	if err != nil {
		fmt.Fprintln(stderr, "bquictl:", err)

		return 1
	}

	if err := cmd.run(ctx, g, client, fs.Args()[1:]); err != nil {
		fmt.Fprintln(stderr, "bquictl:", err)

		return 1
	}

	return 0
}

func usage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintln(w, "usage: bquictl [global flags] <command> [args]")
	fmt.Fprintln(w, "\nCommands:")

	for _, c := range commands() {
		fmt.Fprintf(w, "  %-9s %s\n", c.name, c.usage)
	}

	fmt.Fprintln(w, "\nGlobal flags:")
	fs.PrintDefaults()
}

func (g *globals) table(header string, rows [][]string) error {
	tw := tabwriter.NewWriter(g.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, header)

	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}

	return tw.Flush()
}

func (g *globals) json(v any) error {
	enc := json.NewEncoder(g.stdout)
	enc.SetIndent("", "  ")

	return enc.Encode(v)
}

func runWhoami(ctx context.Context, g *globals, c *bq.Client, _ []string) error {
	cred, err := catalog.DescribeCredentials(ctx)
	if err != nil {
		cred = "unknown (" + err.Error() + ")"
	}

	if _, err := c.Tokens().Token(); err != nil {
		return fmt.Errorf("no usable token: %w", err)
	}

	if g.asJSON {
		return g.json(map[string]string{"credentials": cred, "project": c.Project()})
	}

	fmt.Fprintf(g.stdout, "credentials: %s\nproject:     %s\ntoken:       ok\n", cred, c.Project())

	return nil
}

func runProjects(ctx context.Context, g *globals, c *bq.Client, _ []string) error {
	ps, err := c.ListProjects(ctx)
	if err != nil {
		return err
	}

	if g.asJSON {
		return g.json(ps)
	}

	rows := make([][]string, 0, len(ps))
	for _, p := range ps {
		rows = append(rows, []string{p.ID, p.FriendlyName})
	}

	return g.table("PROJECT\tNAME", rows)
}

func runLs(ctx context.Context, g *globals, c *bq.Client, args []string) error {
	target := c.Project()
	if len(args) > 0 {
		target = args[0]
	}

	if ds, err := bq.ParseDatasetRef(target, ""); err == nil && strings.ContainsAny(target, ".:") {
		return lsTables(ctx, g, c, ds)
	}

	sets, err := c.ListDatasets(ctx, target, false)
	if err != nil {
		return err
	}

	if g.asJSON {
		return g.json(sets)
	}

	rows := make([][]string, 0, len(sets))
	for _, d := range sets {
		rows = append(rows, []string{d.Ref.Dataset, d.Location})
	}

	return g.table("DATASET\tLOCATION", rows)
}

func lsTables(ctx context.Context, g *globals, c *bq.Client, ds bq.DatasetRef) error {
	ts, err := c.ListTables(ctx, ds)
	if err != nil {
		return err
	}

	if g.asJSON {
		return g.json(ts)
	}

	rows := make([][]string, 0, len(ts))
	for _, t := range ts {
		var flags []string
		if t.Partitioned {
			flags = append(flags, "partitioned")
		}

		if t.Clustered {
			flags = append(flags, "clustered")
		}

		rows = append(rows, []string{t.Ref.Table, t.Type, strings.Join(flags, ",")})
	}

	return g.table("TABLE\tTYPE\tFLAGS", rows)
}

func runShow(ctx context.Context, g *globals, c *bq.Client, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: show project.dataset.table")
	}

	ref, err := bq.ParseTableRef(args[0], c.Project())
	if err != nil {
		return err
	}

	t, err := c.GetTable(ctx, ref)
	if err != nil {
		return err
	}

	return g.json(t)
}

func runQuery(ctx context.Context, g *globals, c *bq.Client, args []string) error {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	fs.SetOutput(g.stderr)
	dry := fs.Bool("dry", false, "dry run: validate and estimate bytes only")
	maxBytes := fs.Int64("max-bytes", 0, "maximum bytes billed (0 = project default)")
	rows := fs.Int64("rows", 20, "rows to print")
	requireJob := fs.Bool("job", false, "always create a job (enables query plan)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	sql, err := sqlArg(fs.Args())
	if err != nil {
		return err
	}

	res, err := c.Query(ctx, bq.QueryRequest{
		SQL: sql, DryRun: *dry, MaxBytesBilled: *maxBytes, MaxInlineRows: *rows, RequireJob: *requireJob,
		OnProgress: func(j *bigquery.Job) { progress(g, j) },
	})
	if err != nil {
		return err
	}

	summary(g, res)

	if res.DryRun || res.Schema == nil {
		return nil
	}

	decoded, err := bq.DecodeRows(res.Schema.Fields, res.Rows)
	if err != nil {
		return err
	}

	w := bufio.NewWriter(g.stdout)
	defer w.Flush()

	var buf []byte

	for i, r := range decoded {
		if int64(i) >= *rows {
			break
		}

		buf = bq.AppendRowJSON(buf[:0], res.Schema.Fields, r)
		buf = append(buf, '\n')

		if _, err := w.Write(buf); err != nil {
			return err
		}
	}

	return nil
}

func sqlArg(args []string) (string, error) {
	if len(args) == 1 && args[0] == "-" {
		b, err := io.ReadAll(os.Stdin)

		return string(b), err
	}

	if len(args) == 0 {
		return "", errors.New("usage: query <sql | ->")
	}

	return strings.Join(args, " "), nil
}

// progress prints a one-line status for a running job.
func progress(g *globals, j *bigquery.Job) {
	st := j.Statistics
	if st == nil || st.Query == nil {
		return
	}

	done := 0
	for _, s := range st.Query.QueryPlan {
		if s.Status == "COMPLETE" {
			done++
		}
	}

	fmt.Fprintf(g.stderr, "running:   %d/%d stages complete, slot %s\n",
		done, len(st.Query.QueryPlan), time.Duration(st.TotalSlotMs)*time.Millisecond)
}

func summary(g *globals, r *bq.QueryResult) {
	job := "(no job: answered inline)"
	if r.Job != nil {
		job = r.Job.Project + ":" + r.Job.Location + "." + r.Job.JobID
	}

	fmt.Fprintf(g.stderr, "job:       %s\n", job)

	if r.DryRun {
		fmt.Fprintf(g.stderr, "dry run:   %s would be processed\n", humanBytes(r.BytesProc))

		return
	}

	fmt.Fprintf(g.stderr, "statement: %s  rows: %d  processed: %s  billed: %s  slot: %s  cache: %v  elapsed: %s\n",
		r.Statement, r.TotalRows, humanBytes(r.BytesProc), humanBytes(r.BytesBilled),
		time.Duration(r.SlotMs)*time.Millisecond, r.CacheHit, r.Elapsed.Round(time.Millisecond))

	if r.CreationReason != "" {
		fmt.Fprintf(g.stderr, "job reason: %s\n", r.CreationReason)
	}

	if r.Destination != nil {
		fmt.Fprintf(g.stderr, "results:   %s\n", r.Destination)
	}
}

func humanBytes(n int64) string {
	const unit = 1024

	if n < unit {
		return fmt.Sprintf("%d B", n)
	}

	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func runJobs(ctx context.Context, g *globals, c *bq.Client, args []string) error {
	fs := flag.NewFlagSet("jobs", flag.ContinueOnError)
	fs.SetOutput(g.stderr)
	all := fs.Bool("all", false, "include other users' jobs")
	n := fs.Int("n", 20, "number of jobs")
	state := fs.String("state", "", "filter: pending, running or done")

	if err := fs.Parse(args); err != nil {
		return err
	}

	f := bq.JobFilter{AllUsers: *all, Limit: *n}
	if *state != "" {
		f.States = []string{*state}
	}

	page, err := c.ListJobs(ctx, f)
	if err != nil {
		return err
	}

	if g.asJSON {
		return g.json(page.Jobs)
	}

	rows := make([][]string, 0, len(page.Jobs))

	for _, j := range page.Jobs {
		var created, dur, bytes, stmt string

		if st := j.Statistics; st != nil {
			created = time.UnixMilli(st.CreationTime).Format(time.DateTime)
			if st.EndTime > 0 {
				dur = (time.Duration(st.EndTime-st.StartTime) * time.Millisecond).String()
			}

			if st.Query != nil {
				bytes, stmt = humanBytes(st.Query.TotalBytesProcessed), st.Query.StatementType
			}
		}

		status := strings.ToLower(j.State)
		if j.ErrorResult != nil {
			status = "error"
		}

		rows = append(rows, []string{j.JobReference.JobId, status, j.UserEmail, created, dur, bytes, stmt})
	}

	return g.table("JOB\tSTATE\tUSER\tCREATED\tDURATION\tPROCESSED\tSTATEMENT", rows)
}

func runJob(ctx context.Context, g *globals, c *bq.Client, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: job <job_id>")
	}

	ref := bq.JobRef{Project: c.Project(), JobID: args[0], Location: g.location}
	if p, rest, ok := strings.Cut(args[0], ":"); ok {
		ref.Project = p

		if loc, id, ok := strings.Cut(rest, "."); ok {
			ref.Location, ref.JobID = loc, id
		} else {
			ref.JobID = rest
		}
	}

	job, err := c.GetJob(ctx, ref)
	if err != nil {
		return err
	}

	if g.asJSON || job.Statistics == nil || job.Statistics.Query == nil {
		return g.json(job)
	}

	q := job.Statistics.Query
	fmt.Fprintf(g.stdout, "%s  state=%s  statement=%s  processed=%s  slot=%s\n\n",
		job.JobReference.JobId, job.Status.State, q.StatementType, humanBytes(q.TotalBytesProcessed),
		time.Duration(job.Statistics.TotalSlotMs)*time.Millisecond)

	rows := make([][]string, 0, len(q.QueryPlan))
	for _, s := range q.QueryPlan {
		rows = append(rows, []string{
			strconv.FormatInt(s.Id, 10), s.Name, s.Status,
			strconv.FormatInt(s.RecordsRead, 10), strconv.FormatInt(s.RecordsWritten, 10),
			humanBytes(s.ShuffleOutputBytes), (time.Duration(s.SlotMs) * time.Millisecond).String(),
			joinInts(s.InputStages),
		})
	}

	return g.table("ID\tSTAGE\tSTATUS\tREAD\tWRITTEN\tSHUFFLE\tSLOT\tINPUTS", rows)
}

func joinInts(v []int64) string {
	s := make([]string, len(v))
	for i, x := range v {
		s[i] = strconv.FormatInt(x, 10)
	}

	return strings.Join(s, ",")
}

func runRead(ctx context.Context, g *globals, c *bq.Client, args []string) error {
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	fs.SetOutput(g.stderr)
	rows := fs.Int64("rows", 20, "rows to print (0 = count only, -1 = all)")
	fields := fs.String("fields", "", "comma-separated columns")
	where := fs.String("where", "", "row restriction (SQL boolean expression)")
	streams := fs.Int("streams", 4, "maximum parallel streams")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return errors.New("usage: read [flags] project.dataset.table")
	}

	ref, err := bq.ParseTableRef(fs.Arg(0), c.Project())
	if err != nil {
		return err
	}

	sc, err := bqstorage.New(ctx, option.WithTokenSource(c.Tokens()))
	if err != nil {
		return err
	}
	defer sc.Close()

	opts := bqstorage.ReadOptions{BillingProject: c.Project(), RowRestriction: *where, MaxStreams: *streams}
	if *fields != "" {
		opts.Fields = strings.Split(*fields, ",")
	}

	start := time.Now()

	sess, err := sc.Open(ctx, bqstorage.Table{Project: ref.Project, Dataset: ref.Dataset, Table: ref.Table}, opts)
	if err != nil {
		return err
	}

	fmt.Fprintf(g.stderr, "session: %d streams, ~%d rows, ~%s scanned\n", sess.Streams(), sess.EstimatedRows(), humanBytes(sess.EstimatedBytes()))

	var total, batches int64

	for b, err := range sess.Batches(ctx) {
		if err != nil {
			return err
		}

		batches++

		if *rows < 0 || total < *rows {
			n := b.Record.NumRows()
			if *rows >= 0 {
				n = min(n, *rows-total)
			}

			slice := b.Record.NewSlice(0, n)
			err = array.RecordToJSON(slice, g.stdout)
			slice.Release()

			if err != nil {
				b.Record.Release()

				return err
			}
		}

		total += b.Record.NumRows()
		b.Record.Release()
	}

	fmt.Fprintf(g.stderr, "read %d rows in %d batches in %s\n", total, batches, time.Since(start).Round(time.Millisecond))

	return nil
}
