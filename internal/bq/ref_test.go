package bq

import (
	"errors"
	"testing"
)

func TestParseTableRef(t *testing.T) {
	tests := []struct {
		in, def string
		want    TableRef
		wantErr bool
	}{
		{in: "p.d.t", want: TableRef{"p", "d", "t"}},
		{in: "`bigquery-public-data.samples.shakespeare`", want: TableRef{"bigquery-public-data", "samples", "shakespeare"}},
		{in: "p:d.t", want: TableRef{"p", "d", "t"}},
		{in: "example.com:proj:d.t", want: TableRef{"example.com:proj", "d", "t"}},
		{in: "d.t", def: "dp", want: TableRef{"dp", "d", "t"}},
		{in: " p.d.t ", want: TableRef{"p", "d", "t"}},
		{in: "d.t", wantErr: true},
		{in: "", wantErr: true},
		{in: "a.b.c.d", wantErr: true},
		{in: "p..t", wantErr: true},
		{in: "p:d", wantErr: true},
	}

	for _, tt := range tests {
		got, err := ParseTableRef(tt.in, tt.def)
		if tt.wantErr {
			if !errors.Is(err, ErrBadRef) {
				t.Errorf("ParseTableRef(%q) err = %v, want ErrBadRef", tt.in, err)
			}

			continue
		}

		if err != nil || got != tt.want {
			t.Errorf("ParseTableRef(%q) = %+v, %v; want %+v", tt.in, got, err, tt.want)
		}
	}

	if got := (TableRef{"p", "d", "t"}).SQL(); got != "`p.d.t`" {
		t.Errorf("SQL() = %s", got)
	}
}

func TestParseDatasetRef(t *testing.T) {
	tests := []struct {
		in, def string
		want    DatasetRef
		wantErr bool
	}{
		{in: "p.d", want: DatasetRef{"p", "d"}},
		{in: "p:d", want: DatasetRef{"p", "d"}},
		{in: "d", def: "dp", want: DatasetRef{"dp", "d"}},
		{in: "d", wantErr: true},
		{in: "p.d.t", wantErr: true},
	}

	for _, tt := range tests {
		got, err := ParseDatasetRef(tt.in, tt.def)
		if tt.wantErr != (err != nil) || (!tt.wantErr && got != tt.want) {
			t.Errorf("ParseDatasetRef(%q) = %+v, %v", tt.in, got, err)
		}
	}
}
