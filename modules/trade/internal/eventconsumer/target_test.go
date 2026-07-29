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

func TestHandleTargetAcceptsLatestSequenceAndWakesOnce(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedTargetAccount(t, tradeStore)
	var wakes atomic.Int32
	now := time.Now().UTC()
	opts := TargetOptions{
		Store: tradeStore, Wake: func() { wakes.Add(1) },
		Now: func() time.Time { return now },
	}

	first := targetDelivery(t, now, "execution-1", 2, "2")
	result := HandleTarget(context.Background(), first, opts)
	require.Equal(t, jetstream.ACK, result.Decision)
	require.NoError(t, result.Err)
	require.Equal(t, int32(1), wakes.Load())

	stale := targetDelivery(t, now, "execution-stale", 1, "9")
	result = HandleTarget(context.Background(), stale, opts)
	require.Equal(t, jetstream.ACK, result.Decision)
	require.NoError(t, result.Err)
	require.Equal(t, int32(1), wakes.Load())

	got, err := tradeStore.GetTargetExecutionByBinding(
		context.Background(),
		"space-1",
		"binding-1",
	)
	require.NoError(t, err)
	require.Equal(t, "execution-1", got.ExecutionID)
	require.Equal(t, uint64(2), got.CommandSequence)
	require.Equal(t, "2", got.Targets[0].TargetQuantity)
	require.Equal(t, "RUNNING", got.Status)
}

func TestHandleTargetTermsUnsupportedInstrument(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedTargetAccount(t, tradeStore)
	now := time.Now().UTC()
	delivery := targetDelivery(t, now, "execution-1", 1, "1")
	var message events.EventMessage
	require.NoError(t, proto.Unmarshal(delivery.RawData, &message))
	var payload tradeeventpb.TargetIntent
	require.NoError(t, proto.Unmarshal(message.Payload, &payload))
	payload.Targets[0].InstrumentId = "other"
	var err error
	message.Payload, err = proto.Marshal(&payload)
	require.NoError(t, err)
	delivery.RawData, err = proto.Marshal(&message)
	require.NoError(t, err)

	result := HandleTarget(context.Background(), delivery, TargetOptions{
		Store: tradeStore, Now: func() time.Time { return now },
	})

	require.Equal(t, jetstream.TERM, result.Decision)
	require.Error(t, result.Err)
}

func TestHandleTargetRetriesMissingMetadataUntilAccountReady(t *testing.T) {
	tests := []struct {
		name     string
		ready    bool
		decision jetstream.HandlerDecision
	}{
		{"startup", false, jetstream.RETRY},
		{"unsupported after ready", true, jetstream.TERM},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tradeStore := openTargetStore(t)
			seedTargetAccountOnly(t, tradeStore, test.ready)
			now := time.Now().UTC()

			result := HandleTarget(
				context.Background(),
				targetDelivery(t, now, "execution-1", 1, "1"),
				TargetOptions{Store: tradeStore, Now: func() time.Time { return now }},
			)

			require.Equal(t, test.decision, result.Decision)
			require.Error(t, result.Err)
		})
	}
}

func openTargetStore(t *testing.T) *store.Store {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	return tradeStore
}

func seedTargetAccount(t *testing.T, tradeStore *store.Store) {
	t.Helper()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := createTargetAccount(tx, false); err != nil {
			return err
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", Symbol: "BTC-USDT",
			InstrumentID: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
			ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001",
			PriceTick: "0.1", Status: "TRADING",
		})
	}))
}

func seedTargetAccountOnly(
	t *testing.T,
	tradeStore *store.Store,
	ready bool,
) {
	t.Helper()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return createTargetAccount(tx, ready)
	}))
}

func createTargetAccount(tx *store.Tx, ready bool) error {
	return tx.CreateExchangeAccount(store.ExchangeAccountRecord{
		SpaceID: "space-1", ExchangeAccountID: "account-1", Name: "main",
		Exchange: "BINANCE", MarketType: "SPOT", ExecutionMode: "PAPER",
		CredentialSecretID: "secret-1", SettlementAsset: "USDT",
		Status: "ENABLED", Ready: ready,
	})
}

func targetDelivery(
	t *testing.T,
	now time.Time,
	executionID string,
	sequence uint64,
	quantity string,
) *jetstream.Delivery {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	encoded, err := registry.Encode(
		events.TradeTargetRequested,
		&tradeeventpb.TargetIntent{
			ExecutionId: executionID, StrategyRunId: "run-1",
			ExecutionBindingId: "binding-1", ExchangeAccountId: "account-1",
			DataRevision: "revision-1", CommandSequence: sequence,
			NotAfterUnixMs: now.Add(time.Minute).UnixMilli(),
			Targets: []*tradeeventpb.TargetPosition{{
				InstrumentId: "BTC-USDT", Symbol: "BTC-USDT",
				TargetQuantity: quantity,
			}},
		},
		events.PublishOptions{
			EventID: executionID, OccurredAt: now,
			SpaceID: "space-1", SubjectID: "binding-1",
		},
	)
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	return &jetstream.Delivery{
		RawData: raw, Subject: encoded.Subject,
		RawMessageID: executionID, ContentType: events.ContentType,
	}
}
