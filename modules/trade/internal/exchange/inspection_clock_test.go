package exchange

import (
	"slices"
	"testing"
	"time"
)

func hasFindingCode(findings []InspectionFinding, code string) bool {
	return slices.ContainsFunc(findings, func(f InspectionFinding) bool { return f.Code == code })
}

// 时钟偏差阈值：超过 1 秒提示，达到 3 秒阻断；快慢两个方向一样。
func TestClockSkewThresholds(t *testing.T) {
	for _, test := range []struct {
		skewMillis int64
		warning    bool
		blocked    bool
	}{
		{0, false, false},
		{900, false, false},
		{1500, true, false},
		{-2500, true, false},
		{3000, false, true},
		{-3000, false, true},
		{12000, false, true},
	} {
		result := Inspection{InspectedAt: time.Now(), ClockSkewMillis: test.skewMillis}
		result.ApplyFindings([]MarketType{MarketTypeSpot})
		if got := hasFindingCode(result.Warnings, FindingClockSkew); got != test.warning {
			t.Errorf("偏差 %dms：提示 = %v，期望 %v", test.skewMillis, got, test.warning)
		}
		if got := hasFindingCode(result.Blockers, FindingClockSkew); got != test.blocked {
			t.Errorf("偏差 %dms：阻断 = %v，期望 %v", test.skewMillis, got, test.blocked)
		}
	}
}

// 偏差用一次取交易所时间的请求估算，本机时间取请求前后的中点。
func TestMeasureClockSkewUsesRequestMidpoint(t *testing.T) {
	sent := time.UnixMilli(1_000_000)
	received := sent.Add(200 * time.Millisecond)
	server := sent.Add(100*time.Millisecond + 3*time.Second)
	if got, ok := MeasureClockSkew(sent, received, server); !ok || got != 3*time.Second {
		t.Fatalf("偏差 = %v，有效 = %v", got, ok)
	}
}

// 往返超过 ClockSkewWarn 的样本误差太大，按没测到处理。
func TestMeasureClockSkewDropsSlowSamples(t *testing.T) {
	sent := time.UnixMilli(1_000_000)
	if _, ok := MeasureClockSkew(sent, sent.Add(ClockSkewWarn), sent); !ok {
		t.Fatal("往返恰为阈值的样本应有效")
	}
	if _, ok := MeasureClockSkew(sent, sent.Add(ClockSkewWarn+time.Millisecond), sent); ok {
		t.Fatal("往返超过阈值的样本应无效")
	}
}
