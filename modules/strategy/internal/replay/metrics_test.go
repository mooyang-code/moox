package replay

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 年化只在区间不短于一周且结果有限时给出，否则省略并在局限中说明。
func TestAnnualizedReturnRequiresAWeekAndFiniteValue(t *testing.T) {
	short := newAccumulator(1, periodsPerYear("", time.Hour))
	for bar := 0; bar < 10; bar++ {
		short.add(origin.Add(time.Duration(bar)*time.Hour), true, "", Outcome{EquityAfter: 1.5}, 1)
	}
	metrics := short.finish()
	if metrics.AnnualizedReturn != nil || !strings.Contains(strings.Join(metrics.Limitations, "；"), "不给出年化") {
		t.Fatalf("不足一周不应年化：%+v", metrics)
	}
	if _, err := json.Marshal(metrics); err != nil {
		t.Fatalf("指标应能编码：%v", err)
	}
	overflow := newAccumulator(1, periodsPerYear("", time.Hour))
	for bar := 0; bar < 200; bar++ {
		overflow.add(origin.Add(time.Duration(bar)*time.Hour), true, "", Outcome{EquityAfter: 1e10}, 1)
	}
	if metrics := overflow.finish(); metrics.AnnualizedReturn != nil {
		t.Fatalf("溢出的年化应省略：%v", *metrics.AnnualizedReturn)
	}
	normal := newAccumulator(1, periodsPerYear("", time.Hour))
	for bar := 0; bar < 200; bar++ {
		normal.add(origin.Add(time.Duration(bar)*time.Hour), true, "", Outcome{EquityAfter: 1.01}, 1)
	}
	if metrics := normal.finish(); metrics.AnnualizedReturn == nil || *metrics.AnnualizedReturn <= 0 {
		t.Fatalf("区间足够长时应给出年化：%+v", metrics)
	}
}
