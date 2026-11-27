package app

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/bq"
)

const loadTimeout = 60 * time.Second

// ObjectInfo describes the BigQuery object behind an explorer node.
type ObjectInfo struct {
	ID        uint64
	Kind      NodeKind
	Project   string
	Dataset   string
	Name      string   // table, view, routine or model ID; shard prefix for groups
	TableType string   // TABLE, VIEW, MATERIALIZED_VIEW, EXTERNAL, SNAPSHOT, CLONE
	Shards    []string // table IDs of a shard group, newest first
}

// DatasetRef returns the dataset the object lives in.
func (o ObjectInfo) DatasetRef() bq.DatasetRef {
	return bq.DatasetRef{Project: o.Project, Dataset: o.Dataset}
}

// ObjectRef returns the object's reference. For a shard group it is the
// newest shard.
func (o ObjectInfo) ObjectRef() bq.ObjectRef {
	name := o.Name
	if o.Kind == KindShardGroup && len(o.Shards) > 0 {
		name = o.Shards[0]
	}

	return bq.ObjectRef{Project: o.Project, Dataset: o.Dataset, Table: name}
}

// SQLName returns the reference as written in GoogleSQL (wildcard for shard groups).
func (o ObjectInfo) SQLName() string {
	switch o.Kind {
	case KindProject:
		return "`" + o.Project + "`"
	case KindDataset:
		return "`" + o.Project + "." + o.Dataset + "`"
	case KindShardGroup:
		return "`" + o.Project + "." + o.Dataset + "." + o.Name + "*`"
	default:
		return "`" + o.Project + "." + o.Dataset + "." + o.Name + "`"
	}
}

// FullID returns the dotted identifier (project, project.dataset or project.dataset.name).
func (o ObjectInfo) FullID() string {
	switch o.Kind {
	case KindProject:
		return o.Project
	case KindDataset:
		return o.Project + "." + o.Dataset
	default:
		return o.Project + "." + o.Dataset + "." + o.Name
	}
}

type loadState int

const (
	unloaded loadState = iota
	loading
	loaded
	failed
)

// Explorer is the view model of the dataset browser: a lazily loaded
// project → dataset → object tree over a Backend.
//
// Threading: every method must be called on the UI thread. Fetches run on
// goroutines; their results are applied through post (which runs on the UI
// thread), so the tree only changes on the UI thread. changed(id) asks the
// UI to reload id and its children (0 = everything).
type Explorer struct {
	be      Backend
	post    func(func())
	changed func(id uint64)

	tree   *Tree
	info   map[uint64]ObjectInfo
	state  map[uint64]loadState
	filter string
	vis    map[uint64]bool // nil when no filter

	starred     map[string]bool // "project.dataset"
	starredOnly bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewExplorer creates an explorer. post must run fn on the UI thread later;
// changed is called on the UI thread.
func NewExplorer(be Backend, post func(func()), changed func(id uint64)) *Explorer {
	ctx, cancel := context.WithCancel(context.Background())

	return &Explorer{
		be: be, post: post, changed: changed,
		tree: NewTree(), info: make(map[uint64]ObjectInfo), state: make(map[uint64]loadState),
		ctx: ctx, cancel: cancel,
	}
}

// Close cancels outstanding loads and waits for them.
func (e *Explorer) Close() {
	e.cancel()
	e.wg.Wait()
}

// Tree returns the underlying tree.
func (e *Explorer) Tree() *Tree { return e.tree }

// SetProjects replaces the top-level project list.
func (e *Explorer) SetProjects(projects []string) {
	e.tree.Clear(RootID)
	e.info = make(map[uint64]ObjectInfo)
	e.state = make(map[uint64]loadState)

	for _, p := range projects {
		id := e.tree.Add(RootID, KindProject, p, "")
		e.info[id] = ObjectInfo{ID: id, Kind: KindProject, Project: p}
	}

	e.refilter()
	e.changed(RootID)
}

// Info returns the object behind a node.
func (e *Explorer) Info(id uint64) (ObjectInfo, bool) {
	o, ok := e.info[id]

	return o, ok
}

// Loaded reports whether a container finished loading (successfully or not).
func (e *Explorer) Loaded(id uint64) bool {
	s := e.state[id]

	return s == loaded || s == failed
}

// Find returns the node for project[.dataset[.name]], if it is in the tree.
func (e *Explorer) Find(project, dataset, name string) (uint64, bool) {
	for id, o := range e.info {
		if o.Project == project && o.Dataset == dataset && o.Name == name {
			return id, true
		}
	}

	return 0, false
}

// Children returns the visible children of id.
func (e *Explorer) Children(id uint64) []uint64 {
	kids := e.tree.Children(id)
	if e.vis == nil {
		return kids
	}

	out := kids[:0]

	for _, k := range kids {
		if e.vis[k] {
			out = append(out, k)
		}
	}

	return out
}

// Expandable reports whether id shows a disclosure triangle.
func (e *Explorer) Expandable(id uint64) bool {
	n, ok := e.tree.Node(id)
	if !ok || !n.Kind.Container() {
		return false
	}

	return e.state[id] != loaded || len(n.Children) > 0
}

// Expand starts loading id's children if needed. Failed loads are retried.
func (e *Explorer) Expand(id uint64) {
	o, ok := e.info[id]
	if !ok {
		return
	}

	switch e.state[id] {
	case loading, loaded:
		return
	case unloaded, failed:
	}

	switch o.Kind {
	case KindProject, KindDataset:
	default:
		return
	}

	e.state[id] = loading
	e.tree.Clear(id)
	e.tree.Add(id, KindPlaceholder, "Loading…", "")
	e.refilter()
	e.changed(id)

	e.wg.Go(func() {
		ctx, cancel := context.WithTimeout(e.ctx, loadTimeout)
		defer cancel()

		var apply func()
		if o.Kind == KindProject {
			apply = e.loadProject(ctx, id, o)
		} else {
			apply = e.loadDataset(ctx, id, o)
		}

		if ctx.Err() != nil && e.ctx.Err() != nil {
			return // explorer closed
		}

		e.post(apply)
	})
}

// Refresh reloads a project or dataset.
func (e *Explorer) Refresh(id uint64) {
	if e.state[id] == loading {
		return
	}

	for k := range e.info {
		if e.isDescendant(k, id) {
			delete(e.info, k)
			delete(e.state, k)
		}
	}

	e.state[id] = unloaded
	e.Expand(id)
}

func (e *Explorer) isDescendant(k, ancestor uint64) bool {
	for n, ok := e.tree.Node(k); ok && n.ID != RootID; n, ok = e.tree.Node(n.Parent) {
		if n.Parent == ancestor {
			return true
		}
	}

	return false
}

// SetFilter shows only nodes whose title contains q (case-insensitive), their
// ancestors, and the descendants of matching containers. "" clears it.
func (e *Explorer) SetFilter(q string) {
	e.filter = strings.ToLower(strings.TrimSpace(q))
	e.refilter()
	e.changed(RootID)
}

// SetStarred replaces the starred datasets ("project.dataset" keys).
func (e *Explorer) SetStarred(keys []string) {
	e.starred = make(map[string]bool, len(keys))
	for _, k := range keys {
		e.starred[k] = true
	}

	e.refilter()
	e.changed(RootID)
}

// SetStarredOnly shows only starred datasets (and their projects).
func (e *Explorer) SetStarredOnly(on bool) {
	e.starredOnly = on
	e.refilter()
	e.changed(RootID)
}

// StarredOnly reports whether the starred filter is on.
func (e *Explorer) StarredOnly() bool { return e.starredOnly }

// IsStarred reports whether the node is a starred dataset.
func (e *Explorer) IsStarred(id uint64) bool {
	o, ok := e.info[id]

	return ok && o.Kind == KindDataset && e.starred[o.Project+"."+o.Dataset]
}

// StarredProjects returns project nodes that hold starred datasets.
func (e *Explorer) StarredProjects() []uint64 {
	var out []uint64

	for _, id := range e.tree.Children(RootID) {
		if e.projectHasStar(e.info[id].Project) {
			out = append(out, id)
		}
	}

	return out
}

func (e *Explorer) projectHasStar(project string) bool {
	for k := range e.starred {
		if strings.HasPrefix(k, project+".") {
			return true
		}
	}

	return false
}

// starVisible is the starred-only rule for one node.
func (e *Explorer) starVisible(id uint64) bool {
	n, _ := e.tree.Node(id)
	o := e.info[id]

	switch {
	case id == RootID:
		return true
	case n.Kind == KindProject:
		return e.projectHasStar(o.Project)
	case n.Kind == KindPlaceholder:
		return true // "Loading…" and errors under a visible parent
	case o.Dataset != "":
		return e.starred[o.Project+"."+o.Dataset]
	}

	return false
}

func (e *Explorer) refilter() {
	e.vis = nil

	if e.filter != "" {
		e.textFilter()
	}

	if !e.starredOnly {
		return
	}

	text := e.vis
	e.vis = make(map[uint64]bool)

	var walk func(id uint64)

	walk = func(id uint64) {
		if !e.starVisible(id) || text != nil && !text[id] && id != RootID {
			return
		}

		e.vis[id] = true

		for _, k := range e.tree.Children(id) {
			walk(k)
		}
	}

	walk(RootID)
}

func (e *Explorer) textFilter() {
	e.vis = make(map[uint64]bool)

	var walk func(id uint64, ancestorMatch bool) bool

	walk = func(id uint64, ancestorMatch bool) bool {
		n, _ := e.tree.Node(id)
		match := strings.Contains(strings.ToLower(n.Title), e.filter) && n.Kind != KindPlaceholder
		childVisible := false

		for _, k := range n.Children {
			if walk(k, ancestorMatch || match) {
				childVisible = true
			}
		}

		visible := match || ancestorMatch || childVisible
		if visible {
			e.vis[id] = true
		}

		return visible
	}

	walk(RootID, false)
}

func (e *Explorer) fail(id uint64, err error) func() {
	return func() {
		e.tree.Clear(id)
		e.tree.Add(id, KindPlaceholder, "⚠︎ "+shortError(err), err.Error())
		e.state[id] = failed
		e.refilter()
		e.changed(id)
	}
}

func (e *Explorer) loadProject(ctx context.Context, id uint64, o ObjectInfo) func() {
	sets, err := e.be.ListDatasets(ctx, o.Project, false)
	if err != nil {
		return e.fail(id, err)
	}

	slices.SortFunc(sets, func(a, b bq.Dataset) int {
		return strings.Compare(strings.ToLower(a.Ref.Dataset), strings.ToLower(b.Ref.Dataset))
	})

	return func() {
		e.tree.Clear(id)

		for _, d := range sets {
			k := e.tree.Add(id, KindDataset, d.Ref.Dataset, d.Location)
			e.info[k] = ObjectInfo{ID: k, Kind: KindDataset, Project: o.Project, Dataset: d.Ref.Dataset}
		}

		if len(sets) == 0 {
			e.tree.Add(id, KindPlaceholder, "No datasets", "")
		}

		e.state[id] = loaded
		e.refilter()
		e.changed(id)
	}
}

type datasetListing struct {
	tables   []bq.TableEntry
	routines []*bigquery.Routine
	models   []*bigquery.Model
	err      error
}

func (e *Explorer) loadDataset(ctx context.Context, id uint64, o ObjectInfo) func() {
	ds := o.DatasetRef()

	var (
		l  datasetListing
		wg sync.WaitGroup
	)

	wg.Go(func() { l.tables, l.err = e.be.ListTables(ctx, ds) })
	// Routines and models are optional: missing permissions must not hide tables.
	wg.Go(func() { l.routines, _ = e.be.ListRoutines(ctx, ds) })
	wg.Go(func() { l.models, _ = e.be.ListModels(ctx, ds) })
	wg.Wait()

	if l.err != nil {
		return e.fail(id, l.err)
	}

	entries := buildDatasetEntries(o, l)

	return func() {
		e.tree.Clear(id)

		for _, en := range entries {
			k := e.tree.Add(id, en.info.Kind, en.title, en.detail)
			en.info.ID = k
			e.info[k] = en.info

			if en.info.Kind == KindShardGroup {
				for _, shard := range en.info.Shards {
					sk := e.tree.Add(k, KindTable, shard, "")
					e.info[sk] = ObjectInfo{ID: sk, Kind: KindTable, Project: o.Project, Dataset: o.Dataset, Name: shard, TableType: "TABLE"}
				}

				e.state[k] = loaded
			}
		}

		if len(entries) == 0 {
			e.tree.Add(id, KindPlaceholder, "No tables", "")
		}

		e.state[id] = loaded
		e.refilter()
		e.changed(id)
	}
}

type entry struct {
	title, detail string
	info          ObjectInfo
}

// shardSuffix matches date-sharded table names like events_20240131.
var shardSuffix = regexp.MustCompile(`^(.*?)(\d{8})$`)

// buildDatasetEntries turns listings into sorted explorer entries, grouping
// date-sharded tables that share a prefix (two or more shards), like the
// BigQuery console does.
func buildDatasetEntries(o ObjectInfo, l datasetListing) []entry {
	base := ObjectInfo{Project: o.Project, Dataset: o.Dataset}
	shards := make(map[string][]string)

	var out []entry

	for _, t := range l.tables {
		if m := shardSuffix.FindStringSubmatch(t.Ref.Table); m != nil && t.Type == "TABLE" {
			shards[m[1]] = append(shards[m[1]], t.Ref.Table)
		}
	}

	for prefix, names := range shards {
		if len(names) < 2 {
			delete(shards, prefix)
		}
	}

	grouped := make(map[string]bool)

	for _, t := range l.tables {
		if m := shardSuffix.FindStringSubmatch(t.Ref.Table); m != nil && shards[m[1]] != nil && t.Type == "TABLE" {
			if !grouped[m[1]] {
				grouped[m[1]] = true
				names := slices.Clone(shards[m[1]])
				slices.Sort(names)
				slices.Reverse(names)

				info := base
				info.Kind, info.Name, info.Shards = KindShardGroup, m[1], names
				out = append(out, entry{title: fmt.Sprintf("%s(%d)", m[1], len(names)), detail: "date-sharded table", info: info})
			}

			continue
		}

		info := base
		info.Kind, info.Name, info.TableType = tableKind(t.Type), t.Ref.Table, t.Type
		out = append(out, entry{title: t.Ref.Table, detail: tableDetail(t), info: info})
	}

	for _, r := range l.routines {
		if r.RoutineReference == nil {
			continue
		}

		info := base
		info.Kind, info.Name = KindRoutine, r.RoutineReference.RoutineId
		out = append(out, entry{title: info.Name, detail: strings.ToLower(strings.ReplaceAll(r.RoutineType, "_", " ")), info: info})
	}

	for _, m := range l.models {
		if m.ModelReference == nil {
			continue
		}

		info := base
		info.Kind, info.Name = KindModel, m.ModelReference.ModelId
		out = append(out, entry{title: info.Name, detail: "model · " + strings.ToLower(m.ModelType), info: info})
	}

	slices.SortStableFunc(out, func(a, b entry) int {
		return strings.Compare(strings.ToLower(a.title), strings.ToLower(b.title))
	})

	return out
}

func tableKind(t string) NodeKind {
	switch t {
	case "VIEW":
		return KindView
	case "MATERIALIZED_VIEW":
		return KindMaterializedView
	case "EXTERNAL":
		return KindExternalTable
	case "SNAPSHOT":
		return KindSnapshot
	default:
		return KindTable
	}
}

func tableDetail(t bq.TableEntry) string {
	parts := []string{strings.ToLower(strings.ReplaceAll(t.Type, "_", " "))}
	if t.Partitioned {
		parts = append(parts, "partitioned")
	}

	if t.Clustered {
		parts = append(parts, "clustered")
	}

	return strings.Join(parts, " · ")
}

func shortError(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 && i+2 < len(msg) {
		msg = msg[i+2:]
	}

	const maxLen = 60
	if len(msg) > maxLen {
		msg = msg[:maxLen] + "…"
	}

	return msg
}
