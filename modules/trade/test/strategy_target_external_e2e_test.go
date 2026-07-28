//go:build e2e_external

package test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/stretchr/testify/require"
)

func TestExternalStrategyTargetIntentIsConsumedIntoTradeStore(t *testing.T) {
	natsURL := os.Getenv("MOOX_STRATEGY_TRADE_E2E_NATS_URL")
	require.NotEmpty(t, natsURL)
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateExchangeAccount(store.ExchangeAccountRecord{
			SpaceID: "space", ExchangeAccountID: "acct", Name: "external-e2e",
			Exchange: "BINANCE", MarketType: "SPOT", ExecutionMode: "PAPER",
			SettlementAsset: "USDT", Status: "ENABLED", Ready: true,
		}); err != nil {
			return err
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", Symbol: "BTC-USDT",
			InstrumentID: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettlementAsset: "USDT", ExchangeQuantityStep: "0.001",
			MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
		})
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := jetstream.Connect(ctx, jetstream.Config{
		URLs: []string{natsURL}, Name: "strategy-trade-e2e-consumer",
	})
	require.NoError(t, err)
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		runErr = eventconsumer.RunTarget(ctx, eventconsumer.TargetOptions{
			Client: client, ConsumerName: "strategy-trade-external-e2e",
			Store: tradeStore,
		})
	}()
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			cancel()
			<-done
			client.Close()
			_ = tradeStore.Close()
		})
	}
	t.Cleanup(cleanup)

	require.Eventually(t, func() bool {
		current, getErr := tradeStore.GetTargetExecutionByBinding(
			context.Background(),
			"space",
			"execution-e2e",
		)
		return getErr == nil &&
			current.ExecutionID == "strategy-e2e-run:rebalance:execution-e2e" &&
			current.Targets[0].TargetQuantity == "0.5"
	}, 8*time.Second, 50*time.Millisecond)
	cancel()
	<-done
	require.ErrorIs(t, runErr, context.Canceled)
	cleanup()
}
