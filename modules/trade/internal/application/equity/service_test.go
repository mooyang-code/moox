package equity

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

type equityTestAdapter struct {
	exchange.Adapter
	instruments []exchange.Instrument
	price       exchange.ReferencePrice
	symbol      string
}

func (a *equityTestAdapter) LoadInstruments(context.Context) ([]exchange.Instrument, error) {
	return a.instruments, nil
}

func (a *equityTestAdapter) GetReferencePrice(_ context.Context, symbol string) (exchange.ReferencePrice, error) {
	a.symbol = symbol
	return a.price, nil
}

type equityTestAdapters struct{ adapter exchange.Adapter }

func (a equityTestAdapters) Adapter(string) (exchange.Adapter, error) { return a.adapter, nil }

func TestSampleAccountValuesLiveSpotBalancesWhenSnapshotHasNoEquity(t *testing.T) {
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tradeStore.Close()

	const sourceMillis = int64(1_700_000_000_000)
	if err := tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", Name: "live",
			Exchange: string(exchange.ExchangeBinance), MarketType: string(exchange.MarketTypeSpot),
			ExecutionMode: string(exchange.ExecutionModeLive), Environment: string(exchange.AccountEnvironmentTestnet), CredentialSecretID: "secret-1", SettlementAsset: "USDT",
			Status: string(exchange.AccountStatusEnabled), Ready: true, Snapshot: store.TradingAccountSnapshot{
				Balances: []store.AssetBalance{
					{Asset: "USDT", Available: "500", Total: "500"},
					{Asset: "BTC", Available: "0.01", Total: "0.01"},
				}, ExchangeUpdatedAt: sourceMillis,
			},
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			ExecutionMode: string(exchange.ExecutionModeLive), MarketType: string(exchange.MarketTypeSpot),
			SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "test",
		}); err != nil {
			return err
		}
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: true,
		})
	}); err != nil {
		t.Fatal(err)
	}
	adapter := &equityTestAdapter{
		instruments: []exchange.Instrument{{MarketType: exchange.MarketTypeSpot, Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT"}},
		price:       exchange.ReferencePrice{Price: shared.MustDecimal("50000"), UpdatedAt: time.UnixMilli(sourceMillis + 1)},
	}
	service := &Service{Store: tradeStore, Adapters: equityTestAdapters{adapter: adapter}, Now: func() time.Time { return time.UnixMilli(sourceMillis + 2) }}
	if err := service.SampleAccount(context.Background(), "account-1"); err != nil {
		t.Fatal(err)
	}
	points, err := tradeStore.ListAccountEquityPoints(context.Background(), "space-1", "account-1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].Equity != "1000" || points[0].AvailableFunds != "500" {
		t.Fatalf("equity points = %+v", points)
	}
	if adapter.symbol != "BTCUSDT" {
		t.Fatalf("quote symbol = %q, want legacy instrument Symbol", adapter.symbol)
	}
	logicalPoints, err := tradeStore.ListLogicalAccountEquityPoints(context.Background(), "space-1", "logical-1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(logicalPoints) != 1 || logicalPoints[0].Equity != "1000" {
		t.Fatalf("logical equity points = %+v", logicalPoints)
	}
}
