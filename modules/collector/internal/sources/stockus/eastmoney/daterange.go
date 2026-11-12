package eastmoney

import (
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

const (
	// dateLayout 是 beg、end 参数的日期格式。
	dateLayout = "20060102"
	// beginOffset 是起点往前推的时长：beg 日 22:30（北京时间）之前收盘的根不在返回里。
	beginOffset = 22*time.Hour + 30*time.Minute
)

// dateRange 是美股的日期区间规则。东方财富对美股的 beg、end 不按 UTC 日期、也不按纽约交易日过滤，规律是实测得到的
// （2026-11-12，39 组请求，AAPL、JPM、SPY 结果一致，见 docs/计划/美股K线采集执行计划.md 0.1 节与 testdata/date_range_requests.json）：
//
//   - 返回收盘标签（北京时间）不早于 beg 日 22:30、不晚于 end 日某个时刻的根，这个时刻在 05:00 之后、21:35 之前；
//   - beg 与 end 同一天一定是空的。
//
// 要取收盘标签在 [L1, L2] 的根：beg 取 L1 减 22:30 之后的日期（冬令时首根 22:35 落在当天，夏令时首根 21:35 要取前一天），
// end 取 L2 的日期加一天（晚上的标签 beg 与 end 才不会同一天；多带回的根由流水线按请求区间裁掉）。
// 夏令时那 11 根（当天 21:35～22:25）有直接实测（docs/计划/美股实测数据/out11.json）：beg=20261025（周日）&end=20261027
// 返回 78 根，首根 10-26 21:35；beg=20261028&end=20261030 返回 145 根，含 10-29 的 21:35～22:25 那 11 根；
// beg=20261028&end=20261031 返回 223 根，末根 10-31 04:00；beg=end 为空。
//
// 已知差别：夏令时首根（21:35）实时取时，目标根还没出来，beg 取前一天会带回前一时段的根，返回非空但没有一根在请求区间内，
// 流水线报 ErrHistoryCoverage（hasRowsWithinCoverage），本源不重试（冬令时首根与港股此时返回空，是 ErrNoClosedBar，会重试）；
// 请求区间内一根都没有、区间外的根又全被跳过时报 ErrNoClosedBar，补采会把它当成“行情源确认没有”。
// 请求的区间是 [StartTime, EndTime)（按开始时刻）：L1 是首根的收盘标签 StartTime+周期，L2 是末根的收盘标签 EndTime。
// 不带起点时不带 beg；终点没写时取请求时刻；都没有时取一个远期日期。日期都按北京时间。
func dateRange(req marketdata.KlineRequest, frequency marketdata.Frequency) (beg, end string) {
	end = "20500101"
	last := req.EndTime
	if last.IsZero() {
		last = req.Now
	}
	if !last.IsZero() {
		end = last.In(beijing).AddDate(0, 0, 1).Format(dateLayout)
	}
	if !req.StartTime.IsZero() {
		first := frequency.BarEnd(req.StartTime)
		beg = first.Add(-beginOffset).In(beijing).Format(dateLayout)
	}
	return beg, end
}
