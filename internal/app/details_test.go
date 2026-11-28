package app

import (
	"context"
	"strings"
	"testing"
	"time"

	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/bq"
)

var testFmt = Formatter{Loc: time.FixedZone("BRT", -3*3600)}

func fakeTable(t *testing.T, ref string) *bigquery.Table {
	t.Helper()

	r, err := bq.ParseTableRef(ref, "")
	if err != nil {
		t.Fatal(err)
	}

	tbl, err := (&FakeBackend{}).GetTable(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}

	return tbl
}

func section(ss []Section, title string) map[string]string {
	for _, s := range ss {
		if s.Title == title {
			m := make(map[string]string)
			for _, r := range s.Rows {
				m[r.Key] = r.Value
			}

			return m
		}
	}

	return nil
}

func TestTableSectionsPartitionedClustered(t *testing.T) {
	ss := testFmt.TableSections(fakeTable(t, "bigquery-public-data.wikipedia.pageviews_2024"))

	info := section(ss, "Table info")
	if info["Table ID"] != "bigquery-public-data.wikipedia.pageviews_2024" || !strings.HasSuffix(info["Created"], "UTC-3") {
		t.Errorf("info = %v", info)
	}

	part := section(ss, "Partitioning and clustering")
	if part["Partitioned by"] != "DAY" || part["Partitioned on field"] != "datehour" || part["Partition filter"] != "Required" || part["Clustered by"] == "" {
		t.Errorf("partitioning = %v", part)
	}

	storage := section(ss, "Storage info")
	if storage["Number of rows"] == "" || !strings.Contains(storage["Total logical bytes"], "iB") {
		t.Errorf("storage = %v", storage)
	}
}

func TestTableSectionsViewFormatsSQL(t *testing.T) {
	ss := testFmt.TableSections(fakeTable(t, FakeProject+".analytics.v_active_users"))

	q := section(ss, "Query")
	if q == nil || !strings.Contains(q["View query"], "MAX(event_ts)") || !strings.Contains(q["View query"], "\n  FROM `") {
		t.Fatalf("view query not formatted by bqfmt: %q", q["View query"])
	}

	if section(ss, "Storage info") != nil {
		t.Error("views have no storage section")
	}

	// Public views hide their SQL from other accounts.
	hidden := section(testFmt.TableSections(fakeTable(t, "bigquery-public-data.crypto_bitcoin.transactions")), "Query")
	if !strings.HasPrefix(hidden["View query"], "Not visible") {
		t.Errorf("hidden view query = %q", hidden["View query"])
	}
}

func TestTableSectionsExternal(t *testing.T) {
	ss := testFmt.TableSections(fakeTable(t, "bigquery-public-data.ml_datasets.holidays_and_events_for_forecasting"))

	ext := section(ss, "External data")
	if ext == nil || ext["Source format"] == "" || !strings.HasPrefix(ext["Source URIs"], "gs://") {
		t.Fatalf("external = %v", ext)
	}
}

func TestSchemaTreeNestedAndBadges(t *testing.T) {
	ga := fakeTable(t, "bigquery-public-data.google_analytics_sample.ga_sessions_20170801")
	tree := SchemaTree(ga)

	var hits *SchemaField

	for i := range tree {
		if tree[i].Name == "hits" {
			hits = &tree[i]
		}
	}

	if hits == nil || hits.Type != "RECORD" || hits.Mode != "REPEATED" || len(hits.Children) < 10 {
		t.Fatalf("hits = %+v", hits)
	}

	if n := CountFields(tree); n < 200 {
		t.Errorf("ga_sessions field count = %d", n)
	}

	wiki := SchemaTree(fakeTable(t, "bigquery-public-data.wikipedia.pageviews_2024"))

	var attrs []string
	for _, f := range wiki {
		attrs = append(attrs, f.Name+"="+f.Attr())
	}

	joined := strings.Join(attrs, " ")
	if !strings.Contains(joined, "datehour=R+P") && !strings.Contains(joined, "datehour=P") || !strings.Contains(joined, "C1") {
		t.Errorf("attrs = %s", joined)
	}
}

func TestSchemaAttrCodes(t *testing.T) {
	tbl := &bigquery.Table{
		Schema: &bigquery.TableSchema{Fields: []*bigquery.TableFieldSchema{
			{Name: "order_id", Type: "INTEGER", Mode: "REQUIRED"},
			{Name: "line", Type: "INTEGER", Mode: "REQUIRED"},
			{Name: "customer_id", Type: "INTEGER"},
			{Name: "created_at", Type: "TIMESTAMP", Mode: "REQUIRED"},
			{Name: "email", Type: "STRING", PolicyTags: &bigquery.TableFieldSchemaPolicyTags{Names: []string{"pii"}}},
			{Name: "items", Type: "RECORD", Mode: "REPEATED", Fields: []*bigquery.TableFieldSchema{{Name: "order_id", Type: "INTEGER"}}},
		}},
		TimePartitioning: &bigquery.TimePartitioning{Field: "created_at", Type: "DAY"},
		Clustering:       &bigquery.Clustering{Fields: []string{"created_at", "customer_id"}},
		TableConstraints: &bigquery.TableConstraints{
			PrimaryKey: &bigquery.TableConstraintsPrimaryKey{Columns: []string{"order_id", "line"}},
			ForeignKeys: []*bigquery.TableConstraintsForeignKeys{{
				Name:             "fk_customer",
				ReferencedTable:  &bigquery.TableConstraintsForeignKeysReferencedTable{ProjectId: "p", DatasetId: "d", TableId: "customers"},
				ColumnReferences: []*bigquery.TableConstraintsForeignKeysColumnReferences{{ReferencingColumn: "customer_id", ReferencedColumn: "id"}},
			}},
		},
	}

	tree := SchemaTree(tbl)
	want := map[string]string{
		"order_id": "R+PK", "line": "R+PK", "customer_id": "FK+C2", "created_at": "R+P+C1", "email": "T", "items": "A",
	}

	for _, f := range tree {
		if got := f.Attr(); got != want[f.Name] {
			t.Errorf("%s attr = %q, want %q", f.Name, got, want[f.Name])
		}
	}

	// Constraints name top-level columns: a nested field of the same name is not a key.
	if nested := tree[5].Children[0]; nested.Attr() != "" {
		t.Errorf("nested order_id attr = %q", nested.Attr())
	}

	details := map[string]string{
		"line":        "R: REQUIRED, never NULL\nPK: primary key (column 2 of 2)",
		"customer_id": "FK: foreign key fk_customer → p.d.customers.id\nC2: clustering column 2 of 2",
		"created_at":  "R: REQUIRED, never NULL\nP: partitioning column (DAY)\nC1: clustering column 1 of 2",
		"email":       "T: policy tags pii",
		"items":       "A: REPEATED, an array of values",
	}

	for _, f := range tree {
		if want, ok := details[f.Name]; ok && f.AttrDetail() != want {
			t.Errorf("%s detail = %q, want %q", f.Name, f.AttrDetail(), want)
		}
	}
}

func TestFieldTypeParameterized(t *testing.T) {
	tests := map[string]*bigquery.TableFieldSchema{
		"STRING(10)":       {Type: "STRING", MaxLength: 10},
		"NUMERIC(10, 2)":   {Type: "NUMERIC", Precision: 10, Scale: 2},
		"BIGNUMERIC(40)":   {Type: "BIGNUMERIC", Precision: 40},
		"RANGE<DATE>":      {Type: "RANGE", RangeElementType: &bigquery.TableFieldSchemaRangeElementType{Type: "DATE"}},
		"TIMESTAMP":        {Type: "TIMESTAMP"},
		"BYTES":            {Type: "BYTES"},
		"GEOGRAPHY":        {Type: "GEOGRAPHY"},
		"RECORD":           {Type: "RECORD"},
		"JSON":             {Type: "JSON"},
		"STRING":           {Type: "STRING"},
		"NUMERIC":          {Type: "NUMERIC"},
		"INTERVAL":         {Type: "INTERVAL"},
		"BIGNUMERIC":       {Type: "BIGNUMERIC"},
		"INTEGER":          {Type: "INTEGER"},
		"FLOAT":            {Type: "FLOAT"},
		"BOOLEAN":          {Type: "BOOLEAN"},
		"DATETIME":         {Type: "DATETIME"},
		"TIME":             {Type: "TIME"},
		"DATE":             {Type: "DATE"},
		"RANGE":            {Type: "RANGE"},
		"BYTES(16)":        {Type: "BYTES", MaxLength: 16},
		"NUMERIC(38, 9)":   {Type: "NUMERIC", Precision: 38, Scale: 9},
		"BIGNUMERIC(1, 1)": {Type: "BIGNUMERIC", Precision: 1, Scale: 1},
	}

	for want, f := range tests {
		if got := FieldType(f); got != want {
			t.Errorf("FieldType(%+v) = %q, want %q", f, got, want)
		}
	}
}

func TestDatasetRoutineModelSections(t *testing.T) {
	be := &FakeBackend{}
	ctx := context.Background()

	d, err := be.GetDataset(ctx, bq.DatasetRef{Project: "bigquery-public-data", Dataset: "samples"})
	if err != nil {
		t.Fatal(err)
	}

	if section(testFmt.DatasetSections(d), "Dataset info")["Dataset ID"] != "bigquery-public-data.samples" {
		t.Error("dataset id missing")
	}

	r, err := be.GetRoutine(ctx, bq.ObjectRef{Project: "bigquery-public-data", Dataset: "persistent_udfs", Table: "bignumber_add"})
	if err != nil {
		t.Fatal(err)
	}

	rs := testFmt.RoutineSections(r)
	if section(rs, "Definition")["Body"] == "" || section(rs, "Signature")["Returns"] == "" {
		t.Errorf("routine sections = %+v", rs)
	}

	m, err := be.GetModel(ctx, bq.ObjectRef{Project: "bigquery-public-data", Dataset: "ml_datasets", Table: "bqml_kmeans_model"})
	if err != nil {
		t.Fatal(err)
	}

	if section(testFmt.ModelSections(m), "Model info")["Model type"] != "KMEANS" {
		t.Error("model type missing")
	}
}

func TestFormattingHelpers(t *testing.T) {
	cases := map[string]string{
		Count(0):                      "0",
		Count(999):                    "999",
		Count(164656):                 "164,656",
		Count(1234567890):             "1,234,567,890",
		Bytes(512):                    "512 B",
		Bytes(6_430_000):              "6.13 MiB",
		Duration(60 * 86_400_000):     "60 days",
		Duration(3_600_000):           "1 hour",
		Duration(0):                   "",
		utcOffset(0):                  "UTC",
		utcOffset(-3 * 3600):          "UTC-3",
		utcOffset(5*3600 + 30*60):     "UTC+5:30",
		testFmt.Millis(0):             "",
		testFmt.Millis(1700000000000): "Nov 14, 2023, 7:13:20 PM UTC-3",
	}

	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestFormatterAndSchemaTreeNilInputs(t *testing.T) {
	if got := testFmt.TableSections(nil); got != nil {
		t.Errorf("TableSections(nil) = %+v, want nil", got)
	}

	if got := testFmt.DatasetSections(nil); got != nil {
		t.Errorf("DatasetSections(nil) = %+v, want nil", got)
	}

	if got := testFmt.RoutineSections(nil); got != nil {
		t.Errorf("RoutineSections(nil) = %+v, want nil", got)
	}

	if got := testFmt.ModelSections(nil); got != nil {
		t.Errorf("ModelSections(nil) = %+v, want nil", got)
	}

	if got := SchemaTree(nil); got != nil {
		t.Errorf("SchemaTree(nil) = %+v, want nil", got)
	}

	if got := FieldType(nil); got != "" {
		t.Errorf("FieldType(nil) = %q, want empty", got)
	}

	tree := SchemaTree(&bigquery.Table{
		Schema: &bigquery.TableSchema{
			Fields: []*bigquery.TableFieldSchema{
				nil,
				{
					Name:   "parent",
					Type:   "RECORD",
					Fields: []*bigquery.TableFieldSchema{nil, {Name: "child", Type: "STRING"}},
				},
			},
		},
	})
	if len(tree) != 1 || tree[0].Name != "parent" || len(tree[0].Children) != 1 || tree[0].Children[0].Name != "child" {
		t.Errorf("SchemaTree with nil elements = %+v", tree)
	}
}
