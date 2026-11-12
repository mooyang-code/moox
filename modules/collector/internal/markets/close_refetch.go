package markets

import (
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/barclock"
)

// sessionHours 缓存市场的交易时段判断：构造时要加载时区和交易日历，而每轮对账都会问一次。
var sessionHours sync.Map

func hoursOf(market barclock.Market) (*barclock.Hours, error) {
	if cached, ok := sessionHours.Load(market.ID); ok {
		return cached.(*barclock.Hours), nil
	}
	hours, err := market.Hours()
	if err != nil {
		return nil, err
	}
	sessionHours.Store(market.ID, hours)
	return hours, nil
}

// CloseRefetchDay 判断市场的“收市后整天重取”时刻是否落在 (after, until] 之内。这个时刻是当天实际的收市时刻
// （提前收市日是提前后的时刻）加上 market.yaml 的 refetch_after_close_minutes。落在其中时返回收市那天整天的范围：
// 按市场时区从当天零点到次日零点，左闭右开，用来圈定 K 线的开始时刻。
// 没配这一项的市场、24 小时连续交易的市场、非交易日都返回 due 为 false。
func CloseRefetchDay(marketID string, after, until time.Time) (from, to time.Time, due bool, err error) {
	config, err := ConfigFor(marketID)
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	if config.RefetchAfterCloseMinutes <= 0 {
		return time.Time{}, time.Time{}, false, nil
	}
	market, err := barclock.MarketFor(marketID)
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	if market.Alignment != barclock.AlignmentSession {
		return time.Time{}, time.Time{}, false, nil
	}
	session, err := hoursOf(market)
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	delay := time.Duration(config.RefetchAfterCloseMinutes) * time.Minute
	// 不晚于 until-delay 的最近一次日收盘：非交易日拿到的是上一个交易日的收盘，加上延迟后早于 after，不会命中。
	closeAt, err := session.LastClose(until.Add(-delay))
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	if !closeAt.Add(delay).After(after) {
		return time.Time{}, time.Time{}, false, nil
	}
	local := closeAt.In(session.Location())
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, session.Location())
	return start.UTC(), start.AddDate(0, 0, 1).UTC(), true, nil
}
