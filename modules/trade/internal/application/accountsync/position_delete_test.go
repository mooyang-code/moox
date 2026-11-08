package accountsync

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 现货钱包几天没变动，合并后的账户快照时间很旧；合约仓在交易所被强平、ADL 或手工平掉后，
// 本地持仓要按持仓读取时间删除，不能被旧的账户快照时间挡住。
func TestClosedSwapPositionIsDeletedEvenWhenSpotWalletIsStale(t *testing.T) {
	ctx := context.Background()
	service, tradeStore := newDualMarketSync(t)
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", PositionSide: "NET",
			SignedQuantity: "3", EntryPrice: "100", MarkPrice: "100", Leverage: "5", MarginMode: "CROSS", ExchangeUpdatedAt: 10_000,
		})
	}))

	_, err := service.applySnapshot(ctx, "account-1", Snapshot{
		Ready: true,
		Account: exchange.AccountSnapshot{
			ExchangeUpdatedAt: time.UnixMilli(1_000), // 现货钱包很久没变动：合并后取最早的时间
			Present:           exchange.AccountSnapshotPresence{Balances: true},
		},
		AccountReadAt:   time.UnixMilli(1_000_000),
		PositionsReadAt: time.UnixMilli(1_000_000), // 持仓是刚读的，交易所已经没有这个仓位
	})
	require.NoError(t, err)

	positions, err := tradeStore.ListPositions(ctx, "space-1", "account-1", exchange.SymbolKey{})
	require.NoError(t, err)
	require.Empty(t, positions, "已被交易所平掉的合约仓要从本地删除")
}
