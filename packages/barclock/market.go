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
	// Name 是给人看的市场名（“A 股”“港股”），ZoneName 是给人看的时区名（“上海”“香港”）；写进报错与周期标识。
	Name     string
	ZoneName string
}

// 当前登记的市场：空间 ID 即市场 ID。
var markets = map[string]Market{
	"crypto":  {ID: "crypto", Alignment: AlignmentEpochUTC},
	"stockcn": {ID: "stockcn", Alignment: AlignmentSession, CalendarID: "cn_stock", Timezone: "Asia/Shanghai", Sessions: []string{"09:30-11:30", "13:00-15:00"}, Name: "A 股", ZoneName: "上海"},
	"stockhk": {ID: "stockhk", Alignment: AlignmentSession, CalendarID: "hk_stock", Timezone: "Asia/Hong_Kong", Sessions: []string{"09:30-12:00", "13:00-16:00"}, Name: "港股", ZoneName: "香港"},
	"stockus": {ID: "stockus", Alignment: AlignmentSession, CalendarID: "us_stock", Timezone: "America/New_York", Sessions: []string{"09:30-16:00"}, Name: "美股", ZoneName: "纽约"},
}

// Markets 返回全部已登记的市场，按 ID 排序。
func Markets() []Market {
	out := make([]Market, 0, len(markets))
	for _, market := range markets {
		out = append(out, market)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// MarketFor 返回市场的分桶方式；未登记的市场返回 ErrUnknownMarket（没有交易日历的市场不能合成）。
func MarketFor(id string) (Market, error) {
	market, ok := markets[strings.ToLower(strings.TrimSpace(id))]
	if !ok {
		return Market{}, fmt.Errorf("%w：%q 没有登记交易日历，不能合成", ErrUnknownMarket, id)
	}
	return market, nil
}

// MarketForCalendar 按交易日历 ID 返回使用它的 session 市场（cn_stock → stockcn）；没有市场使用这份日历时 ok 为假。
func MarketForCalendar(calendarID string) (Market, bool) {
	calendarID = strings.ToLower(strings.TrimSpace(calendarID))
	if calendarID == "" {
		return Market{}, false
	}
	for _, market := range Markets() {
		if market.Alignment == AlignmentSession && market.CalendarID == calendarID {
			return market, true
		}
	}
	return Market{}, false
}

// MarketForDataset 按数据集的 alignment 与 calendar 属性解析分桶方式，不依赖 space_id == market_id 静态绑定：
//   - epoch_utc（或 alignment 与 calendar 均为空、或 calendar 为 crypto_24x7）：24 小时连续分桶；
//   - session：按 calendar_id 查找已登记交易时段市场（cn_stock / hk_stock / us_stock 等）；
//   - alignment 为空但给了交易时段 calendar_id 时，同样按该交易日历解析。
func MarketForDataset(alignment, calendarID string) (Market, error) {
	alignment = strings.ToLower(strings.TrimSpace(alignment))
	calendarID = strings.ToLower(strings.TrimSpace(calendarID))
	switch alignment {
	case "":
		if calendarID == "" || calendarID == "crypto_24x7" {
			return Market{Alignment: AlignmentEpochUTC, Timezone: "UTC"}, nil
		}
		market, ok := MarketForCalendar(calendarID)
		if !ok {
			return Market{}, fmt.Errorf("%w：未登记的交易日历 %q", ErrNoCalendar, calendarID)
		}
		return market, nil
	case AlignmentEpochUTC:
		if calendarID != "" && calendarID != "crypto_24x7" {
			return Market{}, fmt.Errorf("alignment %q 不能搭配交易时段日历 %q", AlignmentEpochUTC, calendarID)
		}
		return Market{Alignment: AlignmentEpochUTC, Timezone: "UTC"}, nil
	case AlignmentSession:
		if calendarID == "" {
			return Market{}, fmt.Errorf("%w：session 数据集缺少 calendar 属性", ErrNoCalendar)
		}
		market, ok := MarketForCalendar(calendarID)
		if !ok {
			return Market{}, fmt.Errorf("%w：未登记的交易日历 %q", ErrNoCalendar, calendarID)
		}
		return market, nil
	default:
		return Market{}, fmt.Errorf("未知的 alignment %q（仅支持 %s、%s）", alignment, AlignmentEpochUTC, AlignmentSession)
	}
}

// ClockForDataset 按数据集的 alignment 与 calendar 属性直接构造周期时钟。
func ClockForDataset(alignment, calendarID string, period time.Duration) (Clock, error) {
	market, err := MarketForDataset(alignment, calendarID)
	if err != nil {
		return nil, err
	}
	return market.Clock(period)
}

// AllowedIntradayForDataset 返回数据集 (alignment, calendar_id) 允许的日内周期（规范频率，不含 1d）。
func AllowedIntradayForDataset(alignment, calendarID string) ([]string, error) {
	market, err := MarketForDataset(alignment, calendarID)
	if err != nil {
		return nil, err
	}
	return market.AllowedIntraday(), nil
}

// RegularClose 返回 session 市场完整交易日的收市时刻（距本地零点的时长），即最后一个交易时段的收盘。
func (m Market) RegularClose() (time.Duration, error) {
	spans, err := parseSpans(m.Sessions)
	if err != nil {
		return 0, err
	}
	return spans[len(spans)-1].close, nil
}

// FullDayBuckets 返回 session 市场完整交易日（不提前收市）一天的日内桶数；周期不整除交易时段时按能放下的整根数算。
func (m Market) FullDayBuckets(period time.Duration) int {
	spans, err := parseSpans(m.Sessions)
	if err != nil || period <= 0 {
		return 0
	}
	total := 0
	for _, item := range spans {
		total += int(item.length() / period)
	}
	return total
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
