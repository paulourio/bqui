package app

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/paulourio/bqlang/formatter"
	bigquery "google.golang.org/api/bigquery/v2"
)

// Detail is one key/value row of a details view.
type Detail struct {
	Key   string
	Value string
	Mono  bool // render in a monospace font (IDs, SQL)
}

// Section groups detail rows under a heading.
type Section struct {
	Title string
	Rows  []Detail
}

func (s *Section) add(key, value string) {
	if value != "" {
		s.Rows = append(s.Rows, Detail{Key: key, Value: value})
	}
}

func (s *Section) mono(key, value string) {
	if value != "" {
		s.Rows = append(s.Rows, Detail{Key: key, Value: value, Mono: true})
	}
}

func nonEmpty(ss ...Section) []Section {
	out := make([]Section, 0, len(ss))

	for _, s := range ss {
		if len(s.Rows) > 0 {
			out = append(out, s)
		}
	}

	return out
}

// Formatter controls how times are rendered (tests pin the location).
type Formatter struct {
	Loc *time.Location
}

func (f Formatter) loc() *time.Location {
	if f.Loc == nil {
		return time.Local
	}

	return f.Loc
}

// Millis formats a Unix-milliseconds timestamp like the console
// ("Nov 26, 2026, 6:52:13 PM UTC-3"); 0 renders as "".
func (f Formatter) Millis(ms int64) string {
	if ms == 0 {
		return ""
	}

	t := time.UnixMilli(ms).In(f.loc())
	_, off := t.Zone()

	return t.Format("Jan 2, 2006, 3:04:05 PM") + " " + utcOffset(off)
}

func utcOffset(sec int) string {
	if sec == 0 {
		return "UTC"
	}

	sign := "+"
	if sec < 0 {
		sign, sec = "-", -sec
	}

	h, m := sec/3600, sec%3600/60
	if m == 0 {
		return fmt.Sprintf("UTC%s%d", sign, h)
	}

	return fmt.Sprintf("UTC%s%d:%02d", sign, h, m)
}

// Bytes formats a byte count with binary units ("6.13 MiB").
func Bytes(n int64) string {
	const unit = 1024

	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}

	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.2f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Count formats an integer with thousands separators ("164,656").
func Count(n uint64) string {
	s := strconv.FormatUint(n, 10)
	if len(s) <= 3 {
		return s
	}

	var b strings.Builder

	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}

	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}

		b.WriteString(s[i : i+3])
	}

	return b.String()
}

// Duration formats milliseconds as days/hours ("60 days", "12 hours").
func Duration(ms int64) string {
	if ms <= 0 {
		return ""
	}

	d := time.Duration(ms) * time.Millisecond

	switch {
	case d%(24*time.Hour) == 0:
		return plural(int64(d/(24*time.Hour)), "day")
	case d%time.Hour == 0:
		return plural(int64(d/time.Hour), "hour")
	default:
		return d.String()
	}
}

func plural(n int64, unit string) string {
	if n == 1 {
		return "1 " + unit
	}

	return strconv.FormatInt(n, 10) + " " + unit + "s"
}

func labels(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}

	keys := slices.Sorted(maps.Keys(m))
	parts := make([]string, 0, len(keys))

	for _, k := range keys {
		if m[k] == "" {
			parts = append(parts, k)
		} else {
			parts = append(parts, k+": "+m[k])
		}
	}

	return strings.Join(parts, ", ")
}

func yesNo(b bool) string {
	if b {
		return "Yes"
	}

	return "No"
}

// FormatSQL formats GoogleSQL with bqfmt, returning the input on failure.
func FormatSQL(sql string) string {
	out, err := formatter.Format(sql, formatter.DefaultPrintOptions())
	if err != nil {
		return strings.TrimSpace(sql)
	}

	return strings.TrimSpace(out)
}

// TableSections renders the console's "Details" tab for a table-like object.
func (f Formatter) TableSections(t *bigquery.Table) []Section {
	if t == nil {
		return nil
	}

	info := Section{Title: "Table info"}

	if r := t.TableReference; r != nil {
		info.mono("Table ID", r.ProjectId+"."+r.DatasetId+"."+r.TableId)
	}

	info.add("Type", strings.ToLower(strings.ReplaceAll(t.Type, "_", " ")))
	info.add("Description", t.Description)
	info.add("Created", f.Millis(t.CreationTime))
	info.add("Last modified", f.Millis(int64(t.LastModifiedTime))) //nolint:gosec // ms timestamps fit
	info.add("Table expiration", f.Millis(t.ExpirationTime))
	info.add("Data location", t.Location)
	info.add("Default collation", t.DefaultCollation)
	info.add("Default rounding mode", t.DefaultRoundingMode)
	info.add("Labels", labels(t.Labels))

	if t.EncryptionConfiguration != nil {
		info.mono("Encryption key", t.EncryptionConfiguration.KmsKeyName)
	}

	if c := t.TableConstraints; c != nil && c.PrimaryKey != nil {
		info.mono("Primary key", strings.Join(c.PrimaryKey.Columns, ", "))
	}

	return nonEmpty(info, partitionSection(t), f.storageSection(t), f.viewSection(t), externalSection(t), f.lineageSection(t))
}

func partitionSection(t *bigquery.Table) Section {
	s := Section{Title: "Partitioning and clustering"}

	if p := t.TimePartitioning; p != nil {
		field := p.Field
		if field == "" {
			field = "_PARTITIONTIME (ingestion time)"
		}

		s.add("Partitioned by", strings.ToUpper(p.Type))
		s.mono("Partitioned on field", field)
		s.add("Partition expiration", Duration(p.ExpirationMs))
	}

	if r := t.RangePartitioning; r != nil && r.Range != nil {
		s.add("Partitioned by", "INTEGER RANGE")
		s.mono("Partitioned on field", r.Field)
		s.add("Range", fmt.Sprintf("start %d, end %d, interval %d", r.Range.Start, r.Range.End, r.Range.Interval))
	}

	if t.TimePartitioning != nil || t.RangePartitioning != nil {
		s.add("Partition filter", map[bool]string{true: "Required", false: "Not required"}[t.RequirePartitionFilter])
	}

	if c := t.Clustering; c != nil && len(c.Fields) > 0 {
		s.mono("Clustered by", strings.Join(c.Fields, ", "))
	}

	return s
}

func (f Formatter) storageSection(t *bigquery.Table) Section {
	s := Section{Title: "Storage info"}

	if t.Type == "VIEW" {
		return s
	}

	s.add("Number of rows", Count(t.NumRows))

	if t.NumPartitions > 0 {
		s.add("Number of partitions", Count(uint64(t.NumPartitions)))
	}

	sizes := []struct {
		k string
		v int64
	}{
		{"Total logical bytes", cmp.Or(t.NumTotalLogicalBytes, t.NumBytes)},
		{"Active logical bytes", t.NumActiveLogicalBytes},
		{"Long term logical bytes", cmp.Or(t.NumLongTermLogicalBytes, t.NumLongTermBytes)},
		{"Current physical bytes", t.NumCurrentPhysicalBytes},
		{"Total physical bytes", t.NumTotalPhysicalBytes},
		{"Active physical bytes", t.NumActivePhysicalBytes},
		{"Long term physical bytes", t.NumLongTermPhysicalBytes},
		{"Time travel physical bytes", t.NumTimeTravelPhysicalBytes},
	}

	for _, sz := range sizes {
		if sz.v > 0 {
			s.add(sz.k, Bytes(sz.v))
		}
	}

	if b := t.StreamingBuffer; b != nil {
		s.add("Streaming buffer rows", Count(b.EstimatedRows))
		s.add("Streaming buffer oldest entry", f.Millis(int64(b.OldestEntryTime))) //nolint:gosec // ms timestamps fit
	}

	return s
}

func (f Formatter) viewSection(t *bigquery.Table) Section {
	s := Section{Title: "Query"}

	if v := t.View; v != nil {
		if strings.TrimSpace(v.Query) == "" {
			s.add("View query", "Not visible: the definition is hidden from this account (e.g. an authorized or shared view).")
		} else {
			s.mono("View query", FormatSQL(v.Query))
		}

		if v.UseLegacySql {
			s.add("Legacy SQL", "Yes")
		}
	}

	if mv := t.MaterializedView; mv != nil {
		s.Title = "Materialized view"
		s.mono("Query", FormatSQL(mv.Query))
		s.add("Refresh enabled", yesNo(mv.EnableRefresh))
		s.add("Refresh interval", Duration(mv.RefreshIntervalMs))
		s.add("Last refresh", f.Millis(mv.LastRefreshTime))
		s.add("Max staleness", t.MaxStaleness)
	}

	return s
}

func externalSection(t *bigquery.Table) Section {
	s := Section{Title: "External data"}

	if e := t.ExternalDataConfiguration; e != nil {
		s.add("Source format", e.SourceFormat)
		s.mono("Source URIs", strings.Join(e.SourceUris, "\n"))
		s.mono("Connection", e.ConnectionId)
		s.add("Autodetect", yesNo(e.Autodetect))
	}

	return s
}

func (f Formatter) lineageSection(t *bigquery.Table) Section {
	s := Section{Title: "Source"}

	if sn := t.SnapshotDefinition; sn != nil && sn.BaseTableReference != nil {
		b := sn.BaseTableReference
		s.mono("Base table", b.ProjectId+"."+b.DatasetId+"."+b.TableId)
		s.add("Snapshot time", sn.SnapshotTime)
	}

	if c := t.CloneDefinition; c != nil && c.BaseTableReference != nil {
		b := c.BaseTableReference
		s.mono("Cloned from", b.ProjectId+"."+b.DatasetId+"."+b.TableId)
		s.add("Clone time", c.CloneTime)
	}

	return s
}

// DatasetSections renders dataset metadata.
func (f Formatter) DatasetSections(d *bigquery.Dataset) []Section {
	if d == nil {
		return nil
	}

	info := Section{Title: "Dataset info"}

	if r := d.DatasetReference; r != nil {
		info.mono("Dataset ID", r.ProjectId+"."+r.DatasetId)
	}

	info.add("Description", d.Description)
	info.add("Created", f.Millis(d.CreationTime))
	info.add("Last modified", f.Millis(d.LastModifiedTime))
	info.add("Data location", d.Location)
	info.add("Default table expiration", Duration(d.DefaultTableExpirationMs))
	info.add("Default partition expiration", Duration(d.DefaultPartitionExpirationMs))
	info.add("Default collation", d.DefaultCollation)
	info.add("Default rounding mode", d.DefaultRoundingMode)
	info.add("Case insensitive", map[bool]string{true: "Yes"}[d.IsCaseInsensitive])
	if d.MaxTimeTravelHours > 0 {
		info.add("Time travel window", plural(d.MaxTimeTravelHours, "hour"))
	}

	info.add("Storage billing model", d.StorageBillingModel)
	info.add("Labels", labels(d.Labels))

	return nonEmpty(info)
}

// RoutineSections renders routine metadata.
func (f Formatter) RoutineSections(r *bigquery.Routine) []Section {
	if r == nil {
		return nil
	}

	info := Section{Title: "Routine info"}

	if ref := r.RoutineReference; ref != nil {
		info.mono("Routine ID", ref.ProjectId+"."+ref.DatasetId+"."+ref.RoutineId)
	}

	info.add("Type", strings.ToLower(strings.ReplaceAll(r.RoutineType, "_", " ")))
	info.add("Language", r.Language)
	info.add("Description", r.Description)
	info.add("Created", f.Millis(r.CreationTime))
	info.add("Last modified", f.Millis(r.LastModifiedTime))
	info.add("Determinism", strings.ToLower(strings.ReplaceAll(r.DeterminismLevel, "_", " ")))

	sig := Section{Title: "Signature"}

	for _, a := range r.Arguments {
		sig.mono(a.Name, StandardSQLType(a.DataType)+argMode(a.Mode))
	}

	sig.mono("Returns", StandardSQLType(r.ReturnType))

	body := Section{Title: "Definition"}
	body.mono("Body", strings.TrimSpace(r.DefinitionBody))

	return nonEmpty(info, sig, body)
}

func argMode(m string) string {
	if m == "" || m == "IN" {
		return ""
	}

	return " (" + m + ")"
}

// StandardSQLType renders a StandardSqlDataType as GoogleSQL.
func StandardSQLType(t *bigquery.StandardSqlDataType) string {
	if t == nil {
		return ""
	}

	switch t.TypeKind {
	case "ARRAY":
		return "ARRAY<" + StandardSQLType(t.ArrayElementType) + ">"
	case "STRUCT":
		if t.StructType == nil {
			return "STRUCT<>"
		}

		parts := make([]string, 0, len(t.StructType.Fields))
		for _, f := range t.StructType.Fields {
			parts = append(parts, strings.TrimSpace(f.Name+" "+StandardSQLType(f.Type)))
		}

		return "STRUCT<" + strings.Join(parts, ", ") + ">"
	case "RANGE":
		return "RANGE<" + StandardSQLType(t.RangeElementType) + ">"
	default:
		return t.TypeKind
	}
}

// ModelSections renders BigQuery ML model metadata.
func (f Formatter) ModelSections(m *bigquery.Model) []Section {
	if m == nil {
		return nil
	}

	info := Section{Title: "Model info"}

	if ref := m.ModelReference; ref != nil {
		info.mono("Model ID", ref.ProjectId+"."+ref.DatasetId+"."+ref.ModelId)
	}

	info.add("Model type", m.ModelType)
	info.add("Description", m.Description)
	info.add("Created", f.Millis(m.CreationTime))
	info.add("Last modified", f.Millis(m.LastModifiedTime))
	info.add("Model expiration", f.Millis(m.ExpirationTime))
	info.add("Data location", m.Location)
	info.add("Labels", labels(m.Labels))

	cols := Section{Title: "Features and labels"}

	for _, c := range m.FeatureColumns {
		cols.mono("feature "+c.Name, StandardSQLType(c.Type))
	}

	for _, c := range m.LabelColumns {
		cols.mono("label "+c.Name, StandardSQLType(c.Type))
	}

	runs := Section{Title: "Training"}

	for i, r := range m.TrainingRuns {
		runs.add(fmt.Sprintf("Run %d", i+1), r.StartTime)
	}

	return nonEmpty(info, cols, runs)
}

// SchemaField is one row of the schema tree.
type SchemaField struct {
	Name        string
	Type        string // GoogleSQL type, parameterized (STRING(10), NUMERIC(10, 2), RANGE<DATE>)
	Mode        string // NULLABLE, REQUIRED, REPEATED
	Description string
	Default     string
	Collation   string
	PolicyTags  []string
	Partition   bool   // the time/range partitioning column
	PartitionBy string // its granularity: DAY, HOUR, MONTH, YEAR or RANGE
	ClusterRank int    // 1-based position in the clustering spec; 0 if not clustered
	Clustered   int    // number of clustering columns
	PKRank      int    // 1-based position in the primary key; 0 if not part of it
	PKSize      int    // number of primary key columns
	ForeignKeys []ForeignKeyRef
	Children    []SchemaField
}

// ForeignKeyRef is one foreign key a column takes part in.
type ForeignKeyRef struct {
	Name   string // constraint name ("" if unnamed)
	Table  string // referenced table, project.dataset.table
	Column string // referenced column
}

// Codes are the field's compact attributes for the schema's Attr column,
// in a fixed order: mode (R required, A repeated/array; nullable has
// none), PK, FK, P (partitioning), C1… (clustering order), T (policy tag).
func (f SchemaField) Codes() []string {
	var c []string

	switch f.Mode {
	case "REQUIRED":
		c = append(c, "R")
	case "REPEATED":
		c = append(c, "A")
	}

	if f.PKRank > 0 {
		c = append(c, "PK")
	}

	if len(f.ForeignKeys) > 0 {
		c = append(c, "FK")
	}

	if f.Partition {
		c = append(c, "P")
	}

	if f.ClusterRank > 0 {
		c = append(c, "C"+strconv.Itoa(f.ClusterRank))
	}

	if len(f.PolicyTags) > 0 {
		c = append(c, "T")
	}

	return c
}

// Attr joins Codes with "+" ("R+P+C1"; "" when there are none).
func (f SchemaField) Attr() string { return strings.Join(f.Codes(), "+") }

// AttrDetail spells out Codes, one per line, for the Attr tooltip.
func (f SchemaField) AttrDetail() string {
	var lines []string

	switch f.Mode {
	case "REQUIRED":
		lines = append(lines, "R: REQUIRED, never NULL")
	case "REPEATED":
		lines = append(lines, "A: REPEATED, an array of values")
	}

	if f.PKRank > 0 {
		pk := "PK: primary key"
		if f.PKSize > 1 {
			pk += fmt.Sprintf(" (column %d of %d)", f.PKRank, f.PKSize)
		}

		lines = append(lines, pk)
	}

	for _, fk := range f.ForeignKeys {
		line := "FK: foreign key"
		if fk.Name != "" {
			line += " " + fk.Name
		}

		lines = append(lines, line+" → "+fk.Table+"."+fk.Column)
	}

	if f.Partition {
		p := "P: partitioning column"
		if f.PartitionBy != "" {
			p += " (" + f.PartitionBy + ")"
		}

		lines = append(lines, p)
	}

	if f.ClusterRank > 0 {
		lines = append(lines, fmt.Sprintf("C%d: clustering column %d of %d", f.ClusterRank, f.ClusterRank, max(f.Clustered, f.ClusterRank)))
	}

	if len(f.PolicyTags) > 0 {
		lines = append(lines, "T: policy tags "+strings.Join(f.PolicyTags, ", "))
	}

	return strings.Join(lines, "\n")
}

// SchemaTree converts a table schema into display rows, marking the
// partitioning and clustering columns. Pseudo-column partitioning
// (_PARTITIONTIME) is not part of the schema and is reported in details.
func SchemaTree(t *bigquery.Table) []SchemaField {
	if t == nil || t.Schema == nil {
		return nil
	}

	k := tableKeys{cluster: map[string]int{}, pk: map[string]int{}, fks: map[string][]ForeignKeyRef{}}

	if t.TimePartitioning != nil {
		k.partition, k.partitionBy = t.TimePartitioning.Field, t.TimePartitioning.Type
	}

	if t.RangePartitioning != nil {
		k.partition, k.partitionBy = t.RangePartitioning.Field, "RANGE"
	}

	if t.Clustering != nil {
		k.clustered = len(t.Clustering.Fields)
		for i, c := range t.Clustering.Fields {
			k.cluster[strings.ToLower(c)] = i + 1
		}
	}

	k.addConstraints(t.TableConstraints)

	return schemaFields(t.Schema.Fields, "", &k)
}

// tableKeys holds what marks top-level columns: partitioning, clustering
// and the primary and foreign keys (by lower-case column name).
type tableKeys struct {
	partition, partitionBy string
	cluster                map[string]int
	clustered              int
	pk                     map[string]int
	pkSize                 int
	fks                    map[string][]ForeignKeyRef
}

func (k *tableKeys) addConstraints(c *bigquery.TableConstraints) {
	if c == nil {
		return
	}

	if c.PrimaryKey != nil {
		k.pkSize = len(c.PrimaryKey.Columns)
		for i, col := range c.PrimaryKey.Columns {
			k.pk[strings.ToLower(col)] = i + 1
		}
	}

	for _, fk := range c.ForeignKeys {
		if fk == nil {
			continue
		}

		table := ""
		if r := fk.ReferencedTable; r != nil {
			table = r.ProjectId + "." + r.DatasetId + "." + r.TableId
		}

		for _, cr := range fk.ColumnReferences {
			if cr == nil {
				continue
			}

			col := strings.ToLower(cr.ReferencingColumn)
			k.fks[col] = append(k.fks[col], ForeignKeyRef{Name: fk.Name, Table: table, Column: cr.ReferencedColumn})
		}
	}
}

func schemaFields(fields []*bigquery.TableFieldSchema, prefix string, k *tableKeys) []SchemaField {
	out := make([]SchemaField, 0, len(fields))

	for _, f := range fields {
		if f == nil {
			continue
		}

		path := strings.ToLower(prefix + f.Name)
		mode := f.Mode

		if mode == "" {
			mode = "NULLABLE"
		}

		top := prefix == ""
		sf := SchemaField{
			Name: f.Name, Type: FieldType(f), Mode: mode, Description: f.Description,
			Default: f.DefaultValueExpression, Collation: f.Collation,
			Partition:   top && k.partition != "" && strings.EqualFold(f.Name, k.partition),
			ClusterRank: k.cluster[path],
			Clustered:   k.clustered,
		}

		if sf.Partition {
			sf.PartitionBy = k.partitionBy
		}

		// Keys and constraints name top-level columns only.
		if top {
			sf.PKRank, sf.ForeignKeys = k.pk[path], k.fks[path]
			if sf.PKRank > 0 {
				sf.PKSize = k.pkSize
			}
		}

		if f.PolicyTags != nil {
			sf.PolicyTags = f.PolicyTags.Names
		}

		if len(f.Fields) > 0 {
			sf.Children = schemaFields(f.Fields, prefix+f.Name+".", k)
		}

		out = append(out, sf)
	}

	return out
}

// FieldType renders a REST field type the way the console shows it.
func FieldType(f *bigquery.TableFieldSchema) string {
	if f == nil {
		return ""
	}

	t := f.Type

	switch t {
	case "STRING", "BYTES":
		if f.MaxLength > 0 {
			return fmt.Sprintf("%s(%d)", t, f.MaxLength)
		}
	case "NUMERIC", "BIGNUMERIC":
		switch {
		case f.Precision > 0 && f.Scale > 0:
			return fmt.Sprintf("%s(%d, %d)", t, f.Precision, f.Scale)
		case f.Precision > 0:
			return fmt.Sprintf("%s(%d)", t, f.Precision)
		}
	case "RANGE":
		if f.RangeElementType != nil {
			return "RANGE<" + f.RangeElementType.Type + ">"
		}
	}

	return t
}

// CountFields returns the number of fields in a schema tree, nested included.
func CountFields(fields []SchemaField) int {
	n := len(fields)
	for _, f := range fields {
		n += CountFields(f.Children)
	}

	return n
}

// CountInt formats a non-negative int64 count (negatives render as 0).
func CountInt(n int64) string {
	if n < 0 {
		n = 0
	}

	return Count(uint64(n))
}
