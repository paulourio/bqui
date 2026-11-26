package app

import (
	"context"

	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/bq"
)

// Backend is the BigQuery metadata surface the explorer and inspector use.
// *bq.Client implements it; FakeBackend serves recorded fixtures.
type Backend interface {
	ListProjects(ctx context.Context) ([]bq.Project, error)
	ListDatasets(ctx context.Context, project string, all bool) ([]bq.Dataset, error)
	ListTables(ctx context.Context, ds bq.DatasetRef) ([]bq.TableEntry, error)
	ListRoutines(ctx context.Context, ds bq.DatasetRef) ([]*bigquery.Routine, error)
	ListModels(ctx context.Context, ds bq.DatasetRef) ([]*bigquery.Model, error)
	GetDataset(ctx context.Context, ds bq.DatasetRef) (*bigquery.Dataset, error)
	GetTable(ctx context.Context, ref bq.TableRef) (*bigquery.Table, error)
	GetRoutine(ctx context.Context, ref bq.ObjectRef) (*bigquery.Routine, error)
	GetModel(ctx context.Context, ref bq.ObjectRef) (*bigquery.Model, error)
}

var _ Backend = (*bq.Client)(nil)
