package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func shadowDelivery(t *testing.T, now time.Time, targetID string, targets []*tradeeventpb.InstrumentWeightTarget) *jetstream.Delivery {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	encoded, err := registry.Encode(events.LogicalAccountTargetWeightRequested, &tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId: targetID, InstanceId: "runner-1", SessionId: "session-1", StrategyId: "strategy-1", LogicalAccountId: "logical-1",
		Targets:    targets,
		BarEndTime: timestamppb.New(now), EffectiveAt: timestamppb.New(now), ValidUntil: timestamppb.New(now.Add(time.Hour)),
		Shadow: true,
	}, events.PublishOptions{EventID: targetID, OccurredAt: now, SpaceID: "space-1", SubjectID: "logical-1"})
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	return &jetstream.Delivery{RawData: raw, Subject: encoded.Subject, RawMessageID: targetID, ContentType: events.ContentType}
}

// 影子目标只换算并记录：不成为正式目标，不唤醒执行器，重复投递不重复记录。
func TestHandleShadowTargetRecordsWithoutAccepting(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	now := time.Now().UTC()
	opts := targetOptions(tradeStore, now)
	opts.Wake = func() { t.Error("影子目标不能唤醒执行器") }
	opts.WakeTarget = func(string, string) { t.Error("影子目标不能唤醒执行器") }
	delivery := shadowDelivery(t, now, "shadow-1", []*tradeeventpb.InstrumentWeightTarget{
		{InstrumentId: "BTC-USDT-SPOT", MarketType: "SPOT", TargetWeight: "1.5"},
	})

	for range 2 {
		result := HandleTarget(context.Background(), delivery, opts)
		require.Equal(t, jetstream.ACK, result.Decision)
		require.NoError(t, result.Err)
	}

	shadows, err := tradeStore.ListShadowTargets(context.Background(), "space-1", "runner-1", 10)
	require.NoError(t, err)
	require.Len(t, shadows, 1)
	require.Equal(t, "shadow-1", shadows[0].TargetID)
	require.Equal(t, "logical-1", shadows[0].LogicalAccountID)
	require.Contains(t, shadows[0].QuantityTargetsJSON, "BTC-USDT-SPOT")
	_, err = tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.Error(t, err, "影子目标不能成为账户的正式目标")
	_, err = tradeStore.GetTargetReceipt(context.Background(), "space-1", "shadow-1")
	require.Error(t, err, "影子目标不写回执")
}

// 影子目标里有没有成员能交易的标的：和正式目标一样整份拒绝（unsupported_instrument，需要人看），不换算、不记录，
// 而不是每秒重投到过期。
func TestHandleShadowTargetRejectsUnsupportedInstrumentLikeRealTarget(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	now := time.Now().UTC()
	opts := targetOptions(tradeStore, now)
	opts.WeightResolver = targetResolverFunc(func(context.Context, *tradeeventpb.LogicalAccountTargetWeightRequested, string) (targetapp.WeightConversion, error) {
		t.Error("不支持的标的不能进入换算")
		return targetapp.WeightConversion{}, nil
	})
	targets := []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "ETH-USDT-SPOT", MarketType: "SPOT", TargetWeight: "1"}}

	shadow := HandleTarget(context.Background(), shadowDelivery(t, now, "shadow-unsupported", targets), opts)
	formal := HandleTarget(context.Background(), logicalTargetDelivery(t, now, "real-unsupported", "runner-1", "logical-1", targets), opts)

	require.Equal(t, jetstream.TERM, shadow.Decision)
	require.Equal(t, "unsupported_instrument", targetErrorCode(shadow))
	require.True(t, attentionCode(targetErrorCode(shadow)), "不支持的标的要告警")
	require.Equal(t, formal.Decision, shadow.Decision)
	require.Equal(t, targetErrorCode(formal), targetErrorCode(shadow))
	shadows, err := tradeStore.ListShadowTargets(context.Background(), "space-1", "runner-1", 10)
	require.NoError(t, err)
	require.Empty(t, shadows)
}

// 影子目标里有下架名单上的标的：和正式目标一样只把这一项改成保持，其余照常换算并记录。
func TestHandleShadowTargetHoldsDelistedInstrument(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	seedDelistedInstrument(t, tradeStore, true)
	now := time.Now().UTC()
	resolver := &capturingResolver{}
	opts := targetOptions(tradeStore, now)
	opts.WeightResolver = resolver

	result := HandleTarget(context.Background(), shadowDelivery(t, now, "shadow-delist", []*tradeeventpb.InstrumentWeightTarget{
		{InstrumentId: "BTC-USDT-SPOT", MarketType: "SPOT", TargetWeight: "0.5"},
		{InstrumentId: "FOO-USDT-SPOT", MarketType: "SPOT", TargetWeight: "0.3"},
	}), opts)

	require.Equal(t, jetstream.ACK, result.Decision, "err=%v", result.Err)
	held := map[string]bool{}
	for _, target := range resolver.received {
		held[target.GetInstrumentId()] = target.GetHold()
	}
	require.Equal(t, map[string]bool{"BTC-USDT-SPOT": false, "FOO-USDT-SPOT": true}, held)
	shadows, err := tradeStore.ListShadowTargets(context.Background(), "space-1", "runner-1", 10)
	require.NoError(t, err)
	require.Len(t, shadows, 1)
	require.Equal(t, "shadow-delist", shadows[0].TargetID)
}

// 影子目标换算持续失败（投递满 5 次）时和正式目标一样记关注项，偶发失败只重投。
func TestHandleShadowTargetNotesPersistentConversionFailure(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	now := time.Now().UTC()
	opts := targetOptions(tradeStore, now)
	// 关注项是进程内全局记录，原因带上本次的时间，重复运行（-count）时互不影响。
	reason := fmt.Sprintf("影子换算取价失败 %d", now.UnixNano())
	opts.WeightResolver = targetResolverFunc(func(context.Context, *tradeeventpb.LogicalAccountTargetWeightRequested, string) (targetapp.WeightConversion, error) {
		return targetapp.WeightConversion{}, errors.New(reason)
	})
	delivery := shadowDelivery(t, now, "shadow-conversion", []*tradeeventpb.InstrumentWeightTarget{
		{InstrumentId: "BTC-USDT-SPOT", MarketType: "SPOT", TargetWeight: "1"},
	})
	noted := func() bool {
		for _, item := range telemetry.Recent(telemetry.AttentionTargetRejected, now, time.Minute) {
			if item == "logical：试运行目标换算失败："+reason {
				return true
			}
		}
		return false
	}

	delivery.DeliveryCount = conversionAttentionDeliveries - 1
	require.Equal(t, jetstream.RETRY, HandleTarget(context.Background(), delivery, opts).Decision)
	require.False(t, noted(), "偶发失败不告警")
	delivery.DeliveryCount = conversionAttentionDeliveries
	require.Equal(t, jetstream.RETRY, HandleTarget(context.Background(), delivery, opts).Decision)
	require.True(t, noted(), "持续失败要记关注项")
}

// 试运行目标被拒时，投递报告和关注项都带试运行标记，和正式目标分得开。
func TestTargetActionReportMarksShadowRejection(t *testing.T) {
	now := time.Now().UTC()
	targets := []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "ETH-USDT-SPOT", MarketType: "SPOT", TargetWeight: "1"}}
	result := targetRejection("unsupported_instrument", errors.New("没有启用的成员支持标的"))
	noted := func(detail string) bool {
		for _, item := range telemetry.Recent(telemetry.AttentionTargetRejected, time.Now(), time.Minute) {
			if item == detail {
				return true
			}
		}
		return false
	}

	shadow := shadowDelivery(t, now, "shadow-report", targets)
	var emitted targetActionReport
	targetActionReporter{emit: func(_ context.Context, report targetActionReport) { emitted = report }}.
		ReportAction(context.Background(), shadow, result, nil)
	require.True(t, emitted.Shadow)
	require.Equal(t, "unsupported_instrument", emitted.ErrorCode)
	require.True(t, noted("logical-1：试运行目标被拒：unsupported_instrument"))

	formal := logicalTargetDelivery(t, now, "formal-report", "runner-1", "logical-shadow-report", targets)
	targetActionReporter{emit: func(_ context.Context, report targetActionReport) { emitted = report }}.
		ReportAction(context.Background(), formal, result, nil)
	require.False(t, emitted.Shadow)
	require.True(t, noted("logical-shadow-report：unsupported_instrument"))
}
