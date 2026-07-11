package transport

import (
	"reflect"
	"testing"
	"time"
)

func TestJSONTableRoundTrip(t *testing.T) {
	want := Table{Columns: []string{"close"}, Rows: [][]any{{1.2}, {nil}}}
	b, e := EncodeJSON(want)
	if e != nil {
		t.Fatal(e)
	}
	got, e := DecodeJSON(b)
	if e != nil || len(got.Rows) != 2 {
		t.Fatalf("got=%+v err=%v", got, e)
	}
}

func TestArrowStreamRoundTripPreservesTypesAndNulls(t *testing.T) {
	want := Table{
		Columns: []string{"price", "count", "enabled", "symbol", "when", "empty"},
		Rows: [][]any{
			{1.25, int64(2), true, "BTC", time.UnixMilli(1710000000123).UTC(), nil},
			{nil, int64(3), false, "ETH", time.UnixMilli(1710000001123).UTC(), nil},
		},
	}
	b, err := EncodeArrowStream(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeArrowStream(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 16 || len(got.Rows) != len(want.Rows) || !reflect.DeepEqual(got.Columns, want.Columns) {
		t.Fatalf("got columns/rows=%+v bytes=%d", got, len(b))
	}
	// Arrow timestamps are represented as UTC milliseconds at the transport
	// boundary, which is stable across Go and Python implementations.
	if got.Rows[0][0] != want.Rows[0][0] || got.Rows[0][1] != int64(2) || got.Rows[0][4] != want.Rows[0][4].(time.Time).UnixMilli() {
		t.Fatalf("unexpected decoded values: %#v", got.Rows)
	}
	if got.Rows[1][0] != nil || got.Rows[0][5] != nil {
		t.Fatalf("null values lost: %#v", got.Rows)
	}
}

func TestArrowFileRoundTrip(t *testing.T) {
	want := Table{Columns: []string{"value"}, Rows: [][]any{{int64(1)}, {int64(2)}}}
	b, err := EncodeArrowFile(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeArrowFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%#v want=%#v", got, want)
	}
}

func TestArrowRejectsRaggedRows(t *testing.T) {
	if _, err := EncodeArrowStream(Table{Columns: []string{"a", "b"}, Rows: [][]any{{1}}}); err == nil {
		t.Fatal("expected ragged row error")
	}
}
