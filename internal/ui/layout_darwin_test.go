//go:build darwin

package ui

import (
	"context"
	"math"
	"strings"
	"testing"

	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/results"
	"github.com/paulourio/bqui/internal/theme"
)

func pumpMain(t *testing.T) {
	t.Helper()
	onMain(t, func() {})
}

func assertNoOutlineHorizontalOverflow(t *testing.T, o *appkit.Outline, label string) {
	t.Helper()

	if objc.Send[bool](o.Scroll, "hasHorizontalScroller") {
		t.Errorf("%s: expected hasHorizontalScroller == false", label)
	}

	contentW := objc.Send[appkit.Rect](objc.Msg(o.Scroll, "contentView"), "bounds").Size.Width
	docW := appkit.ViewBounds(o.View).Size.Width

	if docW > contentW+1.5 {
		t.Errorf("%s: horizontal overflow docW=%v > contentW=%v", label, docW, contentW)
	}
}

func findOutlineRowByKey(o *appkit.Outline, keyColID, want string) int {
	n := int(o.Rows())
	colIdx := objc.Send[int64](o.View, "columnWithIdentifier:", objc.String(keyColID))

	for row := range n {
		objc.Msg(o.View, "scrollRowToVisible:", int64(row))
		objc.Msg(o.View, "viewAtColumn:row:makeIfNecessary:", colIdx, int64(row), true)

		g := appkit.OutlineRowGeometry(o, row, keyColID)
		if g.Found && g.Text == want {
			return row
		}
	}

	return -1
}

func inspectOutlineCell(o *appkit.Outline, row int, colID string) appkit.RowCellGeom {
	colIdx := objc.Send[int64](o.View, "columnWithIdentifier:", objc.String(colID))
	objc.Msg(o.View, "scrollRowToVisible:", int64(row))
	objc.Msg(o.View, "viewAtColumn:row:makeIfNecessary:", colIdx, int64(row), true)

	return appkit.OutlineRowGeometry(o, row, colID)
}

func assertWrappedOutlineCell(t *testing.T, o *appkit.Outline, row int, colID string, wantBreak int, label string) {
	t.Helper()

	g := inspectOutlineCell(o, row, colID)
	if !g.Found {
		t.Fatalf("%s: cell (%d, %q) not found", label, row, colID)
	}

	if g.UsesSingleLine {
		t.Errorf("%s: UsesSingleLine = true, want false", label)
	}

	if g.MaxLines != 0 {
		t.Errorf("%s: MaxLines = %d, want 0", label, g.MaxLines)
	}

	if g.LineBreakMode != wantBreak {
		t.Errorf("%s: LineBreakMode = %d, want %d", label, g.LineBreakMode, wantBreak)
	}

	if g.RowBounds.Size.Height <= 24 || g.RowBounds.Size.Height <= g.SingleLineSize.Height+10 {
		t.Errorf("%s: RowBounds.Height = %v (singleLine=%v), want wrapped multi-line (> 24 and > singleLine+10)",
			label, g.RowBounds.Size.Height, g.SingleLineSize.Height)
	}

	if g.WrappedSize.Height > g.TextFrame.Size.Height+2 {
		t.Errorf("%s: clipped wrapped text WrappedSize.Height=%v > TextFrame.Height=%v",
			label, g.WrappedSize.Height, g.TextFrame.Size.Height)
	}
}

func assertRectsNonOverlappingAndContained(t *testing.T, parent appkit.Rect, rects []appkit.Rect, names []string, label string) {
	t.Helper()

	for i, r := range rects {
		if r.Origin.X < -0.5 || r.Origin.Y < -0.5 ||
			r.Origin.X+r.Size.Width > parent.Size.Width+0.5 ||
			r.Origin.Y+r.Size.Height > parent.Size.Height+0.5 {
			t.Errorf("%s: %s frame %+v outside parent %+v", label, names[i], r, parent)
		}
	}

	for i := range rects {
		for j := i + 1; j < len(rects); j++ {
			a, b := rects[i], rects[j]
			overlapX := a.Origin.X < b.Origin.X+b.Size.Width-0.5 && b.Origin.X < a.Origin.X+a.Size.Width-0.5
			overlapY := a.Origin.Y < b.Origin.Y+b.Size.Height-0.5 && b.Origin.Y < a.Origin.Y+a.Size.Height-0.5

			if overlapX && overlapY {
				t.Errorf("%s: %s (%+v) overlaps %s (%+v)", label, names[i], a, names[j], b)
			}
		}
	}
}

func TestInspectorDetailsAndSchemaWrapping(t *testing.T) {
	be := &app.FakeBackend{}
	gaTbl, err := be.GetTable(context.Background(), bq.TableRef{Project: "bigquery-public-data", Dataset: "google_analytics_sample", Table: "ga_sessions_20170801"})
	if err != nil {
		t.Fatal(err)
	}

	pvTbl, err := be.GetTable(context.Background(), bq.TableRef{Project: "bigquery-public-data", Dataset: "wikipedia", Table: "pageviews_2024"})
	if err != nil {
		t.Fatal(err)
	}

	natTbl, err := be.GetTable(context.Background(), bq.TableRef{Project: "bigquery-public-data", Dataset: "samples", Table: "natality"})
	if err != nil {
		t.Fatal(err)
	}

	holTbl, err := be.GetTable(context.Background(), bq.TableRef{Project: "bigquery-public-data", Dataset: "ml_datasets", Table: "holidays_and_events_for_forecasting"})
	if err != nil {
		t.Fatal(err)
	}

	cmekTbl := &bigquery.Table{
		TableReference: &bigquery.TableReference{ProjectId: "my-long-project-id", DatasetId: "analytics_prod_dataset", TableId: "customer_events_partitioned"},
		Type:           "TABLE",
		Location:       "US",
		Description:    "This prose description explains how customer events are ingested from upstream pipelines and should wrap cleanly at word boundaries across narrow inspector panes.",
		EncryptionConfiguration: &bigquery.EncryptionConfiguration{
			KmsKeyName: "projects/my-long-project-id/locations/us/keyRings/main-ring/cryptoKeys/bq-cmek-key-01",
		},
		Schema: &bigquery.TableSchema{Fields: []*bigquery.TableFieldSchema{
			{Name: "event_id", Type: "STRING", Mode: "REQUIRED"},
		}},
	}

	var (
		p   *InspectorPane
		win objc.ID
	)

	onMain(t, func() {
		p = newInspectorPane(nil, be)
		win = appkit.NewWindow("inspector-wrap", appkit.MakeRect(0, 0, 440, 640))
		appkit.AddFilling(objc.Msg(win, "contentView"), p.View, 0)

		gaInfo := app.ObjectInfo{Kind: app.KindTable, Project: "bigquery-public-data", Dataset: "google_analytics_sample", Name: "ga_sessions_20170801"}
		p.current = gaInfo
		p.setSubtitle(gaInfo.Kind.String() + " · " + gaInfo.FullID())
		p.apply(p.gen, gaInfo, gaTbl, nil)
		appkit.WindowResize(win, 440, 640)

		if got := objc.Send[uint64](p.subtitle, "lineBreakMode"); got != lineBreakCharWrap {
			t.Errorf("long FullID subtitle lineBreakMode = %d, want %d", got, lineBreakCharWrap)
		}

		subFrame := appkit.ViewFrameIn(p.subtitle, p.View)
		// Borderless NSTextField alignmentRectForFrame adds ~2pt horizontal padding per side.
		if subFrame.Origin.X < inspectorInset-2.5 || subFrame.Origin.X+subFrame.Size.Width > 440-inspectorInset+2.5 {
			t.Errorf("subtitle frame %+v exceeds horizontal bounds at 440pt", subFrame)
		}

		appkit.WindowResize(win, 300, 640)
		sub300 := appkit.ViewFrameIn(p.subtitle, p.View)
		if sub300.Size.Height <= appkit.MeasureText(p.subtitle, 0).Height {
			t.Errorf("subtitle height %v should wrap > single line at 300pt", sub300.Size.Height)
		}

		// Long unbroken title wraps instead of truncating at 300pt.
		p.setTitle("holidays_and_events_for_forecasting_very_long_table_name")
		appkit.WindowResize(win, 300, 640)
		if objc.Send[bool](p.title, "usesSingleLineMode") {
			t.Error("InspectorPane.title usesSingleLineMode should be false")
		}

		if got := objc.Send[uint64](p.title, "lineBreakMode"); got != lineBreakCharWrap {
			t.Errorf("long InspectorPane.title lineBreakMode = %d, want %d", got, lineBreakCharWrap)
		}

		title300 := appkit.ViewFrameIn(p.title, p.View)
		if title300.Size.Height <= appkit.MeasureText(p.title, 0).Height {
			t.Errorf("title height %v should wrap > single line at 300pt", title300.Size.Height)
		}

		// CMEK + prose description in Details tab at 440pt and 300pt.
		cmekInfo := app.ObjectInfo{Kind: app.KindTable, Project: "my-long-project-id", Dataset: "analytics_prod_dataset", Name: "customer_events_partitioned"}
		p.current = cmekInfo
		p.setSubtitle(cmekInfo.Kind.String() + " · " + cmekInfo.FullID())
		p.apply(p.gen, cmekInfo, cmekTbl, nil)
		p.SelectSegment(segDetails)
		appkit.WindowResize(win, 440, 640)
		assertNoOutlineHorizontalOverflow(t, p.details, "details@440")

		kmsRow := findOutlineRowByKey(p.details, "key", "Encryption key")
		descRow := findOutlineRowByKey(p.details, "key", "Description")
		if kmsRow < 0 || descRow < 0 {
			t.Fatalf("missing rows in details: kms=%d desc=%d", kmsRow, descRow)
		}

		assertWrappedOutlineCell(t, p.details, kmsRow, "value", int(lineBreakCharWrap), "details@440 kms")
		assertWrappedOutlineCell(t, p.details, descRow, "value", int(lineBreakWordWrap), "details@440 desc")

		appkit.WindowResize(win, 300, 640)
	})

	pumpMain(t)

	onMain(t, func() {
		assertNoOutlineHorizontalOverflow(t, p.details, "details@300")
		kmsRow := findOutlineRowByKey(p.details, "key", "Encryption key")
		descRow := findOutlineRowByKey(p.details, "key", "Description")
		assertWrappedOutlineCell(t, p.details, kmsRow, "value", int(lineBreakCharWrap), "details@300 kms")
		assertWrappedOutlineCell(t, p.details, descRow, "value", int(lineBreakWordWrap), "details@300 desc")

		// holidays gs:// URI in Source URIs.
		holInfo := app.ObjectInfo{Kind: app.KindTable, Project: "bigquery-public-data", Dataset: "ml_datasets", Name: "holidays_and_events_for_forecasting"}
		p.current = holInfo
		p.apply(p.gen, holInfo, holTbl, nil)
		p.SelectSegment(segDetails)
		appkit.WindowResize(win, 300, 640)

		uriRow := findOutlineRowByKey(p.details, "key", "Source URIs")
		if uriRow < 0 {
			t.Fatal("Source URIs row not found")
		}

		assertWrappedOutlineCell(t, p.details, uriRow, "value", int(lineBreakCharWrap), "details@300 gs:// URI")

		// pageviews_2024 schema (key column visible).
		pvInfo := app.ObjectInfo{Kind: app.KindTable, Project: "bigquery-public-data", Dataset: "wikipedia", Name: "pageviews_2024"}
		p.current = pvInfo
		p.apply(p.gen, pvInfo, pvTbl, nil)
		p.SelectSegment(segSchema)
		appkit.WindowResize(win, 440, 640)

		descCol := objc.Msg(p.schema.View, "tableColumnWithIdentifier:", objc.String("description"))
		if w := objc.Send[float64](descCol, "width"); w < 120 {
			t.Errorf("pageviews schema description width at 440pt = %v, want >= 120", w)
		}

		assertNoOutlineHorizontalOverflow(t, p.schema, "schema-pageviews@440")
		appkit.WindowResize(win, 300, 640)
	})

	pumpMain(t)

	onMain(t, func() {
		assertNoOutlineHorizontalOverflow(t, p.schema, "schema-pageviews@300")

		// natality schema (key column hidden -> description claims space, child_race multi-line wraps).
		natInfo := app.ObjectInfo{Kind: app.KindTable, Project: "bigquery-public-data", Dataset: "samples", Name: "natality"}
		p.current = natInfo
		p.apply(p.gen, natInfo, natTbl, nil)
		p.SelectSegment(segSchema)
		appkit.WindowResize(win, 440, 640)

		keyCol := objc.Msg(p.schema.View, "tableColumnWithIdentifier:", objc.String("key"))
		if !objc.Send[bool](keyCol, "isHidden") {
			t.Error("expected natality schema key column to be hidden")
		}

		descCol := objc.Msg(p.schema.View, "tableColumnWithIdentifier:", objc.String("description"))
		if w := objc.Send[float64](descCol, "width"); w <= 120 {
			t.Errorf("natality schema description width = %v, expected > 120 when key hidden", w)
		}

		assertNoOutlineHorizontalOverflow(t, p.schema, "schema-natality@440")

		raceRow := findOutlineRowByKey(p.schema, "name", "child_race")
		if raceRow < 0 {
			t.Fatal("natality child_race row not found")
		}

		assertWrappedOutlineCell(t, p.schema, raceRow, "description", int(lineBreakWordWrap), "natality@440 child_race")

		appkit.WindowResize(win, 300, 640)
	})

	pumpMain(t)

	onMain(t, func() {
		defer objc.Msg(win, "close")

		assertNoOutlineHorizontalOverflow(t, p.schema, "schema-natality@300")

		raceRow := findOutlineRowByKey(p.schema, "name", "child_race")
		if raceRow < 0 {
			t.Fatal("natality child_race row not found at 300pt")
		}

		assertWrappedOutlineCell(t, p.schema, raceRow, "description", int(lineBreakWordWrap), "natality@300 child_race")

		if g := inspectOutlineCell(p.schema, raceRow, "description"); g.RowBounds.Size.Height <= 28 {
			t.Errorf("natality@300 child_race RowBounds.Height = %v, want > 28", g.RowBounds.Size.Height)
		}
	})
}

func TestInspectorOutlineColumnCharWrap(t *testing.T) {
	const longFieldName = "very_long_unbroken_bigquery_nested_field_identifier"

	longTbl := &bigquery.Table{
		TableReference: &bigquery.TableReference{ProjectId: "p", DatasetId: "d", TableId: "long_cols"},
		Type:           "TABLE",
		Schema: &bigquery.TableSchema{Fields: []*bigquery.TableFieldSchema{
			{Name: longFieldName, Type: "STRING", Mode: "NULLABLE", Description: "Short description"},
		}},
	}

	fields := results.FieldsFromREST(longTbl.Schema.Fields)
	row := []any{"ok"}

	var (
		p   *InspectorPane
		win objc.ID
	)

	onMain(t, func() {
		p = newInspectorPane(nil, &app.FakeBackend{})
		win = appkit.NewWindow("inspector-charwrap-name", appkit.MakeRect(0, 0, 300, 480))
		appkit.AddFilling(objc.Msg(win, "contentView"), p.View, 0)
		appkit.WindowResize(win, 300, 480)

		info := app.ObjectInfo{Kind: app.KindTable, Project: "p", Dataset: "d", Name: "long_cols"}
		p.current = info
		p.setSubtitle(info.Kind.String() + " · " + info.FullID())
		p.apply(p.gen, info, longTbl, nil)
		p.SelectSegment(segSchema)
		appkit.WindowResize(win, 300, 480)
	})

	pumpMain(t)

	onMain(t, func() {
		assertNoOutlineHorizontalOverflow(t, p.schema, "schema-long-name@300")

		schemaRow := findOutlineRowByKey(p.schema, "name", longFieldName)
		if schemaRow < 0 {
			t.Fatalf("schema row %q not found", longFieldName)
		}

		assertWrappedOutlineCell(t, p.schema, schemaRow, "name", int(lineBreakCharWrap), "schema.name@300")

		if g := inspectOutlineCell(p.schema, schemaRow, "name"); g.RowBounds.Size.Height <= 22 {
			t.Errorf("schema.name@300 RowBounds.Height = %v, want > 22", g.RowBounds.Size.Height)
		}

		p.ShowRow(fields, row, 0, longFieldName)
		appkit.WindowResize(win, 300, 480)
	})

	pumpMain(t)

	onMain(t, func() {
		defer objc.Msg(win, "close")

		assertNoOutlineHorizontalOverflow(t, p.values, "values-long-name@300")

		valRow := findOutlineRowByKey(p.values, "name", longFieldName)
		if valRow < 0 {
			t.Fatalf("values row %q not found", longFieldName)
		}

		assertWrappedOutlineCell(t, p.values, valRow, "name", int(lineBreakCharWrap), "values.name@300")

		if g := inspectOutlineCell(p.values, valRow, "name"); g.RowBounds.Size.Height <= 22 {
			t.Errorf("values.name@300 RowBounds.Height = %v, want > 22", g.RowBounds.Size.Height)
		}
	})
}

func TestInspectorValueAndViewPrompt(t *testing.T) {
	be := &app.FakeBackend{}
	viewTbl, err := be.GetTable(context.Background(), bq.TableRef{Project: app.FakeProject, Dataset: "analytics", Table: "v_active_users"})
	if err != nil {
		t.Fatal(err)
	}

	fields := results.FieldsFromREST([]*bigquery.TableFieldSchema{
		{Name: "id", Type: "INTEGER"},
		{Name: "meta", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{
			{Name: "artifact_uri", Type: "STRING"},
		}},
	})
	row := []any{int64(42), []any{"https://storage.googleapis.com/example-bucket/very_long_unbroken_path_segment_1234567890"}}

	var (
		p   *InspectorPane
		win objc.ID
	)

	onMain(t, func() {
		p = newInspectorPane(nil, be)
		win = appkit.NewWindow("inspector-value-prompt", appkit.MakeRect(0, 0, 440, 640))
		appkit.AddFilling(objc.Msg(win, "contentView"), p.View, 0)

		p.ShowRow(fields, row, 0, "meta.artifact_uri")
		appkit.WindowResize(win, 440, 640)

		cols := objc.ArrayItems(objc.Msg(p.values.View, "tableColumns"))
		if len(cols) != 3 {
			t.Fatalf("values columns = %d, want 3", len(cols))
		}

		lastID := objc.GoString(objc.Msg(cols[2], "identifier"))
		if lastID != "value" {
			t.Errorf("last values column = %q, want %q", lastID, "value")
		}

		assertNoOutlineHorizontalOverflow(t, p.values, "values@440")

		uriRow := findOutlineRowByKey(p.values, "name", "artifact_uri")
		if uriRow < 0 {
			t.Fatal("artifact_uri value row not found")
		}

		assertWrappedOutlineCell(t, p.values, uriRow, "value", int(lineBreakCharWrap), "values@440 artifact_uri")

		appkit.WindowResize(win, 300, 640)
	})

	pumpMain(t)

	onMain(t, func() {
		defer objc.Msg(win, "close")

		assertNoOutlineHorizontalOverflow(t, p.values, "values@300")
		uriRow := findOutlineRowByKey(p.values, "name", "artifact_uri")
		assertWrappedOutlineCell(t, p.values, uriRow, "value", int(lineBreakCharWrap), "values@300 artifact_uri")

		// Test viewPrompt wrapping and alignment with nil Shell avoided by applying table directly.
		viewInfo := app.ObjectInfo{Kind: app.KindView, Project: app.FakeProject, Dataset: "analytics", Name: "v_active_users"}
		p.current = viewInfo
		p.apply(p.gen, viewInfo, viewTbl, nil)
		p.previewFor = viewInfo.FullID()
		p.preview.Reset()
		appkit.SetString(p.promptText, "Previewing a view runs its query (up to 1,024 rows, billed to bqui-demo). It is cancelled if it takes more than 10 s.")
		appkit.SetHidden(p.viewPrompt, false)
		appkit.SetHidden(p.schema.Scroll, true)
		appkit.SetHidden(p.details.Scroll, true)
		appkit.SetHidden(p.previewHolder, false)

		if got := objc.Send[int64](p.promptText, "alignment"); got != appkit.TextAlignmentCenter {
			t.Errorf("promptText alignment = %d, want %d", got, appkit.TextAlignmentCenter)
		}

		for _, w := range []float64{440, 300, 240} {
			appkit.WindowResize(win, w, 640)
			holderB := appkit.ViewBounds(p.previewHolder)
			vp := appkit.ViewFrameIn(p.viewPrompt, p.previewHolder)

			if vp.Origin.X < inspectorCenteredInset-0.5 || vp.Origin.X+vp.Size.Width > holderB.Size.Width-inspectorCenteredInset+0.5 {
				t.Errorf("viewPrompt at %.0fpt: frame %+v exceeds [16, %.0f]", w, vp, holderB.Size.Width-16)
			}

			pt := appkit.ViewFrameIn(p.promptText, p.previewHolder)
			if pt.Size.Height <= appkit.MeasureText(p.promptText, 0).Height {
				t.Errorf("promptText at %.0fpt did not wrap (height %v)", w, pt.Size.Height)
			}
		}

		// Verify preview.status wraps at 300pt instead of truncating.
		appkit.WindowResize(win, 300, 640)
		p.preview.Notice("✓ 1,024 rows · 42.3 MiB processed · 42.3 MiB billed · slot 1.8 s · 950 ms · via Storage Read API")
		objc.Msg(objc.Msg(win, "contentView"), "layoutSubtreeIfNeeded")

		if objc.Send[bool](p.preview.status, "usesSingleLineMode") {
			t.Error("preview.status usesSingleLineMode should be false")
		}

		stFrame := appkit.ViewFrameIn(p.preview.status, p.previewHolder)
		// Borderless NSTextField alignmentRectForFrame adds ~2pt horizontal padding per side.
		if stFrame.Size.Width > 300-2*statusInset+4.5 {
			t.Errorf("preview.status width %v exceeds 300pt pane", stFrame.Size.Width)
		}

		if stFrame.Size.Height <= appkit.MeasureText(p.preview.status, 0).Height {
			t.Errorf("preview.status height %v did not wrap at 300pt", stFrame.Size.Height)
		}
	})
}

func TestJobPaneAndCenteredNotes(t *testing.T) {
	onMain(t, func() {
		be := &app.FakeBackend{}
		jobPane := newJobPane(be)
		win := appkit.NewWindow("job-pane-wrap", appkit.MakeRect(0, 0, 440, 480))
		defer objc.Msg(win, "close")

		appkit.AddFilling(objc.Msg(win, "contentView"), jobPane.View, 0)
		jobPane.apply(jobPane.gen, &bigquery.Job{
			JobReference: &bigquery.JobReference{ProjectId: "my-project", JobId: "bquxjob_1234567890abcdef_0123456789abcdef", Location: "US"},
			Configuration: &bigquery.JobConfiguration{
				Query: &bigquery.JobConfigurationQuery{
					Query: "SELECT * FROM `bigquery-public-data.google_analytics_sample.ga_sessions_20170801`",
					DestinationEncryptionConfiguration: &bigquery.EncryptionConfiguration{
						KmsKeyName: "projects/my-project/locations/us/keyRings/ring/cryptoKeys/long_key_name_for_job",
					},
				},
			},
		}, nil)
		appkit.WindowResize(win, 440, 480)
		assertNoOutlineHorizontalOverflow(t, jobPane.Outline, "jobPane@440")

		// Check all 7 AddCenteredWrapping notes at 440pt and 200pt.
		ins := newInspectorPane(nil, be)
		ins.apply(ins.gen, app.ObjectInfo{Kind: app.KindTable, Name: "bad"}, nil, context.DeadlineExceeded)
		planPane := newPlanPane(nil)
		graphPane := newGraphPane(nil)
		runsPane := newRunsPane(nil)
		resPane := newResultsPane()
		jsonPane := newJSONPane(nil)

		notes := []struct {
			name   string
			parent objc.ID
			label  objc.ID
		}{
			{name: "inspector.status", parent: ins.View, label: ins.status},
			{name: "job.note", parent: jobPane.View, label: jobPane.note},
			{name: "plan.note", parent: planPane.View, label: planPane.note},
			{name: "graph.note", parent: graphPane.View, label: graphPane.note},
			{name: "runs.note", parent: runsPane.View, label: runsPane.note},
			{name: "results.empty", parent: resPane.View, label: resPane.empty},
			{name: "json.note", parent: jsonPane.View, label: jsonPane.note},
		}

		appkit.SetHidden(jobPane.note, false)
		appkit.SetString(jobPane.note, noPlan)
		appkit.SetString(runsPane.note, noPlan)
		appkit.SetString(resPane.empty, noPlan)
		appkit.SetString(jsonPane.note, noPlan)

		for _, tc := range notes {
			nw := appkit.NewWindow("note-"+tc.name, appkit.MakeRect(0, 0, 440, 300))
			appkit.AddFilling(objc.Msg(nw, "contentView"), tc.parent, 0)

			for _, width := range []float64{440, 200} {
				appkit.WindowResize(nw, width, 300)
				pb := appkit.ViewBounds(tc.parent)
				lf := appkit.ViewFrameIn(tc.label, tc.parent)

				if got := objc.Send[int64](tc.label, "alignment"); got != appkit.TextAlignmentCenter {
					t.Errorf("%s alignment = %d, want %d", tc.name, got, appkit.TextAlignmentCenter)
				}

				if lf.Origin.X < 12-0.5 || lf.Origin.X+lf.Size.Width > pb.Size.Width-12+0.5 {
					t.Errorf("%s at %.0fpt: frame %+v outside [12, %.0f]", tc.name, width, lf, pb.Size.Width-12)
				}
			}

			objc.Msg(nw, "close")
		}
	})
}

func TestHistoryHeaderAndPrefsWindow(t *testing.T) {
	onMain(t, func() {
		s, err := NewShell(Options{
			Deterministic: true,
			Backend:       &app.FakeBackend{},
			Queries:       &stubQueryBackend{schema: nestedTestBQSchema()},
			Jobs:          &app.FakeBackend{},
			SpillDir:      t.TempDir(),
		})
		if err != nil {
			t.Fatal(err)
		}

		defer objc.Msg(s.Window, "close")

		// HistoryPane header non-overlap with 100-char error string at 620pt and 440pt.
		hw := appkit.NewWindow("history-header", appkit.MakeRect(0, 0, 620, 320))
		defer objc.Msg(hw, "close")

		appkit.AddFilling(objc.Msg(hw, "contentView"), s.History.View, 0)
		appkit.SetString(s.History.count, "✕ "+strings.Repeat("query execution error detail ", 4))

		subviews := objc.ArrayItems(objc.Msg(s.History.View, "subviews"))
		if len(subviews) < 4 {
			t.Fatalf("expected >=4 subviews in HistoryPane, got %d", len(subviews))
		}

		refresh := subviews[1]
		search := subviews[3]

		for _, w := range []float64{620, 440, 372} {
			appkit.WindowResize(hw, w, 320)
			pb := appkit.ViewBounds(s.History.View)
			if cw := appkit.ViewBounds(s.History.count).Size.Width; cw < 40 {
				t.Errorf("HistoryPane@%.0f: count width = %v, want >= 40", w, cw)
			}

			rects := []appkit.Rect{
				appkit.ViewFrameIn(s.History.scope.View, s.History.View),
				appkit.ViewFrameIn(refresh, s.History.View),
				appkit.ViewFrameIn(s.History.count, s.History.View),
				appkit.ViewFrameIn(search, s.History.View),
			}
			assertRectsNonOverlappingAndContained(t, pb, rects, []string{"scope", "refresh", "count", "search"}, "HistoryPane")
		}

		// PrefsWindow message wrapping & containment.
		s.ShowPreferences()
		pw := s.PreferencesWindow()
		defer objc.Msg(pw, "orderOut:", objc.Nil)

		appkit.SetString(s.prefs.fields[3], "not-a-number")
		s.applyPrefsWindow()
		if got := appkit.StringValue(s.prefs.message); !strings.HasPrefix(got, "✕ ") {
			t.Errorf("expected error prefix in prefs.message, got %q", got)
		}

		// Verify long validation/error messages wrap inside the 260pt field column without widening prefsWindow or clipping buttons.
		appkit.SetString(s.prefs.message, "✕ Invalid preference configuration: cost warning threshold must be a positive dollar amount such as 1.50 or blank to disable. Please check all numeric fields and try again.")
		objc.Msg(objc.Msg(pw, "contentView"), "layoutSubtreeIfNeeded")

		cv := objc.Msg(pw, "contentView")
		cvB := appkit.ViewBounds(cv)
		msgF := appkit.ViewFrameIn(s.prefs.message, cv)

		if msgF.Size.Height <= appkit.MeasureText(s.prefs.message, 0).Height {
			t.Errorf("prefs message height %v did not wrap", msgF.Size.Height)
		}

		// Verify long unbroken token triggers CharWrap and stays inside prefsField (260pt).
		s.SetPreferencesMessage("✕ default dataset: invalid_very_long_project_and_dataset_identifier_without_spaces_2026")
		objc.Msg(cv, "layoutSubtreeIfNeeded")

		if got := objc.Send[uint64](s.prefs.message, "lineBreakMode"); got != lineBreakCharWrap {
			t.Errorf("prefs message with long token lineBreakMode = %d, want %d", got, lineBreakCharWrap)
		}

		msgCharF := appkit.ViewFrameIn(s.prefs.message, cv)
		if msgCharF.Size.Width > prefsField+4.5 || msgCharF.Size.Height <= appkit.MeasureText(s.prefs.message, 0).Height {
			t.Errorf("prefs long-token message frame %+v did not wrap cleanly inside %vpt", msgCharF, prefsField)
		}

		if msgF.Origin.X < 0 || msgF.Origin.X+msgF.Size.Width > cvB.Size.Width+0.5 ||
			msgF.Origin.Y < 0 || msgF.Origin.Y+msgF.Size.Height > cvB.Size.Height+0.5 {
			t.Errorf("prefs message frame %+v exceeds content bounds %+v", msgF, cvB)
		}

		if cvB.Size.Width > prefsWidth+1 {
			t.Errorf("prefs content width expanded to %v > %v", cvB.Size.Width, prefsWidth)
		}

		if cvB.Size.Height < prefsHeight-1 {
			t.Errorf("prefs content height %v < %v", cvB.Size.Height, prefsHeight)
		}

		// Verify the grid (including bottom buttons) remains inside content bounds.
		for _, sub := range objc.ArrayItems(objc.Msg(cv, "subviews")) {
			sf := appkit.ViewFrameIn(sub, cv)
			if sf.Origin.Y < -0.5 || sf.Origin.Y+sf.Size.Height > cvB.Size.Height+0.5 {
				t.Errorf("prefs subview frame %+v exceeds content height %v", sf, cvB.Size.Height)
			}
		}
	})
}

type gateQueryBackend struct {
	gate chan struct{}
}

func (g *gateQueryBackend) Query(ctx context.Context, _ bq.QueryRequest) (*bq.QueryResult, error) {
	select {
	case <-g.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	return &bq.QueryResult{Statement: "SELECT"}, nil
}

func (*gateQueryBackend) CancelJob(context.Context, bq.JobRef) error { return nil }

func (*gateQueryBackend) ResultPage(context.Context, bq.JobRef, string, int64) (*bigquery.GetQueryResultsResponse, error) {
	return &bigquery.GetQueryResultsResponse{}, nil
}

func TestShellTabsToolbarSplitThemeAndImmediateJSON(t *testing.T) {
	fields := results.FieldsFromREST(nestedTestBQSchema().Fields)
	_ = fields

	nestedRows := []*bigquery.TableRow{
		{F: []*bigquery.TableCell{
			{V: "1"},
			{V: "row1"},
			{V: map[string]any{"f": []any{map[string]any{"v": "ana"}, map[string]any{"v": "payload"}}}},
			{V: []any{map[string]any{"v": map[string]any{"f": []any{
				map[string]any{"v": "10"},
				map[string]any{"v": []any{map[string]any{"v": "t1"}}},
			}}}}},
		}},
	}

	var s *Shell

	onMain(t, func() {
		var err error

		s, err = NewShell(Options{
			Deterministic: true,
			Backend:       &app.FakeBackend{},
			Queries:       &stubQueryBackend{schema: nestedTestBQSchema(), rows: nestedRows},
			Jobs:          &app.FakeBackend{},
			SpillDir:      t.TempDir(),
		})
		if err != nil {
			t.Fatal(err)
		}
		defer appkit.SetGridFontSize(defaultGridFontSize)

		// 1. Bottom tabs: 7 exact labels, fitted inside bottomPane at 1280px, including "Runs (12)".
		wantTabs := []string{"Results", "Runs", "JSON", "Execution details", "Execution graph", "Job information", "History"}
		if got := s.Tabs().SegmentCount(); got != len(wantTabs) {
			t.Fatalf("Tabs().SegmentCount() = %d, want %d", got, len(wantTabs))
		}

		for i, want := range wantTabs {
			if got := s.Tabs().SegmentLabel(i); got != want {
				t.Errorf("tab %d label = %q, want %q", i, got, want)
			}
		}

		s.setRunsCount(12)
		objc.Msg(objc.Msg(s.Window, "contentView"), "layoutSubtreeIfNeeded")

		assertTabsLayout := func(requireUnscrolled bool, label string) {
			tabs := s.Tabs()
			var segSum float64
			for i := range tabs.SegmentCount() {
				segSum += tabs.SegmentWidth(i)
			}

			tabsB := appkit.ViewBounds(tabs.View)
			if segSum > tabsB.Size.Width+1 {
				t.Errorf("%s: sum(SegmentWidth)=%v > Tabs.View width=%v", label, segSum, tabsB.Size.Width)
			}

			for _, g := range appkit.SegmentedGeometry(tabs) {
				if g.Frame.Size.Width < math.Ceil(g.TextWidth)+8 {
					t.Errorf("%s: segment %q width %v < TextWidth %v + 8", label, g.Label, g.Frame.Size.Width, g.TextWidth)
				}
			}

			clip := objc.Msg(tabs.View, "superview")
			scroll := objc.Msg(clip, "superview")
			bottom := objc.Msg(scroll, "superview")
			bottomB := appkit.ViewBounds(bottom)
			scrollF := appkit.ViewFrameIn(scroll, bottom)
			if scrollF.Origin.X < 9.5 || scrollF.Origin.X+scrollF.Size.Width > bottomB.Size.Width-9.5 {
				t.Errorf("%s: tabScroll frame %+v outside bottomPane %+v", label, scrollF, bottomB)
			}

			clipB := appkit.ViewBounds(clip)
			if clipB.Size.Height < tabsB.Size.Height-0.5 {
				t.Errorf("%s: tabScroll NSClipView height %v < Tabs.View height %v", label, clipB.Size.Height, tabsB.Size.Height)
			}

			if hScroller := objc.Msg(scroll, "horizontalScroller"); appkit.IsVisible(hScroller) {
				sf := appkit.ViewFrameIn(hScroller, scroll)
				tf := appkit.ViewFrameIn(tabs.View, scroll)
				if sf.Origin.Y < tf.Origin.Y+tf.Size.Height-0.5 && sf.Origin.Y+sf.Size.Height > tf.Origin.Y+0.5 {
					t.Errorf("%s: horizontalScroller %+v occludes tabs %+v", label, sf, tf)
				}
			}

			if requireUnscrolled {
				tabF := appkit.ViewFrameIn(tabs.View, clip)
				if tabF.Origin.X+tabF.Size.Width > clipB.Size.Width+0.5 {
					t.Errorf("%s: bottom tabs clipped/scrolled at wide layout: tabF=%+v clipB=%+v", label, tabF, clipB)
				}
			}
		}

		assertTabsLayout(true, "tabs@1280")

		// 2. Toolbar overflow at 1280px with long project and cost estimate.
		s.SetBillingProject("very-long-billing-project-identifier-2026")
		s.onCost("~14.2 GiB ($0.09) · cached 0 B")
		objc.Msg(objc.Msg(s.Window, "contentView"), "layoutSubtreeIfNeeded")

		total, vis, overflowed := appkit.ToolbarOverflow(s.Window)
		if len(overflowed) > 0 || vis != total {
			t.Errorf("toolbar overflow at 1280px: total=%d visible=%d overflowed=%v", total, vis, overflowed)
		}

		// 3. Immediate s.JSON.Update via showRun.
		emptyRunner := app.NewRunner(app.RunnerOptions{
			Backend:  &stubQueryBackend{schema: nestedTestBQSchema()},
			Post:     func(fn func()) { fn() },
			SpillDir: t.TempDir(),
		})
		r1 := emptyRunner.Start(bq.QueryRequest{SQL: "SELECT 1"})
		<-r1.Done()
		defer r1.Close()

		s.showRun(r1)
		if s.JSON.grid != r1.Snapshot().Grid {
			t.Error("showRun did not immediately attach r1 Grid to JSONPane")
		}

		gate := make(chan struct{})
		defer close(gate)

		pendingRunner := app.NewRunner(app.RunnerOptions{
			Backend:  &gateQueryBackend{gate: gate},
			Post:     func(fn func()) { fn() },
			SpillDir: t.TempDir(),
		})
		r2 := pendingRunner.Start(bq.QueryRequest{SQL: "SELECT 2"})
		defer r2.Close()

		s.showRun(r2)
		if s.JSON.grid != nil {
			t.Error("showRun did not immediately clear stale JSONPane grid for new pending run")
		}

		// 4. Theme scaling: rowHeight on History, Runs, PreviewPane + colors propagated.
		custom := &theme.Theme{
			Name:       "LargeGridTest",
			Appearance: theme.Light,
			Fonts:      theme.Fonts{GridSize: 18},
			Light: theme.Palette{
				Grid: theme.GridColors{Null: "#112233", Number: "#445566"},
			},
		}
		s.Themes = append(s.Themes, custom)
		s.ApplyTheme("LargeGridTest")

		for name, tbl := range map[string]*appkit.Table{
			"Results": s.Results.Table,
			"JSON":    s.JSON.Table,
			"History": s.History.Table,
			"Runs":    s.Runs.Table,
			"Preview": s.Inspector.PreviewPane().Table,
		} {
			if rh := objc.Send[float64](tbl.View, "rowHeight"); rh < 28 {
				t.Errorf("%s Table rowHeight = %v, want >= 28 at GridSize 18", name, rh)
			}
		}

		if s.Inspector.PreviewPane().NullColor == objc.Nil || s.Inspector.PreviewPane().NumberColor == objc.Nil {
			t.Error("ApplyTheme did not propagate NullColor/NumberColor to Inspector.PreviewPane()")
		}

		// 5. SplitView narrow resize (900x600), tabs & HistoryPane non-crushing, and inspector toggle collapse.
		s.SelectTab(TabHistory)
		appkit.SetString(s.History.count, "45+ jobs")
		appkit.WindowResize(s.Window, 900, 600)
		winB := appkit.ViewBounds(objc.Msg(s.Window, "contentView"))
		if math.Abs(winB.Size.Width-900) > 1 {
			t.Errorf("WindowResize(900, 600) resulted in width=%v, want 900", winB.Size.Width)
		}

		assertTabsLayout(true, "tabs@900x600")

		clipView := objc.Msg(s.Tabs().View, "superview")
		s.SelectTab(TabResults)
		if cx := appkit.ViewBounds(clipView).Origin.X; cx > 0.5 {
			t.Errorf("SelectTab(TabResults)@900x600: clipView.Origin.X = %v, want <= 0.5", cx)
		}

		s.SelectTab(TabHistory)
		clipAtHist := appkit.ViewBounds(clipView)
		tabsAtHist := appkit.ViewBounds(s.Tabs().View)
		if tabsAtHist.Size.Width > clipAtHist.Size.Width+1 && clipAtHist.Origin.X <= 0 {
			t.Errorf("SelectTab(TabHistory)@900x600 did not scroll offscreen tab into view: clip=%+v tabs=%+v", clipAtHist, tabsAtHist)
		}

		histSubviews := objc.ArrayItems(objc.Msg(s.History.View, "subviews"))
		if len(histSubviews) < 4 {
			t.Fatalf("expected >=4 subviews in HistoryPane, got %d", len(histSubviews))
		}

		if cw := appkit.ViewBounds(s.History.count).Size.Width; cw < 40 {
			t.Errorf("History.count width at 900x600 = %v, want >= 40", cw)
		}

		histRects := []appkit.Rect{
			appkit.ViewFrameIn(s.History.scope.View, s.History.View),
			appkit.ViewFrameIn(histSubviews[1], s.History.View),
			appkit.ViewFrameIn(s.History.count, s.History.View),
			appkit.ViewFrameIn(histSubviews[3], s.History.View),
		}
		assertRectsNonOverlappingAndContained(t, appkit.ViewBounds(s.History.View), histRects, []string{"scope", "refresh", "count", "search"}, "HistoryPane@900x600")

		items := objc.ArrayItems(objc.Msg(s.Split, "splitViewItems"))
		if len(items) != 3 {
			t.Fatalf("splitViewItems = %d, want 3", len(items))
		}

		for idx, label := range []string{"sidebar", "content", "inspector"} {
			vc := objc.Msg(items[idx], "viewController")
			vw := objc.Msg(vc, "view")
			b := appkit.ViewBounds(vw)
			if b.Size.Width <= 36 {
				t.Errorf("%s pane width = %v <= 36 at 900x600", label, b.Size.Width)
			}
		}

		objc.Msg(items[2], "setCollapsed:", true)
		objc.Msg(objc.Msg(s.Window, "contentView"), "layoutSubtreeIfNeeded")
		objc.Msg(items[2], "setCollapsed:", false)
		objc.Msg(objc.Msg(s.Window, "contentView"), "layoutSubtreeIfNeeded")
		objc.Msg(s.Window, "close")
	})
}

func TestWorkbooksStudioSentinelIdentityAndShardGuard(t *testing.T) {
	onMain(t, func() {
		for _, id := range []uint64{studioGroupID, studioEmptyID, studioBase + 1} {
			if id >= 1<<55 {
				t.Errorf("sentinel ID %d >= 2^55 violates tagged-pointer contract", id)
			}

			if a, b := objc.Number(id), objc.Number(id); a != b {
				t.Errorf("objc.Number(%d) not pointer-identical: %#x != %#x", id, a, b)
			}
		}

		s, err := NewShell(Options{
			Deterministic: true,
			Backend:       &app.FakeBackend{},
			Queries:       &stubQueryBackend{schema: nestedTestBQSchema()},
			Jobs:          &app.FakeBackend{},
			Studio:        app.NewFakeStudio(app.FakeProject),
			SpillDir:      t.TempDir(),
		})
		if err != nil {
			t.Fatal(err)
		}

		defer objc.Msg(s.Window, "close")

		s.SelectSidebar(SidebarWorkbooks)
		s.Workbooks.rebuild()

		if !s.Workbooks.StudioExpanded() {
			t.Error("expected WorkbooksPane BigQuery Studio group to be expanded after rebuild")
		}

		// GAP-3: KindShardGroup with empty Shards must not panic.
		s.Inspector.ShowTable(app.ObjectInfo{
			Kind:    app.KindShardGroup,
			Project: "p",
			Dataset: "d",
			Name:    "events_",
		}, &bigquery.Table{
			TableReference: &bigquery.TableReference{ProjectId: "p", DatasetId: "d", TableId: "events_20260101"},
			Type:           "TABLE",
		})

		if s.Inspector.Current().Kind != app.KindShardGroup {
			t.Errorf("unexpected current kind %v", s.Inspector.Current().Kind)
		}
	})
}

func TestInspectorSchemaPartitionedClustered300(t *testing.T) {
	be := &app.FakeBackend{}
	pvTbl, err := be.GetTable(context.Background(), bq.TableRef{Project: "bigquery-public-data", Dataset: "wikipedia", Table: "pageviews_2024"})
	if err != nil {
		t.Fatal(err)
	}

	var (
		p   *InspectorPane
		win objc.ID
	)

	onMain(t, func() {
		p = newInspectorPane(nil, be)
		win = appkit.NewWindow("inspector-schema-300", appkit.MakeRect(0, 0, 440, 640))
		appkit.AddFilling(objc.Msg(win, "contentView"), p.View, 0)
		appkit.WindowResize(win, 440, 640)

		pvInfo := app.ObjectInfo{Kind: app.KindTable, Project: "bigquery-public-data", Dataset: "wikipedia", Name: "pageviews_2024"}
		p.ShowTable(pvInfo, pvTbl)
		p.SelectSegment(segSchema)
		appkit.WindowResize(win, 300, 640)
	})

	pumpMain(t)

	onMain(t, func() {
		defer objc.Msg(win, "close")

		keyCol := objc.Msg(p.schema.View, "tableColumnWithIdentifier:", objc.String("key"))
		if objc.Send[bool](keyCol, "isHidden") {
			t.Error("expected key column to be visible for partitioned+clustered pageviews_2024")
		}

		assertNoOutlineHorizontalOverflow(t, p.schema, "pageviews@300")

		colW, textW, found := appkit.OutlineHeaderGeometry(p.schema, "name")
		if !found || colW < math.Ceil(textW)+8 {
			t.Errorf("Field name header truncated at 300pt: colW=%.1f textW=%.1f found=%v", colW, textW, found)
		}
	})
}

func TestRevealObjectDomainScopedProject(t *testing.T) {
	onMain(t, func() {
		s, err := NewShell(Options{
			Deterministic: true,
			Backend:       &app.FakeBackend{},
			Queries:       &stubQueryBackend{schema: nestedTestBQSchema()},
			Jobs:          &app.FakeBackend{},
			Studio:        app.NewFakeStudio(app.FakeProject),
			SpillDir:      t.TempDir(),
		})
		if err != nil {
			t.Fatal(err)
		}

		defer func() {
			s.revealGen++
			objc.Msg(s.Window, "close")
		}()

		s.RevealObject("example.com:proj.d.t")

		if _, ok := s.Explorer.Model.Find("example.com:proj", "", ""); !ok {
			t.Error("expected RevealObject(\"example.com:proj.d.t\") to add project \"example.com:proj\" to Explorer")
		}

		if _, ok := s.Explorer.Model.Find("example", "", ""); ok {
			t.Error("RevealObject split domain-scoped project at dot into \"example\"")
		}

		s.RevealObject("not-a-valid-ref")
	})
}
