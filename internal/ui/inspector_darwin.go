//go:build darwin

package ui

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/results"
)

const (
	inspectorInset                  = 12.0
	inspectorCenteredInset          = 16.0
	charWrapTokenRunes              = 18
	lineBreakWordWrap        uint64 = 0
	lineBreakCharWrap        uint64 = 1
	tableAutoresizingUniform uint64 = 1
	metadataTimeout                 = 30 * time.Second
	metadataCacheTTL                = 5 * time.Minute
	segSchema                       = 0
	segDetails                      = 1
	segPreview                      = 2
)

// InspectorPane shows the selected object's schema and details.
type InspectorPane struct {
	View objc.ID

	be   app.Backend
	jobs app.JobsBackend
	fmt  app.Formatter

	title, subtitle, status objc.ID
	copyButton, actions     objc.ID
	queryButton, insertBtn  objc.ID
	segBelowSubtitle        objc.ID // tab bar under the subtitle (no actions)
	segBelowActions         objc.ID // tab bar under the action row
	copyText                string  // what the copy button copies ("" hides it)
	seg                     *appkit.TabBar
	schema, details, values *appkit.Outline
	valueSrc                *valueSource
	schemaSrc               *schemaSource
	detailsSrc              *detailsSource
	s                       *Shell

	gen     uint64
	current app.ObjectInfo
	table   *bigquery.Table // current table/view metadata (nil otherwise)

	preview       *ResultsPane
	previewHolder objc.ID
	viewPrompt    objc.ID
	promptText    objc.ID
	previewFor    string // FullID whose preview is loaded or offered
	wantSeg       int    // tab the user last picked (kept across objects)

	schemaResizeObs objc.ID
	fittingSchema   bool
	lastSchemaW     float64

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
			{ID: "name", Title: "Field name", Width: 118, MinWidth: 80},
			{ID: "type", Title: "Type", Width: 62, MinWidth: 48},
			{ID: "mode", Title: "Mode", Width: 66, MinWidth: 62},
			{ID: "key", Title: "Key", Width: 56, MinWidth: 42},
			{ID: "description", Title: "Description", Width: 126, MinWidth: 48},
		},
		Alternating:         true,
		AutomaticRowHeights: true,
		NoHorizontalScroll:  true,
		Menu:                p.schemaMenu,
	})
	objc.Msg(p.schema.View, "setColumnAutoresizingStyle:", tableAutoresizingUniform)
	objc.Msg(p.schema.Scroll, "setPostsFrameChangedNotifications:", true)
	p.schemaResizeObs = objc.NewTarget(func(objc.ID) { p.fitSchemaColumnsIfResized() })
	objc.Msg(objc.Msg(objc.Cls("NSNotificationCenter"), "defaultCenter"),
		"addObserver:selector:name:object:",
		p.schemaResizeObs, objc.Sel(objc.ActionSelector), objc.String("NSViewFrameDidChangeNotification"), p.schema.Scroll)

	p.details = appkit.NewOutline(p.detailsSrc, appkit.OutlineOptions{
		Columns: []appkit.Column{
			{ID: "key", Title: "Property", Width: 130, MinWidth: 70},
			{ID: "value", Title: "Value", Width: 160, MinWidth: 100},
		},
		Menu:                p.detailsMenu,
		AutomaticRowHeights: true,
		NoHorizontalScroll:  true,
	})

	p.title = appkit.WrappingLabel("No selection", 0)
	objc.Msg(p.title, "setFont:", objc.Msg(objc.Cls("NSFont"), "boldSystemFontOfSize:", 14.0))
	p.subtitle = appkit.WrappingLabel("Select a table, view, routine or model in the explorer.", 0)
	objc.Msg(p.subtitle, "setTextColor:", objc.Msg(objc.Cls("NSColor"), "secondaryLabelColor"))
	p.status = appkit.SecondaryLabel("")
	p.seg = appkit.NewTabBar([]string{"Schema", "Details", "Preview"}, func(i int) {
		p.wantSeg = i
		p.showSegment(i)
	})

	// The copy button follows the title text: the title hugs its text and
	// wraps only when the pane is too narrow for both.
	p.copyButton = symbolButton("doc.on.doc", "Copy ID", func() { appkit.CopyToPasteboard(p.copyText) })
	appkit.SetHuggingPriority(p.title, 0, hugHigh)
	appkit.SetCompressionResistance(p.copyButton, 0, hugRequired)
	p.queryButton = appkit.ActionButton("Query", "text.badge.plus", p.QueryCurrent)
	p.insertBtn = appkit.ActionButton("Insert Reference", "text.insert", p.InsertCurrent)
	p.actions = objc.Msg(objc.Cls("NSStackView"), "stackViewWithViews:", objc.Array(p.queryButton, p.insertBtn))
	objc.Msg(p.actions, "setSpacing:", actionSpacing)

	p.View = appkit.NewView()
	content := appkit.NewView()

	for _, v := range []objc.ID{p.title, p.copyButton, p.subtitle, p.actions, p.seg.View, content} {
		appkit.UseAutoLayout(v)
		objc.Msg(p.View, "addSubview:", v)
	}

	appkit.AddFilling(content, p.schema.Scroll, 0)
	appkit.AddFilling(content, p.details.Scroll, 0)

	p.valueSrc = &valueSource{}
	p.values = appkit.NewOutline(p.valueSrc, appkit.OutlineOptions{
		Columns: []appkit.Column{
			{ID: "name", Title: "Name", Width: 100, MinWidth: 60},
			{ID: "type", Title: "Type", Width: 86, MinWidth: 50},
			{ID: "value", Title: "Value", Width: 104, MinWidth: 80},
		},
		AutomaticRowHeights: true,
		NoHorizontalScroll:  true,
		Menu:                p.valueMenu,
	})
	appkit.AddFilling(content, p.values.Scroll, 0)
	appkit.AddFilling(content, p.newPreview(), 0)
	appkit.AddCenteredWrapping(content, p.status, inspectorCenteredInset)

	safeTop := objc.Msg(objc.Msg(p.View, "safeAreaLayoutGuide"), "topAnchor")
	lead, trail := appkit.Anchor(p.View, "leadingAnchor"), appkit.Anchor(p.View, "trailingAnchor")
	appkit.Activate(
		appkit.Equal(appkit.Anchor(p.title, "topAnchor"), safeTop, inspectorInset),
		appkit.Equal(appkit.Anchor(p.title, "leadingAnchor"), lead, inspectorInset),
		appkit.Equal(appkit.Anchor(p.copyButton, "leadingAnchor"), appkit.Anchor(p.title, "trailingAnchor"), copyGap),
		appkit.LessOrEqual(appkit.Anchor(p.copyButton, "trailingAnchor"), trail, -inspectorInset),
		appkit.Equal(appkit.Anchor(p.copyButton, "firstBaselineAnchor"), appkit.Anchor(p.title, "firstBaselineAnchor"), 0),
		appkit.Equal(appkit.Anchor(p.subtitle, "topAnchor"), appkit.Anchor(p.title, "bottomAnchor"), 2),
		appkit.Equal(appkit.Anchor(p.subtitle, "leadingAnchor"), lead, inspectorInset),
		appkit.Equal(appkit.Anchor(p.subtitle, "trailingAnchor"), trail, -inspectorInset),
		appkit.Equal(appkit.Anchor(p.actions, "topAnchor"), appkit.Anchor(p.subtitle, "bottomAnchor"), actionsGap),
		appkit.Equal(appkit.Anchor(p.actions, "leadingAnchor"), lead, inspectorInset),
		appkit.LessOrEqual(appkit.Anchor(p.actions, "trailingAnchor"), trail, -inspectorInset),
		appkit.Equal(appkit.Anchor(p.seg.View, "leadingAnchor"), lead, inspectorInset),
		appkit.Equal(appkit.Anchor(content, "topAnchor"), appkit.Anchor(p.seg.View, "bottomAnchor"), 8),
		appkit.Equal(appkit.Anchor(content, "leadingAnchor"), lead, 0),
		appkit.Equal(appkit.Anchor(content, "trailingAnchor"), trail, 0),
		appkit.Equal(appkit.Anchor(content, "bottomAnchor"), appkit.Anchor(p.View, "bottomAnchor"), 0),
	)

	// The tab bar sits under the action row when it is shown, else under
	// the subtitle; setActions switches between the two.
	p.segBelowSubtitle = appkit.Equal(appkit.Anchor(p.seg.View, "topAnchor"), appkit.Anchor(p.subtitle, "bottomAnchor"), segGap)
	p.segBelowActions = appkit.Equal(appkit.Anchor(p.seg.View, "topAnchor"), appkit.Anchor(p.actions, "bottomAnchor"), actionsGap)
	p.setActions(app.ObjectInfo{}, "")

	p.setState(stateEmpty)

	return p
}

type inspectorState int

const (
	stateEmpty inspectorState = iota
	stateLoading
	stateError
	stateReady
	stateValue
)

func (p *InspectorPane) setState(st inspectorState) {
	appkit.SetHidden(p.seg.View, st != stateReady)
	appkit.SetHidden(p.status, st == stateReady || st == stateValue)
	appkit.SetHidden(p.values.Scroll, st != stateValue)

	if st != stateReady {
		appkit.SetHidden(p.schema.Scroll, true)
		appkit.SetHidden(p.details.Scroll, true)
		appkit.SetHidden(p.previewHolder, true)
	}
}

// ShowSections shows prepared detail sections (plan stages).
func (p *InspectorPane) ShowSections(title, subtitle string, sections []app.Section) {
	p.gen++
	p.current = app.ObjectInfo{Kind: app.KindPlaceholder, Name: title}

	p.setTitle(title)
	p.setSubtitle(subtitle)
	p.setActions(app.ObjectInfo{}, "")
	p.schemaSrc.set(nil)
	p.detailsSrc.set(sections)
	p.schema.Reload(0)
	p.details.Reload(0)
	p.details.Expand(0, true)
	p.seg.SetEnabled(segSchema, false)
	p.seg.SetEnabled(segPreview, false)
	p.table = nil
	p.resetPreview()
	p.setState(stateReady)
	p.seg.Select(segDetails)
	p.showSegment(segDetails)
}

// ShowRow shows one result row as a typed value tree, revealing and
// selecting focus (a value path such as "items[1].sku"; "" for none).
func (p *InspectorPane) ShowRow(fields []results.Field, row []any, index int64, focus string) {
	p.gen++
	p.current = app.ObjectInfo{Kind: app.KindPlaceholder, Name: "Row " + app.Count(uint64(index+1))} //nolint:gosec // non-negative

	p.setTitle(p.current.Name)
	p.setSubtitle("result row · " + strconv.Itoa(len(fields)) + " fields")
	p.setActions(app.ObjectInfo{}, "")

	p.valueSrc.set(results.Tree(fields, row))
	p.values.Reload(0)
	p.setState(stateValue)

	if id, ok := p.valueSrc.byPath[focus]; ok {
		p.values.ExpandAll(p.valueSrc.ancestors(id))
		p.values.Select(id)
	}
}

// ValueRows reports visible value-tree rows and the selected path (smoke).
func (p *InspectorPane) ValueRows() (int64, string) {
	sel := p.values.Selected()
	if n, ok := p.valueSrc.nodes[sel]; ok {
		return p.values.Rows(), n.Path
	}

	return p.values.Rows(), ""
}

func (p *InspectorPane) valueMenu(item uint64) []appkit.MenuItem {
	n, ok := p.valueSrc.nodes[item]
	if !ok {
		return nil
	}

	return []appkit.MenuItem{
		{Title: "Copy Value", Run: func() { appkit.CopyToPasteboard(n.Display) }},
		{Title: "Copy Path", Run: func() { appkit.CopyToPasteboard(n.Path) }},
		{Title: "Insert Path in Editor", Run: func() { p.s.Editor.InsertText(n.Path) }},
	}
}

// valueSource flattens a ValueNode tree into outline handles.
type valueSource struct {
	nodes    map[uint64]*results.ValueNode
	children map[uint64][]uint64
	parent   map[uint64]uint64
	byPath   map[string]uint64
}

func (s *valueSource) set(tree []results.ValueNode) {
	s.nodes = make(map[uint64]*results.ValueNode)
	s.children = make(map[uint64][]uint64)
	s.parent = make(map[uint64]uint64)
	s.byPath = make(map[string]uint64)

	next := uint64(1)

	var add func(parent uint64, ns []results.ValueNode)

	add = func(parent uint64, ns []results.ValueNode) {
		for i := range ns {
			id := next
			next++
			s.nodes[id], s.parent[id], s.byPath[ns[i].Path] = &ns[i], parent, id
			s.children[parent] = append(s.children[parent], id)
			add(id, ns[i].Children)
		}
	}

	add(0, tree)
}

func (s *valueSource) ancestors(id uint64) []uint64 {
	var out []uint64
	for p := s.parent[id]; p != 0; p = s.parent[p] {
		out = append([]uint64{p}, out...)
	}

	return out
}

func (s *valueSource) Children(parent uint64) []uint64 { return s.children[parent] }
func (s *valueSource) Expandable(item uint64) bool     { return len(s.children[item]) > 0 }

func (s *valueSource) Cell(item uint64) appkit.OutlineCell {
	n, ok := s.nodes[item]
	if !ok || n == nil {
		return appkit.OutlineCell{}
	}

	return appkit.OutlineCell{Title: n.Name, Wrap: true, CharWrap: needsCharWrap(n.Name)}
}

func (s *valueSource) CellText(item uint64, column string) appkit.CellText {
	n, ok := s.nodes[item]
	if !ok || n == nil {
		return appkit.CellText{}
	}

	switch column {
	case "value":
		return appkit.CellText{
			Text:      n.Display,
			Secondary: n.Null || len(n.Children) > 0,
			Italic:    n.Null,
			Wrap:      true,
			CharWrap:  needsCharWrap(n.Display),
			Mono:      !n.Null && len(n.Children) == 0,
		}
	case "type":
		return appkit.CellText{Text: n.Type, Secondary: true, Wrap: true, CharWrap: needsCharWrap(n.Type)}
	}

	return appkit.CellText{}
}

func (p *InspectorPane) showSegment(i int) {
	appkit.SetHidden(p.schema.Scroll, i != segSchema)
	appkit.SetHidden(p.details.Scroll, i != segDetails)
	appkit.SetHidden(p.previewHolder, i != segPreview)

	switch i {
	case segSchema:
		p.fitSchemaColumns(true)
	case segPreview:
		p.preparePreview()
	}
}

func (p *InspectorPane) fitSchemaColumnsIfResized() {
	clip := objc.Msg(p.schema.Scroll, "contentView")
	avail := appkit.ViewBounds(clip).Size.Width
	if avail <= 0 {
		avail = appkit.ViewBounds(p.schema.Scroll).Size.Width
	}

	if avail <= 0 || math.Abs(avail-p.lastSchemaW) <= 0.5 {
		return
	}

	p.fitSchemaColumns(false)
	objc.Main(func() { p.schema.InvalidateRowHeights() })
}

func (p *InspectorPane) fitSchemaColumns(invalidate bool) {
	if p.fittingSchema {
		return
	}

	clip := objc.Msg(p.schema.Scroll, "contentView")
	avail := appkit.ViewBounds(clip).Size.Width
	if avail <= 0 {
		avail = appkit.ViewBounds(p.schema.Scroll).Size.Width
	}

	if avail <= 0 {
		return
	}

	p.fittingSchema = true
	defer func() { p.fittingSchema = false }()

	p.lastSchemaW = avail

	keyCol := objc.Msg(p.schema.View, "tableColumnWithIdentifier:", objc.String("key"))
	showKey := keyCol != objc.Nil && !objc.Send[bool](keyCol, "isHidden")

	visibleCols := 4
	if showKey {
		visibleCols = 5
	}

	spacing := objc.Send[appkit.Size](p.schema.View, "intercellSpacing").Width
	budget := max(0.0, avail-float64(visibleCols)*spacing)

	type colSpec struct {
		id        string
		min, wide float64
	}

	specs := []colSpec{
		{id: "name", min: 80, wide: 118},
		{id: "type", min: 48, wide: 62},
		{id: "mode", min: 62, wide: 66},
	}
	if showKey {
		specs = append(specs, colSpec{id: "key", min: 42, wide: 56})
	}

	const descMin, descWide = 48.0, 126.0

	var minFixed, wideFixed float64
	for _, sp := range specs {
		minFixed += sp.min
		wideFixed += sp.wide
	}

	minTotal := minFixed + descMin
	wideTotal := wideFixed + descWide

	var ratio float64
	if wideTotal > minTotal {
		ratio = min(1.0, max(0.0, (budget-minTotal)/(wideTotal-minTotal)))
	}

	var usedFixed float64
	for _, sp := range specs {
		w := math.Floor(sp.min + ratio*(sp.wide-sp.min))
		usedFixed += w

		if col := objc.Msg(p.schema.View, "tableColumnWithIdentifier:", objc.String(sp.id)); col != objc.Nil {
			objc.Msg(col, "setWidth:", w)
		}
	}

	descW := max(descMin, budget-usedFixed)
	if col := objc.Msg(p.schema.View, "tableColumnWithIdentifier:", objc.String("description")); col != objc.Nil {
		objc.Msg(col, "setWidth:", descW)
	}

	objc.Msg(p.schema.View, "sizeLastColumnToFit")
	if invalidate {
		p.schema.InvalidateRowHeights()
	}
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
	w0 := objc.Send[appkit.Rect](p.seg.Button(0), "frame").Size.Width
	w1 := objc.Send[appkit.Rect](p.seg.Button(1), "frame").Size.Width
	en := p.seg.Enabled(1)

	return fmt.Sprintf("frame %.0fx%.0f at %.0f,%.0f; widths %.0f/%.0f; details enabled %v; hidden %v",
		f.Size.Width, f.Size.Height, f.Origin.X, f.Origin.Y, w0, w1, en, objc.Send[bool](p.seg.View, "isHidden"))
}

// SegmentView returns the segmented control (smoke diagnostics).
func (p *InspectorPane) SegmentView() objc.ID { return p.seg.View }

// TitleView returns the title label (smoke checks).
func (p *InspectorPane) TitleView() objc.ID { return p.title }

// SubtitleView returns the subtitle label (smoke checks).
func (p *InspectorPane) SubtitleView() objc.ID { return p.subtitle }

// StatusView returns the loading/error status label (smoke checks).
func (p *InspectorPane) StatusView() objc.ID { return p.status }

// TabBar returns the inspector tab bar (smoke checks).
func (p *InspectorPane) TabBar() *appkit.TabBar { return p.seg }

// SchemaOutline returns the schema outline view (smoke checks).
func (p *InspectorPane) SchemaOutline() *appkit.Outline { return p.schema }

// DetailsOutline returns the details outline view (smoke checks).
func (p *InspectorPane) DetailsOutline() *appkit.Outline { return p.details }

// ValuesOutline returns the value-tree outline view (smoke checks).
func (p *InspectorPane) ValuesOutline() *appkit.Outline { return p.values }

// ViewPromptViews returns the preview container, prompt stack, and prompt label (smoke checks).
func (p *InspectorPane) ViewPromptViews() (holder, prompt, text objc.ID) {
	return p.previewHolder, p.viewPrompt, p.promptText
}

// ShowTable loads table metadata directly on the main thread (smoke checks).
func (p *InspectorPane) ShowTable(info app.ObjectInfo, tbl *bigquery.Table) {
	p.gen++
	if info.FullID() != p.current.FullID() {
		p.table = nil
		p.resetPreview()
	}

	p.current = info
	p.setTitle(displayName(info))
	p.setSubtitle(info.Kind.String() + " · " + info.FullID())
	p.setActions(info, info.FullID())
	p.apply(p.gen, info, tbl, nil)
}

// ShowJob displays a job's details (from the job history).
func (p *InspectorPane) ShowJob(ref bq.JobRef) {
	if p.jobs == nil {
		return
	}

	p.gen++
	gen := p.gen
	info := app.ObjectInfo{Kind: app.KindPlaceholder, Project: ref.Project, Name: ref.JobID}
	p.current = info

	p.setTitle(ref.JobID)
	p.setSubtitle("job · " + ref.Project + " · " + ref.Location)
	p.setActions(app.ObjectInfo{}, ref.JobID)
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

	// Re-selecting the same object keeps its preview (and running query).
	if info.FullID() != p.current.FullID() {
		p.table = nil
		p.resetPreview()
	}

	p.current = info

	p.setTitle(displayName(info))
	p.setSubtitle(info.Kind.String() + " · " + info.FullID())
	p.setActions(info, info.FullID())

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

const (
	copyGap       = 6.0
	segGap        = 10.0
	actionsGap    = 8.0
	actionSpacing = 16.0
	hugHigh       = 750 // NSLayoutPriorityDefaultHigh
	hugRequired   = 1000
)

// setActions shows the copy button (copying copyText) and the editor
// actions that fit the object: Query for tables and table-like objects,
// Insert Reference for those and routines.
func (p *InspectorPane) setActions(info app.ObjectInfo, copyText string) {
	p.copyText = copyText
	appkit.SetHidden(p.copyButton, copyText == "")

	query, insert := false, false

	switch info.Kind {
	case app.KindTable, app.KindView, app.KindMaterializedView, app.KindExternalTable,
		app.KindSnapshot, app.KindShardGroup:
		query, insert = true, true
	case app.KindRoutine:
		insert = true
	case app.KindRoot, app.KindProject, app.KindDataset, app.KindSection, app.KindModel, app.KindPlaceholder:
	}

	shown := (query || insert) && p.s != nil
	appkit.SetHidden(p.queryButton, !query)
	appkit.SetHidden(p.insertBtn, !insert)
	appkit.SetHidden(p.actions, !shown)
	objc.Msg(p.segBelowActions, "setActive:", shown)
	objc.Msg(p.segBelowSubtitle, "setActive:", !shown)
}

// QueryCurrent appends a SELECT of the shown object to the editor and
// focuses it.
func (p *InspectorPane) QueryCurrent() {
	if p.s == nil {
		return
	}

	p.s.Editor.AppendText(selectStar(p.current))
	p.s.Editor.Focus()
}

// InsertCurrent inserts the shown object's SQL reference at the editor's
// cursor and focuses it.
func (p *InspectorPane) InsertCurrent() {
	if p.s == nil {
		return
	}

	p.s.Editor.InsertText(p.current.SQLName())
	p.s.Editor.Focus()
}

// ActionViews returns the copy button and the action row (smoke checks).
func (p *InspectorPane) ActionViews() (copyButton, actions objc.ID) { return p.copyButton, p.actions }

// CopyText returns what the copy button copies (smoke checks).
func (p *InspectorPane) CopyText() string { return p.copyText }

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
		p.table = x
		fields = app.SchemaTree(x)
		sections = p.fmt.TableSections(x)

		if info.Kind == app.KindShardGroup && len(info.Shards) > 0 {
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
	// Reload before unhiding so NSOutlineView does not query stale handles.
	p.schema.Reload(0)
	keyCol := objc.Msg(p.schema.View, "tableColumnWithIdentifier:", objc.String("key"))
	objc.Msg(keyCol, "setHidden:", !anyBadges(fields))
	p.fitSchemaColumns(true)
	p.details.Reload(0)
	p.details.Expand(0, true)

	hasSchema := len(fields) > 0
	canPreview := p.table != nil && info.Kind != app.KindShardGroup
	p.seg.SetEnabled(segSchema, hasSchema)
	p.seg.SetEnabled(segPreview, canPreview)
	p.setState(stateReady)

	// Keep the tab the user picked when the new object has it.
	seg := segDetails

	switch {
	case p.wantSeg == segPreview && canPreview:
		seg = segPreview
	case p.wantSeg == segDetails:
	case hasSchema:
		seg = segSchema
	}

	p.seg.Select(seg)
	p.showSegment(seg)
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
	f, ok := s.fields[item]
	if !ok || f == nil {
		return appkit.OutlineCell{}
	}

	return appkit.OutlineCell{Title: f.Name, ToolTip: f.Description, Wrap: true, CharWrap: needsCharWrap(f.Name)}
}

func (s *schemaSource) CellText(item uint64, column string) appkit.CellText {
	f, ok := s.fields[item]
	if !ok || f == nil {
		return appkit.CellText{}
	}

	switch column {
	case "type":
		return appkit.CellText{Text: f.Type, Mono: true, Wrap: true, CharWrap: needsCharWrap(f.Type)}
	case "mode":
		return appkit.CellText{Text: f.Mode, Secondary: f.Mode == "NULLABLE"}
	case "key":
		return appkit.CellText{Text: strings.Join(f.Badges(), ", "), Secondary: true}
	case "description":
		return appkit.CellText{Text: f.Description, Secondary: true, Wrap: true, CharWrap: needsCharWrap(f.Description), ToolTip: f.Description}
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

	key := s.rows[item].Key

	return appkit.OutlineCell{Title: key, Wrap: true, CharWrap: needsCharWrap(key)}
}

func (s *detailsSource) CellText(item uint64, column string) appkit.CellText {
	r, ok := s.rows[item]
	if !ok || column != "value" {
		return appkit.CellText{}
	}

	return appkit.CellText{Text: r.Value, Mono: r.Mono, Wrap: true, CharWrap: needsCharWrap(r.Value), ToolTip: r.Value}
}

// needsCharWrap reports whether s contains any whitespace-delimited token
// longer than charWrapTokenRunes (such as KMS keys, gs:// URIs, table/job IDs,
// URLs, emails, or UUID labels) that would overflow narrow columns under word wrap.
func needsCharWrap(s string) bool {
	for tok := range strings.FieldsSeq(s) {
		if utf8.RuneCountInString(tok) > charWrapTokenRunes {
			return true
		}
	}

	return false
}

func (p *InspectorPane) setTitle(text string) {
	appkit.SetString(p.title, text)

	mode := lineBreakWordWrap
	if needsCharWrap(text) {
		mode = lineBreakCharWrap
	}

	objc.Msg(p.title, "setLineBreakMode:", mode)
}

func (p *InspectorPane) setSubtitle(text string) {
	appkit.SetString(p.subtitle, text)

	mode := lineBreakWordWrap
	if needsCharWrap(text) {
		mode = lineBreakCharWrap
	}

	objc.Msg(p.subtitle, "setLineBreakMode:", mode)
}

// newPreview builds the Preview tab: a results grid, and a prompt with a
// Load Preview button for views (whose preview runs a query).
func (p *InspectorPane) newPreview() objc.ID {
	p.preview = newResultsPane()
	p.preview.SetEmptyText("Select a table to preview its rows.")

	p.promptText = appkit.WrappingLabel("", 0)
	objc.Msg(p.promptText, "setTextColor:", objc.Msg(objc.Cls("NSColor"), "secondaryLabelColor"))
	objc.Msg(p.promptText, "setAlignment:", appkit.TextAlignmentCenter)

	button := appkit.Button("Load Preview", func() { p.LoadViewPreview() })
	p.viewPrompt = objc.Msg(objc.Cls("NSStackView"), "stackViewWithViews:", objc.Array(p.promptText, button))
	objc.Msg(p.viewPrompt, "setOrientation:", int64(1)) // vertical
	objc.Msg(p.viewPrompt, "setAlignment:", int64(9))   // NSLayoutAttributeCenterX
	objc.Msg(p.viewPrompt, "setSpacing:", 10.0)
	appkit.UseAutoLayout(p.promptText)
	appkit.Activate(appkit.Equal(appkit.Anchor(p.promptText, "widthAnchor"), appkit.Anchor(p.viewPrompt, "widthAnchor"), 0))

	p.previewHolder = appkit.NewView()
	appkit.AddFilling(p.previewHolder, p.preview.View, 0)
	appkit.UseAutoLayout(p.viewPrompt)
	objc.Msg(p.previewHolder, "addSubview:", p.viewPrompt)
	appkit.Activate(
		appkit.Equal(appkit.Anchor(p.viewPrompt, "centerXAnchor"), appkit.Anchor(p.previewHolder, "centerXAnchor"), 0),
		appkit.Equal(appkit.Anchor(p.viewPrompt, "centerYAnchor"), appkit.Anchor(p.previewHolder, "centerYAnchor"), 0),
		appkit.LessOrEqualConst(appkit.Anchor(p.viewPrompt, "widthAnchor"), previewPromptWidth),
		appkit.GreaterOrEqual(appkit.Anchor(p.viewPrompt, "leadingAnchor"), appkit.Anchor(p.previewHolder, "leadingAnchor"), inspectorCenteredInset),
		appkit.LessOrEqual(appkit.Anchor(p.viewPrompt, "trailingAnchor"), appkit.Anchor(p.previewHolder, "trailingAnchor"), -inspectorCenteredInset),
	)
	appkit.SetHidden(p.viewPrompt, true)
	appkit.SetHidden(p.previewHolder, true)

	return p.previewHolder
}

const previewPromptWidth = 300.0

// isView reports whether the current object previews through a query.
func (p *InspectorPane) isView() bool {
	return p.table != nil && (p.table.Type == "VIEW" || p.table.Type == "MATERIALIZED_VIEW")
}

// resetPreview stops any preview of the previous object.
func (p *InspectorPane) resetPreview() {
	p.previewFor = ""
	p.preview.Reset()
	appkit.SetHidden(p.viewPrompt, true)
}

// preparePreview starts a table preview (Storage Read API, free) or offers
// to run the view's query.
func (p *InspectorPane) preparePreview() {
	id := p.current.FullID()
	if p.table == nil || p.previewFor == id {
		return
	}

	p.previewFor = id

	if p.isView() {
		p.preview.Reset()
		appkit.SetString(p.promptText, fmt.Sprintf(
			"Previewing a %s runs its query (up to %s rows, billed to %s). It is cancelled if it takes more than %s.",
			strings.ToLower(strings.ReplaceAll(p.table.Type, "_", " ")), app.Count(app.PreviewQueryRows),
			p.s.BillingProject(), seconds(app.PreviewQueryTimeout)))
		appkit.SetHidden(p.viewPrompt, false)

		return
	}

	appkit.SetHidden(p.viewPrompt, true)
	p.preview.Show(p.s.Runner.Preview(p.current.ObjectRef()))
}

// LoadViewPreview runs the view preview query (capped at 1,024 rows and 10
// seconds).
func (p *InspectorPane) LoadViewPreview() {
	if p.table == nil {
		return
	}

	appkit.SetHidden(p.viewPrompt, true)
	p.preview.Show(p.s.Runner.PreviewQuery(p.current.ObjectRef(), p.s.BillingProject(), app.PreviewQueryTimeout))
}

// TableLoaded reports whether the current table's metadata has arrived.
func (p *InspectorPane) TableLoaded() bool {
	return p.table != nil && p.table.TableReference != nil && p.table.TableReference.TableId == p.current.Name
}

// PreviewPane is the Preview tab's results pane (smoke checks).
func (p *InspectorPane) PreviewPane() *ResultsPane { return p.preview }

// ViewPromptVisible reports whether the Load Preview prompt is shown.
func (p *InspectorPane) ViewPromptVisible() bool { return !objc.Send[bool](p.viewPrompt, "isHidden") }

// SelectSegment switches tabs as if clicked (smoke checks).
func (p *InspectorPane) SelectSegment(i int) {
	p.seg.Select(i)
	p.wantSeg = i
	p.showSegment(i)
}
