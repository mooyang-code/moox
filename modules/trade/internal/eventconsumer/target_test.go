package eventconsumer

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestHandleLogicalAccountTargetMapsTargetIdentity(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	var wakes atomic.Int32
	now := time.UnixMilli(1_700_000_000_000).UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-2", "runner-1", "logical-1", 2,
		[]*tradeeventpb.InstrumentTarget{{
			InstrumentId: "BTC-USDT-SPOT", Quantity: "2.5",
		}},
	), TargetOptions{
		Store: tradeStore, Wake: func() { wakes.Add(1) },
		Now: func() time.Time { return now },
	})

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

func TestHandleLogicalAccountTargetAcceptsEmptyFullWhilePaused(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, false, true)
	now := time.UnixMilli(1_700_000_000_000).UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-empty", "runner-1", "logical-1", 1, nil,
	), TargetOptions{Store: tradeStore, Now: func() time.Time { return now }})

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
	now := time.UnixMilli(1_700_000_000_000).UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-1", "runner-other", "logical-1", 1, nil,
	), TargetOptions{Store: tradeStore, Now: func() time.Time { return now }})

	require.Equal(t, jetstream.TERM, result.Decision)
	require.ErrorIs(t, result.Err, store.ErrConflict)
}

func TestHandleLogicalAccountTargetRejectsUnsupportedInstrument(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	now := time.UnixMilli(1_700_000_000_000).UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-1", "runner-1", "logical-1", 1,
		[]*tradeeventpb.InstrumentTarget{{
			InstrumentId: "ETH-USDT-SPOT", Quantity: "1",
		}},
	), TargetOptions{Store: tradeStore, Now: func() time.Time { return now }})

	require.Equal(t, jetstream.TERM, result.Decision)
	require.Error(t, result.Err)
	require.Contains(t, result.Err.Error(), "ETH-USDT-SPOT")
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
			now := time.UnixMilli(1_700_000_000_000).UTC()

			result := HandleTarget(context.Background(), logicalTargetDelivery(
				t, now, "target-1", "runner-1", "logical-1", 1,
				[]*tradeeventpb.InstrumentTarget{{
					InstrumentId: "BTC-USDT-SPOT", Quantity: "1",
				}},
			), TargetOptions{Store: tradeStore, Now: func() time.Time { return now }})

			require.Equal(t, test.decision, result.Decision)
			require.Error(t, result.Err)
		})
	}
}

func TestHandleLogicalAccountTargetDoesNotWakeForExactRetry(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	var wakes atomic.Int32
	now := time.UnixMilli(1_700_000_000_000).UTC()
	delivery := logicalTargetDelivery(
		t, now, "target-1", "runner-1", "logical-1", 1, nil,
	)
	opts := TargetOptions{
		Store: tradeStore, Wake: func() { wakes.Add(1) },
		Now: func() time.Time { return now },
	}

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
	result := HandleTarget(context.Background(), &jetstream.Delivery{
		RawData: []byte("malformed"), ContentType: events.ContentType,
	}, TargetOptions{Store: openTargetStore(t)})
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

func seedLogicalTargetAccount(
	t *testing.T,
	tradeStore *store.Store,
	ready bool,
	withInstrument bool,
) {
	t.Helper()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateExchangeAccount(store.ExchangeAccountRecord{
			SpaceID: "space-1", ExchangeAccountID: "account-1", Name: "main",
			Exchange: "BINANCE", MarketType: "SPOT", ExecutionMode: "PAPER",
			Environment: "PAPER", SettlementAsset: "USDT",
			Status: "ENABLED", Ready: ready,
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			OwnerRunnerID: "runner-1", ExecutionMode: "PAPER",
			MarketType: "SPOT", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			ExchangeAccountID: "account-1", Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		if !withInstrument {
			return nil
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", Symbol: "BTCUSDT",
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
	targets []*tradeeventpb.InstrumentTarget,
) *jetstream.Delivery {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	encoded, err := registry.Encode(
		events.LogicalAccountTargetRequested,
		&tradeeventpb.LogicalAccountTargetRequested{
			TargetId: targetID, RunnerId: runnerID,
			LogicalAccountId: logicalAccountID,
			CommandSequence:  sequence, Targets: targets,
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
