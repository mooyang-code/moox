package stream

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
)

// 连续 3 个 1m 周期没有收盘推送才判定疑似下架；中间到了一次就重新计数；整期零到达不计。
func TestDelistTrackerFlagsAfterThreeMissedMinutes(t *testing.T) {
	var reports []Suspect
	tracker := newDelistTracker("swap", func(s Suspect) { reports = append(reports, s) })
	plan, _ := testStreamPlan("1m", 5, copyOf(closeAt, "A-USDT", "B-USDT"))
	tracker.setPlan(plan)
	observe := func(frequency string, arrived int, missing ...string) PeriodStat {
		stat := PeriodStat{Frequency: frequency, Arrived: arrived, MissingSubjects: missing}
		tracker.observe(&stat)
		return stat
	}
	observe("1m", 1, "A-USDT")
	observe("1m", 1, "A-USDT")
	observe("1m", 2)
	observe("1m", 1, "A-USDT")
	observe("1m", 0, "A-USDT", "B-USDT")
	observe("1h", 1, "A-USDT")
	require.Empty(t, reports)
	observe("1m", 1, "A-USDT")
	stat := observe("1m", 1, "A-USDT")
	require.Equal(t, []Suspect{{Market: "swap", SubjectID: "A-USDT", Reason: "连续 3 个 1m 周期没有收盘推送"}}, reports)
	require.Equal(t, 1, stat.Suspected, "疑似下架的标的不计入缺失告警")
	require.Equal(t, 1, observe("1h", 1, "A-USDT").Suspected, "1h 跟随 1m 的判定")
	observe("1m", 1, "A-USDT")
	require.Len(t, reports, 1, "只上报一次")

	tracker.flag("B-USDT", "合约状态")
	tracker.flag("B-USDT", "合约状态")
	require.Len(t, reports, 2)
	tracker.setPlan(nil)
	tracker.flag("B-USDT", "合约状态")
	require.Len(t, reports, 3, "计划去掉后重新出现时可以再报")
}

// 订阅随各份的生效时间变化：新增的标的拿到计划就订阅，移除的标的在它最后负责的周期过去后才退订。
func TestDesiredStreamsFollowCopyEffectiveTimes(t *testing.T) {
	source := fakeSource{}
	p := closeAt
	plan, _ := testStreamPlan("1m", 5, copyOf(p.Add(-time.Hour), "OLD-USDT", "KEEP-USDT"), copyOf(p, "KEEP-USDT", "NEW-USDT"))
	market := &marketRuntime{source: source, plan: plan}
	streams := func(now time.Time) []string { return market.desiredStreams(now)["1m"] }
	require.Equal(t, []string{"keepusdt@1m", "newusdt@1m", "oldusdt@1m"}, streams(p.Add(-2*time.Minute)), "P 之前已订阅新标的")
	require.Equal(t, []string{"keepusdt@1m", "newusdt@1m", "oldusdt@1m"}, streams(p.Add(30*time.Second)), "旧标的负责到 P 前一期")
	require.Equal(t, []string{"keepusdt@1m", "newusdt@1m"}, streams(p.Add(61*time.Second)))
}

type fakeSource struct{}

func (fakeSource) Key() marketdata.SourceKey {
	return marketdata.SourceKey{ProviderID: "binance", SourceID: "swap_http"}
}
func (fakeSource) MarketType() string  { return "swap" }
func (fakeSource) Endpoints() []string { return nil }
func (fakeSource) Limits() Limits      { return Limits{} }
func (fakeSource) Stream(symbol string, frequency marketdata.Frequency) (string, bool) {
	return toLower(symbol) + "@" + string(frequency), true
}
func (fakeSource) StatusStreams() []string                   { return nil }
func (fakeSource) Subscribe([]string, int) ([]byte, error)   { return nil, nil }
func (fakeSource) Unsubscribe([]string, int) ([]byte, error) { return nil, nil }
func (fakeSource) Decode([]byte, time.Time) ([]Event, error) { return nil, nil }
func (fakeSource) Keepalive() Keepalive                      { return Keepalive{} }

var _ = marketfetch.PlanExecutorStreaming

func toLower(value string) string {
	out := []rune(value)
	for i, r := range out {
		if r >= 'A' && r <= 'Z' {
			out[i] = r + 32
		}
	}
	return string(out)
}
