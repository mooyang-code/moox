package period

import (
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/barclock"
	"github.com/mooyang-code/moox/packages/marketcalendar"
)

// sessionMarket 是一个按交易时段交易的市场在策略里用到的全部信息：barclock 登记的市场、内嵌交易日历、时区与完整交易日的
// 收市时刻。按策略日历 ID 从 barclock 的市场表查出（cn_stock → stockcn，hk_stock → stockhk，us_stock → stockus），
// 这里不写任何具体市场：barclock 新登记一个市场，策略就能用它的日历。
type sessionMarket struct {
	calendarID string
	market     barclock.Market
	calendar   marketcalendar.TradingCalendar
	location   *time.Location
	// regularClose 是完整交易日的收市时刻（距当地零点的时长）；提前收市日（半日市）看日历的 EarlyClose。
	regularClose time.Duration
}

// sessionMarkets 缓存已加载的市场（日历 ID → *sessionMarket）：内嵌日历的解析与校验较重，进程内每份只做一次；日历本身不可变。
var sessionMarkets sync.Map

// sessionMarketFor 返回日历 ID 对应的市场；ok 为假表示这不是按交易时段交易的市场的日历（例如 crypto）。
func sessionMarketFor(calendarID string) (*sessionMarket, bool, error) {
	calendarID = normalizeCalendar(calendarID)
	if cached, ok := sessionMarkets.Load(calendarID); ok {
		return cached.(*sessionMarket), true, nil
	}
	market, ok := barclock.MarketForCalendar(calendarID)
	if !ok {
		return nil, false, nil
	}
	calendar, err := marketcalendar.Load(market.CalendarID)
	if err != nil {
		return nil, true, &describedError{message: market.Name + "内嵌交易日历无法加载", cause: err}
	}
	location, err := time.LoadLocation(market.Timezone)
	if err != nil {
		return nil, true, &describedError{message: fmt.Sprintf("无法加载%s时区 %s", market.ZoneName, market.Timezone), cause: err}
	}
	regularClose, err := market.RegularClose()
	if err != nil {
		return nil, true, &describedError{message: market.Name + "的交易时段无效", cause: err}
	}
	value := &sessionMarket{calendarID: calendarID, market: market, calendar: calendar, location: location, regularClose: regularClose}
	actual, _ := sessionMarkets.LoadOrStore(calendarID, value)
	return actual.(*sessionMarket), true, nil
}

// loadedSessionMarket 返回已能使用的市场；日历不属于交易时段市场或加载失败时返回 nil（调用方退回 UTC 的写法）。
func loadedSessionMarket(calendarID string) *sessionMarket {
	market, ok, err := sessionMarketFor(calendarID)
	if !ok || err != nil {
		return nil
	}
	return market
}

// SessionCalendar 报告日历是不是按交易时段交易的市场的日历（A 股、港股等）：这类日历有休市日，内嵌日历有覆盖范围，
// 期望运行间隔、积累期要按交易日历折算。
func SessionCalendar(calendarID string) bool {
	_, ok, _ := sessionMarketFor(calendarID)
	return ok
}

// name 是给人看的市场名；m 为 nil（说不清是哪个市场）时返回空串。
func (m *sessionMarket) name() string {
	if m == nil {
		return ""
	}
	return m.market.Name
}

// spacedName 是接在汉字后面的市场名：以拉丁字母开头的（“A 股”）前面留一个空格。
func (m *sessionMarket) spacedName() string {
	name := m.name()
	if name != "" && name[0] < 0x80 {
		return " " + name
	}
	return name
}

// closeAt 返回交易日 day 的收盘时刻：提前收市日（半日市）是当天实际的收市时刻。
func (m *sessionMarket) closeAt(day marketcalendar.CivilDate) time.Time {
	offset := m.regularClose
	if early, ok := m.calendar.EarlyClose(day); ok {
		offset = early
	}
	hours := int(offset / time.Hour)
	minutes := int((offset % time.Hour) / time.Minute)
	return time.Date(day.Year(), day.Month(), day.Day(), hours, minutes, 0, 0, m.location)
}

// label 把时刻写成市场当地时间（精确到秒），给报错用，例如“2026-10-08 13:00:00 上海时间”。
func (m *sessionMarket) label(at time.Time) string {
	return at.In(m.location).Format("2006-01-02 15:04:05") + " " + m.market.ZoneName + "时间"
}

// MarketName 返回日历对应的市场名（如“A 股”、“港股”）；非交易时段市场返回空串。
func MarketName(calendarID string) string { return loadedSessionMarket(calendarID).name() }

// MarketSpacedName 返回接在汉字后面的市场名（以拉丁字母开头的前置空格）；非交易时段市场返回空串。
func MarketSpacedName(calendarID string) string { return loadedSessionMarket(calendarID).spacedName() }
