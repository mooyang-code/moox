package eventconsumer

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestHandleLogicalAccountTargetMapsTargetIdentity(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	var wakes atomic.Int32
	now := time.Now().UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-2", "runner-1", "logical-1",
		[]*tradeeventpb.InstrumentWeightTarget{{
			InstrumentId: "BTC-USDT-SPOT", MarketType: "spot", TargetWeight: "2.5",
		}},
	), func() TargetOptions {
		opts := targetOptions(tradeStore, now)
		opts.Wake = func() { wakes.Add(1) }
		return opts
	}())

	require.Equal(t, jetstream.ACK, result.Decision)
	require.NoError(t, result.Err)
	require.Equal(t, int32(1), wakes.Load())
	got, err := tradeStore.GetLogicalAccountTarget(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Equal(t, "target-2", got.TargetID)
	require.Equal(t, "runner-1", got.InstanceID)
	require.Equal(t, now.UnixMilli(), got.BarEndTime)
	require.Equal(t, []store.InstrumentQuantity{{
		InstrumentID: "BTC-USDT-SPOT", Quantity: "2.5",
	}}, got.Targets)
	require.Equal(t, "PENDING", got.Status)
	require.Equal(t, now.UnixMilli(), got.AcceptedAt)
}

func TestHandleTargetDirectedWakeOnlyAfterNewAcceptance(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	now := time.Now().UTC()
	opts := targetOptions(tradeStore, now)
	var wakes []string
	opts.Wake = func() { t.Error("directed wake must not trigger global scan") }
	opts.WakeTarget = func(space, logical string) {
		record, err := tradeStore.GetLogicalAccountTarget(context.Background(), space, logical)
		require.NoError(t, err)
		_, err = tradeStore.GetAcceptedTargetHistory(context.Background(), space, record.TargetID)
		require.NoError(t, err, "receipt must be committed before wake")
		wakes = append(wakes, space+"/"+logical)
	}
	delivery := logicalTargetDelivery(t, now, "target-directed", "runner-1", "logical-1", nil)
	require.Equal(t, jetstream.ACK, HandleTarget(context.Background(), delivery, opts).Decision)
	require.Equal(t, []string{"space-1/logical-1"}, wakes)
	require.Equal(t, jetstream.ACK, HandleTarget(context.Background(), delivery, opts).Decision)
	rejected := logicalTargetDelivery(t, now, "target-rejected", "runner-other", "logical-1", nil)
	require.Equal(t, jetstream.TERM, HandleTarget(context.Background(), rejected, opts).Decision)
	opts.WeightResolver = targetResolverFunc(func(context.Context, *tradeeventpb.LogicalAccountTargetWeightRequested, string) (targetapp.WeightConversion, error) {
		return targetapp.WeightConversion{}, errors.New("resolver unavailable")
	})
	next := now.Add(time.Minute)
	opts.Now = func() time.Time { return next }
	failed := logicalTargetDelivery(t, next, "target-failed", "runner-1", "logical-1", nil)
	require.Equal(t, jetstream.RETRY, HandleTarget(context.Background(), failed, opts).Decision)
	require.Equal(t, []string{"space-1/logical-1"}, wakes)
}

func TestManualTargetReplayRejectedBeforeResolverAndWake(t *testing.T) {
	db := openTargetStore(t)
	seedLogicalTargetAccount(t, db, true, true)
	now := time.Now().UTC()
	delivery := logicalTargetDelivery(t, now, "target-manual", "runner-1", "logical-1", nil)
	opts := targetOptions(db, now)
	require.Equal(t, jetstream.ACK, HandleTarget(context.Background(), delivery, opts).Decision)
	require.NoError(t, db.DBForTest().Exec("UPDATE t_logical_accounts SET c_control_mode = 'MANUAL'").Error)
	opts.Wake = func() { t.Error("manual target must not wake") }
	opts.WeightResolver = targetResolverFunc(func(context.Context, *tradeeventpb.LogicalAccountTargetWeightRequested, string) (targetapp.WeightConversion, error) {
		t.Error("manual target must not resolve")
		return targetapp.WeightConversion{}, nil
	})
	require.Equal(t, jetstream.TERM, HandleTarget(context.Background(), delivery, opts).Decision)
}

func TestHandleLogicalAccountTargetAcceptsEmptyFullWhilePaused(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, false, true)
	now := time.Now().UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-empty", "runner-1", "logical-1", nil,
	), targetOptions(tradeStore, now))

	require.Equal(t, jetstream.ACK, result.Decision)
	require.NoError(t, result.Err)
	got, err := tradeStore.GetLogicalAccountTarget(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Empty(t, got.Targets)
}

func TestHandleLogicalAccountTargetAcceptsBeforeBarEnd(t *testing.T) {
	db := openTargetStore(t)
	seedLogicalTargetAccount(t, db, true, true)
	now := time.Now().UTC()
	effective := now.Add(500 * time.Millisecond)
	delivery := logicalTargetDeliveryWithTimes(t, effective, effective.Add(time.Hour), "future-target", "runner-1", "logical-1", nil)
	result := HandleTarget(context.Background(), delivery, targetOptions(db, now))
	require.Equal(t, jetstream.ACK, result.Decision)
	require.NoError(t, result.Err)
	target, err := db.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, effective.UnixMilli(), target.BarEndTime)
	receipt, err := db.GetAcceptedTargetHistory(context.Background(), "space-1", "future-target")
	require.NoError(t, err)
	require.Equal(t, effective.UnixMilli(), receipt.BarEndTime)
}

func TestHandleLogicalAccountTargetTermsAfterValidityWindow(t *testing.T) {
	db := openTargetStore(t)
	seedLogicalTargetAccount(t, db, true, true)
	now := time.Now().UTC()
	delivery := logicalTargetDeliveryWithTimes(t, now.Add(-2*time.Hour), now.Add(-time.Hour), "expired-target", "runner-1", "logical-1", nil)
	result := HandleTarget(context.Background(), delivery, targetOptions(db, now))
	require.Equal(t, jetstream.TERM, result.Decision)
	require.ErrorIs(t, result.Err, store.ErrTargetExpired)
}

// 同毫秒不同会话（不应出现）：开始时刻与组合上的当前目标相同、会话却不同，按授权冲突拒绝并告警，不进入换算、不唤醒执行器。
func TestHandleTargetSameStartDifferentSessionNeedsAttention(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	now := time.Now().UTC()
	opts := targetOptions(tradeStore, now)
	require.Equal(t, jetstream.ACK, HandleTarget(context.Background(), logicalTargetDelivery(t, now, "target-1", "runner-1", "logical-1", nil), opts).Decision)

	opts.WeightResolver = targetResolverFunc(func(context.Context, *tradeeventpb.LogicalAccountTargetWeightRequested, string) (targetapp.WeightConversion, error) {
		t.Error("会话冲突的目标不能进入换算")
		return targetapp.WeightConversion{}, nil
	})
	opts.WakeTarget = func(string, string) { t.Error("会话冲突的目标不能唤醒执行器") }
	result := HandleTarget(context.Background(), logicalTargetDelivery(t, now.Add(time.Minute), "target-other", "runner-other", "logical-1", nil), opts)

	require.Equal(t, jetstream.TERM, result.Decision)
	require.ErrorIs(t, result.Err, store.ErrTargetSessionConflict)
	require.Equal(t, "authorization_conflict", targetErrorCode(result))
	require.True(t, attentionCode(targetErrorCode(result)), "同毫秒不同会话要告警")
	current, err := tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "target-1", current.TargetID)
}

// testSessions 是测试里用到的会话，按开始时刻从早到晚排列；testSessionStartedAt 给出各自的开始时刻（毫秒）。
var testSessions = []string{"session-1", "session-2", "session-9"}

// testSessionStartedAt 是测试会话的开始时刻：session-1 < session-2 < session-9，别的会话名按 session-1 处理。
func testSessionStartedAt(sessionID string) int64 {
	switch sessionID {
	case "session-2":
		return 2_000
	case "session-9":
		return 9_000
	default:
		return 1_000
	}
}

// closeTargetSession 对 logical-1 关闭 sessionID（按 testSessionStartedAt 取开始时刻），就是 Trade 的 CloseTargetSession 在库里做的事：
// 推进 c_closed_through，缩短这个会话的目标行的有效期。
func closeTargetSession(t *testing.T, tradeStore *store.Store, sessionID string) {
	t.Helper()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		_, _, err := tx.CloseTargetSession("space-1", "logical-1", testSessionStartedAt(sessionID), time.Now().UnixMilli())
		return err
	}))
}

// 停用再启用策略（改策略的常规操作）后，旧会话还在路上的目标：已过期的按过期终止；没过期的，会话已关闭或组合上
// 已是更晚会话的目标，都按被取代处理、不告警；只有开始时刻相同而会话不同（不应出现）才是需要人看的授权冲突。
func TestHandleTargetFromOldSessionIsSupersededNotConflict(t *testing.T) {
	for _, test := range []struct {
		name string
		// arrange 是投递 session-1 的目标之前的前置。
		arrange             func(t *testing.T, db *store.Store)
		expired             bool
		code                string
		needsHumanAttention bool
	}{
		{name: "已过期且会话已关闭", arrange: func(t *testing.T, db *store.Store) { closeTargetSession(t, db, "session-1") }, expired: true, code: "target_expired"},
		{name: "会话已关闭", arrange: func(t *testing.T, db *store.Store) { closeTargetSession(t, db, "session-1") }, code: "superseded"},
		{name: "组合上已是更晚会话的目标", arrange: func(t *testing.T, db *store.Store) { seedSessionTarget(t, db, "later", "runner-2", "session-2", 0) }, code: "superseded"},
		{name: "开始时刻相同、会话不同", arrange: func(t *testing.T, db *store.Store) {
			seedSessionTarget(t, db, "same-start", "runner-2", "session-x", testSessionStartedAt("session-1"))
		}, code: "authorization_conflict", needsHumanAttention: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			tradeStore := openTargetStore(t)
			seedLogicalTargetAccount(t, tradeStore, true, true)
			test.arrange(t, tradeStore)
			now := time.Now().UTC()
			barEnd, validUntil := now, now.Add(time.Hour)
			if test.expired {
				barEnd, validUntil = now.Add(-2*time.Hour), now.Add(-time.Hour)
			}
			opts := targetOptions(tradeStore, now)
			opts.WeightResolver = targetResolverFunc(func(context.Context, *tradeeventpb.LogicalAccountTargetWeightRequested, string) (targetapp.WeightConversion, error) {
				t.Error("旧会话的目标不能进入换算")
				return targetapp.WeightConversion{}, nil
			})
			result := HandleTarget(context.Background(), logicalTargetDeliveryWithTimes(
				t, barEnd, validUntil, "old-session-target", "runner-1", "logical-1",
				[]*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "BTC-USDT-SPOT", MarketType: "spot", TargetWeight: "1"}},
			), opts)
			require.Equal(t, jetstream.TERM, result.Decision)
			require.Equal(t, test.code, targetErrorCode(result))
			require.Equal(t, test.needsHumanAttention, attentionCode(targetErrorCode(result)))
		})
	}
}

// seedSessionTarget 直接在库里给 logical-1 写一行 instanceID/sessionID 的当前目标（空目标、有效一小时）；
// startedAt 为 0 时按会话取 testSessionStartedAt。
func seedSessionTarget(t *testing.T, db *store.Store, targetID, instanceID, sessionID string, startedAt int64) {
	t.Helper()
	if startedAt == 0 {
		startedAt = testSessionStartedAt(sessionID)
	}
	now := time.Now().UTC()
	require.NoError(t, db.Transaction(context.Background(), func(tx *store.Tx) error {
		_, accepted, err := tx.AcceptLogicalAccountTarget(store.LogicalAccountTargetRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: targetID,
			InstanceID: instanceID, SessionID: sessionID, SessionStartedAt: startedAt, StrategyID: "strategy-1",
			BarEndTime: now.Add(-time.Second).UnixMilli(), ValidUntil: now.Add(time.Hour).UnixMilli(),
			Targets: []store.InstrumentQuantity{}, Status: "PENDING", AcceptedAt: now.UnixMilli(),
		})
		if err == nil && !accepted {
			err = errors.New("目标没有写成当前目标")
		}
		return err
	}))
}

func TestHandleLogicalAccountTargetRejectsUnsupportedInstrument(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	now := time.Now().UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-1", "runner-1", "logical-1",
		[]*tradeeventpb.InstrumentWeightTarget{{
			InstrumentId: "ETH-USDT-SPOT", MarketType: "spot", TargetWeight: "1",
		}},
	), targetOptions(tradeStore, now))

	require.Equal(t, jetstream.TERM, result.Decision)
	require.Error(t, result.Err)
	require.Contains(t, result.Err.Error(), "ETH-USDT-SPOT")
}

func TestHandleLogicalAccountTargetRejectsInstrumentForDifferentSettlementAsset(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "binance", MarketType: "spot", ExchangeSymbol: "BTCUSDC",
			InstrumentID: "BTC-USDC-SPOT", BaseAsset: "BTC", QuoteAsset: "USDC",
			SettlementAsset: "USDC", ExchangeQuantityStep: "0.001",
			MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
		})
	}))
	now := time.Now().UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-usdc", "runner-1", "logical-1",
		[]*tradeeventpb.InstrumentWeightTarget{{
			InstrumentId: "BTC-USDC-SPOT", MarketType: "spot", TargetWeight: "1",
		}},
	), targetOptions(tradeStore, now))

	require.Equal(t, jetstream.TERM, result.Decision)
	require.Error(t, result.Err)
	require.Contains(t, result.Err.Error(), "BTC-USDC-SPOT")
}

func TestHandleLogicalAccountTargetRejectsNonCanonicalInstrumentStatus(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, false)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "binance", MarketType: "spot", ExchangeSymbol: "BTCUSDT",
			InstrumentID: "BTC-USDT-SPOT", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettlementAsset: "USDT", ExchangeQuantityStep: "0.001",
			MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "trading",
		})
	}))
	now := time.Now().UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-lowercase-status", "runner-1", "logical-1",
		[]*tradeeventpb.InstrumentWeightTarget{{
			InstrumentId: "BTC-USDT-SPOT", MarketType: "spot", TargetWeight: "1",
		}},
	), targetOptions(tradeStore, now))

	require.Equal(t, jetstream.TERM, result.Decision)
	require.Error(t, result.Err)
	require.Contains(t, result.Err.Error(), "BTC-USDT-SPOT")
}

func TestHandleLogicalAccountTargetRetriesMissingMetadataUntilMembersReady(t *testing.T) {
	for _, test := range []struct {
		name     string
		ready    bool
		decision jetstream.HandlerDecision
	}{
		{name: "startup", ready: false, decision: jetstream.RETRY},
		{name: "permanently unsupported", ready: true, decision: jetstream.TERM},
	} {
		t.Run(test.name, func(t *testing.T) {
			tradeStore := openTargetStore(t)
			seedLogicalTargetAccount(t, tradeStore, test.ready, false)
			now := time.Now().UTC()

			result := HandleTarget(context.Background(), logicalTargetDelivery(
				t, now, "target-1", "runner-1", "logical-1",
				[]*tradeeventpb.InstrumentWeightTarget{{
					InstrumentId: "BTC-USDT-SPOT", MarketType: "spot", TargetWeight: "1",
				}},
			), targetOptions(tradeStore, now))

			require.Equal(t, test.decision, result.Decision)
			require.Error(t, result.Err)
		})
	}
}

func TestHandleLogicalAccountTargetRetriesWhenAllMembersDisabled(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetTradingAccountStatus("space-1", "account-1", "DISABLED")
	}))
	now := time.Now().UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-disabled", "runner-1", "logical-1",
		[]*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "BTC-USDT-SPOT", MarketType: "spot", TargetWeight: "1"}},
	), targetOptions(tradeStore, now))

	require.Equal(t, jetstream.RETRY, result.Decision)
	require.Error(t, result.Err)
}

func TestHandleLogicalAccountTargetRetriesUntilMemberExists(t *testing.T) {
	tradeStore := openTargetStore(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			ExecutionMode:   "PAPER",
			SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		})
	}))
	now := time.Now().UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-1", "runner-1", "logical-1",
		[]*tradeeventpb.InstrumentWeightTarget{{
			InstrumentId: "BTC-USDT-SPOT", MarketType: "spot", TargetWeight: "1",
		}},
	), targetOptions(tradeStore, now))

	require.Equal(t, jetstream.RETRY, result.Decision)
	require.Error(t, result.Err)
}

func TestHandleLogicalAccountTargetAcceptsOKXLiveInstrument(t *testing.T) {
	tradeStore := openTargetStore(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "okx-1", Name: "okx",
			Exchange: "okx", Markets: []string{"swap"}, ExecutionMode: "PAPER",
			Environment: "PAPER", SettlementAsset: "USDT", MarginMode: "CROSS",
			Status: "ENABLED", Ready: true,
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			ExecutionMode:   "PAPER",
			SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TradingAccountID: "okx-1", Enabled: true,
		}); err != nil {
			return err
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "okx", MarketType: "swap", ExchangeSymbol: "BTC-USDT-SWAP",
			InstrumentID: "BTC-USDT-SWAP", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettlementAsset: "USDT", Linear: true, ContractValue: "0.01",
			ContractValueAsset: "BTC", ExchangeQuantityStep: "1",
			MinExchangeQuantity: "1", PriceTick: "0.1", Status: "live",
		})
	}))
	now := time.Now().UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-1", "runner-1", "logical-1",
		[]*tradeeventpb.InstrumentWeightTarget{{
			InstrumentId: "BTC-USDT-SWAP", MarketType: "swap", TargetWeight: "1",
		}},
	), targetOptions(tradeStore, now))

	require.Equal(t, jetstream.ACK, result.Decision)
	require.NoError(t, result.Err)
}

func TestHandleLogicalAccountTargetDoesNotWakeForExactRetry(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	var wakes atomic.Int32
	now := time.Now().UTC()
	delivery := logicalTargetDelivery(
		t, now, "target-1", "runner-1", "logical-1", nil,
	)
	opts := targetOptions(tradeStore, now)
	opts.Wake = func() { wakes.Add(1) }

	require.Equal(t, jetstream.ACK, HandleTarget(
		context.Background(), delivery, opts,
	).Decision)
	require.Equal(t, jetstream.ACK, HandleTarget(
		context.Background(), delivery, opts,
	).Decision)
	require.Equal(t, int32(1), wakes.Load())
}

func TestHandleTargetTermsInvalidDelivery(t *testing.T) {
	result := HandleTarget(context.Background(), nil, TargetOptions{})
	require.Equal(t, jetstream.TERM, result.Decision)
	require.ErrorIs(t, result.Err, jetstream.ErrInvalidDelivery)
}

func TestHandleTargetTermsMalformedEnvelope(t *testing.T) {
	now := time.Now().UTC()
	result := HandleTarget(context.Background(), &jetstream.Delivery{
		RawData: []byte("malformed"), ContentType: events.ContentType,
	}, targetOptions(openTargetStore(t), now))
	require.Equal(t, jetstream.TERM, result.Decision)
	require.Error(t, result.Err)
}

func TestHandleTargetRejectsLegacyQuantityEvent(t *testing.T) {
	tradeStore := openTargetStore(t)
	raw, err := proto.Marshal(&eventpb.EventMessage{
		EventId:      "legacy-target",
		EventName:    "event.trade.target.requested",
		EventVersion: 1,
		SpaceId:      "space-1",
		SubjectId:    "logical-1",
		OccurredAt:   time.Now().UTC().UnixMilli(),
		Payload:      []byte("unregistered target payload"),
	})
	require.NoError(t, err)

	result := HandleTarget(context.Background(), &jetstream.Delivery{
		RawData: raw, Subject: "legacy-target-subject", RawMessageID: "legacy-target", ContentType: events.ContentType,
	}, targetOptions(tradeStore, time.Now().UTC()))
	require.Equal(t, jetstream.TERM, result.Decision)
	require.Contains(t, result.Err.Error(), "event event.trade.target.requested@1 is not registered")
}

func TestHandleTargetRejectsIncompleteModernEventBeforeAccountLookup(t *testing.T) {
	tradeStore := openTargetStore(t)
	incompletePayload, err := proto.Marshal(&tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId:         "incomplete-target",
		LogicalAccountId: "logical-1",
	})
	require.NoError(t, err)
	now := time.Now().UTC()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	subject, err := registry.RenderSubject(events.LogicalAccountTargetWeightRequested, "space-1", "logical-1")
	require.NoError(t, err)
	raw, err := proto.Marshal(&eventpb.EventMessage{
		EventId:      "incomplete-target",
		EventName:    events.LogicalAccountTargetWeightRequested.Name(),
		EventVersion: events.LogicalAccountTargetWeightRequested.Version(),
		SpaceId:      "space-1",
		SubjectId:    "logical-1",
		OccurredAt:   now.UnixMilli(),
		Payload:      incompletePayload,
	})
	require.NoError(t, err)

	result := HandleTarget(context.Background(), &jetstream.Delivery{
		RawData: raw, Subject: subject, RawMessageID: "incomplete-target", ContentType: events.ContentType,
	}, targetOptions(tradeStore, now))
	require.Equal(t, jetstream.TERM, result.Decision)
	require.Contains(t, result.Err.Error(), "session identity is incomplete")
}

func openTargetStore(t *testing.T) *store.Store {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	return tradeStore
}

type testWeightResolver struct{}

func (testWeightResolver) Resolve(_ context.Context, request *tradeeventpb.LogicalAccountTargetWeightRequested, _ string) (targetapp.WeightConversion, error) {
	signalTime := request.GetBarEndTime()
	hash, err := targetapp.RequestHash(request)
	if err != nil {
		return targetapp.WeightConversion{}, err
	}
	targets := make([]store.InstrumentQuantity, 0, len(request.GetTargets()))
	weights := make([]map[string]string, 0, len(request.GetTargets()))
	for _, target := range request.GetTargets() {
		targets = append(targets, store.InstrumentQuantity{InstrumentID: target.GetInstrumentId(), Quantity: target.GetTargetWeight()})
		weights = append(weights, map[string]string{"instrument_id": target.GetInstrumentId(), "target_weight": target.GetTargetWeight()})
	}
	weightsJSON, _ := json.Marshal(weights)
	if signalTime <= 0 {
		signalTime = 1_700_000_000_000
	}
	return targetapp.WeightConversion{RequestHash: hash, SignalTime: signalTime, Equity: shared.MustDecimal("1"), EquitySourceTime: signalTime, WeightsJSON: string(weightsJSON), ReferencePrices: map[string]string{}, QuantityTargets: targets}, nil
}

func targetOptions(tradeStore *store.Store, now time.Time) TargetOptions {
	return TargetOptions{Store: tradeStore, Now: func() time.Time { return now }, WeightResolver: testWeightResolver{}}
}

func seedLogicalTargetAccount(
	t *testing.T,
	tradeStore *store.Store,
	ready bool,
	withInstrument bool,
) {
	t.Helper()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", Name: "main",
			Exchange: "binance", Markets: []string{"spot"}, ExecutionMode: "PAPER",
			Environment: "PAPER", SettlementAsset: "USDT",
			Status: "ENABLED", Ready: ready,
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			ExecutionMode:   "PAPER",
			SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TradingAccountID: "account-1", Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		if !withInstrument {
			return nil
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "binance", MarketType: "spot", ExchangeSymbol: "BTCUSDT",
			InstrumentID: "BTC-USDT-SPOT", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettlementAsset: "USDT", ExchangeQuantityStep: "0.001",
			MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
		})
	}))
}

func logicalTargetDelivery(
	t *testing.T,
	now time.Time,
	targetID string,
	instanceID string,
	logicalAccountID string,
	targets []*tradeeventpb.InstrumentWeightTarget,
) *jetstream.Delivery {
	return logicalTargetDeliveryWithTimes(t, now, now.Add(time.Hour), targetID, instanceID, logicalAccountID, targets)
}

func logicalTargetDeliveryWithTimes(
	t *testing.T,
	barEnd time.Time,
	validUntil time.Time,
	targetID string,
	instanceID string,
	logicalAccountID string,
	targets []*tradeeventpb.InstrumentWeightTarget,
) *jetstream.Delivery {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	encoded, err := registry.Encode(
		events.LogicalAccountTargetWeightRequested,
		&tradeeventpb.LogicalAccountTargetWeightRequested{
			TargetId: targetID, InstanceId: instanceID, SessionId: "session-1", SessionStartedAt: testSessionStartedAt("session-1"), StrategyId: "strategy-1",
			LogicalAccountId: logicalAccountID, Targets: targets,
			BarEndTime: barEnd.UnixMilli(), ValidUntil: validUntil.UnixMilli(),
		},
		events.PublishOptions{
			EventID: targetID, OccurredAt: barEnd,
			SpaceID: "space-1", SubjectID: logicalAccountID,
		},
	)
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	return &jetstream.Delivery{
		RawData: raw, Subject: encoded.Subject,
		RawMessageID: targetID, ContentType: events.ContentType,
	}
}

// Trade 暂停（生命周期门禁关闭）时，真实目标不被接收、也不被销毁：延迟重投，放开后照常接收。
// 暂停前已经发布、暂停期间才送到的目标因此不会被静默终止。
func TestHandleTargetHoldsTargetsWhileIntakeClosed(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	now := time.Now().UTC()
	opts := targetOptions(tradeStore, now)
	open := false
	opts.Intake = func() bool { return open }
	opts.WakeTarget = func(string, string) { t.Error("暂停时被拒绝的目标不能唤醒执行") }

	delivery := logicalTargetDelivery(t, now, "target-paused", "runner-1", "logical-1", nil)
	result := HandleTarget(context.Background(), delivery, opts)
	require.Equal(t, jetstream.RETRY, result.Decision, "暂停时目标留在消费者里等待，不能终止")
	require.Equal(t, pausedTargetDelay, result.Delay)
	var business *targetBusinessError
	require.False(t, errors.As(result.Err, &business), "不是业务拒绝，没有终态回执")
	_, err := tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.Error(t, err, "暂停期间目标不落库")

	open = true
	woke := false
	opts.WakeTarget = func(string, string) { woke = true }
	require.Equal(t, jetstream.ACK, HandleTarget(context.Background(), delivery, opts).Decision)
	require.True(t, woke)
}

// 目标通过入口检查后在等组合锁、换算期间，会话被关闭或组合上来了别的会话的目标：落库时（在组合锁和事务里）按同样的规则分类，
// 会话已关闭、来了更晚会话的目标的按被取代处理、不告警，开始时刻相同而会话不同的才是授权冲突；都不写回执、不唤醒执行器。
func TestHandleTargetSessionChangedWhileWaitingIsClassifiedAtCommit(t *testing.T) {
	for _, test := range []struct {
		name                string
		change              func(t *testing.T, db *store.Store)
		code                string
		needsHumanAttention bool
	}{
		{name: "会话被关闭", change: func(t *testing.T, db *store.Store) { closeTargetSession(t, db, "session-1") }, code: "superseded"},
		{name: "来了更晚会话的目标", change: func(t *testing.T, db *store.Store) { seedSessionTarget(t, db, "later", "runner-2", "session-2", 0) }, code: "superseded"},
		{name: "来了开始时刻相同的别的会话的目标", change: func(t *testing.T, db *store.Store) {
			seedSessionTarget(t, db, "same-start", "runner-2", "session-x", testSessionStartedAt("session-1"))
		}, code: "authorization_conflict", needsHumanAttention: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			tradeStore := openTargetStore(t)
			seedLogicalTargetAccount(t, tradeStore, true, true)
			now := time.Now().UTC()
			opts := targetOptions(tradeStore, now)
			opts.WeightResolver = targetResolverFunc(func(ctx context.Context, request *tradeeventpb.LogicalAccountTargetWeightRequested, space string) (targetapp.WeightConversion, error) {
				test.change(t, tradeStore)
				return testWeightResolver{}.Resolve(ctx, request, space)
			})
			opts.WakeTarget = func(string, string) { t.Error("会话已变的目标不能唤醒执行器") }
			result := HandleTarget(context.Background(), logicalTargetDelivery(
				t, now, "target-session-changed", "runner-1", "logical-1",
				[]*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "BTC-USDT-SPOT", MarketType: "spot", TargetWeight: "1"}},
			), opts)
			require.Equal(t, jetstream.TERM, result.Decision)
			require.Equal(t, test.code, targetErrorCode(result))
			require.Equal(t, test.needsHumanAttention, attentionCode(targetErrorCode(result)))
			_, err := tradeStore.GetAcceptedTargetHistory(context.Background(), "space-1", "target-session-changed")
			require.Error(t, err, "会话已变的目标不写回执")
		})
	}
}
