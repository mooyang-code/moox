package binance

import (
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestQualifyResampleFieldIDs(t *testing.T) {
	keys := []*storagepb.RowKey{{SpaceId: "crypto_market", DatasetId: "binance_spot_kline_1m"}}
	got := expandResampleFieldIDs(keys, []string{"open", "binance_spot_kline_1m.close", ""})
	want := []string{"open", "binance_spot_kline_1m.open", "binance_spot_kline_1m.close", ""}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}
