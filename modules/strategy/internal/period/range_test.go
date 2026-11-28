package period

import (
	"strings"
	"testing"
	"time"
)

// A 股超出内嵌日历的报错按上海时间书写，与其余 A 股报错一致。
func TestBeyondCalendarErrorUsesShanghaiTime(t *testing.T) {
	err := beyondCalendarError("cn_stock", time.Date(2026, 12, 31, 1, 0, 0, 0, time.UTC))
	if !strings.Contains(err.Error(), "2026-12-31 09:00:00 上海时间 之后的周期") || strings.Contains(err.Error(), "T01:00:00Z") {
		t.Fatalf("应按上海时间书写：%v", err)
	}
}
