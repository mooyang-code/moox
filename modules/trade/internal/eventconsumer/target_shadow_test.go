package eventconsumer

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func shadowDelivery(t *testing.T, now time.Time, targetID string) *jetstream.Delivery {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	encoded, err := registry.Encode(events.LogicalAccountTargetWeightRequested, &tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId: targetID, InstanceId: "runner-1", SessionId: "session-1", StrategyId: "strategy-1", LogicalAccountId: "logical-1",
		Targets:    []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "BTC-USDT-SPOT", TargetWeight: "1.5"}},
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
	delivery := shadowDelivery(t, now, "shadow-1")

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
