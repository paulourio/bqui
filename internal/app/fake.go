package app

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"

	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/bq"
)

// fixtures were recorded from public bigquery-public-data metadata (etags,
// self links and dataset access lists stripped).
//
//go:embed fixtures/*.json
var fixtures embed.FS

// Fake project layout, besides the recorded bigquery-public-data subset.
const (
	FakeProject      = "bqui-demo"
	FakeScaleDataset = "scale_test"
	FakeScaleTables  = 2000
	fakePublic       = "bigquery-public-data"
)

// ErrFakeNotFound mimics a 404.
var ErrFakeNotFound = errors.New("fake: not found")

// FakeBackend serves recorded metadata plus a synthetic project with a large
// dataset, for deterministic UI tests and offline demos.
type FakeBackend struct {
	// Latency is added to every call to exercise loading states.
	Latency time.Duration

	mu      sync.Mutex
	nextJob int
	jobs    []*bigquery.Job // newest last
	totals  map[string]int  // result rows per job ID
	seeded  bool
}

var _ Backend = (*FakeBackend)(nil)

func (f *FakeBackend) wait(ctx context.Context) error {
	if f.Latency <= 0 {
		return ctx.Err()
	}

	select {
	case <-time.After(f.Latency):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func readFixture(name string, v any) error {
	b, err := fs.ReadFile(fixtures, "fixtures/"+name+".json")
	if err != nil {
		return fmt.Errorf("%w: %s", ErrFakeNotFound, name)
	}

	return json.Unmarshal(b, v)
}

// ListProjects implements Backend.
func (f *FakeBackend) ListProjects(ctx context.Context) ([]bq.Project, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}

	return []bq.Project{{ID: FakeProject, FriendlyName: "bqui demo"}, {ID: fakePublic, FriendlyName: "BigQuery public data"}}, nil
}

// ListDatasets implements Backend.
func (f *FakeBackend) ListDatasets(ctx context.Context, project string, _ bool) ([]bq.Dataset, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}

	if project == FakeProject {
		return []bq.Dataset{
			{Ref: bq.DatasetRef{Project: FakeProject, Dataset: "analytics"}, Location: "US"},
			{Ref: bq.DatasetRef{Project: FakeProject, Dataset: FakeScaleDataset}, Location: "US"},
		}, nil
	}

	var list bigquery.DatasetList
	if err := readFixture("datasets."+project, &list); err != nil {
		return nil, err
	}

	out := make([]bq.Dataset, 0, len(list.Datasets))
	for _, d := range list.Datasets {
		out = append(out, bq.Dataset{
			Ref:      bq.DatasetRef{Project: d.DatasetReference.ProjectId, Dataset: d.DatasetReference.DatasetId},
			Location: d.Location,
		})
	}

	return out, nil
}

// ListTables implements Backend.
func (f *FakeBackend) ListTables(ctx context.Context, ds bq.DatasetRef) ([]bq.TableEntry, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}

	if ds.Project == FakeProject {
		return fakeTables(ds), nil
	}

	var list bigquery.TableList
	if err := readFixture("tables."+ds.Project+"."+ds.Dataset, &list); err != nil {
		return nil, nil //nolint:nilerr // datasets without recorded tables are empty
	}

	out := make([]bq.TableEntry, 0, len(list.Tables))
	for _, t := range list.Tables {
		out = append(out, bq.TableEntry{
			Ref:          bq.TableRef{Project: t.TableReference.ProjectId, Dataset: t.TableReference.DatasetId, Table: t.TableReference.TableId},
			Type:         t.Type,
			CreationTime: t.CreationTime,
			Partitioned:  t.TimePartitioning != nil || t.RangePartitioning != nil,
			Clustered:    t.Clustering != nil,
		})
	}

	return out, nil
}

func fakeTables(ds bq.DatasetRef) []bq.TableEntry {
	ref := func(name string) bq.TableRef {
		return bq.TableRef{Project: ds.Project, Dataset: ds.Dataset, Table: name}
	}

	if ds.Dataset == FakeScaleDataset {
		out := make([]bq.TableEntry, 0, FakeScaleTables)
		for i := range FakeScaleTables {
			out = append(out, bq.TableEntry{Ref: ref(fmt.Sprintf("table_%04d", i)), Type: "TABLE"})
		}

		return out
	}

	return []bq.TableEntry{
		{Ref: ref("events"), Type: "TABLE", Partitioned: true, Clustered: true},
		{Ref: ref("events_daily"), Type: "MATERIALIZED_VIEW"},
		{Ref: ref("users"), Type: "TABLE"},
		{Ref: ref("v_active_users"), Type: "VIEW"},
	}
}

// ListRoutines implements Backend.
func (f *FakeBackend) ListRoutines(ctx context.Context, ds bq.DatasetRef) ([]*bigquery.Routine, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}

	var list bigquery.ListRoutinesResponse
	if err := readFixture("routines."+ds.Project+"."+ds.Dataset, &list); err != nil {
		return nil, nil //nolint:nilerr // most datasets have no routines
	}

	return list.Routines, nil
}

// ListModels implements Backend.
func (f *FakeBackend) ListModels(ctx context.Context, ds bq.DatasetRef) ([]*bigquery.Model, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}

	var list bigquery.ListModelsResponse
	if err := readFixture("models."+ds.Project+"."+ds.Dataset, &list); err != nil {
		return nil, nil //nolint:nilerr // most datasets have no models
	}

	return list.Models, nil
}

// GetDataset implements Backend.
func (f *FakeBackend) GetDataset(ctx context.Context, ds bq.DatasetRef) (*bigquery.Dataset, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}

	var d bigquery.Dataset
	if readFixture("dataset."+ds.Project+"."+ds.Dataset, &d) != nil {
		//nolint:nilerr // unrecorded datasets get synthetic metadata by design
		return &bigquery.Dataset{
			DatasetReference: &bigquery.DatasetReference{ProjectId: ds.Project, DatasetId: ds.Dataset},
			Location:         "US",
			Description:      "Synthetic dataset served by bqui's fake backend.",
		}, nil
	}

	return &d, nil
}

// GetTable implements Backend.
func (f *FakeBackend) GetTable(ctx context.Context, ref bq.TableRef) (*bigquery.Table, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}

	var t bigquery.Table
	if err := readFixture("table."+ref.String(), &t); err == nil {
		return &t, nil
	}

	if ref.Project != FakeProject && !strings.HasPrefix(ref.Table, "ga_sessions_") {
		return nil, fmt.Errorf("%w: table %s", ErrFakeNotFound, ref)
	}

	return syntheticTable(ref), nil
}

func syntheticTable(ref bq.TableRef) *bigquery.Table {
	if strings.HasPrefix(ref.Table, "v_") {
		return &bigquery.Table{
			TableReference: &bigquery.TableReference{ProjectId: ref.Project, DatasetId: ref.Dataset, TableId: ref.Table},
			Type:           "VIEW",
			Location:       "US",
			CreationTime:   1_700_000_000_000,
			View: &bigquery.ViewDefinition{Query: "select user_id, max(event_ts) as last_seen from `" + ref.Project + "." +
				ref.Dataset + ".events` where event_ts > timestamp_sub(current_timestamp(), interval 30 day) group by user_id"},
			Schema: &bigquery.TableSchema{Fields: []*bigquery.TableFieldSchema{
				{Name: "user_id", Type: "STRING"},
				{Name: "last_seen", Type: "TIMESTAMP"},
			}},
		}
	}

	return &bigquery.Table{
		TableReference: &bigquery.TableReference{ProjectId: ref.Project, DatasetId: ref.Dataset, TableId: ref.Table},
		Type:           "TABLE",
		Location:       "US",
		CreationTime:   1_700_000_000_000,
		NumRows:        1234,
		NumBytes:       56789,
		Schema: &bigquery.TableSchema{Fields: []*bigquery.TableFieldSchema{
			{Name: "id", Type: "INTEGER", Mode: "REQUIRED"},
			{Name: "created_at", Type: "TIMESTAMP"},
			{Name: "payload", Type: "JSON"},
		}},
	}
}

// GetRoutine implements Backend.
func (f *FakeBackend) GetRoutine(ctx context.Context, ref bq.ObjectRef) (*bigquery.Routine, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}

	var r bigquery.Routine
	if err := readFixture("routine."+ref.String(), &r); err != nil {
		// Fall back to the list entry, which has the reference and type.
		list, lerr := f.ListRoutines(ctx, bq.DatasetRef{Project: ref.Project, Dataset: ref.Dataset})
		if lerr != nil {
			return nil, lerr
		}

		for _, x := range list {
			if x.RoutineReference != nil && x.RoutineReference.RoutineId == ref.Table {
				return x, nil
			}
		}

		return nil, err
	}

	return &r, nil
}

// GetModel implements Backend.
func (f *FakeBackend) GetModel(ctx context.Context, ref bq.ObjectRef) (*bigquery.Model, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}

	var m bigquery.Model
	if err := readFixture("model."+ref.String(), &m); err != nil {
		list, lerr := f.ListModels(ctx, bq.DatasetRef{Project: ref.Project, Dataset: ref.Dataset})
		if lerr != nil {
			return nil, lerr
		}

		for _, x := range list {
			if x.ModelReference != nil && x.ModelReference.ModelId == ref.Table {
				return x, nil
			}
		}

		return nil, err
	}

	return &m, nil
}
