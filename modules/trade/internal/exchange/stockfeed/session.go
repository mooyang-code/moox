// Package stockfeed 是股票场所（A 股 ASHARE、港股 HKEX）共用的实现：按交易日历的交易时段、公共行情的快照缓存、
// 标的列表的后台刷新，以及只提供行情、不下单的适配器。各场所自己的包（ashare、hkex）只给出行情来源、标的规则和时段参数。
package stockfeed

import (
	"errors"
	"time"
	// 内嵌时区数据：主机缺少 tzdata 时各市场的时区（上海、香港）仍然可用，交易时段不会因此构造失败。
	_ "time/tzdata"

	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/packages/barclock"
)

// ReasonCalendarExpired 是交易日历不覆盖当前日期时写进目标的原因。
const ReasonCalendarExpired = "交易日历已过期"

// Session 是一个股票市场的交易时段：取 barclock 登记的市场（交易日历、时段、半日市），按交易日历跳过休市日。
type Session struct {
	hours *barclock.Hours
	// orderCutoff 是每天停止发新单的时刻（距当地零点）；0 表示连续竞价时段内都能发单。
	orderCutoff time.Duration
}

var _ execution.TradingSession = (*Session)(nil)

// NewSession 用 barclock 里 marketID 登记的交易日历和交易时段构造。orderCutoff 不为 0 时，当天这个时刻之后不再发新单
// （A 股 14:57 之后是收盘集合竞价，撤不了单）。
func NewSession(marketID string, orderCutoff time.Duration) (*Session, error) {
	market, err := barclock.MarketFor(marketID)
	if err != nil {
		return nil, err
	}
	hours, err := market.Hours()
	if err != nil {
		return nil, err
	}
	return &Session{hours: hours, orderCutoff: orderCutoff}, nil
}

// InSession 报告是否在连续竞价时段；交易日历不覆盖时按不在时段内处理。半日市按当天实际的时段算。
func (s *Session) InSession(t time.Time) bool {
	in, err := s.hours.InSession(t)
	return err == nil && in
}

// CanOrder 报告能否发新单：在连续竞价时段内，且不在下单截止时刻之后。
func (s *Session) CanOrder(t time.Time) (bool, string) {
	in, err := s.hours.InSession(t)
	if errors.Is(err, barclock.ErrOutOfCoverage) {
		return false, ReasonCalendarExpired
	}
	if err != nil || !in {
		return false, execution.ReasonOutOfSession
	}
	if s.orderCutoff > 0 {
		local := t.In(s.hours.Location())
		midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
		if local.Sub(midnight) >= s.orderCutoff {
			return false, execution.ReasonOutOfSession
		}
	}
	return true, ""
}

// LastBreak 返回最近一次休市开始时刻（午间休市或日收盘）。
func (s *Session) LastBreak(t time.Time) time.Time {
	at, err := s.hours.LastBreak(t)
	if err != nil {
		return time.Time{}
	}
	return at
}

// LastClose 返回最近一次日收盘。
func (s *Session) LastClose(t time.Time) time.Time {
	at, err := s.hours.LastClose(t)
	if err != nil {
		return time.Time{}
	}
	return at
}
