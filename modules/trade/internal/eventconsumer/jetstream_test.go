package eventconsumer

import (
	"context"
	"errors"
	"testing"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// 一个组合的目标持续可重试失败时，不能连带拖住其他组合的目标：消费者每次只拉一条，排在后面的照常接收。
func TestRunTargetRetryDoesNotHoldBackOtherAccounts(t *testing.T) {
	server := testkit.Start(t)
	server.AddStream(t, &nats.StreamConfig{Name: events.LogicalAccountTargetWeightRequested.Stream(), Subjects: []string{"moox.>"}})
	ctx, cancel := context.WithCancel(context.Background())
	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{server.URL()}, Name: "target-batch-test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	// logical-2 没有成员：它的目标一直按“组合账户还没有启用的成员”重投。
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-2", Name: "stuck",
			OwnerInstanceID: "runner-2", OwnerSessionID: "session-1", ExecutionMode: "PAPER",
			SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "configure",
		})
	}))
	now := time.Now().UTC()
	// 启动消费者之前两条都已进流，重投的那条排在前面，原来的批量拉取会把两条拉进同一批。
	stuck := logicalTargetDelivery(t, now, "target-stuck", "runner-2", "logical-2",
		[]*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "BTC-USDT-SPOT", MarketType: "SPOT", TargetWeight: "1"}})
	normal := logicalTargetDelivery(t, now, "target-normal", "runner-1", "logical-1", nil)
	for _, delivery := range []*jetstream.Delivery{stuck, normal} {
		_, err = client.PublishRaw(ctx, delivery.Subject, delivery.RawMessageID, delivery.RawData, delivery.ContentType)
		require.NoError(t, err)
	}

	options := targetOptions(tradeStore, now)
	options.Client, options.ConsumerName = client, "target-retry-isolation"
	done := make(chan error, 1)
	go func() { done <- RunTarget(ctx, options) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("目标消费者没有停下")
		}
	})

	require.Eventually(t, func() bool {
		target, lookupErr := tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
		return lookupErr == nil && target.TargetID == "target-normal"
	}, 3*time.Second, 20*time.Millisecond, "其他组合的目标被重投的那条连带拖住了")
	_, err = tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-2")
	require.Error(t, err, "没有成员的组合不能接收目标")
}

// 两个组合每次处理都要一秒多才可重试失败（等组合锁、取价超时）时，服务端会优先把到期的重投送回来；
// 消费者并发处理，第三个组合的正常目标仍能很快被接收，不会被这两个组合轮流占住处理位置。
func TestRunTargetSlowRetriesDoNotStarveOtherAccounts(t *testing.T) {
	server := testkit.Start(t)
	server.AddStream(t, &nats.StreamConfig{Name: events.LogicalAccountTargetWeightRequested.Stream(), Subjects: []string{"moox.>"}})
	ctx, cancel := context.WithCancel(context.Background())
	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{server.URL()}, Name: "target-slow-retry-test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	// 两个慢失败的组合：空目标不需要成员，直接进入换算，换算每次睡约 1.2 秒后返回可重试错误。
	for _, slow := range []struct{ logical, instance string }{{"logical-slow-a", "runner-a"}, {"logical-slow-b", "runner-b"}} {
		require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
			return tx.CreateLogicalAccount(store.LogicalAccountRecord{
				SpaceID: "space-1", LogicalAccountID: slow.logical, Name: slow.logical,
				OwnerInstanceID: slow.instance, OwnerSessionID: "session-1", ExecutionMode: "PAPER",
				SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "configure",
			})
		}))
	}
	now := time.Now().UTC()
	deliveries := []*jetstream.Delivery{
		logicalTargetDelivery(t, now, "target-slow-a", "runner-a", "logical-slow-a", nil),
		logicalTargetDelivery(t, now, "target-slow-b", "runner-b", "logical-slow-b", nil),
		logicalTargetDelivery(t, now, "target-normal", "runner-1", "logical-1", nil),
	}
	for _, delivery := range deliveries {
		_, err = client.PublishRaw(ctx, delivery.Subject, delivery.RawMessageID, delivery.RawData, delivery.ContentType)
		require.NoError(t, err)
	}

	options := targetOptions(tradeStore, now)
	options.Client, options.ConsumerName = client, "target-slow-retry-isolation"
	options.WeightResolver = targetResolverFunc(func(resolveCtx context.Context, request *tradeeventpb.LogicalAccountTargetWeightRequested, space string) (targetapp.WeightConversion, error) {
		if request.GetLogicalAccountId() == "logical-1" {
			return testWeightResolver{}.Resolve(resolveCtx, request, space)
		}
		select {
		case <-time.After(1200 * time.Millisecond):
		case <-resolveCtx.Done():
		}
		return targetapp.WeightConversion{}, errors.New("取价超时")
	})
	done := make(chan error, 1)
	go func() { done <- RunTarget(ctx, options) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("目标消费者没有停下")
		}
	})

	require.Eventually(t, func() bool {
		target, lookupErr := tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
		return lookupErr == nil && target.TargetID == "target-normal"
	}, 3*time.Second, 20*time.Millisecond, "慢失败的组合轮流占住了处理位置，其他组合的目标没被接收")
}
