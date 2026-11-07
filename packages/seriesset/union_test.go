package seriesset

import (
	"reflect"
	"testing"
)

func TestUnionMergesDedupesAndSorts(t *testing.T) {
	spot, swap := "venue:binance|market:spot|source:spot_http", "venue:binance|market:swap|source:swap_http"
	shared := []Series{{SubjectID: "ETH", SeriesTag: swap}, {SubjectID: "BTC", SeriesTag: swap}}
	own := []Series{{SubjectID: "BTC", SeriesTag: swap}, {SubjectID: " BTC ", SeriesTag: spot}, {SubjectID: "", SeriesTag: swap}, {SubjectID: "SOL", SeriesTag: ""}}
	want := []Series{{SubjectID: "BTC", SeriesTag: spot}, {SubjectID: "BTC", SeriesTag: swap}, {SubjectID: "ETH", SeriesTag: swap}}
	if got := Union(shared, own); !reflect.DeepEqual(got, want) {
		t.Fatalf("Union = %v, want %v", got, want)
	}
	if got := Union(nil, nil); got != nil {
		t.Fatalf("空名单应返回 nil，得到 %v", got)
	}
}
