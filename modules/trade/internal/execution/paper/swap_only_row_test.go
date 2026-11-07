package paper

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type swapOnlyFacts struct{ twoMarketFacts }

func (swapOnlyFacts) GetTradingAccountByID(context.Context, string) (store.TradingAccountRecord, error) {
	return store.TradingAccountRecord{
		SpaceID: "s", TradingAccountID: "a", Exchange: "BINANCE", Markets: []string{"SWAP"}, SettlementAsset: "USDT",
	}, nil
}

// 只开合约的模拟账户没有现货买单：结算币那一行保持“总额 - 冻结”，不被改成资金池口径，管理台显示不变。
func TestSwapOnlyPaperSettlementRowIsNotRewritten(t *testing.T) {
	now := time.UnixMilli(1_000_000).UTC()
	market := &twoMarketPrices{seen: map[exchange.SymbolKey]int{}, at: now}
	adapter := &Adapter{
		Store: swapOnlyFacts{twoMarketFacts{positions: []store.PositionRecord{{
			MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", SignedQuantity: "3", EntryPrice: "100", Leverage: "5",
		}}}},
		MarketData: market, Now: func() time.Time { return now },
	}

	snapshot, err := adapter.GetAccountSnapshot(context.Background())

	require.NoError(t, err)
	for _, balance := range snapshot.Balances {
		if balance.Asset == "USDT" {
			require.Zero(t, balance.Available.Cmp(shared.MustDecimal("990")), "总额 1000 - 冻结 10：%s", balance.Available)
		}
	}
	require.Zero(t, snapshot.AvailableFunds.Cmp(shared.MustDecimal("954")), "账户级可用资金含浮盈、扣保证金")
}
