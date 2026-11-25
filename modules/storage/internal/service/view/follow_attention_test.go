package view

import (
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/view/follow"
)

// 落后超过 2 分钟才上报；同一次落后持续多久，detail 都不变（只写起点），不会每分钟换一条序列。
func TestFollowLagAttention(t *testing.T) {
	since := time.Date(2026, 11, 24, 3, 0, 0, 0, time.UTC)
	key := follow.Key{SpaceID: "crypto", DatasetID: "dataset_kline_1m"}
	if got := followLagAttention([]follow.Lag{{Key: key, Seconds: 119, Since: since}}); got != nil {
		t.Fatalf("落后 119 秒不应上报: %v", got)
	}
	first := followLagAttention([]follow.Lag{{Key: key, Seconds: 121, Since: since}})[observability.AttentionViewFollowLagging]
	if len(first) != 1 {
		t.Fatalf("落后 121 秒应上报一条: %v", first)
	}
	if !strings.HasPrefix(first[0], "crypto/dataset_kline_1m 自") {
		t.Fatalf("detail 应写空间/数据集: %q", first[0])
	}
	later := followLagAttention([]follow.Lag{{Key: key, Seconds: 721, Since: since}})[observability.AttentionViewFollowLagging]
	if later[0] != first[0] {
		t.Fatalf("同一次落后 detail 应不变: %q vs %q", first[0], later[0])
	}
	// 追上后（等待新变更）Seconds 为 0，不再上报。
	if got := followLagAttention([]follow.Lag{{Key: key}}); got != nil {
		t.Fatalf("追上后不应上报: %v", got)
	}
}
