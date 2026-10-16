package observability

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
)

// spaceClock 是一个空间的市场时钟；err 非空时说明为什么无法按交易日历判断。
type spaceClock struct {
	clock monmetrics.MarketClock
	err   error
}

// spaceClocks 按采集任务清单给每个空间选市场时钟：有交易日历的任务（A 股）优先，其次是全天交易的加密货币。
// 清单不可用或空间里没有采集任务时，这个空间不按交易日历判断（只显示原因，不改变数据集状态）。
func (b Builder) spaceClocks(ctx context.Context, now time.Time) map[string]spaceClock {
	out := make(map[string]spaceClock)
	if b.Inventory == nil {
		return out
	}
	snapshot, _, err := b.Inventory.GetAllowStale(ctx, now)
	if err != nil {
		return out
	}
	chosen := make(map[string]monmetrics.TaskResultInventoryEntry)
	for _, entry := range snapshot.Entries {
		current, ok := chosen[entry.SpaceID]
		if !ok || strings.TrimSpace(current.CalendarID) == "" && strings.TrimSpace(entry.CalendarID) != "" {
			chosen[entry.SpaceID] = entry
		}
	}
	for spaceID, entry := range chosen {
		clock, err := monmetrics.MarketClockFor(entry.MarketID, entry.CalendarID, entry.Timezone, entry.Sessions)
		out[spaceID] = spaceClock{clock: clock, err: err}
	}
	return out
}

// barFacts 是一个数据集最新一根 K 线的到达情况。
type barFacts struct {
	latestClose time.Time
	// delay 是最新一根的到达时间 − 收线时间，在到达那一刻定值，之后不随当前时间增长。
	delay      time.Duration
	delayKnown bool
	// expected 是预期 K 线：此刻应已收线的最新一根。
	expected monmetrics.Bar
	// waiting 是应收却还没到的根数，waitingSince 是其中最早一根的收线时间（应到时间），overdue 是它已晚多久（当前时间 − 应到时间）。
	waiting      int
	waitingSince time.Time
	overdue      time.Duration
	// confirmed 表示生产者在应到时间之后上报过、上报里仍没有这根；confirmedOverdue 是那次上报时它已晚多久。
	confirmed        bool
	confirmedOverdue time.Duration
	// issue 说明无法按交易日历判断的原因。
	issue error
}

// evaluateBars 用市场时钟识别输出水位对应的那一根（水位是 K 线在存储里的 data_time，即开盘时间），
// 算出它的收线时间与到达延迟，并和此刻应已收线的最新一根（预期 K 线）比较，数出还在等待的根数。
// 不使用“当前时间 − 最新 K 线时间”：休市、午休、节假日不会被当作落后。
//
// confirmedAt 是生产者最近一次上报快照的时刻（Monitor 只能看到这一刻的输出水位）。生产者每 30 秒上报一次，
// 一根准时写完的 K 线最长要等 30 秒才出现在 Monitor 里：overdue 按当前时间算，用于展示；判过期看 confirmedOverdue——
// 上报那一刻它已晚超过允许迟到仍没有，才算确认迟到，不会把还没来得及上报的准时 K 线判成过期。
// confirmedAt 为零、晚于 now（时钟偏差）或超过 monmetrics.ReportSnapshotGrace 没有新上报时按 now 处理。
func evaluateBars(sc spaceClock, freqRaw string, output, arrival, confirmedAt, now time.Time) barFacts {
	facts := barFacts{}
	if sc.err != nil {
		facts.issue = sc.err
		return facts
	}
	freq, err := frequencypkg.Parse(freqRaw)
	if err != nil {
		facts.issue = fmt.Errorf("周期 %q 无效", freqRaw)
		return facts
	}
	expected, err := sc.clock.LatestClosed(now, freq)
	if err != nil {
		facts.issue = err
		return facts
	}
	facts.expected = expected
	if output.IsZero() {
		return facts
	}
	latest, err := sc.clock.BarAt(output, freq)
	if err != nil {
		facts.issue = err
		return facts
	}
	facts.latestClose = latest.Close
	if !arrival.IsZero() {
		facts.delay = max(arrival.Sub(latest.Close), 0)
		facts.delayKnown = true
	}
	if latest.Close.Before(expected.Close) {
		count, first, err := sc.clock.ClosedBarsAfter(latest, expected.Close, freq)
		if err != nil {
			facts.issue = err
			return facts
		}
		facts.waiting, facts.waitingSince = count, first.Close
		facts.overdue = max(now.Sub(first.Close), 0)
		confirmedAt = monmetrics.SnapshotTime(confirmedAt, now)
		if !confirmedAt.Before(first.Close) {
			facts.confirmed, facts.confirmedOverdue = true, confirmedAt.Sub(first.Close)
		}
	}
	return facts
}

// clockIssueText 是无法按交易日历判断时给人看的原因。没有交易日历的空间（例如 mooxsys 的主机、服务指标）不是行情 K 线，
// 本来就不判断到达延迟，不给提示。
func clockIssueText(err error) string {
	switch {
	case err == nil, errors.Is(err, monmetrics.ErrNoTradingCalendar):
		return ""
	case errors.Is(err, monmetrics.ErrCalendarNotCovered):
		return "日历未覆盖：" + strings.TrimPrefix(err.Error(), monmetrics.ErrCalendarNotCovered.Error()+"：")
	case errors.Is(err, monmetrics.ErrAlignmentUnconfirmed):
		return "周期对齐未确认，暂不判断"
	default:
		return err.Error()
	}
}

// waitingReason 说明还没到的那根：哪一根、已晚多久、缺几根、允许迟到多久。
func waitingReason(item DatasetFrequencyStatus) string {
	return fmt.Sprintf("等待中：%s 收线的那根已晚 %s 仍未到（缺 %d 根，允许迟到 %s）",
		alerttext.Time(item.WaitingSince), alerttext.Duration(time.Duration(item.OverdueSeconds)*time.Second),
		item.WaitingBars, alerttext.Duration(time.Duration(item.LatenessLimitSeconds)*time.Second))
}
