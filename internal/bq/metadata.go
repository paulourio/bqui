package bq

import (
	"context"
	"fmt"

	bigquery "google.golang.org/api/bigquery/v2"
)

// Project is one entry of projects.list.
type Project struct {
	ID           string `json:"id"`
	FriendlyName string `json:"friendlyName,omitempty"`
	Number       uint64 `json:"number,omitempty"`
}

// ListProjects returns every project the caller can use with BigQuery.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	var out []Project

	err := c.svc.Projects.List().MaxResults(pageSize).Pages(ctx, func(page *bigquery.ProjectList) error {
		for _, p := range page.Projects {
			id := p.Id
			if p.ProjectReference != nil {
				id = p.ProjectReference.ProjectId
			}

			out = append(out, Project{ID: id, FriendlyName: p.FriendlyName, Number: p.NumericId})
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bq: list projects: %w", err)
	}

	return out, nil
}

const pageSize = 1000

// Dataset is one entry of datasets.list.
type Dataset struct {
	Ref          DatasetRef        `json:"ref"`
	Location     string            `json:"location,omitempty"`
	FriendlyName string            `json:"friendlyName,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
}

// ListDatasets returns the datasets of project (including hidden ones when all is true).
func (c *Client) ListDatasets(ctx context.Context, project string, all bool) ([]Dataset, error) {
	project, err := c.projectOr(project)
	if err != nil {
		return nil, err
	}

	var out []Dataset

	err = c.svc.Datasets.List(project).All(all).MaxResults(pageSize).Pages(ctx, func(page *bigquery.DatasetList) error {
		for _, d := range page.Datasets {
			out = append(out, Dataset{
				Ref:          DatasetRef{Project: d.DatasetReference.ProjectId, Dataset: d.DatasetReference.DatasetId},
				Location:     d.Location,
				FriendlyName: d.FriendlyName,
				Labels:       d.Labels,
			})
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bq: list datasets %s: %w", project, err)
	}

	return out, nil
}

// TableEntry is one entry of tables.list.
type TableEntry struct {
	Ref          TableRef `json:"ref"`
	Type         string   `json:"type"` // TABLE, VIEW, MATERIALIZED_VIEW, EXTERNAL, SNAPSHOT, CLONE
	CreationTime int64    `json:"creationTime,omitempty"`
	Partitioned  bool     `json:"partitioned,omitempty"`
	Clustered    bool     `json:"clustered,omitempty"`
}

// ListTables returns the tables, views and other table-like objects of a dataset.
func (c *Client) ListTables(ctx context.Context, ds DatasetRef) ([]TableEntry, error) {
	var out []TableEntry

	err := c.svc.Tables.List(ds.Project, ds.Dataset).MaxResults(pageSize).Pages(ctx, func(page *bigquery.TableList) error {
		for _, t := range page.Tables {
			out = append(out, TableEntry{
				Ref:          TableRef{Project: t.TableReference.ProjectId, Dataset: t.TableReference.DatasetId, Table: t.TableReference.TableId},
				Type:         t.Type,
				CreationTime: t.CreationTime,
				Partitioned:  t.TimePartitioning != nil || t.RangePartitioning != nil,
				Clustered:    t.Clustering != nil,
			})
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bq: list tables %s.%s: %w", ds.Project, ds.Dataset, err)
	}

	return out, nil
}

// GetDataset returns full dataset metadata.
func (c *Client) GetDataset(ctx context.Context, ds DatasetRef) (*bigquery.Dataset, error) {
	d, err := c.svc.Datasets.Get(ds.Project, ds.Dataset).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("bq: get dataset %s.%s: %w", ds.Project, ds.Dataset, err)
	}

	return d, nil
}

// GetTable returns full table metadata (schema, partitioning, storage stats, view SQL).
func (c *Client) GetTable(ctx context.Context, ref TableRef) (*bigquery.Table, error) {
	t, err := c.svc.Tables.Get(ref.Project, ref.Dataset, ref.Table).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("bq: get table %s: %w", ref, err)
	}

	return t, nil
}

// ListRoutines returns the routines (UDFs, TVFs, procedures) of a dataset.
func (c *Client) ListRoutines(ctx context.Context, ds DatasetRef) ([]*bigquery.Routine, error) {
	var out []*bigquery.Routine

	err := c.svc.Routines.List(ds.Project, ds.Dataset).MaxResults(pageSize).Pages(ctx, func(page *bigquery.ListRoutinesResponse) error {
		out = append(out, page.Routines...)

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bq: list routines %s.%s: %w", ds.Project, ds.Dataset, err)
	}

	return out, nil
}

// ListModels returns the BigQuery ML models of a dataset.
func (c *Client) ListModels(ctx context.Context, ds DatasetRef) ([]*bigquery.Model, error) {
	var out []*bigquery.Model

	err := c.svc.Models.List(ds.Project, ds.Dataset).MaxResults(pageSize).Pages(ctx, func(page *bigquery.ListModelsResponse) error {
		out = append(out, page.Models...)

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bq: list models %s.%s: %w", ds.Project, ds.Dataset, err)
	}

	return out, nil
}

// ObjectRef identifies a dataset-scoped object (table, view, routine, model).
type ObjectRef = TableRef

// GetRoutine returns full routine metadata (arguments, body, return type).
func (c *Client) GetRoutine(ctx context.Context, ref ObjectRef) (*bigquery.Routine, error) {
	r, err := c.svc.Routines.Get(ref.Project, ref.Dataset, ref.Table).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("bq: get routine %s: %w", ref, err)
	}

	return r, nil
}

// GetModel returns full BigQuery ML model metadata.
func (c *Client) GetModel(ctx context.Context, ref ObjectRef) (*bigquery.Model, error) {
	m, err := c.svc.Models.Get(ref.Project, ref.Dataset, ref.Table).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("bq: get model %s: %w", ref, err)
	}

	return m, nil
}
