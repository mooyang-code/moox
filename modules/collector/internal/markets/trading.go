// Package markets 按市场回答“某根 K 线是不是交易时段内的桶”：分桶统一取 packages/barclock，
// 交易日历统一取 packages/marketcalendar 的内嵌日历。连续交易的市场（加密货币）每根都是交易桶，
// 交易时段市场（A 股）只有交易日的交易时段内才有桶。调用方不区分市场，只调用这里。
package markets

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/packages/barclock"
	"github.com/mooyang-code/moox/packages/marketcalendar"
)

var (
	// ErrCalendarExpiringSoon 表示交易日历的覆盖期即将用完。
	ErrCalendarExpiringSoon = errors.New("交易日历覆盖期即将用完")
	// ErrCalendarExpired 表示交易日历的覆盖期已经用完。
	ErrCalendarExpired = errors.New("交易日历覆盖期已用完")
)

// clocks 缓存（市场，频率）的时钟：构造时要加载时区、解析交易时段，完整性检查会逐根调用。
var clocks sync.Map

// Clock 返回市场某个频率的分桶时钟；未登记的市场按连续交易（epoch_utc）处理。
func Clock(marketID, frequency string) (barclock.Clock, error) {
	parsed, err := marketdata.ParseFrequency(frequency)
	if err != nil {
		return nil, err
	}
	key := strings.ToLower(strings.TrimSpace(marketID)) + "\x00" + string(parsed)
	if cached, ok := clocks.Load(key); ok {
		return cached.(barclock.Clock), nil
	}
	clock, err := newClock(marketID, parsed)
	if err != nil {
		return nil, err
	}
	clocks.Store(key, clock)
	return clock, nil
}

func newClock(marketID string, parsed marketdata.Frequency) (barclock.Clock, error) {
	market, err := barclock.MarketFor(marketID)
	if errors.Is(err, barclock.ErrUnknownMarket) {
		return barclock.NewEpoch(parsed.Duration())
	}
	if err != nil {
		return nil, err
	}
	return market.Clock(parsed.Duration())
}

// Location 返回市场的时区；连续交易或未登记的市场是 UTC。
func Location(marketID string) (*time.Location, error) {
	market, err := barclock.MarketFor(marketID)
	if errors.Is(err, barclock.ErrUnknownMarket) || err == nil && market.Timezone == "" {
		return time.UTC, nil
	}
	if err != nil {
		return nil, err
	}
	if cached, ok := locations.Load(market.Timezone); ok {
		return cached.(*time.Location), nil
	}
	location, err := time.LoadLocation(market.Timezone)
	if err != nil {
		return nil, fmt.Errorf("市场 %s 的时区 %q 无效：%w", marketID, market.Timezone, err)
	}
	locations.Store(market.Timezone, location)
	return location, nil
}

var locations sync.Map

// TradingBar 判断 barStart 是否是该市场 frequency 周期某个交易桶的键（日内是开盘时刻）。
// 连续交易的市场总是返回真；超出交易日历覆盖期返回错误，不能当作非交易时间。
func TradingBar(marketID, frequency string, barStart time.Time) (bool, error) {
	if continuous(marketID) {
		return true, nil
	}
	clock, err := Clock(marketID, frequency)
	if err != nil {
		return false, err
	}
	buckets, err := clock.Between(barStart, barStart.Add(clock.Period()), 1)
	if err != nil {
		return false, err
	}
	return len(buckets) == 1 && buckets[0].Key.Equal(barStart.UTC()), nil
}

// CheckCalendarHorizon 检查市场交易日历的覆盖期：已用完返回 ErrCalendarExpired，剩余不足 minDays 天返回
// ErrCalendarExpiringSoon；没有交易日历的市场（连续交易）总是通过。
func CheckCalendarHorizon(marketID string, now time.Time, minDays int) error {
	market, err := barclock.MarketFor(marketID)
	if errors.Is(err, barclock.ErrUnknownMarket) || err == nil && market.Alignment != barclock.AlignmentSession {
		return nil
	}
	if err != nil {
		return err
	}
	calendar, err := marketcalendar.Load(market.CalendarID)
	if err != nil {
		return fmt.Errorf("加载交易日历 %s：%w", market.CalendarID, err)
	}
	location, err := Location(marketID)
	if err != nil {
		return err
	}
	local := now.In(location)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	last := calendar.LastDate()
	end := time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, time.UTC)
	if today.After(end) {
		return fmt.Errorf("%w：%s 日历覆盖到 %s", ErrCalendarExpired, market.CalendarID, last)
	}
	if end.Before(today.AddDate(0, 0, minDays)) {
		return fmt.Errorf("%w：%s 日历覆盖到 %s", ErrCalendarExpiringSoon, market.CalendarID, last)
	}
	return nil
}

func continuous(marketID string) bool {
	market, err := barclock.MarketFor(marketID)
	return errors.Is(err, barclock.ErrUnknownMarket) || err == nil && market.Alignment == barclock.AlignmentEpochUTC
}
