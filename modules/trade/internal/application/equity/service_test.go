package equity

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

type equityTestAdapter struct {
	execution.ExecutionAdapter
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

type equityTestAdapters struct{ adapter execution.ExecutionAdapter }

func (a equityTestAdapters) Adapter(string) (execution.ExecutionAdapter, error) {
	return a.adapter, nil
}

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
		instruments: []exchange.Instrument{{MarketType: exchange.MarketTypeSpot, ExchangeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT"}},
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
		t.Fatalf("quote symbol = %q, want instrument ExchangeSymbol", adapter.symbol)
	}
	logicalPoints, err := tradeStore.ListLogicalAccountEquityPoints(context.Background(), "space-1", "logical-1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(logicalPoints) != 1 || logicalPoints[0].Equity != "1000" {
		t.Fatalf("logical equity points = %+v", logicalPoints)
	}
}

func TestSampleLogicalAccountCountsSharedLiveCredentialOnce(t *testing.T) {
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tradeStore.Close()
	const sourceMillis = int64(1_700_000_000_000)
	if err := tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		for _, id := range []string{"account-1", "account-2"} {
			if err := tx.CreateTradingAccount(store.TradingAccountRecord{
				SpaceID: "space-1", TradingAccountID: id, Name: id,
				Exchange: string(exchange.ExchangeBinance), MarketType: string(exchange.MarketTypeSwap),
				ExecutionMode: string(exchange.ExecutionModeLive), Environment: string(exchange.AccountEnvironmentTestnet), CredentialSecretID: "shared-secret", SettlementAsset: "USDT",
				Status: string(exchange.AccountStatusEnabled), Ready: true, Snapshot: store.TradingAccountSnapshot{Equity: "1000", AvailableFunds: "1000", ExchangeUpdatedAt: sourceMillis},
			}); err != nil {
				return err
			}
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical", ExecutionMode: string(exchange.ExecutionModeLive), MarketType: string(exchange.MarketTypeSwap), SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "test"}); err != nil {
			return err
		}
		for _, id := range []string{"account-1", "account-2"} {
			if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: id, Enabled: true}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: tradeStore, Now: func() time.Time { return time.UnixMilli(sourceMillis + 1) }}
	if err := service.SampleAccount(context.Background(), "account-1"); err != nil {
		t.Fatal(err)
	}
	points, err := tradeStore.ListLogicalAccountEquityPoints(context.Background(), "space-1", "logical-1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].Equity != "1000" {
		t.Fatalf("logical equity points = %+v, want one shared-capital snapshot", points)
	}
}

// 交易所给的余额时间是“最后变更时间”：余额长期不变时它很旧，但完整同步持续成功（LastSyncAt 持续推进）就说明余额仍然准确，
// 权益不能因此被判为过期；权益有效期也要覆盖采样周期，让两次采样之间的策略目标仍能转换。
func TestEquityFreshnessFollowsConfirmedSyncNotBalanceChangeTime(t *testing.T) {
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tradeStore.Close()

	const syncedAt = int64(1_700_000_000_000)
	changedAt := syncedAt - int64(10*time.Minute/time.Millisecond) // 余额 10 分钟前最后一次变更
	if err := tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", Name: "live",
			Exchange: string(exchange.ExchangeBinance), MarketType: string(exchange.MarketTypeSpot),
			ExecutionMode: string(exchange.ExecutionModeLive), Environment: string(exchange.AccountEnvironmentTestnet), CredentialSecretID: "secret-1", SettlementAsset: "USDT",
			Status: string(exchange.AccountStatusEnabled), Ready: true, LastSyncAt: syncedAt,
			Snapshot: store.TradingAccountSnapshot{
				Balances:          []store.AssetBalance{{Asset: "USDT", Available: "500", Total: "500"}},
				ExchangeUpdatedAt: changedAt,
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
	now := time.UnixMilli(syncedAt + 40_000) // 上一次采样之后 40 秒，已超过旧的 30 秒有效期
	service := &Service{Store: tradeStore, Now: func() time.Time { return now }}
	if err := service.SampleAccount(context.Background(), "account-1"); err != nil {
		t.Fatal(err)
	}
	equity, sourceTime, err := service.ResolveLogicalAccountEquity(context.Background(), "space-1", "logical-1")
	if err != nil {
		t.Fatalf("equity must stay usable while full syncs keep confirming the balance: %v", err)
	}
	if equity.String() != "500" || sourceTime != syncedAt {
		t.Fatalf("equity = %s source = %d", equity.String(), sourceTime)
	}
}
