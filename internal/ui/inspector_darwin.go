//go:build darwin

package ui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/objc"
)

const (
	inspectorInset   = 12.0
	metadataTimeout  = 30 * time.Second
	metadataCacheTTL = 5 * time.Minute
	segSchema        = 0
	segDetails       = 1
)

// InspectorPane shows the selected object's schema and details.
type InspectorPane struct {
	View objc.ID

	be   app.Backend
	jobs app.JobsBackend
	fmt  app.Formatter

	title, subtitle, status objc.ID
	seg                     *appkit.Segmented
	schema, details         *appkit.Outline
	schemaSrc               *schemaSource
	detailsSrc              *detailsSource
	s                       *Shell

	gen     uint64
	current app.ObjectInfo

	cacheMu sync.Mutex
	cache   map[string]cached
}

type cached struct {
	v  any
	at time.Time
}

func newInspectorPane(s *Shell, be app.Backend) *InspectorPane {
	p := &InspectorPane{s: s, be: be, cache: make(map[string]cached)}
	p.schemaSrc = &schemaSource{}
	p.detailsSrc = &detailsSource{}

	p.schema = appkit.NewOutline(p.schemaSrc, appkit.OutlineOptions{
		Columns: []appkit.Column{
			{ID: "name", Title: "Field name", Width: 170, MinWidth: 80},
			{ID: "type", Title: "Type", Width: 90, MinWidth: 50},
			{ID: "mode", Title: "Mode", Width: 82, MinWidth: 50},
			{ID: "key", Title: "Key", Width: 90, MinWidth: 30},
			{ID: "description", Title: "Description", Width: 160, MinWidth: 60},
		},
		Alternating: true,
		Menu:        p.schemaMenu,
	})

	p.details = appkit.NewOutline(p.detailsSrc, appkit.OutlineOptions{
		Columns: []appkit.Column{
			{ID: "key", Title: "Property", Width: 170, MinWidth: 80},
			{ID: "value", Title: "Value", Width: 320, MinWidth: 100},
		},
		Menu:                p.detailsMenu,
		AutomaticRowHeights: true,
	})

	p.title = appkit.BoldLabel("No selection", 14)
	p.subtitle = appkit.SecondaryLabel("Select a table, view, routine or model in the explorer.")
	p.status = appkit.SecondaryLabel("")
	p.seg = appkit.NewSegmented([]string{"Schema", "Details"}, p.showSegment)

	p.View = appkit.NewView()
	content := appkit.NewView()

	for _, v := range []objc.ID{p.title, p.subtitle, p.seg.View, content} {
		appkit.UseAutoLayout(v)
		objc.Msg(p.View, "addSubview:", v)
	}

	appkit.AddFilling(content, p.schema.Scroll, 0)
	appkit.AddFilling(content, p.details.Scroll, 0)
	appkit.AddCentered(content, p.status)

	safeTop := objc.Msg(objc.Msg(p.View, "safeAreaLayoutGuide"), "topAnchor")
	lead, trail := appkit.Anchor(p.View, "leadingAnchor"), appkit.Anchor(p.View, "trailingAnchor")
	appkit.Activate(
		appkit.Equal(appkit.Anchor(p.title, "topAnchor"), safeTop, inspectorInset),
		appkit.Equal(appkit.Anchor(p.title, "leadingAnchor"), lead, inspectorInset),
		appkit.Equal(appkit.Anchor(p.title, "trailingAnchor"), trail, -inspectorInset),
		appkit.Equal(appkit.Anchor(p.subtitle, "topAnchor"), appkit.Anchor(p.title, "bottomAnchor"), 2),
		appkit.Equal(appkit.Anchor(p.subtitle, "leadingAnchor"), lead, inspectorInset),
		appkit.Equal(appkit.Anchor(p.subtitle, "trailingAnchor"), trail, -inspectorInset),
		appkit.Equal(appkit.Anchor(p.seg.View, "topAnchor"), appkit.Anchor(p.subtitle, "bottomAnchor"), 10),
		appkit.Equal(appkit.Anchor(p.seg.View, "leadingAnchor"), lead, inspectorInset),
		appkit.Equal(appkit.Anchor(content, "topAnchor"), appkit.Anchor(p.seg.View, "bottomAnchor"), 8),
		appkit.Equal(appkit.Anchor(content, "leadingAnchor"), lead, 0),
		appkit.Equal(appkit.Anchor(content, "trailingAnchor"), trail, 0),
		appkit.Equal(appkit.Anchor(content, "bottomAnchor"), appkit.Anchor(p.View, "bottomAnchor"), 0),
	)

	p.setState(stateEmpty)

	return p
}

type inspectorState int

const (
	stateEmpty inspectorState = iota
	stateLoading
	stateError
	stateReady
)

func (p *InspectorPane) setState(st inspectorState) {
	appkit.SetHidden(p.seg.View, st != stateReady)
	appkit.SetHidden(p.status, st == stateReady)

	if st != stateReady {
		appkit.SetHidden(p.schema.Scroll, true)
		appkit.SetHidden(p.details.Scroll, true)
	}
}

func (p *InspectorPane) showSegment(i int) {
	appkit.SetHidden(p.schema.Scroll, i != segSchema)
	appkit.SetHidden(p.details.Scroll, i != segDetails)
}

// Current returns the object being shown.
func (p *InspectorPane) Current() app.ObjectInfo { return p.current }

// SchemaRows reports visible schema rows (smoke checks).
func (p *InspectorPane) SchemaRows() int64 { return p.schema.Rows() }

// DetailRows reports visible detail rows (smoke checks).
func (p *InspectorPane) DetailRows() int64 { return p.details.Rows() }

// SchemaFields reports the number of schema fields, nested included.
func (p *InspectorPane) SchemaFields() int { return len(p.schemaSrc.fields) }

// Segment reports the selected tab (0 schema, 1 details).
func (p *InspectorPane) Segment() int { return p.seg.Selected() }

// StatusText reports the loading/error message.
func (p *InspectorPane) StatusText() string { return objc.GoString(objc.Msg(p.status, "stringValue")) }

// SegmentGeometry describes the segmented control (smoke diagnostics).
func (p *InspectorPane) SegmentGeometry() string {
	f := objc.Send[appkit.Rect](p.seg.View, "frame")
	w0 := objc.Send[float64](p.seg.View, "widthForSegment:", int64(0))
	w1 := objc.Send[float64](p.seg.View, "widthForSegment:", int64(1))
	en := objc.Send[bool](p.seg.View, "isEnabledForSegment:", int64(1))

	return fmt.Sprintf("frame %.0fx%.0f at %.0f,%.0f; widths %.0f/%.0f; details enabled %v; hidden %v",
		f.Size.Width, f.Size.Height, f.Origin.X, f.Origin.Y, w0, w1, en, objc.Send[bool](p.seg.View, "isHidden"))
}

// SegmentView returns the segmented control (smoke diagnostics).
func (p *InspectorPane) SegmentView() objc.ID { return p.seg.View }

// ShowJob displays a job's details (from the job history).
func (p *InspectorPane) ShowJob(ref bq.JobRef) {
	if p.jobs == nil {
		return
	}

	p.gen++
	gen := p.gen
	info := app.ObjectInfo{Kind: app.KindPlaceholder, Project: ref.Project, Name: ref.JobID}
	p.current = info

	appkit.SetString(p.title, ref.JobID)
	appkit.SetString(p.subtitle, "job · "+ref.Project+" · "+ref.Location)
	appkit.SetString(p.status, "Loading…")
	p.setState(stateLoading)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), metadataTimeout)
		defer cancel()

		job, err := p.jobs.GetJob(ctx, ref)

		objc.Main(func() { p.apply(gen, info, job, err) })
	}()
}

// Show displays an explorer object, fetching its metadata off the main thread.
func (p *InspectorPane) Show(info app.ObjectInfo) {
	p.gen++
	gen := p.gen
	p.current = info

	appkit.SetString(p.title, displayName(info))
	appkit.SetString(p.subtitle, info.Kind.String()+" · "+info.FullID())

	if info.Kind == app.KindProject || info.Kind == app.KindPlaceholder {
		p.apply(gen, info, nil, nil)

		return
	}

	if v, ok := p.cached(info.FullID()); ok {
		p.apply(gen, info, v, nil)

		return
	}

	appkit.SetString(p.status, "Loading…")
	p.setState(stateLoading)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), metadataTimeout)
		defer cancel()

		v, err := p.fetch(ctx, info)
		if err == nil {
			p.store(info.FullID(), v)
		}

		objc.Main(func() { p.apply(gen, info, v, err) })
	}()
}

func displayName(info app.ObjectInfo) string {
	switch info.Kind {
	case app.KindProject:
		return info.Project
	case app.KindDataset:
		return info.Dataset
	default:
		return info.Name
	}
}

func (p *InspectorPane) fetch(ctx context.Context, info app.ObjectInfo) (any, error) {
	switch info.Kind {
	case app.KindDataset:
		return p.be.GetDataset(ctx, info.DatasetRef())
	case app.KindRoutine:
		return p.be.GetRoutine(ctx, info.ObjectRef())
	case app.KindModel:
		return p.be.GetModel(ctx, info.ObjectRef())
	default:
		return p.be.GetTable(ctx, info.ObjectRef())
	}
}

func (p *InspectorPane) cached(key string) (any, bool) {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()

	c, ok := p.cache[key]
	if !ok || time.Since(c.at) > metadataCacheTTL {
		return nil, false
	}

	return c.v, true
}

func (p *InspectorPane) store(key string, v any) {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()

	p.cache[key] = cached{v: v, at: time.Now()}
}

// apply runs on the main thread; stale responses (older gen) are dropped.
func (p *InspectorPane) apply(gen uint64, info app.ObjectInfo, v any, err error) {
	if gen != p.gen {
		return
	}

	if err != nil {
		appkit.SetString(p.status, "Could not load metadata: "+err.Error())
		p.setState(stateError)

		return
	}

	var (
		fields   []app.SchemaField
		sections []app.Section
	)

	switch x := v.(type) {
	case *bigquery.Table:
		fields = app.SchemaTree(x)
		sections = p.fmt.TableSections(x)

		if info.Kind == app.KindShardGroup {
			sections = append([]app.Section{{Title: "Sharded table", Rows: []app.Detail{
				{Key: "Shards", Value: app.Count(uint64(len(info.Shards)))},
				{Key: "Newest", Value: info.Shards[0], Mono: true},
				{Key: "Oldest", Value: info.Shards[len(info.Shards)-1], Mono: true},
			}}}, sections...)
		}
	case *bigquery.Dataset:
		sections = p.fmt.DatasetSections(x)
	case *bigquery.Routine:
		sections = p.fmt.RoutineSections(x)
	case *bigquery.Model:
		sections = p.fmt.ModelSections(x)
	case *bigquery.Job:
		sections = p.fmt.JobSections(x)
	default:
		sections = []app.Section{{Title: "Project", Rows: []app.Detail{{Key: "Project ID", Value: info.Project, Mono: true}}}}
	}

	p.schemaSrc.set(fields)
	p.detailsSrc.set(sections)

	// The Key column only earns its space when some field has a badge.
	keyCol := objc.Msg(p.schema.View, "tableColumnWithIdentifier:", objc.String("key"))
	objc.Msg(keyCol, "setHidden:", !anyBadges(fields))
	p.schema.Reload(0)
	p.details.Reload(0)
	p.details.Expand(0, true)

	hasSchema := len(fields) > 0
	p.seg.SetEnabled(segSchema, hasSchema)
	p.setState(stateReady)

	if hasSchema {
		p.seg.Select(segSchema)
		p.showSegment(segSchema)
	} else {
		p.seg.Select(segDetails)
		p.showSegment(segDetails)
	}
}

func (p *InspectorPane) schemaMenu(item uint64) []appkit.MenuItem {
	f, ok := p.schemaSrc.fields[item]
	if !ok {
		return nil
	}

	path := p.schemaSrc.path(item)

	return []appkit.MenuItem{
		{Title: "Insert Field Name", Run: func() { p.s.Editor.InsertText(path) }},
		{Title: "Copy Field Name", Run: func() { appkit.CopyToPasteboard(f.Name) }},
		{Title: "Copy Field Path", Run: func() { appkit.CopyToPasteboard(path) }},
	}
}

func (p *InspectorPane) detailsMenu(item uint64) []appkit.MenuItem {
	r, ok := p.detailsSrc.rows[item]
	if !ok {
		return nil
	}

	items := []appkit.MenuItem{
		{Title: "Copy Value", Run: func() { appkit.CopyToPasteboard(r.Value) }},
		{Title: "Copy Property and Value", Run: func() { appkit.CopyToPasteboard(r.Key + ": " + r.Value) }},
	}

	if r.Mono && strings.Contains(r.Value, "\n") {
		items = append(items, appkit.MenuItem{}, appkit.MenuItem{
			Title: "Open in Editor", Run: func() { p.s.Editor.AppendText(strings.TrimRight(r.Value, ";") + ";") },
		})
	}

	return items
}

func anyBadges(fields []app.SchemaField) bool {
	for _, f := range fields {
		if len(f.Badges()) > 0 || anyBadges(f.Children) {
			return true
		}
	}

	return false
}

// schemaSource flattens a SchemaField tree into outline handles.
type schemaSource struct {
	fields   map[uint64]*app.SchemaField
	children map[uint64][]uint64
	parent   map[uint64]uint64
}

func (s *schemaSource) set(fields []app.SchemaField) {
	s.fields = make(map[uint64]*app.SchemaField)
	s.children = make(map[uint64][]uint64)
	s.parent = make(map[uint64]uint64)

	next := uint64(1)

	var add func(parent uint64, fs []app.SchemaField)

	add = func(parent uint64, fs []app.SchemaField) {
		for i := range fs {
			id := next
			next++
			s.fields[id] = &fs[i]
			s.parent[id] = parent
			s.children[parent] = append(s.children[parent], id)
			add(id, fs[i].Children)
		}
	}

	add(0, fields)
}

func (s *schemaSource) path(id uint64) string {
	var parts []string
	for ; id != 0; id = s.parent[id] {
		parts = append([]string{s.fields[id].Name}, parts...)
	}

	return strings.Join(parts, ".")
}

func (s *schemaSource) Children(parent uint64) []uint64 { return s.children[parent] }
func (s *schemaSource) Expandable(item uint64) bool     { return len(s.children[item]) > 0 }

func (s *schemaSource) Cell(item uint64) appkit.OutlineCell {
	f := s.fields[item]

	return appkit.OutlineCell{Title: f.Name, ToolTip: f.Description}
}

func (s *schemaSource) CellText(item uint64, column string) appkit.CellText {
	f := s.fields[item]

	switch column {
	case "type":
		return appkit.CellText{Text: f.Type, Mono: true}
	case "mode":
		return appkit.CellText{Text: f.Mode, Secondary: f.Mode == "NULLABLE"}
	case "key":
		return appkit.CellText{Text: strings.Join(f.Badges(), ", "), Secondary: true}
	case "description":
		return appkit.CellText{Text: f.Description, Secondary: true, ToolTip: f.Description}
	}

	return appkit.CellText{}
}

// detailsSource shows sections as group rows with key/value children.
type detailsSource struct {
	titles   map[uint64]string
	rows     map[uint64]app.Detail
	children map[uint64][]uint64
}

func (s *detailsSource) set(sections []app.Section) {
	s.titles = make(map[uint64]string)
	s.rows = make(map[uint64]app.Detail)
	s.children = make(map[uint64][]uint64)

	next := uint64(1)

	for _, sec := range sections {
		sid := next
		next++
		s.titles[sid] = sec.Title
		s.children[0] = append(s.children[0], sid)

		for _, r := range sec.Rows {
			s.rows[next] = r
			s.children[sid] = append(s.children[sid], next)
			next++
		}
	}
}

func (s *detailsSource) Children(parent uint64) []uint64 { return s.children[parent] }
func (s *detailsSource) Expandable(item uint64) bool     { return len(s.children[item]) > 0 }

func (s *detailsSource) Cell(item uint64) appkit.OutlineCell {
	if t, ok := s.titles[item]; ok {
		return appkit.OutlineCell{Title: t, Group: true}
	}

	return appkit.OutlineCell{Title: s.rows[item].Key}
}

func (s *detailsSource) CellText(item uint64, column string) appkit.CellText {
	r, ok := s.rows[item]
	if !ok || column != "value" {
		return appkit.CellText{}
	}

	return appkit.CellText{Text: r.Value, Mono: r.Mono, Wrap: strings.Contains(r.Value, "\n"), ToolTip: r.Value}
}
