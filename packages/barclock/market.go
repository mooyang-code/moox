package barclock

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/frequency"
	"github.com/mooyang-code/moox/packages/marketcalendar"
)

// Market 描述一个市场（空间）的分桶方式。
type Market struct {
	ID        string
	Alignment string
	// 以下只对 session 市场有意义。
	CalendarID string
	Timezone   string
	Sessions   []string
}

// 当前登记的市场：空间 ID 即市场 ID。港股、美股补上交易日历后在此登记。
var markets = map[string]Market{
	"crypto":  {ID: "crypto", Alignment: AlignmentEpochUTC},
	"stockcn": {ID: "stockcn", Alignment: AlignmentSession, CalendarID: "cn_stock", Timezone: "Asia/Shanghai", Sessions: []string{"09:30-11:30", "13:00-15:00"}},
}

// MarketFor 返回市场的分桶方式；未登记的市场返回 ErrUnknownMarket（没有交易日历的市场不能合成）。
func MarketFor(id string) (Market, error) {
	market, ok := markets[strings.ToLower(strings.TrimSpace(id))]
	if !ok {
		return Market{}, fmt.Errorf("%w：%q 没有登记交易日历，不能合成", ErrUnknownMarket, id)
	}
	return market, nil
}

// Clock 返回该市场某个周期的时钟。
func (m Market) Clock(period time.Duration) (Clock, error) {
	if m.Alignment == AlignmentEpochUTC {
		return NewEpoch(period)
	}
	calendar, err := marketcalendar.Load(m.CalendarID)
	if err != nil {
		return nil, fmt.Errorf("%w：%v", ErrNoCalendar, err)
	}
	location, err := time.LoadLocation(m.Timezone)
	if err != nil {
		return nil, fmt.Errorf("%w：时区 %q 无效", ErrNoCalendar, m.Timezone)
	}
	return NewSession(calendar, location, m.Sessions, period)
}

// ClockFor 是 MarketFor 加 Clock 的组合。
func ClockFor(marketID string, period time.Duration) (Clock, error) {
	market, err := MarketFor(marketID)
	if err != nil {
		return nil, err
	}
	return market.Clock(period)
}

// AllowedIntraday 返回该市场允许的日内周期（规范频率，从短到长，不含 1d）：整除一天，并且在 session 市场整除每个交易时段。
func (m Market) AllowedIntraday() []string {
	var spans []span
	if m.Alignment == AlignmentSession {
		spans, _ = parseSpans(m.Sessions)
	}
	var allowed []frequency.Frequency
	for minutes := 1; minutes < 24*60; minutes++ {
		if (24*60)%minutes != 0 {
			continue
		}
		period := time.Duration(minutes) * time.Minute
		ok := true
		for _, item := range spans {
			if item.length()%period != 0 {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		parsed, err := frequency.Parse(fmt.Sprintf("%dm", minutes))
		if err == nil {
			allowed = append(allowed, parsed)
		}
	}
	sort.Slice(allowed, func(i, j int) bool { return allowed[i].Duration() < allowed[j].Duration() })
	out := make([]string, len(allowed))
	for i, item := range allowed {
		out[i] = string(item)
	}
	return out
}
