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
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestHandleLogicalAccountTargetMapsTargetIdentity(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	var wakes atomic.Int32
	now := time.Now().UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-2", "runner-1", "logical-1", 2,
		[]*tradeeventpb.InstrumentWeightTarget{{
			InstrumentId: "BTC-USDT-SPOT", TargetWeight: "2.5",
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
	require.Equal(t, "runner-1", got.RunnerID)
	require.Equal(t, uint64(2), got.CommandSequence)
	require.Equal(t, []store.InstrumentTarget{{
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
		_, err = tradeStore.GetTargetReceipt(context.Background(), space, record.TargetID)
		require.NoError(t, err, "receipt must be committed before wake")
		wakes = append(wakes, space+"/"+logical)
	}
	delivery := logicalTargetDelivery(t, now, "target-directed", "runner-1", "logical-1", 2, nil)
	require.Equal(t, jetstream.ACK, HandleTarget(context.Background(), delivery, opts).Decision)
	require.Equal(t, []string{"space-1/logical-1"}, wakes)
	require.Equal(t, jetstream.ACK, HandleTarget(context.Background(), delivery, opts).Decision)
	rejected := logicalTargetDelivery(t, now, "target-rejected", "runner-other", "logical-1", 3, nil)
	require.Equal(t, jetstream.TERM, HandleTarget(context.Background(), rejected, opts).Decision)
	opts.WeightResolver = targetResolverFunc(func(context.Context, int64, *tradeeventpb.LogicalAccountTargetWeightRequested, string) (targetapp.WeightConversion, error) {
		return targetapp.WeightConversion{}, errors.New("resolver unavailable")
	})
	next := now.Add(time.Minute)
	opts.Now = func() time.Time { return next }
	failed := logicalTargetDelivery(t, next, "target-failed", "runner-1", "logical-1", 3, nil)
	require.Equal(t, jetstream.RETRY, HandleTarget(context.Background(), failed, opts).Decision)
	require.Equal(t, []string{"space-1/logical-1"}, wakes)
}

func TestHandleLogicalAccountTargetAcceptsEmptyFullWhilePaused(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, false, true)
	now := time.Now().UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-empty", "runner-1", "logical-1", 1, nil,
	), targetOptions(tradeStore, now))

	require.Equal(t, jetstream.ACK, result.Decision)
	require.NoError(t, result.Err)
	got, err := tradeStore.GetLogicalAccountTarget(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Empty(t, got.Targets)
}

func TestHandleLogicalAccountTargetRejectsWrongRunner(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	now := time.Now().UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-1", "runner-other", "logical-1", 1, nil,
	), targetOptions(tradeStore, now))

	require.Equal(t, jetstream.TERM, result.Decision)
	require.ErrorIs(t, result.Err, store.ErrConflict)
}

func TestHandleLogicalAccountTargetRejectsDelayedEventFromPreviousOwnerLifecycle(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	claimAt := time.Now().UTC()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountOwnerAt("space-1", "logical-1", "runner-1", claimAt)
	}))
	oldEventAt := claimAt.Add(-time.Second)
	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, oldEventAt, "delayed-old-target", "runner-1", "logical-1", 1,
		[]*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "BTC-USDT-SPOT", TargetWeight: "1"}},
	), targetOptions(tradeStore, oldEventAt))
	require.Equal(t, jetstream.TERM, result.Decision)
	require.ErrorIs(t, result.Err, store.ErrConflict)
	_, err := tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.Error(t, err)
}

func TestHandleLogicalAccountTargetAcceptsMatchingOwnerGeneration(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	now := time.Now().UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-generation-match", "runner-1", "logical-1", 1,
		[]*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "BTC-USDT-SPOT", TargetWeight: "1"}}, 1,
	), targetOptions(tradeStore, now))

	require.Equal(t, jetstream.ACK, result.Decision)
	require.NoError(t, result.Err)
	got, err := tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "target-generation-match", got.TargetID)
}

func TestHandleLogicalAccountTargetRejectsUnsupportedInstrument(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	now := time.Now().UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-1", "runner-1", "logical-1", 1,
		[]*tradeeventpb.InstrumentWeightTarget{{
			InstrumentId: "ETH-USDT-SPOT", TargetWeight: "1",
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
			Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDC",
			InstrumentID: "BTC-USDC-SPOT", BaseAsset: "BTC", QuoteAsset: "USDC",
			SettlementAsset: "USDC", ExchangeQuantityStep: "0.001",
			MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
		})
	}))
	now := time.Now().UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-usdc", "runner-1", "logical-1", 1,
		[]*tradeeventpb.InstrumentWeightTarget{{
			InstrumentId: "BTC-USDC-SPOT", TargetWeight: "1",
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
			Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT",
			InstrumentID: "BTC-USDT-SPOT", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettlementAsset: "USDT", ExchangeQuantityStep: "0.001",
			MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "trading",
		})
	}))
	now := time.Now().UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-lowercase-status", "runner-1", "logical-1", 1,
		[]*tradeeventpb.InstrumentWeightTarget{{
			InstrumentId: "BTC-USDT-SPOT", TargetWeight: "1",
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
				t, now, "target-1", "runner-1", "logical-1", 1,
				[]*tradeeventpb.InstrumentWeightTarget{{
					InstrumentId: "BTC-USDT-SPOT", TargetWeight: "1",
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
		t, now, "target-disabled", "runner-1", "logical-1", 1,
		[]*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "BTC-USDT-SPOT", TargetWeight: "1"}},
	), targetOptions(tradeStore, now))

	require.Equal(t, jetstream.RETRY, result.Decision)
	require.Error(t, result.Err)
}

func TestHandleLogicalAccountTargetRetriesUntilMemberExists(t *testing.T) {
	tradeStore := openTargetStore(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			OwnerRunnerID: "runner-1", OwnerInstanceID: "runner-1", OwnerSessionID: "session-1", ExecutionMode: "PAPER",
			MarketType: "SPOT", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		})
	}))
	now := time.Now().UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-1", "runner-1", "logical-1", 1,
		[]*tradeeventpb.InstrumentWeightTarget{{
			InstrumentId: "BTC-USDT-SPOT", TargetWeight: "1",
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
			Exchange: "OKX", MarketType: "SWAP", ExecutionMode: "PAPER",
			Environment: "PAPER", SettlementAsset: "USDT", MarginMode: "CROSS",
			Status: "ENABLED", Ready: true,
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			OwnerRunnerID: "runner-1", OwnerInstanceID: "runner-1", OwnerSessionID: "session-1", ExecutionMode: "PAPER",
			MarketType: "SWAP", SettlementAsset: "USDT",
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
			Exchange: "OKX", MarketType: "SWAP", ExchangeSymbol: "BTC-USDT-SWAP",
			InstrumentID: "BTC-USDT-SWAP", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettlementAsset: "USDT", Linear: true, ContractValue: "0.01",
			ContractValueAsset: "BTC", ExchangeQuantityStep: "1",
			MinExchangeQuantity: "1", PriceTick: "0.1", Status: "live",
		})
	}))
	now := time.Now().UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-1", "runner-1", "logical-1", 1,
		[]*tradeeventpb.InstrumentWeightTarget{{
			InstrumentId: "BTC-USDT-SWAP", TargetWeight: "1",
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
		t, now, "target-1", "runner-1", "logical-1", 1, nil,
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

func openTargetStore(t *testing.T) *store.Store {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	return tradeStore
}

type testWeightResolver struct{}

func (testWeightResolver) Resolve(_ context.Context, signalTime int64, request *tradeeventpb.LogicalAccountTargetWeightRequested, _ string) (targetapp.WeightConversion, error) {
	hash, err := targetapp.RequestHash(request)
	if err != nil {
		return targetapp.WeightConversion{}, err
	}
	targets := make([]store.InstrumentTarget, 0, len(request.GetTargets()))
	weights := make([]map[string]string, 0, len(request.GetTargets()))
	for _, target := range request.GetTargets() {
		targets = append(targets, store.InstrumentTarget{InstrumentID: target.GetInstrumentId(), Quantity: target.GetTargetWeight()})
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
			Exchange: "BINANCE", MarketType: "SPOT", ExecutionMode: "PAPER",
			Environment: "PAPER", SettlementAsset: "USDT",
			Status: "ENABLED", Ready: ready,
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			OwnerRunnerID: "runner-1", OwnerInstanceID: "runner-1", OwnerSessionID: "session-1", ExecutionMode: "PAPER",
			MarketType: "SPOT", SettlementAsset: "USDT",
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
			Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT",
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
	runnerID string,
	logicalAccountID string,
	sequence int64,
	targets []*tradeeventpb.InstrumentWeightTarget,
	ownerGeneration ...int64,
) *jetstream.Delivery {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	var generation int64
	if len(ownerGeneration) > 0 {
		generation = ownerGeneration[0]
	}
	bar := timestamppb.New(now)
	encoded, err := registry.Encode(
		events.LogicalAccountTargetWeightRequested,
		&tradeeventpb.LogicalAccountTargetWeightRequested{
			TargetId: targetID, RunnerId: runnerID, InstanceId: runnerID, SessionId: "session-1", StrategyId: "strategy-1",
			LogicalAccountId: logicalAccountID, CommandSequence: sequence, Targets: targets, OwnerGeneration: generation,
			BarEndTime: bar, EffectiveAt: bar, ValidUntil: timestamppb.New(now.Add(time.Hour)),
		},
		events.PublishOptions{
			EventID: targetID, OccurredAt: now,
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
