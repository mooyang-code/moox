package execution

import (
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// TradingSession 是场所的交易时段。没有登记时段的场所（加密货币）全天交易。
//
// 有两种口径：
//   - 市场时段（InSession）：交易所撮合的时段，A 股是 09:30-11:30、13:00-15:00。报价新鲜度、模拟撮合、收盘撤单按它判断。
//   - 下单时段（CanOrder）：Trade 自己发新单的时段，A 股在 14:57 截止（之后是收盘集合竞价，撤不了单）。
type TradingSession interface {
	// InSession 报告 t 是否在市场时段内；交易日历不覆盖 t 时按不在时段内处理。
	InSession(t time.Time) bool
	// CanOrder 报告 t 能否发新单；不能时 reason 是给人看的原因（“当前不在交易时段”“交易日历已过期”）。
	CanOrder(t time.Time) (ok bool, reason string)
	// LastBreak 返回不晚于 t 的最近一次休市开始时刻（A 股是 11:30 或 15:00）；交易日历不覆盖时返回零值。
	LastBreak(t time.Time) time.Time
	// LastClose 返回不晚于 t 的最近一次日收盘（A 股只指 15:00）；交易日历不覆盖时返回零值。
	LastClose(t time.Time) time.Time
}

// ReasonOutOfSession 是不在交易时段时写进目标的原因。
const ReasonOutOfSession = "当前不在交易时段"

// QuoteCloseSlack 是休市后报价时间允许早于休市时刻的余量：冷门股收盘集合竞价可能没有成交，
// 收盘快照的行情时间会早于 15:00:00。
const QuoteCloseSlack = 5 * time.Minute

func sessionOf(exchanges ExchangeCatalog, name string) TradingSession {
	if exchanges == nil {
		return nil
	}
	descriptor, ok := exchanges.Descriptor(exchange.Exchange(name))
	if !ok {
		return nil
	}
	return descriptor.Session
}

// CanOrder 报告账户所在场所当前能否发新单；没有登记时段的场所全天可以。
func CanOrder(exchanges ExchangeCatalog, name string, now time.Time) (bool, string) {
	session := sessionOf(exchanges, name)
	if session == nil {
		return true, ""
	}
	return session.CanOrder(now)
}

// InSession 报告账户所在场所当前是否在市场时段内；没有登记时段的场所全天都在。
func InSession(exchanges ExchangeCatalog, name string, now time.Time) bool {
	session := sessionOf(exchanges, name)
	return session == nil || session.InSession(now)
}

// LastClose 返回场所最近一次日收盘；没有登记时段（或日历不覆盖）时 ok 为假。
func LastClose(exchanges ExchangeCatalog, name string, now time.Time) (time.Time, bool) {
	session := sessionOf(exchanges, name)
	if session == nil {
		return time.Time{}, false
	}
	closeAt := session.LastClose(now)
	return closeAt, !closeAt.IsZero()
}

// QuoteFresh 判断报价时间是否新鲜，各处报价新鲜度检查共用：
//   - 时段内（以及全天交易的场所）照原来的窗口判断：|now − quoteTime| ≤ window；
//   - 时段外（午休、收盘后、休市日）只要求报价时间不早于最近一次休市时刻减 5 分钟，也不晚于 now + window：
//     腾讯收盘后返回的就是收盘价，夜里、周末不会再有新报价。
//
// 只用于报价时间。余额、持仓的同步时间不能用它：夜里同步一直失败也会被当成新鲜。
func QuoteFresh(exchanges ExchangeCatalog, name string, quoteTime, now time.Time, window time.Duration) bool {
	if quoteTime.IsZero() {
		return false
	}
	age := now.Sub(quoteTime)
	if age < -window {
		return false
	}
	session := sessionOf(exchanges, name)
	if session == nil || session.InSession(now) {
		return age <= window
	}
	lastBreak := session.LastBreak(now)
	if lastBreak.IsZero() {
		return false
	}
	return !quoteTime.Before(lastBreak.Add(-QuoteCloseSlack))
}
