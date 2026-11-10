package accountsync

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func isolatedMixedPositions() []exchange.Position {
	return []exchange.Position{
		{Market: exchange.MarketTypeSwap, ExchangeSymbol: "BTCUSDT", SignedQuantity: shared.MustDecimal("1"), MarginMode: exchange.MarginModeCross},
		{Market: exchange.MarketTypeSwap, ExchangeSymbol: "ETHUSDT", SignedQuantity: shared.MustDecimal("2"), MarginMode: exchange.MarginModeIsolated},
	}
}

// 快照里有逐仓持仓：逐仓的不入投影，其余照常返回，组合暂停，原因里写明标的。
func TestFilterIsolatedPositionsPausesLogicalAccountWithSymbol(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	setLogicalAccountAutomation(t, tradeStore, "ACTIVE")
	service := &Service{Store: tradeStore}

	cross, err := service.FilterIsolatedPositions(context.Background(), "account-1", isolatedMixedPositions())
	require.NoError(t, err)
	require.Len(t, cross, 1)
	require.Equal(t, "BTCUSDT", cross[0].ExchangeSymbol)

	logical, err := tradeStore.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "PAUSED", logical.AutomationState)
	require.True(t, store.IsAutoPauseReason(logical.PauseReason))
	require.Contains(t, logical.PauseReason, "ETHUSDT")
	require.Contains(t, logical.PauseReason, "逐仓")
}

// 私有流的逐仓持仓事件同样暂停组合，不动账户就绪状态。
func TestApplyIsolatedPositionEventPausesLogicalAccount(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	setLogicalAccountAutomation(t, tradeStore, "ACTIVE")
	service := &Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills: &consumer.Reducer{Store: tradeStore},
	}

	require.NoError(t, service.ApplyPosition(context.Background(), "account-1", isolatedMixedPositions()[1]))

	logical, err := tradeStore.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "PAUSED", logical.AutomationState)
	require.Contains(t, logical.PauseReason, "ETHUSDT")
}

// 没有逐仓持仓不动组合；已经暂停的组合不改写暂停原因（可能是风控或用户暂停）。
func TestFilterIsolatedPositionsLeavesOtherStatesAlone(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	setLogicalAccountAutomation(t, tradeStore, "ACTIVE")
	service := &Service{Store: tradeStore}

	cross, err := service.FilterIsolatedPositions(context.Background(), "account-1", isolatedMixedPositions()[:1])
	require.NoError(t, err)
	require.Len(t, cross, 1)
	requireLogicalAccountRemainsActive(t, tradeStore)

	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "PAUSED", "用户手动暂停")
	}))
	_, err = service.FilterIsolatedPositions(context.Background(), "account-1", isolatedMixedPositions())
	require.NoError(t, err)
	logical, err := tradeStore.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "用户手动暂停", logical.PauseReason)
}

// 带逐仓持仓的交易所账户同步：同步成功，全仓持仓入库，逐仓持仓不入库，组合暂停且原因里有标的名。
type isolatedPositionAdapter struct{ *syncAdapter }

func (isolatedPositionAdapter) ListOpenOrders(context.Context) ([]exchange.Order, error) {
	return nil, nil
}

func (isolatedPositionAdapter) ListRecentFills(context.Context, exchange.SymbolKey, string) ([]exchange.Fill, string, error) {
	return nil, "", nil
}

func (a isolatedPositionAdapter) ListPositionSnapshots(ctx context.Context) ([]exchange.Position, error) {
	positions, err := a.syncAdapter.ListPositionSnapshots(ctx)
	return append(positions, exchange.Position{
		Market: exchange.MarketTypeSwap, ExchangeSymbol: "ETHUSDT", PositionSide: exchange.PositionSideNet,
		SignedQuantity: shared.MustDecimal("2"), MarginMode: exchange.MarginModeIsolated,
	}), err
}

func TestSyncWithIsolatedPositionKeepsCrossPositionsAndPausesLogicalAccount(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	setLogicalAccountAutomation(t, tradeStore, "ACTIVE")
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: isolatedPositionAdapter{&syncAdapter{}}},
		SessionState: readySessionState(true),
		Fills:        &consumer.Reducer{Store: tradeStore},
	}

	_, err := service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)

	positions, err := tradeStore.ListPositions(context.Background(), "space-1", "account-1", exchange.SymbolKey{})
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.Equal(t, "BTC-USDT", positions[0].ExchangeSymbol)
	logical, err := tradeStore.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "PAUSED", logical.AutomationState)
	require.Contains(t, logical.PauseReason, "ETHUSDT")
}
