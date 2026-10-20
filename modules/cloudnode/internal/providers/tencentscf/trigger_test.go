package tencentscf

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	scf "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/scf/v20180416"
)

func TestTimerTriggerInfoNormalizesProviderCronAndState(t *testing.T) {
	item := &scf.TriggerInfo{
		TriggerName:     common.StringPtr("moox-market-fetch-timer"),
		Type:            common.StringPtr("timer"),
		TriggerDesc:     common.StringPtr(`{"cron":"0 * * * * * *"}`),
		Enable:          common.Uint64Ptr(1),
		AvailableStatus: common.StringPtr("Available"),
		Qualifier:       common.StringPtr("$LATEST"),
		CustomArgument:  common.StringPtr("market_fetch_timer_v1"),
	}
	info := timerTriggerInfoFromSDK(item)
	require.Equal(t, "moox-market-fetch-timer", info.Name)
	require.Equal(t, "timer", info.Type)
	require.Equal(t, "0 * * * * * *", info.Cron)
	require.True(t, info.Enabled)
	require.Equal(t, "Available", info.AvailableStatus)
	require.Equal(t, "$LATEST", info.Qualifier)
	require.Equal(t, "market_fetch_timer_v1", info.Message)
}

func TestTimerTriggerHelpersUseTencentContract(t *testing.T) {
	require.Equal(t, "OPEN", timerEnable(true))
	require.Equal(t, "CLOSE", timerEnable(false))
	require.Equal(t, "0 * * * * * *", normalizeTimerCron(common.StringPtr("0 * * * * * *")))
	require.Equal(t, "0 */5 * * * * *", normalizeTimerCron(common.StringPtr(`{"cron":"0 */5 * * * * *"}`)))
	require.Equal(t, "$LATEST", firstNonEmpty("", " $LATEST "))
}

func TestValidateTimerTriggerInfoRejectsStaleOrUnavailableReadback(t *testing.T) {
	req := TimerTriggerRequest{Name: "timer", Cron: "0 * * * * * *", Enabled: true, Qualifier: "$LATEST", Message: "market_fetch_timer_v1"}
	base := &TimerTriggerInfo{Name: req.Name, Type: "timer", Cron: req.Cron, Enabled: true, AvailableStatus: "Available", Qualifier: req.Qualifier, Message: req.Message}
	require.NoError(t, validateTimerTriggerInfo(base, req, req.Qualifier, req.Message))
	base.AvailableStatus = "Updating"
	require.ErrorContains(t, validateTimerTriggerInfo(base, req, req.Qualifier, req.Message), "not available")
	base.AvailableStatus = "Available"
	base.Cron = "0 */5 * * * * *"
	require.ErrorContains(t, validateTimerTriggerInfo(base, req, req.Qualifier, req.Message), "does not match")
}

func pollRequest() TimerTriggerRequest {
	return TimerTriggerRequest{Name: "timer", Cron: "0 * * * * * *", Enabled: true, Qualifier: "$LATEST", Message: "market_fetch_timer_v1"}
}

func pollTrigger(cron string) *scf.TriggerInfo {
	return &scf.TriggerInfo{
		TriggerName: common.StringPtr("timer"), Type: common.StringPtr("timer"), TriggerDesc: common.StringPtr(cron),
		Enable: common.Uint64Ptr(1), AvailableStatus: common.StringPtr("Available"),
		Qualifier: common.StringPtr("$LATEST"), CustomArgument: common.StringPtr("market_fetch_timer_v1"),
	}
}

func TestPollTimerTriggerBacksOffAndReturnsWhenMatched(t *testing.T) {
	clock := useFakeClock(t)
	start := clock.Now()
	var polls []time.Duration
	item, err := pollTimerTrigger(context.Background(), func(context.Context) (*scf.TriggerInfo, error) {
		polls = append(polls, clock.Now().Sub(start))
		if len(polls) < 4 {
			return pollTrigger("0 */5 * * * * *"), nil // 云端仍是旧值
		}
		return pollTrigger("0 * * * * * *"), nil
	}, pollRequest(), "$LATEST", "market_fetch_timer_v1")
	require.NoError(t, err)
	require.Equal(t, "0 * * * * * *", normalizeTimerCron(item.TriggerDesc))
	// 间隔 500ms、1s、2s 退避；固定 200ms 轮询在同样时间内要查约 17 次。
	require.Equal(t, []time.Duration{0, 500 * time.Millisecond, 1500 * time.Millisecond, 3500 * time.Millisecond}, polls)
}

func TestPollTimerTriggerStopsAtFiveSecondBudget(t *testing.T) {
	clock := useFakeClock(t)
	start := clock.Now()
	polls := 0
	item, err := pollTimerTrigger(context.Background(), func(context.Context) (*scf.TriggerInfo, error) {
		polls++
		return pollTrigger("0 */5 * * * * *"), nil // 永远读不到期望值
	}, pollRequest(), "$LATEST", "market_fetch_timer_v1")
	require.NoError(t, err)
	require.NotNil(t, item, "预算耗尽时返回最后一次读到的结果，由调用方校验")
	require.Equal(t, 4, polls)
	require.LessOrEqual(t, clock.Now().Sub(start), 5*time.Second, "总等待不得超过 5 秒预算")
}

func TestPollTimerTriggerPassesLocalDeadlineToEveryQuery(t *testing.T) {
	useFakeClock(t)
	var deadlines []time.Time
	_, err := pollTimerTrigger(context.Background(), func(ctx context.Context) (*scf.TriggerInfo, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok, "每次查询都必须带局部截止时间，慢查询才不会越过 5 秒预算")
		deadlines = append(deadlines, deadline)
		return pollTrigger("0 * * * * * *"), nil
	}, pollRequest(), "$LATEST", "market_fetch_timer_v1")
	require.NoError(t, err)
	require.Len(t, deadlines, 1)
}

func TestPollTimerTriggerSlowQueryStopsAtBudget(t *testing.T) {
	oldBudget := timerConfirmBudget
	timerConfirmBudget = 50 * time.Millisecond
	t.Cleanup(func() { timerConfirmBudget = oldBudget })
	started := time.Now()
	item, err := pollTimerTrigger(context.Background(), func(ctx context.Context) (*scf.TriggerInfo, error) {
		<-ctx.Done() // 云端查询一直卡住，直到局部预算到期
		return nil, ctx.Err()
	}, pollRequest(), "$LATEST", "market_fetch_timer_v1")
	require.NoError(t, err)
	require.Nil(t, item)
	require.Less(t, time.Since(started), time.Second)
}

func TestPollTimerTriggerDistinguishesParentCancelAndQueryError(t *testing.T) {
	useFakeClock(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := pollTimerTrigger(ctx, func(context.Context) (*scf.TriggerInfo, error) { return nil, context.Canceled }, pollRequest(), "$LATEST", "m")
	require.ErrorIs(t, err, context.Canceled)

	boom := errors.New("provider failed")
	_, err = pollTimerTrigger(context.Background(), func(context.Context) (*scf.TriggerInfo, error) { return nil, boom }, pollRequest(), "$LATEST", "m")
	require.ErrorIs(t, err, boom)
}

func TestPollTimerTriggerParentDeadlineIsNotSwallowed(t *testing.T) {
	useFakeClock(t)
	// 父 ctx 的剩余时间比 5 秒预算更短：令牌等待超限属于调用方预算不足，必须返回错误交给外层重试。
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := pollTimerTrigger(ctx, func(context.Context) (*scf.TriggerInfo, error) {
		return nil, ErrRateLimitWaitExceedsDeadline
	}, pollRequest(), "$LATEST", "m")
	require.ErrorIs(t, err, ErrRateLimitWaitExceedsDeadline)

	// 父 ctx 没有更紧的截止时间时，同样的错误表示本地预算耗尽。
	item, err := pollTimerTrigger(context.Background(), func(context.Context) (*scf.TriggerInfo, error) {
		return nil, ErrRateLimitWaitExceedsDeadline
	}, pollRequest(), "$LATEST", "m")
	require.NoError(t, err)
	require.Nil(t, item)
}
