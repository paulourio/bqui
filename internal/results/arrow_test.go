package results

import (
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// The expected strings below are what BigQuery returned over REST for the
// same values (probed live against the BigQuery API).
func TestFormatIntervalMatchesBigQuery(t *testing.T) {
	cases := []struct {
		v    arrow.MonthDayNanoInterval
		want string
	}{
		{arrow.MonthDayNanoInterval{Days: 3}, "0-0 3 0:0:0"},
		{arrow.MonthDayNanoInterval{Months: -14}, "-1-2 0 0:0:0"},
		{arrow.MonthDayNanoInterval{Nanoseconds: -(26*3600 + 60 + 1) * int64(time.Second)}, "0-0 0 -26:1:1"},
		{arrow.MonthDayNanoInterval{Months: 14, Days: 3, Nanoseconds: (4*3600+5*60+6)*int64(time.Second) + 1000}, "1-2 3 4:5:6.000001"},
		{arrow.MonthDayNanoInterval{Nanoseconds: int64(500 * time.Millisecond)}, "0-0 0 0:0:0.500"},
	}

	for _, c := range cases {
		if got := FormatInterval(c.v); got != c.want {
			t.Errorf("FormatInterval(%+v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestTimeFormatsMatchBigQuery(t *testing.T) {
	half := time.Date(2024, 1, 2, 3, 4, 5, 500_000_000, time.UTC)
	whole := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)

	cases := map[string]string{
		FormatTimeOfDay(half):       "03:04:05.500000",
		FormatTimeOfDay(whole):      "03:04:05",
		FormatDatetime(half):        "2024-01-02T03:04:05.500000",
		FormatDatetime(whole):       "2024-01-02T03:04:05",
		trimDecimal("12.500000000"): "12.5",
		trimDecimal("12.000000000"): "12",
		trimDecimal("-0.000000001"): "-0.000000001",
	}

	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestRowsFromRecordAllTypes(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.NewGoAllocator())
	defer mem.AssertSize(t, 0)

	rangeType := arrow.StructOf(
		arrow.Field{Name: "start", Type: arrow.FixedWidthTypes.Date32, Nullable: true},
		arrow.Field{Name: "end", Type: arrow.FixedWidthTypes.Date32, Nullable: true},
	)
	itemType := arrow.StructOf(arrow.Field{Name: "y", Type: arrow.BinaryTypes.String, Nullable: true})
	stType := arrow.StructOf(
		arrow.Field{Name: "x", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
		arrow.Field{Name: "zs", Type: arrow.ListOf(itemType)},
	)

	schema := arrow.NewSchema([]arrow.Field{
		{Name: "i", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
		{Name: "n", Type: &arrow.Decimal128Type{Precision: 38, Scale: 9}, Nullable: true},
		{Name: "ts", Type: &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}, Nullable: true},
		{Name: "d", Type: arrow.FixedWidthTypes.Date32, Nullable: true},
		{Name: "t", Type: arrow.FixedWidthTypes.Time64us, Nullable: true},
		{Name: "dt", Type: &arrow.TimestampType{Unit: arrow.Microsecond}, Nullable: true},
		{Name: "iv", Type: arrow.FixedWidthTypes.MonthDayNanoInterval, Nullable: true},
		{Name: "r", Type: rangeType, Nullable: true},
		{Name: "arr", Type: arrow.ListOf(arrow.PrimitiveTypes.Int64)},
		{Name: "st", Type: stType, Nullable: true},
		{Name: "b", Type: arrow.BinaryTypes.Binary, Nullable: true},
	}, nil)

	fields := []Field{
		{Name: "i", Type: TypeInt64},
		{Name: "n", Type: TypeNumeric},
		{Name: "ts", Type: TypeTimestamp},
		{Name: "d", Type: TypeDate},
		{Name: "t", Type: TypeTime},
		{Name: "dt", Type: TypeDatetime},
		{Name: "iv", Type: TypeInterval},
		{Name: "r", Type: TypeRange, RangeElem: TypeDate},
		{Name: "arr", Type: TypeInt64, Repeated: true},
		{Name: "st", Type: TypeStruct, Fields: []Field{
			{Name: "x", Type: TypeInt64},
			{Name: "zs", Type: TypeStruct, Repeated: true, Fields: []Field{{Name: "y", Type: TypeString}}},
		}},
		{Name: "b", Type: TypeBytes},
	}

	ts := time.Date(2024, 1, 2, 3, 4, 5, 123456000, time.UTC)
	day := arrow.Date32FromTime(time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC))

	b := array.NewRecordBuilder(mem, schema)
	defer b.Release()

	// Row 0: values. Row 1: NULLs / empties.
	b.Field(0).(*array.Int64Builder).AppendValues([]int64{1}, nil)
	b.Field(0).(*array.Int64Builder).AppendNull()

	n, _ := decimal128.FromString("12.5", 38, 9)
	b.Field(1).(*array.Decimal128Builder).Append(n)
	b.Field(1).(*array.Decimal128Builder).AppendNull()

	b.Field(2).(*array.TimestampBuilder).Append(arrow.Timestamp(ts.UnixMicro()))
	b.Field(2).(*array.TimestampBuilder).AppendNull()

	b.Field(3).(*array.Date32Builder).Append(day)
	b.Field(3).(*array.Date32Builder).AppendNull()

	b.Field(4).(*array.Time64Builder).Append(arrow.Time64((3*3600 + 4*60 + 5) * 1_000_000))
	b.Field(4).(*array.Time64Builder).AppendNull()

	b.Field(5).(*array.TimestampBuilder).Append(arrow.Timestamp(ts.UnixMicro()))
	b.Field(5).(*array.TimestampBuilder).AppendNull()

	b.Field(6).(*array.MonthDayNanoIntervalBuilder).Append(arrow.MonthDayNanoInterval{Days: 3})
	b.Field(6).(*array.MonthDayNanoIntervalBuilder).AppendNull()

	rb := b.Field(7).(*array.StructBuilder)
	rb.Append(true)
	rb.FieldBuilder(0).(*array.Date32Builder).Append(day)
	rb.FieldBuilder(1).(*array.Date32Builder).AppendNull()
	rb.AppendNull()

	lb := b.Field(8).(*array.ListBuilder)
	lb.Append(true)
	lb.ValueBuilder().(*array.Int64Builder).AppendValues([]int64{1, 2}, nil)
	lb.Append(true) // empty list

	sb := b.Field(9).(*array.StructBuilder)
	sb.Append(true)
	sb.FieldBuilder(0).(*array.Int64Builder).Append(1)
	zs := sb.FieldBuilder(1).(*array.ListBuilder)
	zs.Append(true)
	zsb := zs.ValueBuilder().(*array.StructBuilder)
	zsb.Append(true)
	zsb.FieldBuilder(0).(*array.StringBuilder).Append("a")
	sb.AppendNull()

	b.Field(10).(*array.BinaryBuilder).Append([]byte{1, 2})
	b.Field(10).(*array.BinaryBuilder).AppendNull()

	rec := b.NewRecordBatch()
	defer rec.Release()

	rows, err := RowsFromRecord(fields, rec)
	if err != nil {
		t.Fatal(err)
	}

	r := rows[0]
	checks := []struct {
		got, want any
	}{
		{r[0], int64(1)},
		{r[1], "12.5"},
		{r[3], "2024-01-02"},
		{r[4], "03:04:05"},
		{r[5], "2024-01-02T03:04:05.123456"},
		{r[6], "0-0 3 0:0:0"},
		{r[7], "[2024-01-02, UNBOUNDED)"},
	}

	for i, c := range checks {
		if c.got != c.want {
			t.Errorf("check %d: got %#v, want %#v", i, c.got, c.want)
		}
	}

	if got, ok := r[2].(time.Time); !ok || !got.Equal(ts) {
		t.Errorf("timestamp = %#v", r[2])
	}

	if arr := r[8].([]any); len(arr) != 2 || arr[1] != int64(2) {
		t.Errorf("arr = %#v", r[8])
	}

	st := r[9].([]any)
	if st[0] != int64(1) || st[1].([]any)[0].([]any)[0] != "a" {
		t.Errorf("struct = %#v", st)
	}

	if bs := r[10].([]byte); len(bs) != 2 || bs[1] != 2 {
		t.Errorf("bytes = %#v", r[10])
	}

	n1 := rows[1]
	for i, v := range n1 {
		if i == 8 {
			if arr, ok := v.([]any); !ok || len(arr) != 0 {
				t.Errorf("empty list should be empty slice, got %#v", v)
			}

			continue
		}

		if v != nil {
			t.Errorf("row 1 col %d = %#v, want nil", i, v)
		}
	}
}
