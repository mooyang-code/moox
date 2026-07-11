package transport

import "testing"

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
