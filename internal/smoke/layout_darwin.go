//go:build darwin

package smoke

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/plan"
	"github.com/paulourio/bqui/internal/theme"
	"github.com/paulourio/bqui/internal/ui"
)

const (
	layoutInspectorWide   = 440.0
	layoutInspectorNarrow = 300.0
	layoutNarrowWidth     = 900.0
	layoutNarrowHeight    = 600.0
	layoutLineBreakWord   = 0
	layoutLineBreakChar   = 1
	layoutMinJSONPx       = 2400.0
	layoutTabPadMin       = 12.0
	layoutThemeGridSize   = 18.0
	layoutThemeMinRowH    = 28.0
	layoutInsetTol        = 2.5
	layoutBoundTol        = 0.5
)

type layoutResult struct {
	NestedRenderedOK     bool
	NestedRenderedDetail string

	InspectorWrapOK     bool
	InspectorWrapDetail string

	InspectorSegOK     bool
	InspectorSegDetail string

	BottomTabsOK     bool
	BottomTabsDetail string

	ToolbarOK     bool
	ToolbarDetail string

	HistoryNotesThemeOK     bool
	HistoryNotesThemeDetail string

	NarrowWindowOK     bool
	NarrowWindowDetail string

	Screenshots []string
}

type smokeNestedBackend struct{}

func (*smokeNestedBackend) Query(context.Context, bq.QueryRequest) (*bq.QueryResult, error) {
	longSummary := strings.Repeat("nested_payload_segment_", 18) + "end"

	schema := &bigquery.TableSchema{Fields: []*bigquery.TableFieldSchema{
		{Name: "id", Type: "INTEGER", Mode: "REQUIRED"},
		{Name: "repo", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{
			{Name: "name", Type: "STRING"},
			{Name: "url", Type: "STRING"},
		}},
		{Name: "payload", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{
			{Name: "action", Type: "STRING"},
			{Name: "pull_request", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{
				{Name: "title", Type: "STRING"},
				{Name: "merged", Type: "BOOLEAN"},
			}},
			{Name: "pages", Type: "RECORD", Mode: "REPEATED", Fields: []*bigquery.TableFieldSchema{
				{Name: "page_name", Type: "STRING"},
				{Name: "summary", Type: "STRING"},
			}},
		}},
		{Name: "hits", Type: "RECORD", Mode: "REPEATED", Fields: []*bigquery.TableFieldSchema{
			{Name: "hit_number", Type: "INTEGER"},
			{Name: "tags", Type: "STRING", Mode: "REPEATED"},
		}},
		{Name: "notes", Type: "STRING"},
	}}

	rows := []*bigquery.TableRow{
		{F: []*bigquery.TableCell{
			{V: "101"},
			{V: map[string]any{"f": []any{
				map[string]any{"v": "paulourio/bqui"},
				map[string]any{"v": "https://github.com/paulourio/bqui/tree/main/internal/smoke/nested_rendering_verification_path"},
			}}},
			{V: map[string]any{"f": []any{
				map[string]any{"v": "opened"},
				map[string]any{"v": map[string]any{"f": []any{
					map[string]any{"v": "Fix nested STRUCT and REPEATED RECORD rendering in Results and JSON panes"},
					map[string]any{"v": "true"},
				}}},
				map[string]any{"v": []any{
					map[string]any{"v": map[string]any{"f": []any{
						map[string]any{"v": "Home"},
						map[string]any{"v": longSummary},
					}}},
					map[string]any{"v": map[string]any{"f": []any{
						map[string]any{"v": "NestedSubRow"},
						map[string]any{"v": "second_page_summary"},
					}}},
				}},
			}}},
			{V: []any{
				map[string]any{"v": map[string]any{"f": []any{
					map[string]any{"v": "1"},
					map[string]any{"v": []any{map[string]any{"v": "nested"}, map[string]any{"v": "storage"}}},
				}}},
				map[string]any{"v": map[string]any{"f": []any{
					map[string]any{"v": "2"},
					map[string]any{"v": []any{map[string]any{"v": "json"}, map[string]any{"v": "wrapping"}}},
				}}},
			}},
			{V: "café · naïve · 日本語\nline2"},
		}},
		{F: []*bigquery.TableCell{
			{V: "102"},
			{V: nil},
			{V: map[string]any{"f": []any{
				map[string]any{"v": "closed"},
				map[string]any{"v": nil},
				map[string]any{"v": []any{}},
			}}},
			{V: []any{}},
			{V: nil},
		}},
		{F: []*bigquery.TableCell{
			{V: "103"},
			{V: map[string]any{"f": []any{
				map[string]any{"v": "bigquery-public-data/samples"},
				map[string]any{"v": "https://console.cloud.google.com/bigquery"},
			}}},
			{V: map[string]any{"f": []any{
				map[string]any{"v": "edited"},
				map[string]any{"v": map[string]any{"f": []any{
					map[string]any{"v": "Add github_nested fixture"},
					map[string]any{"v": "false"},
				}}},
				map[string]any{"v": []any{
					map[string]any{"v": map[string]any{"f": []any{
						map[string]any{"v": "Overview"},
						map[string]any{"v": "Short summary"},
					}}},
				}},
			}}},
			{V: []any{
				map[string]any{"v": map[string]any{"f": []any{
					map[string]any{"v": "1"},
					map[string]any{"v": []any{map[string]any{"v": "smoke"}}},
				}}},
			}},
			{V: "ok"},
		}},
	}

	return &bq.QueryResult{
		Statement: "SELECT",
		Schema:    schema,
		TotalRows: uint64(len(rows)),
		Rows:      rows,
	}, nil
}

func (*smokeNestedBackend) CancelJob(context.Context, bq.JobRef) error { return nil }

func (*smokeNestedBackend) ResultPage(context.Context, bq.JobRef, string, int64) (*bigquery.GetQueryResultsResponse, error) {
	return &bigquery.GetQueryResultsResponse{}, nil
}

func smokeCMEKTable() *bigquery.Table {
	return &bigquery.Table{
		TableReference: &bigquery.TableReference{
			ProjectId: "my-long-project-id",
			DatasetId: "analytics_prod_dataset",
			TableId:   "customer_events_partitioned_2026_long_table_name",
		},
		Type:        "VIEW",
		Location:    "US",
		Description: "This prose description explains how customer events are ingested from upstream pipelines and should wrap cleanly at word boundaries across narrow inspector panes without clipping.",
		EncryptionConfiguration: &bigquery.EncryptionConfiguration{
			KmsKeyName: "projects/my-long-project-id/locations/us/keyRings/main-ring/cryptoKeys/bq-cmek-key-very-long-identifier-01",
		},
		View: &bigquery.ViewDefinition{
			Query: "SELECT\n  event_id,\n  user_id,\n  COUNT(*) AS total_events\nFROM `my-long-project-id.analytics_prod_dataset.raw_events`\nGROUP BY 1, 2",
		},
		Schema: &bigquery.TableSchema{Fields: []*bigquery.TableFieldSchema{
			{Name: "very_long_unbroken_bigquery_nested_field_identifier", Type: "STRING", Mode: "NULLABLE", Description: "Long unbroken identifier field"},
			{Name: "event_id", Type: "STRING", Mode: "REQUIRED", Description: "Unique identifier for each ingested customer event across regions."},
		}},
	}
}

func layoutFlow(s *ui.Shell, dir string) layoutResult {
	var r layoutResult

	var origFrame appkit.Rect

	onMain(func() {
		origFrame = objc.Send[appkit.Rect](s.Window, "frame")
	})

	runNestedChecks(s, dir, &r)

	// Restore rich 5000-row orders run so Plan, Job, Results, JSON remain populated.
	waitRun(s, func() { s.RunSQL("SELECT order_id, created_at, customer, items, total FROM orders LIMIT 5000") })

	runInspectorLayoutChecks(s, dir, &r)
	runBottomTabsAndToolbarChecks(s, &r)
	runHistoryNotesAndThemeChecks(s, dir, &r)
	runNarrowWindowChecks(s, origFrame, &r)

	return r
}

func runNestedChecks(s *ui.Shell, dir string, r *layoutResult) {
	rn := app.NewRunner(app.RunnerOptions{
		Backend:  &smokeNestedBackend{},
		Post:     objc.Main,
		SpillDir: dir,
	})
	run := rn.Start(bq.QueryRequest{SQL: "SELECT id, repo, payload, hits, notes FROM nested_smoke"})
	<-run.Done()

	onMain(func() {
		s.ShowRun(run)
		s.SelectTab(ui.TabResults)
	})
	waitMain(loadTimeout, func() bool {
		return s.Results.Table.Rows() > 3 && s.JSON.Rows() == 3
	})

	var errs []string

	onMain(func() {
		errs = append(errs, checkNestedResults(s, dir, r)...)
		errs = append(errs, checkNestedJSON(s, dir, r)...)
		s.SelectTab(ui.TabResults)
	})

	r.NestedRenderedOK = len(errs) == 0
	if len(errs) == 0 {
		r.NestedRenderedDetail = "TableRenderedCell verified on Results nested parent/sub-rows and >2400px JSON row"
	} else {
		r.NestedRenderedDetail = strings.Join(errs, "; ")
	}
}

func checkNestedResults(s *ui.Shell, dir string, r *layoutResult) []string {
	var errs []string

	s.SelectTab(ui.TabResults)
	layoutWindow(s.Window)

	if s.Results.Table.Rows() <= 3 {
		errs = append(errs, fmt.Sprintf("Results.Table.Rows() = %d, want > 3", s.Results.Table.Rows()))
	}

	forceTableCell(s.Results.Table, 0, "c1")
	cParent := appkit.TableRenderedCell(s.Results.Table, 0, "c1")
	if !cParent.Found || cParent.Hidden || cParent.Text != "paulourio/bqui" {
		errs = append(errs, fmt.Sprintf("Results parent cell (0,c1) = %+v, want paulourio/bqui", cParent))
	}

	// Find nested sub-row columns dynamically from the grid schema.
	pageColID, tagsColID := "c6", "c9"
	if cur := s.Results.Current(); cur != nil {
		if g := cur.Snapshot().Grid; g != nil {
			for idx, col := range g.Columns() {
				switch col.Name {
				case "payload.pages.page_name":
					pageColID = "c" + strconv.Itoa(idx)
				case "hits.tags":
					tagsColID = "c" + strconv.Itoa(idx)
				}
			}
		}
	}

	forceTableCell(s.Results.Table, 1, pageColID)
	cSub := appkit.TableRenderedCell(s.Results.Table, 1, pageColID)
	if !cSub.Found || cSub.Hidden || cSub.Text != "NestedSubRow" {
		errs = append(errs, fmt.Sprintf("Results nested sub-row cell (1,%s) = %+v, want NestedSubRow", pageColID, cSub))
	}

	forceTableCell(s.Results.Table, 1, tagsColID)
	cTags := appkit.TableRenderedCell(s.Results.Table, 1, tagsColID)
	if !cTags.Found || cTags.Hidden || cTags.Text == "" {
		errs = append(errs, fmt.Sprintf("Results nested tags cell (1,%s) = %+v, want non-empty", tagsColID, cTags))
	}

	layoutWindow(s.Window)

	if err := capturePane(s.Results.View, filepath.Join(dir, "pane-results-nested.png")); err != nil {
		errs = append(errs, "pane-results-nested.png: "+err.Error())
	} else {
		r.Screenshots = append(r.Screenshots, "pane-results-nested.png")
	}

	return errs
}

func checkNestedJSON(s *ui.Shell, dir string, r *layoutResult) []string {
	var errs []string

	s.SelectTab(ui.TabJSON)
	layoutWindow(s.Window)

	wantJSON := s.JSON.Line(0)
	jsonW := appkit.MeasureGridTextWidth(wantJSON, true)
	if jsonW <= layoutMinJSONPx {
		errs = append(errs, fmt.Sprintf("nested JSON line width = %.1f, want > %.0f", jsonW, layoutMinJSONPx))
	}

	forceTableCell(s.JSON.Table, 0, "json")
	jc := appkit.TableRenderedCell(s.JSON.Table, 0, "json")
	if !jc.Found || jc.Hidden || jc.Text != wantJSON || jc.ColWidth <= layoutMinJSONPx {
		errs = append(errs, fmt.Sprintf("JSON TableRenderedCell (0,json) found=%v hidden=%v len=%d/%d colW=%.1f",
			jc.Found, jc.Hidden, len(jc.Text), len(wantJSON), jc.ColWidth))
	}

	if err := capturePane(s.JSON.View, filepath.Join(dir, "pane-json-nested.png")); err != nil {
		errs = append(errs, "pane-json-nested.png: "+err.Error())
	} else {
		r.Screenshots = append(r.Screenshots, "pane-json-nested.png")
	}

	return errs
}

func runInspectorLayoutChecks(s *ui.Shell, dir string, r *layoutResult) {
	var errs []string

	h440KMS, h440Desc, segDetail, detErrs, segErrs := checkInspectorDetails440And300(s, dir, r)
	errs = append(errs, detErrs...)
	errs = append(errs, checkInspectorNatalitySchema(s, dir, r)...)
	errs = append(errs, checkInspectorPageviewsSchema(s)...)
	errs = append(errs, checkInspectorValueTree(s, dir, r)...)

	r.InspectorSegOK = len(segErrs) == 0
	if len(segErrs) == 0 {
		r.InspectorSegDetail = segDetail
	} else {
		r.InspectorSegDetail = strings.Join(segErrs, "; ")
	}

	r.InspectorWrapOK = len(errs) == 0
	if len(errs) == 0 {
		r.InspectorWrapDetail = fmt.Sprintf("440pt (kms=%.0f, desc=%.0f) and 300pt wrapping/resize, CharWrap, title/subtitle, Schema child_race height > 28 verified", h440KMS, h440Desc)
	} else {
		r.InspectorWrapDetail = strings.Join(errs, "; ")
	}
}

func checkInspectorSegments(s *ui.Shell) (string, []string) {
	tb := s.Inspector.TabBar()
	btns := make([]objc.ID, 0, 3)
	names := make([]string, 0, 3)

	var errs []string

	for i := range 3 {
		b := tb.Button(i)
		btns = append(btns, b)
		names = append(names, fmt.Sprintf("seg%d", i))

		if w := appkit.ViewBounds(b).Size.Width; w <= 0 {
			errs = append(errs, fmt.Sprintf("button %d width=%v", i, w))
		}
	}

	if ok, detail := checkNoOverlap(s.Inspector.View, btns, names); !ok {
		errs = append(errs, detail)
	}

	return s.Inspector.SegmentGeometry(), errs
}

func checkInspectorDetails440And300(s *ui.Shell, dir string, r *layoutResult) (h440KMS, h440Desc float64, segDetail string, errs, segErrs []string) {
	cmekInfo := app.ObjectInfo{
		Kind:    app.KindView,
		Project: "my-long-project-id",
		Dataset: "analytics_prod_dataset",
		Name:    "customer_events_partitioned_2026_long_table_name",
	}

	onMain(func() {
		setInspectorWidth(s, layoutInspectorWide)
		s.Inspector.ShowTable(cmekInfo, smokeCMEKTable())
		s.Inspector.SelectSegment(1)
		layoutWindow(s.Window)
	})
	pumpMainLoop()

	onMain(func() {
		layoutWindow(s.Window)
		segDetail, segErrs = checkInspectorSegments(s)
		h440KMS, h440Desc, errs = inspectDetailsAt440(s, dir, r)
		setInspectorWidth(s, layoutInspectorNarrow)
	})
	pumpMainLoop()

	onMain(func() {
		layoutWindow(s.Window)
		errs = append(errs, inspectDetailsAt300(s, h440KMS, h440Desc)...)
		setInspectorWidth(s, layoutInspectorWide)
	})
	pumpMainLoop()

	return h440KMS, h440Desc, segDetail, errs, segErrs
}

func inspectDetailsAt440(s *ui.Shell, dir string, r *layoutResult) (h440KMS, h440Desc float64, errs []string) {
	details := s.Inspector.DetailsOutline()
	if ok, detail := checkOutlineNoHorizontalOverflow(details); !ok {
		errs = append(errs, "details@440: "+detail)
	}

	kmsRow := findOutlineRow(details, "key", "Encryption key")
	descRow := findOutlineRow(details, "key", "Description")
	sqlRow := findOutlineRow(details, "key", "View query")

	if kmsRow < 0 || descRow < 0 || sqlRow < 0 {
		errs = append(errs, fmt.Sprintf("details@440 missing rows kms=%d desc=%d sql=%d", kmsRow, descRow, sqlRow))
	} else {
		if g, ok, d := checkOutlineRowWrapped(details, kmsRow, "value", layoutLineBreakChar, 24); !ok {
			errs = append(errs, "details@440 kms: "+d)
		} else {
			h440KMS = g.RowBounds.Size.Height
		}

		if g, ok, d := checkOutlineRowWrapped(details, descRow, "value", layoutLineBreakWord, 24); !ok {
			errs = append(errs, "details@440 desc: "+d)
		} else {
			h440Desc = g.RowBounds.Size.Height
		}

		if _, ok, d := checkOutlineRowWrapped(details, sqlRow, "value", layoutLineBreakChar, 24); !ok {
			errs = append(errs, "details@440 sql: "+d)
		}
	}

	if err := capturePane(s.Inspector.View, filepath.Join(dir, "pane-inspector-details.png")); err != nil {
		errs = append(errs, "pane-inspector-details.png: "+err.Error())
	} else {
		r.Screenshots = append(r.Screenshots, "pane-inspector-details.png")
	}

	return h440KMS, h440Desc, errs
}

func inspectDetailsAt300(s *ui.Shell, h440KMS, h440Desc float64) []string {
	var errs []string

	details := s.Inspector.DetailsOutline()

	if ok, detail := checkOutlineNoHorizontalOverflow(details); !ok {
		errs = append(errs, "details@300: "+detail)
	}

	if row := findOutlineRow(details, "key", "Encryption key"); row < 0 {
		errs = append(errs, "details@300 missing Encryption key row")
	} else {
		if g, ok, d := checkOutlineRowWrapped(details, row, "value", layoutLineBreakChar, 24); !ok {
			errs = append(errs, "details@300 kms: "+d)
		} else if h440KMS > 0 && g.RowBounds.Size.Height < h440KMS {
			errs = append(errs, fmt.Sprintf("details@300 kms height %v < @440 %v", g.RowBounds.Size.Height, h440KMS))
		}
	}

	if row := findOutlineRow(details, "key", "Description"); row < 0 {
		errs = append(errs, "details@300 missing Description row")
	} else {
		if g, ok, d := checkOutlineRowWrapped(details, row, "value", layoutLineBreakWord, 24); !ok {
			errs = append(errs, "details@300 desc: "+d)
		} else if h440Desc > 0 && g.RowBounds.Size.Height < h440Desc {
			errs = append(errs, fmt.Sprintf("details@300 desc height %v < @440 %v", g.RowBounds.Size.Height, h440Desc))
		}
	}

	titleV := s.Inspector.TitleView()
	if objc.Send[bool](titleV, "usesSingleLineMode") {
		errs = append(errs, "Inspector title usesSingleLineMode=true")
	}

	if ok, d := checkContainment(s.Inspector.View, titleV, 12); !ok {
		errs = append(errs, "title@300: "+d)
	}

	if ok, d := checkTextFits(titleV, 0); !ok {
		errs = append(errs, "title@300 fit: "+d)
	}

	subV := s.Inspector.SubtitleView()
	if ok, d := checkContainment(s.Inspector.View, subV, 12); !ok {
		errs = append(errs, "subtitle@300: "+d)
	}

	if ok, d := checkTextFits(subV, 0); !ok {
		errs = append(errs, "subtitle@300 fit: "+d)
	}

	return errs
}

func checkInspectorNatalitySchema(s *ui.Shell, dir string, r *layoutResult) []string {
	natTbl, err := (&app.FakeBackend{}).GetTable(context.Background(), bq.TableRef{
		Project: "bigquery-public-data", Dataset: "samples", Table: "natality",
	})
	if err != nil {
		return []string{"load natality: " + err.Error()}
	}

	natInfo := app.ObjectInfo{Kind: app.KindTable, Project: "bigquery-public-data", Dataset: "samples", Name: "natality"}

	onMain(func() {
		s.Inspector.ShowTable(natInfo, natTbl)
		s.Inspector.SelectSegment(0)
		layoutWindow(s.Window)
	})
	pumpMainLoop()

	var errs []string

	onMain(func() {
		schema := s.Inspector.SchemaOutline()
		if ok, d := checkOutlineNoHorizontalOverflow(schema); !ok {
			errs = append(errs, "natality@440: "+d)
		}

		descCol := objc.Msg(schema.View, "tableColumnWithIdentifier:", objc.String("description"))
		if w := objc.Send[float64](descCol, "width"); w < 100 {
			errs = append(errs, fmt.Sprintf("natality@440 description col width = %.1f, want >= 100", w))
		}

		raceRow := findOutlineRow(schema, "name", "child_race")
		if raceRow < 0 {
			errs = append(errs, "natality@440 missing child_race")
		} else if _, ok, d := checkOutlineRowWrapped(schema, raceRow, "description", layoutLineBreakWord, 28); !ok {
			errs = append(errs, "natality@440 child_race: "+d)
		}

		if err := capturePane(s.Inspector.View, filepath.Join(dir, "pane-inspector-schema.png")); err != nil {
			errs = append(errs, "pane-inspector-schema.png: "+err.Error())
		} else {
			r.Screenshots = append(r.Screenshots, "pane-inspector-schema.png")
		}

		setInspectorWidth(s, layoutInspectorNarrow)
	})
	pumpMainLoop()

	onMain(func() {
		schema := s.Inspector.SchemaOutline()

		if ok, d := checkOutlineNoHorizontalOverflow(schema); !ok {
			errs = append(errs, "natality@300: "+d)
		}

		if colW, textW, found := appkit.OutlineHeaderGeometry(schema, "name"); !found || colW < math.Ceil(textW)+8 {
			errs = append(errs, fmt.Sprintf("natality@300 Field name header truncated: colW=%.1f textW=%.1f found=%v", colW, textW, found))
		}

		if raceRow := findOutlineRow(schema, "name", "child_race"); raceRow < 0 {
			errs = append(errs, "natality@300 missing child_race")
		} else if _, ok, d := checkOutlineRowWrapped(schema, raceRow, "description", layoutLineBreakWord, 28); !ok {
			errs = append(errs, "natality@300 child_race: "+d)
		}

		setInspectorWidth(s, layoutInspectorWide)
	})
	pumpMainLoop()

	return errs
}

func checkInspectorPageviewsSchema(s *ui.Shell) []string {
	pvTbl, err := (&app.FakeBackend{}).GetTable(context.Background(), bq.TableRef{
		Project: "bigquery-public-data", Dataset: "wikipedia", Table: "pageviews_2024",
	})
	if err != nil {
		return []string{"load pageviews_2024: " + err.Error()}
	}

	pvInfo := app.ObjectInfo{Kind: app.KindTable, Project: "bigquery-public-data", Dataset: "wikipedia", Name: "pageviews_2024"}

	onMain(func() {
		s.Inspector.ShowTable(pvInfo, pvTbl)
		s.Inspector.SelectSegment(0)
		layoutWindow(s.Window)
	})
	pumpMainLoop()

	var errs []string

	onMain(func() {
		schema := s.Inspector.SchemaOutline()
		attrCol := objc.Msg(schema.View, "tableColumnWithIdentifier:", objc.String("attr"))
		if objc.Send[bool](attrCol, "isHidden") {
			errs = append(errs, "pageviews@440 Attr column unexpectedly hidden")
		}

		if ok, d := checkOutlineNoHorizontalOverflow(schema); !ok {
			errs = append(errs, "pageviews@440: "+d)
		}

		setInspectorWidth(s, layoutInspectorNarrow)
	})
	pumpMainLoop()

	onMain(func() {
		schema := s.Inspector.SchemaOutline()
		if ok, d := checkOutlineNoHorizontalOverflow(schema); !ok {
			errs = append(errs, "pageviews@300: "+d)
		}

		if colW, textW, found := appkit.OutlineHeaderGeometry(schema, "name"); !found || colW < math.Ceil(textW)+8 {
			errs = append(errs, fmt.Sprintf("pageviews@300 Field name header truncated: colW=%.1f textW=%.1f found=%v", colW, textW, found))
		}

		attrCol := objc.Msg(schema.View, "tableColumnWithIdentifier:", objc.String("attr"))
		if objc.Send[bool](attrCol, "isHidden") {
			errs = append(errs, "pageviews@300 Attr column unexpectedly hidden")
		}

		// The Attr column fits its widest code exactly and never truncates it.
		if dateRow := findOutlineRow(schema, "name", "datehour"); dateRow < 0 {
			errs = append(errs, "pageviews@300 missing datehour row")
		} else if g := appkit.OutlineRowGeometry(schema, dateRow, "attr"); !g.Found || !slices.Contains(strings.Split(g.Text, "+"), "P") {
			errs = append(errs, fmt.Sprintf("pageviews@300 datehour attr = %+v, want a P code", g))
		} else if aw := objc.Send[float64](attrCol, "width"); aw < appkit.TextWidth(g.Text, appkit.FontRegular) || aw > appkit.TextWidth(g.Text, appkit.FontRegular)+40 {
			errs = append(errs, fmt.Sprintf("pageviews@300 Attr width %.1f for %q: truncated or too wide", aw, g.Text))
		}

		setInspectorWidth(s, layoutInspectorWide)
	})
	pumpMainLoop()

	return errs
}

func checkInspectorValueTree(s *ui.Shell, dir string, r *layoutResult) []string {
	var errs []string

	onMain(func() {
		if cur := s.Results.Current(); cur != nil {
			if g := cur.Snapshot().Grid; g != nil {
				if first, ferr := g.FirstLine(2); ferr == nil {
					s.Results.InspectCell(int(first)+1, 7)
				}
			}
		}

		layoutWindow(s.Window)

		if ok, d := checkOutlineNoHorizontalOverflow(s.Inspector.ValuesOutline()); !ok {
			errs = append(errs, "values@440: "+d)
		}

		if err := capturePane(s.Inspector.View, filepath.Join(dir, "pane-inspector-value.png")); err != nil {
			errs = append(errs, "pane-inspector-value.png: "+err.Error())
		} else {
			r.Screenshots = append(r.Screenshots, "pane-inspector-value.png")
		}
	})

	return errs
}

func runBottomTabsAndToolbarChecks(s *ui.Shell, r *layoutResult) {
	onMain(func() {
		s.SetBillingProject(app.FakeProject)
		appkit.SetString(s.Cost, "~14.2 GiB ($0.09)")
		layoutWindow(s.Window)

		r.BottomTabsOK, r.BottomTabsDetail = checkBottomTabsFit(s)

		total, vis, overflowed := appkit.ToolbarOverflow(s.Window)
		r.ToolbarOK = total >= 7 && vis == total && len(overflowed) == 0
		r.ToolbarDetail = fmt.Sprintf("toolbar total=%d visible=%d hidden=%d (%v)", total, vis, len(overflowed), overflowed)
	})
}

func checkBottomTabsFit(s *ui.Shell) (bool, string) {
	tabs := s.Tabs()
	n := tabs.SegmentCount()
	if n != 7 {
		return false, fmt.Sprintf("segment count=%d, want 7", n)
	}

	var segSum float64
	for i := range n {
		segSum += tabs.SegmentWidth(i)
	}

	tabsB := appkit.ViewBounds(tabs.View)
	if segSum > tabsB.Size.Width+1 {
		return false, fmt.Sprintf("sum(SegmentWidth)=%.1f > Tabs.View.Width=%.1f", segSum, tabsB.Size.Width)
	}

	for _, g := range appkit.SegmentedGeometry(tabs) {
		if g.Frame.Size.Width < math.Ceil(g.TextWidth)+layoutTabPadMin {
			return false, fmt.Sprintf("segment %q width %.1f < TextWidth %.1f + %.0f", g.Label, g.Frame.Size.Width, g.TextWidth, layoutTabPadMin)
		}
	}

	clip := objc.Msg(tabs.View, "superview")
	scroll := objc.Msg(clip, "superview")
	bottom := objc.Msg(scroll, "superview")
	bottomB := appkit.ViewBounds(bottom)
	scrollF := appkit.ViewFrameIn(scroll, bottom)
	if scrollF.Origin.X < 9.5 || scrollF.Origin.X+scrollF.Size.Width > bottomB.Size.Width-9.5 {
		return false, fmt.Sprintf("tabScroll frame %+v outside bottomPane %+v", scrollF, bottomB)
	}

	clipB := appkit.ViewBounds(clip)
	if clipB.Size.Height < tabsB.Size.Height-layoutBoundTol {
		return false, fmt.Sprintf("tabScroll NSClipView height %.1f < Tabs.View height %.1f", clipB.Size.Height, tabsB.Size.Height)
	}

	if hScroller := objc.Msg(scroll, "horizontalScroller"); appkit.IsVisible(hScroller) {
		sf := appkit.ViewFrameIn(hScroller, scroll)
		tf := appkit.ViewFrameIn(tabs.View, scroll)
		if sf.Origin.Y < tf.Origin.Y+tf.Size.Height-layoutBoundTol && sf.Origin.Y+sf.Size.Height > tf.Origin.Y+layoutBoundTol {
			return false, fmt.Sprintf("tabScroll horizontalScroller %+v occludes tabs %+v", sf, tf)
		}
	}

	tabF := appkit.ViewFrameIn(tabs.View, clip)
	if tabF.Origin.X < -layoutBoundTol || tabF.Origin.X+tabF.Size.Width > clipB.Size.Width+layoutBoundTol {
		return false, fmt.Sprintf("7 bottom tabs clipped by NSClipView: tabF=%+v clipB=%+v", tabF, clipB)
	}

	return true, fmt.Sprintf("7 bottom tabs fit (sum=%.0f <= clip=%.0f)", segSum, clipB.Size.Width)
}

func runHistoryNotesAndThemeChecks(s *ui.Shell, dir string, r *layoutResult) {
	var errs []string

	onMain(func() {
		s.SelectTab(ui.TabHistory)
		s.History.Shown()
	})
	waitMain(loadTimeout, func() bool { return len(s.History.Rows()) > 0 && !s.History.Model.Loading() })

	onMain(func() {
		layoutWindow(s.Window)

		if ok, d := checkHistoryHeader(s); !ok {
			errs = append(errs, "history@1280: "+d)
		}

		errs = append(errs, capturePaneTabs(s, dir, r)...)
		errs = append(errs, checkPlanAndGraphRuntime(s)...)
		errs = append(errs, checkCenteredNotes(s)...)
		errs = append(errs, checkPrefsMultiLineError(s)...)
		errs = append(errs, checkThemeGridScaling(s)...)
		s.SelectTab(ui.TabResults)
	})

	r.HistoryNotesThemeOK = len(errs) == 0
	if len(errs) == 0 {
		r.HistoryNotesThemeDetail = "History header, Plan/Graph boxes, 7 centered notes, prefs multi-line/CharWrap message, and theme GridSize scaling verified"
	} else {
		r.HistoryNotesThemeDetail = strings.Join(errs, "; ")
	}
}

func checkPlanAndGraphRuntime(s *ui.Shell) []string {
	var errs []string

	if rows := s.Plan.Rows(); rows < 1 {
		errs = append(errs, fmt.Sprintf("Plan.Rows() = %d, want >= 1", rows))
	}

	l := s.Graph.Layout()
	if len(l.Nodes) == 0 {
		return append(errs, "Graph.Layout() has 0 nodes")
	}

	nodes := make([]*plan.Node, 0, len(l.Nodes))
	for _, n := range l.Nodes {
		nodes = append(nodes, n)

		if n.X < -layoutBoundTol || n.Y < -layoutBoundTol ||
			n.X+plan.NodeWidth > l.Width+layoutBoundTol ||
			n.Y+plan.NodeHeight > l.Height+layoutBoundTol {
			errs = append(errs, fmt.Sprintf("graph stage %d (%v,%v) exceeds canvas %.0fx%.0f", n.ID, n.X, n.Y, l.Width, l.Height))
		}
	}

	for i := range nodes {
		for j := i + 1; j < len(nodes); j++ {
			a, b := nodes[i], nodes[j]
			overlapX := a.X < b.X+plan.NodeWidth-layoutBoundTol && b.X < a.X+plan.NodeWidth-layoutBoundTol
			overlapY := a.Y < b.Y+plan.NodeHeight-layoutBoundTol && b.Y < a.Y+plan.NodeHeight-layoutBoundTol

			if overlapX && overlapY {
				errs = append(errs, fmt.Sprintf("graph stage %d overlaps stage %d", a.ID, b.ID))
			}
		}
	}

	return errs
}

func capturePaneTabs(s *ui.Shell, dir string, r *layoutResult) []string {
	var errs []string

	tabs := []struct {
		tab  int
		view objc.ID
		name string
	}{
		{ui.TabHistory, s.History.View, "pane-history.png"},
		{ui.TabPlan, s.Plan.View, "pane-plan.png"},
		{ui.TabGraph, s.Graph.View, "pane-graph.png"},
		{ui.TabJob, s.Job.View, "pane-job.png"},
		{ui.TabRuns, s.Runs.View, "pane-runs.png"},
	}

	for _, tc := range tabs {
		s.SelectTab(tc.tab)
		layoutWindow(s.Window)

		if err := capturePane(tc.view, filepath.Join(dir, tc.name)); err != nil {
			errs = append(errs, tc.name+": "+err.Error())
		} else {
			r.Screenshots = append(r.Screenshots, tc.name)
		}
	}

	return errs
}

func checkCenteredNotes(s *ui.Shell) []string {
	var errs []string

	notes := []struct {
		name   string
		parent objc.ID
		view   objc.ID
	}{
		{"results.empty", s.Results.View, s.Results.EmptyView()},
		{"json.note", s.JSON.View, s.JSON.NoteView()},
		{"plan.note", s.Plan.View, s.Plan.NoteView()},
		{"graph.note", s.Graph.View, s.Graph.NoteView()},
		{"job.note", s.Job.View, s.Job.NoteView()},
		{"runs.note", s.Runs.View, s.Runs.NoteView()},
		{"inspector.status", s.Inspector.View, s.Inspector.StatusView()},
	}

	for _, n := range notes {
		if got := objc.Send[int64](n.view, "alignment"); got != appkit.TextAlignmentCenter {
			errs = append(errs, fmt.Sprintf("%s alignment=%d want %d", n.name, got, appkit.TextAlignmentCenter))
		}

		pb := appkit.ViewBounds(n.parent)
		lf := appkit.ViewFrameIn(n.view, n.parent)
		if lf.Size.Width > 0 && (lf.Origin.X < 12-layoutBoundTol || lf.Origin.X+lf.Size.Width > pb.Size.Width-12+layoutBoundTol) {
			errs = append(errs, fmt.Sprintf("%s frame %+v outside [12, %.0f]", n.name, lf, pb.Size.Width-12))
		}
	}

	return errs
}

func checkPrefsMultiLineError(s *ui.Shell) []string {
	var errs []string

	s.ShowPreferences()
	pw := s.PreferencesWindow()
	msg := s.PreferencesMessageView()
	appkit.SetString(msg, "✕ Invalid preference configuration: cost warning threshold must be a positive dollar amount such as 1.50 or blank to disable. Please review all fields.")
	layoutWindow(pw)

	cv := objc.Msg(pw, "contentView")
	cvB := appkit.ViewBounds(cv)
	msgF := appkit.ViewFrameIn(msg, cv)

	if msgF.Size.Height <= appkit.MeasureText(msg, 0).Height {
		errs = append(errs, fmt.Sprintf("prefs message did not wrap (h=%v)", msgF.Size.Height))
	}

	for _, sub := range objc.ArrayItems(objc.Msg(cv, "subviews")) {
		sf := appkit.ViewFrameIn(sub, cv)
		if sf.Origin.Y < -layoutBoundTol || sf.Origin.Y+sf.Size.Height > cvB.Size.Height+layoutBoundTol {
			errs = append(errs, fmt.Sprintf("prefs subview %+v exceeds content bounds %+v", sf, cvB))
		}
	}

	s.SetPreferencesMessage("✕ default dataset: invalid_very_long_project_and_dataset_identifier_without_spaces_2026")
	layoutWindow(pw)

	if got := objc.Send[int64](msg, "lineBreakMode"); got != layoutLineBreakChar {
		errs = append(errs, fmt.Sprintf("prefs long token lineBreakMode = %d, want %d", got, layoutLineBreakChar))
	}

	charF := appkit.ViewFrameIn(msg, cv)
	if charF.Size.Height <= appkit.MeasureText(msg, 0).Height || charF.Origin.X+charF.Size.Width > cvB.Size.Width+layoutBoundTol {
		errs = append(errs, fmt.Sprintf("prefs long token frame %+v invalid inside %+v", charF, cvB))
	}

	objc.Msg(pw, "orderOut:", objc.Nil)

	return errs
}

func checkThemeGridScaling(s *ui.Shell) []string {
	var errs []string

	base := theme.Find(s.Themes, theme.DefaultName)
	if base == nil {
		return []string{"missing default theme"}
	}

	large := *base
	large.Name = "SmokeLargeGrid"
	large.Fonts.GridSize = layoutThemeGridSize
	s.Themes = append(s.Themes, &large)
	s.ApplyTheme("SmokeLargeGrid")

	for name, tbl := range map[string]*appkit.Table{
		"Results": s.Results.Table,
		"JSON":    s.JSON.Table,
		"History": s.History.Table,
		"Runs":    s.Runs.Table,
		"Preview": s.Inspector.PreviewPane().Table,
	} {
		if rh := objc.Send[float64](tbl.View, "rowHeight"); rh < layoutThemeMinRowH {
			errs = append(errs, fmt.Sprintf("%s rowHeight=%v < %v", name, rh, layoutThemeMinRowH))
		}
	}

	s.ApplyTheme(theme.DefaultName)

	return errs
}

func checkHistoryHeader(s *ui.Shell) (bool, string) {
	scope, refresh, count, search := s.History.HeaderViews()
	if cw := appkit.ViewBounds(count).Size.Width; cw < 40 {
		return false, fmt.Sprintf("count width=%v < 40", cw)
	}

	for _, g := range appkit.SegmentedGeometry(scope) {
		if g.Frame.Size.Width < math.Ceil(g.TextWidth)+14 {
			return false, fmt.Sprintf("history scope %q width %.1f < TextWidth %.1f + 14", g.Label, g.Frame.Size.Width, g.TextWidth)
		}
	}

	return checkNoOverlap(s.History.View, []objc.ID{scope.View, refresh, count, search}, []string{"scope", "refresh", "count", "search"})
}

func runNarrowWindowChecks(s *ui.Shell, origFrame appkit.Rect, r *layoutResult) {
	restoreWideLayout(s)

	onMain(func() {
		appkit.WindowResize(s.Window, layoutNarrowWidth, layoutNarrowHeight)
		layoutWindow(s.Window)
	})
	pumpMainLoop()

	var errs []string

	onMain(func() {
		errs = verifyNarrowLayout(s)
		objc.Msg(s.Window, "setFrame:display:", origFrame, true)
		setInspectorWidth(s, layoutInspectorWide)
		s.SelectTab(ui.TabResults)
		layoutWindow(s.Window)
	})

	restoreWideLayout(s)

	r.NarrowWindowOK = len(errs) == 0
	if len(errs) == 0 {
		r.NarrowWindowDetail = "900x600 narrow window containment, non-overlap, split panes, bottom tabs, schema header/rows, results status/gutter, editor bounds, explorer alignment, history header, and toolbar verified"
	} else {
		r.NarrowWindowDetail = strings.Join(errs, "; ")
	}
}

func verifyNarrowLayout(s *ui.Shell) []string {
	var errs []string

	cvB := appkit.ViewBounds(objc.Msg(s.Window, "contentView"))
	if math.Abs(cvB.Size.Width-layoutNarrowWidth) > 1.5 || math.Abs(cvB.Size.Height-layoutNarrowHeight) > 1.5 {
		errs = append(errs, fmt.Sprintf("contentView size=%.0fx%.0f want 900x600", cvB.Size.Width, cvB.Size.Height))
	}

	items := objc.ArrayItems(objc.Msg(s.Split, "splitViewItems"))
	var contentView objc.ID

	for idx, name := range []string{"sidebar", "content", "inspector"} {
		if idx >= len(items) {
			break
		}

		vw := objc.Msg(objc.Msg(items[idx], "viewController"), "view")
		if name == "content" {
			contentView = vw
		}

		if w := appkit.ViewBounds(vw).Size.Width; w <= 36 {
			errs = append(errs, fmt.Sprintf("%s pane width=%v <= 36", name, w))
		}
	}

	if ok, d := checkBottomTabsFit(s); !ok {
		errs = append(errs, "tabs@900x600: "+d)
	}

	errs = append(errs, checkNarrowToolbar(s)...)
	errs = append(errs, checkNarrowSchema(s)...)
	errs = append(errs, checkNarrowResults(s)...)
	errs = append(errs, checkNarrowEditor(s, contentView)...)
	errs = append(errs, checkNarrowExplorer(s)...)

	s.SelectTab(ui.TabHistory)
	layoutWindow(s.Window)

	if ok, d := checkHistoryHeader(s); !ok {
		errs = append(errs, "history@900x600: "+d)
	}

	s.SelectTab(ui.TabResults)

	return errs
}

func checkNarrowToolbar(s *ui.Shell) []string {
	_, vis, over := appkit.ToolbarOverflow(s.Window)
	if vis < 6 {
		return []string{fmt.Sprintf("toolbar@900x600 visible=%d < 6 over=%v", vis, over)}
	}

	for _, id := range over {
		if id != "cost" {
			return []string{fmt.Sprintf("toolbar@900x600 critical item %q overflowed: %v", id, over)}
		}
	}

	return nil
}

func checkNarrowSchema(s *ui.Shell) []string {
	var errs []string

	schema := s.Inspector.SchemaOutline()
	if ok, d := checkOutlineNoHorizontalOverflow(schema); !ok {
		errs = append(errs, "schema@900x600: "+d)
	}

	if colW, textW, found := appkit.OutlineHeaderGeometry(schema, "name"); !found || colW < math.Ceil(textW)+8 {
		errs = append(errs, fmt.Sprintf("schema@900x600 Field name header truncated: colW=%.1f textW=%.1f found=%v", colW, textW, found))
	}

	for _, fieldName := range []string{"repository", "created_at"} {
		row := findOutlineRow(schema, "name", fieldName)
		if row < 0 {
			continue
		}

		g := appkit.OutlineRowGeometry(schema, row, "name")
		if g.Found && g.RowBounds.Size.Height > g.SingleLineSize.Height+10 {
			errs = append(errs, fmt.Sprintf("schema@900x600 field %q wrapped unnecessarily (rowH=%.1f singleH=%.1f)", fieldName, g.RowBounds.Size.Height, g.SingleLineSize.Height))
		}
	}

	return errs
}

func checkNarrowResults(s *ui.Shell) []string {
	var errs []string

	s.SelectTab(ui.TabResults)
	layoutWindow(s.Window)

	statusV := s.Results.StatusView()
	if ok, d := checkContainment(s.Results.View, statusV, 8); !ok {
		errs = append(errs, "results.status@900x600: "+d)
	}

	if ok, d := checkTextFits(statusV, 0); !ok {
		errs = append(errs, "results.status@900x600 fit: "+d)
	}

	stB := appkit.ViewBounds(statusV)
	single := appkit.MeasureText(statusV, 0)
	if stB.Size.Height > single.Height+2.5 {
		errs = append(errs, fmt.Sprintf("results.status@900x600 wrapped to multiple lines (h=%.1f single=%.1f text=%q)", stB.Size.Height, single.Height, s.Results.StatusText()))
	}

	tblScroll := s.Results.Table.Scroll
	if ok, d := checkContainment(s.Results.View, tblScroll, 0); !ok {
		errs = append(errs, "results.table@900x600: "+d)
	}

	colIdx := objc.Send[int64](s.Results.Table.View, "columnWithIdentifier:", objc.String("__row"))
	if colIdx < 0 {
		errs = append(errs, "results.table@900x600 missing __row gutter column")
	}

	return errs
}

func checkNarrowEditor(s *ui.Shell, contentView objc.ID) []string {
	if contentView == objc.Nil {
		return []string{"editor@900x600 missing contentView"}
	}

	edF := appkit.ViewFrameIn(s.Editor.Web.View, contentView)
	if edF.Size.Width < 200 || edF.Size.Height < 100 {
		return []string{fmt.Sprintf("editor@900x600 frame too small: %+v", edF)}
	}

	return nil
}

func checkNarrowExplorer(s *ui.Shell) []string {
	var errs []string

	if ok, d := checkOutlineNoHorizontalOverflow(s.Explorer.Outline); !ok {
		errs = append(errs, "explorer@900x600: "+d)
	}

	search, star := s.Explorer.HeaderViews()
	if ok, d := checkNoOverlap(s.Explorer.View, []objc.ID{search, star}, []string{"explorer.search", "explorer.star"}); !ok {
		errs = append(errs, "explorer.header@900x600: "+d)
	}

	return errs
}

func restoreWideLayout(s *ui.Shell) {
	tbl, err := (&app.FakeBackend{}).GetTable(context.Background(), bq.TableRef{
		Project: "bigquery-public-data", Dataset: "samples", Table: "github_nested",
	})

	onMain(func() {
		if err == nil {
			info := app.ObjectInfo{Kind: app.KindTable, Project: "bigquery-public-data", Dataset: "samples", Name: "github_nested"}
			s.Inspector.ShowTable(info, tbl)
			s.Inspector.SelectSegment(0)
		}

		if cur := s.Results.Current(); cur != nil {
			if g := cur.Snapshot().Grid; g != nil && len(g.Columns()) > 0 {
				s.Results.Table.ScrollToColumn("c" + strconv.Itoa(len(g.Columns())-1))
			}
		}

		layoutWindow(s.Window)
	})
}

func layoutChecks(rep *Report, r layoutResult) {
	rep.check("table-rendered-cells-nested", r.NestedRenderedOK, "%s", r.NestedRenderedDetail)
	rep.check("inspector-wrap-resize-440-300", r.InspectorWrapOK, "%s", r.InspectorWrapDetail)
	rep.check("inspector-segments", r.InspectorSegOK, "%s", r.InspectorSegDetail)
	rep.check("bottom-tabs-fit", r.BottomTabsOK, "%s", r.BottomTabsDetail)
	rep.check("toolbar-layout", r.ToolbarOK, "%s", r.ToolbarDetail)
	rep.check("history-notes-theme-layout", r.HistoryNotesThemeOK, "%s", r.HistoryNotesThemeDetail)
	rep.check("narrow-window-900x600", r.NarrowWindowOK, "%s", r.NarrowWindowDetail)
	rep.Screenshots = append(rep.Screenshots, r.Screenshots...)
}

func setInspectorWidth(s *ui.Shell, width float64) {
	outer := objc.Msg(s.Split, "splitView")
	totalW := appkit.ViewBounds(outer).Size.Width
	if totalW > width {
		objc.Msg(outer, "setPosition:ofDividerAtIndex:", totalW-width, int64(1))
	}

	layoutWindow(s.Window)
}

func layoutWindow(win objc.ID) {
	if win == objc.Nil {
		return
	}

	cv := objc.Msg(win, "contentView")
	if cv == objc.Nil {
		return
	}

	target := objc.Msg(cv, "superview")
	if target == objc.Nil {
		target = cv
	}

	objc.Msg(target, "layoutSubtreeIfNeeded")
	objc.Msg(target, "displayIfNeeded")
}

func pumpMainLoop() {
	onMain(func() {})
	time.Sleep(50 * time.Millisecond)
	onMain(func() {})
}

func capturePane(view objc.ID, path string) error {
	if view == objc.Nil {
		return fmt.Errorf("nil view for %s", filepath.Base(path))
	}

	objc.Msg(view, "layoutSubtreeIfNeeded")
	objc.Msg(view, "displayIfNeeded")

	return appkit.WritePNG(view, path)
}

func forceTableCell(t *appkit.Table, row int, colID string) {
	colIdx := objc.Send[int64](t.View, "columnWithIdentifier:", objc.String(colID))
	if colIdx < 0 {
		return
	}

	objc.Msg(t.View, "scrollRowToVisible:", int64(row))
	objc.Msg(t.View, "scrollColumnToVisible:", colIdx)
	objc.Msg(t.View, "viewAtColumn:row:makeIfNecessary:", colIdx, int64(row), true)
	objc.Msg(t.View, "layoutSubtreeIfNeeded")
}

func findOutlineRow(o *appkit.Outline, colID, want string) int {
	n := int(o.Rows())
	colIdx := objc.Send[int64](o.View, "columnWithIdentifier:", objc.String(colID))

	for row := range n {
		objc.Msg(o.View, "scrollRowToVisible:", int64(row))
		objc.Msg(o.View, "viewAtColumn:row:makeIfNecessary:", colIdx, int64(row), true)

		g := appkit.OutlineRowGeometry(o, row, colID)
		if g.Found && g.Text == want {
			return row
		}
	}

	return -1
}

func checkOutlineRowWrapped(o *appkit.Outline, row int, colID string, wantBreak int, minHeight float64) (appkit.RowCellGeom, bool, string) {
	colIdx := objc.Send[int64](o.View, "columnWithIdentifier:", objc.String(colID))
	objc.Msg(o.View, "scrollRowToVisible:", int64(row))
	objc.Msg(o.View, "viewAtColumn:row:makeIfNecessary:", colIdx, int64(row), true)

	g := appkit.OutlineRowGeometry(o, row, colID)
	if !g.Found {
		return g, false, fmt.Sprintf("cell (%d,%s) not found", row, colID)
	}

	if g.UsesSingleLine {
		return g, false, "usesSingleLineMode=true"
	}

	if g.MaxLines != 0 {
		return g, false, fmt.Sprintf("maximumNumberOfLines=%d want 0", g.MaxLines)
	}

	if g.LineBreakMode != wantBreak {
		return g, false, fmt.Sprintf("lineBreakMode=%d want %d", g.LineBreakMode, wantBreak)
	}

	if g.RowBounds.Size.Height <= minHeight || g.RowBounds.Size.Height <= g.SingleLineSize.Height+10 {
		return g, false, fmt.Sprintf("row height=%.1f (single=%.1f) want > %.1f", g.RowBounds.Size.Height, g.SingleLineSize.Height, minHeight)
	}

	if g.WrappedSize.Height > g.TextFrame.Size.Height+2 {
		return g, false, fmt.Sprintf("clipped wrapped=%.1f > textFrame=%.1f", g.WrappedSize.Height, g.TextFrame.Size.Height)
	}

	return g, true, ""
}

func checkOutlineNoHorizontalOverflow(o *appkit.Outline) (bool, string) {
	if objc.Send[bool](o.Scroll, "hasHorizontalScroller") {
		return false, "hasHorizontalScroller=true"
	}

	contentW := objc.Send[appkit.Rect](objc.Msg(o.Scroll, "contentView"), "bounds").Size.Width
	docW := appkit.ViewBounds(o.View).Size.Width

	if docW > contentW+1.5 {
		return false, fmt.Sprintf("docW=%.1f > contentW=%.1f", docW, contentW)
	}

	return true, ""
}

func checkContainment(parent, child objc.ID, inset float64) (bool, string) {
	pb := appkit.ViewBounds(parent)
	cf := appkit.ViewFrameIn(child, parent)

	if cf.Origin.X < inset-layoutInsetTol ||
		cf.Origin.X+cf.Size.Width > pb.Size.Width-inset+layoutInsetTol ||
		cf.Origin.Y < -layoutBoundTol ||
		cf.Origin.Y+cf.Size.Height > pb.Size.Height+layoutBoundTol {
		return false, fmt.Sprintf("frame %+v outside parent %+v (inset %.0f)", cf, pb, inset)
	}

	return true, ""
}

func checkNoOverlap(parent objc.ID, views []objc.ID, names []string) (bool, string) {
	pb := appkit.ViewBounds(parent)
	rects := make([]appkit.Rect, len(views))

	for i, v := range views {
		r := appkit.ViewFrameIn(v, parent)
		rects[i] = r

		if r.Origin.X < -layoutBoundTol || r.Origin.Y < -layoutBoundTol ||
			r.Origin.X+r.Size.Width > pb.Size.Width+layoutBoundTol ||
			r.Origin.Y+r.Size.Height > pb.Size.Height+layoutBoundTol {
			return false, fmt.Sprintf("%s frame %+v outside parent %+v", names[i], r, pb)
		}
	}

	for i := range rects {
		for j := i + 1; j < len(rects); j++ {
			a, b := rects[i], rects[j]
			overlapX := a.Origin.X < b.Origin.X+b.Size.Width-layoutBoundTol && b.Origin.X < a.Origin.X+a.Size.Width-layoutBoundTol
			overlapY := a.Origin.Y < b.Origin.Y+b.Size.Height-layoutBoundTol && b.Origin.Y < a.Origin.Y+a.Size.Height-layoutBoundTol

			if overlapX && overlapY {
				return false, fmt.Sprintf("%s (%+v) overlaps %s (%+v)", names[i], a, names[j], b)
			}
		}
	}

	return true, ""
}

func checkTextFits(tf objc.ID, maxWidth float64) (bool, string) {
	b := appkit.ViewBounds(tf)
	w := maxWidth
	if w <= 0 {
		w = b.Size.Width
	}

	measured := appkit.MeasureText(tf, w)
	if measured.Height > b.Size.Height+2.5 {
		return false, fmt.Sprintf("measured=%.1f > bounds=%.1f", measured.Height, b.Size.Height)
	}

	return true, ""
}
