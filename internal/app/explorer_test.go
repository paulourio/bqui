package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/bq"
)

// uiLoop runs posted functions on a single goroutine, like the AppKit main
// thread, and lets tests run code there synchronously.
type uiLoop struct {
	ch      chan func()
	mu      sync.Mutex
	changes []uint64
}

func newUILoop(t *testing.T) *uiLoop {
	t.Helper()

	l := &uiLoop{ch: make(chan func(), 256)}
	done := make(chan struct{})

	go func() {
		for {
			select {
			case fn := <-l.ch:
				fn()
			case <-done:
				return
			}
		}
	}()

	t.Cleanup(func() { close(done) })

	return l
}

func (l *uiLoop) post(fn func()) { l.ch <- fn }

func (l *uiLoop) do(fn func()) {
	done := make(chan struct{})
	l.ch <- func() { fn(); close(done) }
	<-done
}

func (l *uiLoop) changed(id uint64) {
	l.mu.Lock()
	l.changes = append(l.changes, id)
	l.mu.Unlock()
}

func newTestExplorer(t *testing.T, be Backend) (*Explorer, *uiLoop) {
	t.Helper()

	l := newUILoop(t)
	e := NewExplorer(be, l.post, l.changed)
	t.Cleanup(e.Close)

	return e, l
}

// expandAndWait expands a node and waits until it finished loading.
func expandAndWait(t *testing.T, e *Explorer, l *uiLoop, id uint64) {
	t.Helper()

	l.do(func() { e.Expand(id) })

	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		var done bool

		l.do(func() { done = e.Loaded(id) })

		if done {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatalf("node %d did not load", id)
}

func titles(e *Explorer, l *uiLoop, id uint64) []string {
	var out []string

	l.do(func() {
		for _, k := range e.Children(id) {
			n, _ := e.Tree().Node(k)
			out = append(out, n.Title)
		}
	})

	return out
}

func find(t *testing.T, e *Explorer, l *uiLoop, project, dataset, name string) uint64 {
	t.Helper()

	var (
		id uint64
		ok bool
	)

	l.do(func() { id, ok = e.Find(project, dataset, name) })

	if !ok {
		t.Fatalf("node %s.%s.%s not found", project, dataset, name)
	}

	return id
}

func TestExplorerLazyLoadsAndShowsPlaceholder(t *testing.T) {
	e, l := newTestExplorer(t, &FakeBackend{Latency: 50 * time.Millisecond})
	l.do(func() { e.SetProjects([]string{"bigquery-public-data", FakeProject}) })

	if got := titles(e, l, RootID); strings.Join(got, ",") != "bigquery-public-data,"+FakeProject {
		t.Fatalf("projects = %v", got)
	}

	pub := find(t, e, l, "bigquery-public-data", "", "")

	l.do(func() {
		if !e.Expandable(pub) {
			t.Error("unloaded project must be expandable")
		}

		e.Expand(pub)
	})

	if got := titles(e, l, pub); len(got) != 1 || got[0] != "Loading…" {
		t.Fatalf("while loading = %v", got)
	}

	expandAndWait(t, e, l, pub)

	got := titles(e, l, pub)
	if len(got) != 10 || got[0] != "austin_bikeshare" || got[len(got)-1] != "wikipedia" {
		t.Fatalf("datasets = %v", got)
	}
}

func TestExplorerDatasetKindsShardsRoutinesModels(t *testing.T) {
	e, l := newTestExplorer(t, &FakeBackend{})
	l.do(func() { e.SetProjects([]string{"bigquery-public-data"}) })

	pub := find(t, e, l, "bigquery-public-data", "", "")
	expandAndWait(t, e, l, pub)

	ga := find(t, e, l, "bigquery-public-data", "google_analytics_sample", "")
	expandAndWait(t, e, l, ga)

	if got := titles(e, l, ga); !slices.Contains(got, "ga_sessions_(366)") || len(got) > 10 {
		t.Fatalf("sharded tables not grouped: %v", got)
	}

	group := find(t, e, l, "bigquery-public-data", "google_analytics_sample", "ga_sessions_")

	var info ObjectInfo

	l.do(func() { info, _ = e.Info(group) })

	if info.Kind != KindShardGroup || len(info.Shards) != 366 || info.Shards[0] != "ga_sessions_20170801" {
		t.Fatalf("group = %+v (first %v)", info.Kind, info.Shards[:1])
	}

	if info.SQLName() != "`bigquery-public-data.google_analytics_sample.ga_sessions_*`" || info.ObjectRef().Table != "ga_sessions_20170801" {
		t.Errorf("group refs: %s %v", info.SQLName(), info.ObjectRef())
	}

	crypto := find(t, e, l, "bigquery-public-data", "crypto_bitcoin", "")
	expandAndWait(t, e, l, crypto)

	tx := find(t, e, l, "bigquery-public-data", "crypto_bitcoin", "transactions")
	l.do(func() { info, _ = e.Info(tx) })

	if info.Kind != KindView {
		t.Errorf("transactions kind = %v", info.Kind)
	}

	udfs := find(t, e, l, "bigquery-public-data", "persistent_udfs", "")
	expandAndWait(t, e, l, udfs)

	if n := len(titles(e, l, udfs)); n < 10 {
		t.Errorf("routines = %d", n)
	}

	ml := find(t, e, l, "bigquery-public-data", "ml_datasets", "")
	expandAndWait(t, e, l, ml)

	m := find(t, e, l, "bigquery-public-data", "ml_datasets", "bqml_kmeans_model")
	l.do(func() { info, _ = e.Info(m) })

	if info.Kind != KindModel {
		t.Errorf("model kind = %v", info.Kind)
	}

	ext := find(t, e, l, "bigquery-public-data", "ml_datasets", "holidays_and_events_for_forecasting")
	l.do(func() { info, _ = e.Info(ext) })

	if info.Kind != KindExternalTable {
		t.Errorf("external kind = %v", info.Kind)
	}
}

func TestExplorerScaleAndFilter(t *testing.T) {
	e, l := newTestExplorer(t, &FakeBackend{})
	l.do(func() { e.SetProjects([]string{FakeProject, "bigquery-public-data"}) })

	demo := find(t, e, l, FakeProject, "", "")
	expandAndWait(t, e, l, demo)

	scale := find(t, e, l, FakeProject, FakeScaleDataset, "")
	expandAndWait(t, e, l, scale)

	if n := len(titles(e, l, scale)); n != FakeScaleTables {
		t.Fatalf("scale tables = %d", n)
	}

	pub := find(t, e, l, "bigquery-public-data", "", "")
	expandAndWait(t, e, l, pub)

	samples := find(t, e, l, "bigquery-public-data", "samples", "")
	expandAndWait(t, e, l, samples)

	l.do(func() { e.SetFilter("SHAKES") })

	if got := titles(e, l, RootID); len(got) != 1 || got[0] != "bigquery-public-data" {
		t.Fatalf("filtered roots = %v", got)
	}

	if got := titles(e, l, samples); len(got) != 1 || got[0] != "shakespeare" {
		t.Fatalf("filtered samples = %v", got)
	}

	// A matching container keeps all its children visible.
	l.do(func() { e.SetFilter("samples") })

	if n := len(titles(e, l, samples)); n != 7 {
		t.Errorf("children of matching dataset = %d", n)
	}

	l.do(func() { e.SetFilter("") })

	if n := len(titles(e, l, scale)); n != FakeScaleTables {
		t.Errorf("filter not cleared: %d", n)
	}
}

type failingBackend struct {
	FakeBackend
	fail bool
	mu   sync.Mutex
}

func (f *failingBackend) ListDatasets(ctx context.Context, project string, all bool) ([]bq.Dataset, error) {
	f.mu.Lock()
	fail := f.fail
	f.mu.Unlock()

	if fail {
		return nil, errors.New("bq: list datasets p: googleapi: Error 403: Access Denied")
	}

	return f.FakeBackend.ListDatasets(ctx, project, all)
}

func TestExplorerErrorsAndRetry(t *testing.T) {
	be := &failingBackend{fail: true}
	e, l := newTestExplorer(t, be)
	l.do(func() { e.SetProjects([]string{FakeProject}) })

	demo := find(t, e, l, FakeProject, "", "")
	expandAndWait(t, e, l, demo)

	got := titles(e, l, demo)
	if len(got) != 1 || !strings.Contains(got[0], "Access Denied") {
		t.Fatalf("error placeholder = %v", got)
	}

	be.mu.Lock()
	be.fail = false
	be.mu.Unlock()

	l.do(func() { e.Refresh(demo) })
	expandAndWait(t, e, l, demo)

	if got := titles(e, l, demo); len(got) != 2 {
		t.Fatalf("after refresh = %v", got)
	}
}

func TestBuildDatasetEntriesSingleShardNotGrouped(t *testing.T) {
	o := ObjectInfo{Project: "p", Dataset: "d"}
	l := datasetListing{
		tables: []bq.TableEntry{
			{Ref: bq.TableRef{Table: "events_20240101"}, Type: "TABLE"},
			{Ref: bq.TableRef{Table: "logs_20240101"}, Type: "TABLE"},
			{Ref: bq.TableRef{Table: "logs_20240102"}, Type: "TABLE"},
			{Ref: bq.TableRef{Table: "v_20240101"}, Type: "VIEW"},
		},
		routines: []*bigquery.Routine{{RoutineReference: &bigquery.RoutineReference{RoutineId: "fn"}, RoutineType: "SCALAR_FUNCTION"}},
	}

	var got []string
	for _, en := range buildDatasetEntries(o, l) {
		got = append(got, en.title)
	}

	if strings.Join(got, ",") != "events_20240101,fn,logs_(2),v_20240101" {
		t.Fatalf("entries = %v", got)
	}
}
