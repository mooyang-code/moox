// Package ashare 是 A 股场所（ASHARE）：交易时段、腾讯实时快照做的公共行情、东方财富标的列表。
// 模拟盘用它做行情；实盘下单走 QMT 执行端（尚未接入）。
package ashare

import (
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/packages/barclock"
)

// orderCutoff 是每天停止发新单的时刻：14:57-15:00 是收盘集合竞价，撤不了单。09:15-09:25 的开盘集合竞价第一版也不参与。
const orderCutoff = 14*time.Hour + 57*time.Minute

// ReasonCalendarExpired 是交易日历不覆盖当前日期时写进目标的原因。
const ReasonCalendarExpired = "交易日历已过期"

// Session 是 A 股的交易时段：09:30-11:30、13:00-15:00，按交易日历跳过休市日。
type Session struct {
	hours *barclock.Hours
}

var _ execution.TradingSession = (*Session)(nil)

// NewSession 用 stockcn 市场登记的交易日历和交易时段构造。
func NewSession() (*Session, error) {
	market, err := barclock.MarketFor("stockcn")
	if err != nil {
		return nil, err
	}
	hours, err := market.Hours()
	if err != nil {
		return nil, err
	}
	return &Session{hours: hours}, nil
}

// InSession 报告是否在连续竞价时段；交易日历不覆盖时按不在时段内处理。
func (s *Session) InSession(t time.Time) bool {
	in, err := s.hours.InSession(t)
	return err == nil && in
}

// CanOrder 报告能否发新单：在连续竞价时段内，且不在 14:57 之后。
func (s *Session) CanOrder(t time.Time) (bool, string) {
	in, err := s.hours.InSession(t)
	if errors.Is(err, barclock.ErrOutOfCoverage) {
		return false, ReasonCalendarExpired
	}
	if err != nil || !in {
		return false, execution.ReasonOutOfSession
	}
	local := t.In(s.hours.Location())
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	if local.Sub(midnight) >= orderCutoff {
		return false, execution.ReasonOutOfSession
	}
	return true, ""
}

// LastBreak 返回最近一次休市开始时刻（11:30 或 15:00）。
func (s *Session) LastBreak(t time.Time) time.Time {
	at, err := s.hours.LastBreak(t)
	if err != nil {
		return time.Time{}
	}
	return at
}

// LastClose 返回最近一次日收盘（15:00）。
func (s *Session) LastClose(t time.Time) time.Time {
	at, err := s.hours.LastClose(t)
	if err != nil {
		return time.Time{}
	}
	return at
}
