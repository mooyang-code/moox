package observability

import (
	"strings"
	"testing"
	"time"
)

// 最近一次拒绝在 10 分钟内上报，超过 10 分钟（没有新拒绝）回到 0；detail 写数据集 ID 与最近时刻。
func TestDeadlineRejectionAttentionWindow(t *testing.T) {
	d := NewDeadlineRejections(nil)
	at := time.Date(2026, 11, 24, 3, 0, 0, 0, time.UTC)
	d.ObserveDeadlineRejection("crypto", "ds_1m", "1m", at)
	items := d.attention(at.Add(9*time.Minute + 59*time.Second))[AttentionDeadlineRejected]
	if len(items) != 1 {
		t.Fatalf("9 分 59 秒时应上报: %v", items)
	}
	if want := "crypto/ds_1m（1m，最近 11-24 03:00 UTC）"; !strings.Contains(items[0], want) {
		t.Fatalf("detail 应含 %q: %q", want, items[0])
	}
	if got := d.attention(at.Add(10*time.Minute + time.Second)); got != nil {
		t.Fatalf("10 分 01 秒时应回到 0: %v", got)
	}
}
