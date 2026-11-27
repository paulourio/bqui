//go:build darwin

package ui

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/results"
)

func TestMain(m *testing.M) {
	runtime.LockOSThread()

	if err := objc.Load(); err != nil {
		panic(err)
	}

	appkit.SharedApplication()

	done := make(chan int, 1)

	go func() {
		done <- m.Run()
	}()

	rl := objc.Msg(objc.Cls("NSRunLoop"), "mainRunLoop")

	mode, err := objc.Const(objc.Foundation, "NSDefaultRunLoopMode")
	if err != nil || mode == objc.Nil {
		mode = objc.String("kCFRunLoopDefaultMode")
	}

	for {
		select {
		case code := <-done:
			os.Exit(code)
		default:
			date := objc.Msg(objc.Cls("NSDate"), "dateWithTimeIntervalSinceNow:", 0.01)
			objc.Send[bool](rl, "runMode:beforeDate:", mode, date)
		}
	}
}

func onMain(t *testing.T, fn func()) {
	t.Helper()

	done := make(chan struct{})

	objc.Main(func() {
		defer close(done)

		fn()
	})
	<-done
}

type stubQueryBackend struct {
	schema *bigquery.TableSchema
	rows   []*bigquery.TableRow
}

func (b *stubQueryBackend) Query(_ context.Context, _ bq.QueryRequest) (*bq.QueryResult, error) {
	return &bq.QueryResult{
		Statement: "SELECT",
		Schema:    b.schema,
		Rows:      b.rows,
		TotalRows: uint64(len(b.rows)),
	}, nil
}

func (*stubQueryBackend) CancelJob(context.Context, bq.JobRef) error { return nil }

func (*stubQueryBackend) ResultPage(context.Context, bq.JobRef, string, int64) (*bigquery.GetQueryResultsResponse, error) {
	return &bigquery.GetQueryResultsResponse{}, nil
}

func nestedTestBQSchema() *bigquery.TableSchema {
	return &bigquery.TableSchema{Fields: []*bigquery.TableFieldSchema{
		{Name: "id", Type: "INTEGER"},
		{Name: "descrição 日本語", Type: "STRING"},
		{Name: "meta", Type: "RECORD", Fields: []*bigquery.TableFieldSchema{
			{Name: "owner", Type: "STRING"},
			{Name: "payload", Type: "STRING"},
		}},
		{Name: "hits", Type: "RECORD", Mode: "REPEATED", Fields: []*bigquery.TableFieldSchema{
			{Name: "seq", Type: "INTEGER"},
			{Name: "tags", Type: "STRING", Mode: "REPEATED"},
		}},
	}}
}

func TestResultsAndJSONPaneNestedRendering(t *testing.T) {
	bqSchema := nestedTestBQSchema()
	fields := results.FieldsFromREST(bqSchema.Fields)

	// 1. Run real 0-row query through app.Runner and drive ResultsPane.Show/Update.
	emptyRunner := app.NewRunner(app.RunnerOptions{
		Backend:  &stubQueryBackend{schema: bqSchema},
		Post:     func(fn func()) { fn() },
		SpillDir: t.TempDir(),
	})
	emptyRun := emptyRunner.Start(bq.QueryRequest{SQL: "SELECT * FROM t LIMIT 0"})
	<-emptyRun.Done()
	defer emptyRun.Close()

	// 2. Run real nested query (>2000-char JSON, UTF-8 header, embedded '\n', repeated struct+array).
	longPayload := strings.Repeat("nested-segment-value-", 110) // >2300 bytes
	nestedRows := []*bigquery.TableRow{
		{F: []*bigquery.TableCell{
			{V: "1"},
			{V: "line1\nline2"},
			{V: map[string]any{"f": []any{map[string]any{"v": "ana"}, map[string]any{"v": longPayload}}}},
			{V: []any{
				map[string]any{"v": map[string]any{"f": []any{
					map[string]any{"v": "10"},
					map[string]any{"v": []any{map[string]any{"v": "t1"}, map[string]any{"v": "t2"}}},
				}}},
				map[string]any{"v": map[string]any{"f": []any{
					map[string]any{"v": "20"},
					map[string]any{"v": []any{map[string]any{"v": "t3"}}},
				}}},
			}},
		}},
	}
	nestedRunner := app.NewRunner(app.RunnerOptions{
		Backend:  &stubQueryBackend{schema: bqSchema, rows: nestedRows},
		Post:     func(fn func()) { fn() },
		SpillDir: t.TempDir(),
	})
	nestedRun := nestedRunner.Start(bq.QueryRequest{SQL: "SELECT * FROM t"})
	<-nestedRun.Done()
	defer nestedRun.Close()

	onMain(t, func() {
		resPane := newResultsPane()
		jsonPane := newJSONPane(nil)
		colors := []objc.ID{
			objc.Retain(appkit.RGB(0.2, 0.4, 0.8, 1.0)),
			objc.Retain(appkit.RGB(0.1, 0.6, 0.2, 1.0)),
			objc.Retain(appkit.RGB(0.7, 0.3, 0.1, 1.0)),
			objc.Retain(appkit.RGB(0.5, 0.2, 0.7, 1.0)),
		}

		defer func() {
			jsonPane.colors = nil

			for _, c := range colors {
				objc.Release(c)
			}
		}()

		jsonPane.colors = map[results.JSONKind]objc.ID{
			results.JSONKey:     colors[0],
			results.JSONString:  colors[1],
			results.JSONNumber:  colors[2],
			results.JSONLiteral: colors[3],
		}

		win := appkit.NewWindow("ui-results-json-test", appkit.MakeRect(0, 0, 960, 480))
		defer objc.Msg(win, "close")

		container := appkit.NewView()
		appkit.AddFilling(objc.Msg(win, "contentView"), container, 0)
		appkit.AddFilling(container, resPane.View, 0)
		appkit.AddFilling(container, jsonPane.View, 0)

		// Exercise 0-row Grid in both ResultsPane and JSONPane.
		resPane.Show(emptyRun)
		jsonPane.Update(emptyRun.Snapshot())

		if resPane.Table.Rows() != 0 {
			t.Fatalf("ResultsPane 0-row Reload() rows = %d, want 0", resPane.Table.Rows())
		}

		if gotCols := len(objc.ArrayItems(objc.Msg(resPane.Table.View, "tableColumns"))); gotCols != len(results.Leaves(fields))+1 {
			t.Fatalf("ResultsPane 0-row columns = %d, want %d", gotCols, len(results.Leaves(fields))+1)
		}

		if jsonPane.Table.Rows() != 0 {
			t.Fatalf("empty JSONPane rows = %d, want 0", jsonPane.Table.Rows())
		}

		// Attach populated nested Grid via ResultsPane.Show + Update.
		resPane.Show(nestedRun)
		objc.Msg(objc.Msg(win, "contentView"), "layoutSubtreeIfNeeded")

		if resPane.Table.Rows() != 3 {
			t.Fatalf("ResultsPane nested visual lines = %d, want 3", resPane.Table.Rows())
		}

		// The UTF-8 header is measured as rendered text (not bytes), so its
		// column is at least the header's measured width plus padding.
		c1ColIdx := objc.Send[int64](resPane.Table.View, "columnWithIdentifier:", objc.String("c1"))
		c1Col := objc.Msg(objc.Msg(resPane.Table.View, "tableColumns"), "objectAtIndex:", uint64(c1ColIdx))
		gotC1Width := objc.Send[float64](c1Col, "width")
		minC1Width := max(appkit.TextWidth("descrição 日本語", appkit.FontHeader)+colPadding, colMinWidth)

		if gotC1Width < minC1Width || gotC1Width > colMaxWidth {
			t.Errorf("UTF-8 header column width = %v, want in [%v, %v]", gotC1Width, minC1Width, colMaxWidth)
		}

		// Force cell instantiation on nested columns and embedded-newline column.
		for line := range 3 {
			for ci := range len(results.Leaves(fields)) + 1 {
				objc.Msg(resPane.Table.View, "viewAtColumn:row:makeIfNecessary:", int64(ci), int64(line), true)
			}
		}

		// c1 is "descrição 日本語" with "line1\nline2" -> "line1⏎line2".
		cellNL := appkit.TableRenderedCell(resPane.Table, 0, "c1")
		if !cellNL.Found || cellNL.Text != "line1⏎line2" {
			t.Errorf("embedded newline cell = %+v, want line1⏎line2", cellNL)
		}

		// c4 is hits.seq; line 0 has "10", line 2 has "20".
		hitSeq0 := appkit.TableRenderedCell(resPane.Table, 0, "c4")
		hitSeq2 := appkit.TableRenderedCell(resPane.Table, 2, "c4")

		if !hitSeq0.Found || hitSeq0.Text != "10" || !hitSeq2.Found || hitSeq2.Text != "20" {
			t.Errorf("nested hits.seq cells = %+v / %+v", hitSeq0, hitSeq2)
		}

		// c5 is hits.tags; line 1 has "t2".
		tag1 := appkit.TableRenderedCell(resPane.Table, 1, "c5")
		if !tag1.Found || tag1.Text != "t2" {
			t.Errorf("nested hits.tags[1] = %+v, want t2", tag1)
		}

		// Drive JSONPane with nested Grid (>2000 char row) and verify untruncated width & cell text.
		nestedSnap := nestedRun.Snapshot()
		row0, err := nestedSnap.Grid.Row(0)
		if err != nil {
			t.Fatal(err)
		}

		wantJSON := string(results.AppendRowJSON(nil, fields, row0))
		if len(wantJSON) <= 2000 {
			t.Fatalf("expected nested JSON > 2000 chars, got %d", len(wantJSON))
		}

		jsonPane.Update(nestedSnap)
		objc.Msg(objc.Msg(win, "contentView"), "layoutSubtreeIfNeeded")

		if jsonPane.Table.Rows() != 1 {
			t.Fatalf("JSONPane rows = %d, want 1", jsonPane.Table.Rows())
		}

		jsonColIdx := objc.Send[int64](jsonPane.Table.View, "columnWithIdentifier:", objc.String("json"))
		jsonCol := objc.Msg(objc.Msg(jsonPane.Table.View, "tableColumns"), "objectAtIndex:", uint64(jsonColIdx))
		colW := objc.Send[float64](jsonCol, "width")
		wantMinW := appkit.MeasureGridTextWidth(wantJSON, true)

		if colW <= 2400 || colW < wantMinW {
			t.Fatalf("JSONPane column width = %v, want > 2400 and >= %v", colW, wantMinW)
		}

		cv := objc.Msg(jsonPane.Table.View, "viewAtColumn:row:makeIfNecessary:", jsonColIdx, int64(0), true)
		if cv == objc.Nil {
			t.Fatal("JSONPane row 0 cell view is nil")
		}

		tf := objc.Msg(cv, "textField")
		if got := objc.GoString(objc.Msg(objc.Msg(tf, "attributedStringValue"), "string")); got != wantJSON {
			t.Errorf("JSONPane cell attributedStringValue len=%d, want=%d", len(got), len(wantJSON))
		}

		if info := appkit.TableRenderedCell(jsonPane.Table, 0, "json"); !info.Found || info.Text != wantJSON {
			t.Errorf("JSONPane TableRenderedCell mismatch: found=%v len=%d want=%d", info.Found, len(info.Text), len(wantJSON))
		}

		// Streaming growth on JSONPane: start with 0 rows, append row0, ensure SetColumnWidth widens > 2400.
		streamStore := &results.MemoryStore{}
		streamGrid := results.NewGrid(fields, streamStore)
		jsonPane.Update(app.RunSnapshot{State: app.RunFetching, Grid: streamGrid})

		if jsonPane.Table.Rows() != 0 {
			t.Fatalf("streaming initial rows = %d, want 0", jsonPane.Table.Rows())
		}

		results.AppendRows(streamGrid, streamStore, [][]any{row0})
		jsonPane.Update(app.RunSnapshot{State: app.RunFetching, Grid: streamGrid})
		objc.Msg(objc.Msg(win, "contentView"), "layoutSubtreeIfNeeded")

		if jsonPane.Table.Rows() != 1 {
			t.Fatalf("streaming after append rows = %d, want 1", jsonPane.Table.Rows())
		}

		streamW := objc.Send[float64](jsonCol, "width")
		if streamW <= 2400 || streamW < wantMinW {
			t.Fatalf("streaming JSONPane width = %v, want > 2400 and >= %v", streamW, wantMinW)
		}
	})
}

func makeNestedTestRow(id int64, payload string) []any {
	return []any{
		id,
		"row-" + strconv.FormatInt(id, 10),
		[]any{"ana", payload},
		[]any{
			[]any{int64(10), []any{"t1", "t2"}},
			[]any{int64(20), []any{"t3"}},
		},
	}
}

func TestJSONPaneRows200PlusAndLazyExpansion(t *testing.T) {
	fields := results.FieldsFromREST(nestedTestBQSchema().Fields)
	longPayload := strings.Repeat("nested-wide-segment-", 165) // >3300 chars

	shortRows := make([][]any, 200)
	for i := range shortRows {
		shortRows[i] = makeNestedTestRow(int64(i), "s")
	}

	longRows := make([][]any, 5)
	for i := range longRows {
		longRows[i] = makeNestedTestRow(int64(200+i), longPayload)
	}

	wantRow200JSON := string(results.AppendRowJSON(nil, fields, longRows[0]))
	if len(wantRow200JSON) <= 3200 {
		t.Fatalf("expected row 200 JSON > 3200 chars, got %d", len(wantRow200JSON))
	}

	t.Run("initial_load_200_plus", func(t *testing.T) {
		store := &results.MemoryStore{}
		grid := results.NewGrid(fields, store)
		results.AppendRows(grid, store, shortRows)
		results.AppendRows(grid, store, longRows)

		onMain(t, func() {
			jsonPane := newJSONPane(nil)
			win := appkit.NewWindow("ui-json-200plus-initial", appkit.MakeRect(0, 0, 960, 480))
			defer objc.Msg(win, "close")

			appkit.AddFilling(objc.Msg(win, "contentView"), jsonPane.View, 0)
			jsonPane.Update(app.RunSnapshot{State: app.RunDone, Grid: grid})
			objc.Msg(objc.Msg(win, "contentView"), "layoutSubtreeIfNeeded")

			jsonColIdx := objc.Send[int64](jsonPane.Table.View, "columnWithIdentifier:", objc.String("json"))
			jsonCol := objc.Msg(objc.Msg(jsonPane.Table.View, "tableColumns"), "objectAtIndex:", uint64(jsonColIdx))
			colW := objc.Send[float64](jsonCol, "width")
			needW := appkit.MeasureGridTextWidth(wantRow200JSON, true)

			if colW < needW {
				t.Fatalf("initial 200+ json width = %v, want >= %v", colW, needW)
			}

			objc.Msg(jsonPane.Table.View, "scrollRowToVisible:", int64(200))
			cv := objc.Msg(jsonPane.Table.View, "viewAtColumn:row:makeIfNecessary:", jsonColIdx, int64(200), true)

			if cv == objc.Nil {
				t.Fatal("row 200 cell view is nil")
			}

			tf := objc.Msg(cv, "textField")
			if got := appkit.StringValue(tf); got != wantRow200JSON {
				t.Errorf("row 200 cell text len = %d, want %d", len(got), len(wantRow200JSON))
			}

			if info := appkit.TableRenderedCell(jsonPane.Table, 200, "json"); !info.Found || info.Text != wantRow200JSON {
				t.Errorf("row 200 TableRenderedCell = found:%v len:%d wantLen:%d", info.Found, len(info.Text), len(wantRow200JSON))
			}
		})
	})

	t.Run("streaming_200_plus", func(t *testing.T) {
		store := &results.MemoryStore{}
		grid := results.NewGrid(fields, store)
		results.AppendRows(grid, store, shortRows)

		onMain(t, func() {
			jsonPane := newJSONPane(nil)
			win := appkit.NewWindow("ui-json-200plus-stream", appkit.MakeRect(0, 0, 960, 480))
			defer objc.Msg(win, "close")

			appkit.AddFilling(objc.Msg(win, "contentView"), jsonPane.View, 0)
			jsonPane.Update(app.RunSnapshot{State: app.RunFetching, Grid: grid})
			objc.Msg(objc.Msg(win, "contentView"), "layoutSubtreeIfNeeded")

			jsonColIdx := objc.Send[int64](jsonPane.Table.View, "columnWithIdentifier:", objc.String("json"))
			jsonCol := objc.Msg(objc.Msg(jsonPane.Table.View, "tableColumns"), "objectAtIndex:", uint64(jsonColIdx))

			if initialW := objc.Send[float64](jsonCol, "width"); initialW >= 2000 {
				t.Fatalf("expected short-only initial width < 2000, got %v", initialW)
			}

			results.AppendRows(grid, store, longRows)
			jsonPane.Update(app.RunSnapshot{State: app.RunDone, Grid: grid})
			objc.Msg(objc.Msg(win, "contentView"), "layoutSubtreeIfNeeded")

			colW := objc.Send[float64](jsonCol, "width")
			needW := appkit.MeasureGridTextWidth(wantRow200JSON, true)

			if colW < needW {
				t.Fatalf("streaming 200+ json width = %v, want >= %v", colW, needW)
			}

			objc.Msg(jsonPane.Table.View, "scrollRowToVisible:", int64(200))
			objc.Msg(jsonPane.Table.View, "viewAtColumn:row:makeIfNecessary:", jsonColIdx, int64(200), true)

			if info := appkit.TableRenderedCell(jsonPane.Table, 200, "json"); !info.Found || info.Text != wantRow200JSON {
				t.Errorf("streaming row 200 TableRenderedCell = found:%v len:%d wantLen:%d", info.Found, len(info.Text), len(wantRow200JSON))
			}
		})
	})

	t.Run("lazy_beyond_scan_limit_2500_plus", func(t *testing.T) {
		store := &results.MemoryStore{}
		grid := results.NewGrid(fields, store)

		manyShort := make([][]any, 2500)
		for i := range manyShort {
			manyShort[i] = makeNestedTestRow(int64(i), "s")
		}

		row2500 := makeNestedTestRow(2500, longPayload)
		wantRow2500JSON := string(results.AppendRowJSON(nil, fields, row2500))

		results.AppendRows(grid, store, manyShort)
		results.AppendRows(grid, store, [][]any{row2500})

		var (
			jsonPane *JSONPane
			win      objc.ID
			jsonCol  objc.ID
		)

		onMain(t, func() {
			jsonPane = newJSONPane(nil)
			win = appkit.NewWindow("ui-json-2500plus-lazy", appkit.MakeRect(0, 0, 960, 480))
			appkit.AddFilling(objc.Msg(win, "contentView"), jsonPane.View, 0)
			jsonPane.Update(app.RunSnapshot{State: app.RunDone, Grid: grid})
			objc.Msg(objc.Msg(win, "contentView"), "layoutSubtreeIfNeeded")

			jsonColIdx := objc.Send[int64](jsonPane.Table.View, "columnWithIdentifier:", objc.String("json"))
			jsonCol = objc.Msg(objc.Msg(jsonPane.Table.View, "tableColumns"), "objectAtIndex:", uint64(jsonColIdx))

			if initialW := objc.Send[float64](jsonCol, "width"); initialW >= 2000 {
				t.Fatalf("expected 0..1999 short-only width < 2000, got %v", initialW)
			}

			objc.Msg(jsonPane.Table.View, "scrollRowToVisible:", int64(2500))
			objc.Msg(jsonPane.Table.View, "viewAtColumn:row:makeIfNecessary:", jsonColIdx, int64(2500), true)
		})

		// Pump next main-thread runloop turn so lazy objc.Main SetColumnWidth runs.
		onMain(t, func() {
			defer objc.Msg(win, "close")

			objc.Msg(objc.Msg(win, "contentView"), "layoutSubtreeIfNeeded")

			colW := objc.Send[float64](jsonCol, "width")
			needW := appkit.MeasureGridTextWidth(wantRow2500JSON, true)

			if colW < needW {
				t.Fatalf("lazy row 2500 json width = %v, want >= %v", colW, needW)
			}

			if info := appkit.TableRenderedCell(jsonPane.Table, 2500, "json"); !info.Found || info.Text != wantRow2500JSON {
				t.Errorf("lazy row 2500 TableRenderedCell = found:%v len:%d wantLen:%d", info.Found, len(info.Text), len(wantRow2500JSON))
			}
		})
	})
}

func TestJSONPaneLargeNestedRowAndFontResize(t *testing.T) {
	bqSchema := nestedTestBQSchema()
	fields := results.FieldsFromREST(bqSchema.Fields)

	hugePayload := strings.Repeat("ga-session-hit-product-nested-field-αβγ-", 200) // >7700 runes
	nestedRows := []*bigquery.TableRow{
		{F: []*bigquery.TableCell{
			{V: "1"},
			{V: "descrição"},
			{V: map[string]any{"f": []any{map[string]any{"v": "ana"}, map[string]any{"v": hugePayload}}}},
			{V: []any{
				map[string]any{"v": map[string]any{"f": []any{
					map[string]any{"v": "10"},
					map[string]any{"v": []any{map[string]any{"v": "t1"}, map[string]any{"v": "t2"}}},
				}}},
			}},
		}},
	}

	runner := app.NewRunner(app.RunnerOptions{
		Backend:  &stubQueryBackend{schema: bqSchema, rows: nestedRows},
		Post:     func(fn func()) { fn() },
		SpillDir: t.TempDir(),
	})
	run := runner.Start(bq.QueryRequest{SQL: "SELECT * FROM t"})
	<-run.Done()
	defer run.Close()

	snap := run.Snapshot()
	row0, err := snap.Grid.Row(0)
	if err != nil {
		t.Fatal(err)
	}

	wantJSON := string(results.AppendRowJSON(nil, fields, row0))
	if gotRunes := utf8.RuneCountInString(wantJSON); gotRunes <= 7700 {
		t.Fatalf("expected >7700-rune nested JSON, got %d runes", gotRunes)
	}

	var (
		resPane  *ResultsPane
		jsonPane *JSONPane
		win      objc.ID
		jsonCol  objc.ID
		c1Col    objc.ID
		needW12  float64
		needW20  float64
	)

	defer func() {
		onMain(t, func() {
			appkit.SetGridFontSize(defaultGridFontSize)
		})
	}()

	onMain(t, func() {
		appkit.SetGridFontSize(defaultGridFontSize)

		resPane = newResultsPane()
		jsonPane = newJSONPane(nil)
		win = appkit.NewWindow("ui-large-nested-font-resize", appkit.MakeRect(0, 0, 960, 480))

		container := appkit.NewView()
		appkit.AddFilling(objc.Msg(win, "contentView"), container, 0)
		appkit.AddFilling(container, resPane.View, 0)
		appkit.AddFilling(container, jsonPane.View, 0)

		resPane.Show(run)
		jsonPane.Update(snap)
		objc.Msg(objc.Msg(win, "contentView"), "layoutSubtreeIfNeeded")

		if gotLine := jsonPane.Line(0); gotLine != wantJSON {
			t.Errorf("JSONPane.Line(0) runes = %d, want %d (must not truncate with ellipsis)",
				utf8.RuneCountInString(gotLine), utf8.RuneCountInString(wantJSON))
		}

		jsonColIdx := objc.Send[int64](jsonPane.Table.View, "columnWithIdentifier:", objc.String("json"))
		jsonCol = objc.Msg(objc.Msg(jsonPane.Table.View, "tableColumns"), "objectAtIndex:", uint64(jsonColIdx))
		needW12 = appkit.MeasureGridTextWidth(wantJSON, true)

		if colW := objc.Send[float64](jsonCol, "width"); colW < needW12 {
			t.Fatalf("font 12 json column width = %v, want >= %v", colW, needW12)
		}

		cv := objc.Msg(jsonPane.Table.View, "viewAtColumn:row:makeIfNecessary:", jsonColIdx, int64(0), true)
		if cv == objc.Nil {
			t.Fatal("row 0 cell view is nil")
		}

		tf := objc.Msg(cv, "textField")
		if got := objc.GoString(objc.Msg(objc.Msg(tf, "attributedStringValue"), "string")); got != wantJSON {
			t.Errorf("live attributedStringValue runes = %d, want %d",
				utf8.RuneCountInString(got), utf8.RuneCountInString(wantJSON))
		}

		c1ColIdx := objc.Send[int64](resPane.Table.View, "columnWithIdentifier:", objc.String("c1"))
		c1Col = objc.Msg(objc.Msg(resPane.Table.View, "tableColumns"), "objectAtIndex:", uint64(c1ColIdx))

		// Simulate ApplyTheme font-size change (12 -> 20) + Reload().
		appkit.SetGridFontSize(20)
		needW20 = appkit.MeasureGridTextWidth(wantJSON, true)
		resPane.Table.SetRowHeight(30)
		jsonPane.Table.SetRowHeight(30)
		resPane.Table.Reload()
		jsonPane.Table.Reload()
	})

	// Pump next main-thread runloop turn so font-resize SetColumnWidth runs.
	onMain(t, func() {
		defer objc.Msg(win, "close")

		objc.Msg(objc.Msg(win, "contentView"), "layoutSubtreeIfNeeded")

		if needW20 <= needW12 {
			t.Fatalf("expected font 20 measured width (%v) > font 12 (%v)", needW20, needW12)
		}

		if colW20 := objc.Send[float64](jsonCol, "width"); colW20 < needW20 {
			t.Errorf("font 20 json column width = %v, want >= %v", colW20, needW20)
		}

		// Columns are measured in the current grid font: the c1 cell text at
		// 20pt sets the floor (capped like the pane caps wide columns).
		cellW20 := appkit.TextWidth(results.FormatCell("STRING", "descrição"), appkit.FontRegular) + colPadding
		wantC1W20 := min(cellW20, colMaxWidth*20.0/defaultGridFontSize)
		if gotC1W20 := objc.Send[float64](c1Col, "width"); gotC1W20 < wantC1W20 {
			t.Errorf("font 20 ResultsPane c1 width = %v, want >= %v", gotC1W20, wantC1W20)
		}
	})
}
