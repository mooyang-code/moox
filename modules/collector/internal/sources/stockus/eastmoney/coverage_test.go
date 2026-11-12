package eastmoney

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

// 流水线取数成功后还会按数据源声明的回看能力核对覆盖（KlineHistoryCapability.ValidateCoverage）：返回里最早一根的
// 开始时刻不能晚于请求起点。补采、收市后整天重取的分块起点是 barclock 的桶（首根缺失时是首个缺失的桶），不是当地零点；
// 起点那一根在返回里时，最早一根不会晚于它。这里按上游模型，对每一根作起点（含周一、夏令时首根）核对日期区间规则不破坏这一点。
func TestDateRangeKeepsHistoryCoverage(t *testing.T) {
	model, _ := loadRecordedRequests(t)
	five := marketdata.Frequency5Min
	history := New(Config{}).KlineSpec().HistoryFor("5m")
	check := func(first, last time.Time) {
		req := marketdata.KlineRequest{StartTime: first.Add(-5 * time.Minute), EndTime: last}
		beg, end := dateRange(req, five)
		for _, cutoff := range cutoffs {
			var rows []marketdata.NormalizedKline
			for _, label := range model.fetch(beg, end, 0, cutoff.offset, cutoff.inclusive) {
				rows = append(rows, marketdata.NormalizedKline{BarStart: label.Add(-5 * time.Minute), BarEnd: label})
			}
			require.NoError(t, history.ValidateCoverage(rows, req.StartTime), "%s～%s %s", first.In(beijing), last.In(beijing), cutoff.name)
		}
	}
	for index, label := range model.labels {
		check(label, label)
		// 起点之后一小时（12 根）的分块：中间没有跨天的才检查。
		if index+12 < len(model.labels) && model.labels[index+12].Sub(label) == time.Hour {
			check(label, model.labels[index+12])
		}
	}
}
