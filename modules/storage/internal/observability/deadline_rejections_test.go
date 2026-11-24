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
	if want := "crypto/ds_1m（1m，自 11-24 03:00 UTC 起）"; !strings.Contains(items[0], want) {
		t.Fatalf("detail 应含 %q: %q", want, items[0])
	}
	if got := d.attention(at.Add(10*time.Minute + time.Second)); got != nil {
		t.Fatalf("10 分 01 秒时应回到 0: %v", got)
	}
}

// 持续被拒时 detail 写这一段的起点，不随每次拒绝换序列；隔了超过 10 分钟再被拒算新的一段。
func TestDeadlineRejectionDetailKeepsSpanStart(t *testing.T) {
	d := NewDeadlineRejections(nil)
	start := time.Date(2026, 11, 24, 3, 0, 0, 0, time.UTC)
	var first string
	for i := 0; i < 30; i++ {
		at := start.Add(time.Duration(i) * time.Minute)
		d.ObserveDeadlineRejection("crypto", "ds_1m", "1m", at)
		items := d.attention(at)[AttentionDeadlineRejected]
		if i == 0 {
			first = items[0]
		}
		if items[0] != first || !strings.Contains(items[0], "自 11-24 03:00 UTC 起") {
			t.Fatalf("第 %d 分钟 detail 变了: %q", i, items[0])
		}
	}
	later := start.Add(29*time.Minute + 11*time.Minute)
	d.ObserveDeadlineRejection("crypto", "ds_1m", "1m", later)
	if got := d.attention(later)[AttentionDeadlineRejected]; !strings.Contains(got[0], "自 11-24 03:40 UTC 起") {
		t.Fatalf("隔了超过 10 分钟应算新的一段: %v", got)
	}
}
