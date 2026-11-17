// Package ashare 是 A 股场所（ASHARE）：交易时段、腾讯实时快照做的公共行情、东方财富标的列表。
// 模拟盘用它做行情；实盘下单走 QMT 执行端（尚未接入）。时段、快照缓存、只读适配器的通用实现在 stockfeed。
package ashare

import (
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange/stockfeed"
)

// orderCutoff 是每天停止发新单的时刻：14:57-15:00 是收盘集合竞价，撤不了单。09:15-09:25 的开盘集合竞价第一版也不参与。
const orderCutoff = 14*time.Hour + 57*time.Minute

// ReasonCalendarExpired 是交易日历不覆盖当前日期时写进目标的原因。
const ReasonCalendarExpired = stockfeed.ReasonCalendarExpired

// NewSession 返回 A 股的交易时段：09:30-11:30、13:00-15:00（取 barclock 的 stockcn），14:57 之后不发新单。
func NewSession() (*stockfeed.Session, error) {
	return stockfeed.NewSession("stockcn", orderCutoff)
}
