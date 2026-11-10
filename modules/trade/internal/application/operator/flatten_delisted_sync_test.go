package operator

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/accountsync"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/execution/adaptertest"
	"github.com/stretchr/testify/require"
)

// realSyncer 和生产里 bootstrap 的 accountSyncer 一样包装真实的 accountsync.Service：清仓的轻量同步、撤单后的同步都走真实同步。
type realSyncer struct{ service *accountsync.Service }

func (s realSyncer) SyncAccount(ctx context.Context, accountID string) error {
	_, err := s.service.SyncAccount(ctx, accountID)
	return err
}

func (s realSyncer) SyncAccountLight(ctx context.Context, accountID string) error {
	_, err := s.service.SyncAccountLight(ctx, accountID)
	return err
}

// delistedSwapAdapter 模拟合约 BTCUSDT 已下架的交易所：合约上的查单、查成交一律回 -1121，交易所也已经没有这笔合约持仓；
// 现货 BTCUSDT 照常交易，钱包里还有 balances。
type delistedSwapAdapter struct {
	adaptertest.Stub
	now      func() time.Time
	balances []exchange.AssetBalance
}

func (a *delistedSwapAdapter) Exchange() exchange.Exchange { return exchange.ExchangeBinance }
func (a *delistedSwapAdapter) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	return exchange.AccountSnapshot{
		AvailableFunds: shared.MustDecimal("1000"), Equity: shared.MustDecimal("1200"),
		Balances: a.balances, ExchangeUpdatedAt: a.now(),
	}, nil
}
func (*delistedSwapAdapter) ListPositionSnapshots(context.Context) ([]exchange.Position, error) {
	return nil, nil
}
func (*delistedSwapAdapter) ListOpenOrders(context.Context) ([]exchange.Order, error) {
	return nil, nil
}
func (*delistedSwapAdapter) ListRecentFills(_ context.Context, key exchange.SymbolKey, cursor string) ([]exchange.Fill, string, error) {
	if key.Market == exchange.MarketTypeSwap {
		return nil, cursor, &exchange.Error{Kind: exchange.ErrorRejected, HTTPStatus: 400, Code: exchange.CodeBinanceInvalidSymbol}
	}
	return nil, cursor, nil
}
func (*delistedSwapAdapter) GetOrder(_ context.Context, key exchange.SymbolKey, _ string) (exchange.Order, error) {
	if key.Market == exchange.MarketTypeSwap {
		return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorRejected, HTTPStatus: 400, Code: exchange.CodeBinanceInvalidSymbol}
	}
	return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorOrderNotFound, Code: "-2013"}
}
func (a *delistedSwapAdapter) GetOrderByExchangeID(ctx context.Context, key exchange.SymbolKey, _ string) (exchange.Order, error) {
	return a.GetOrder(ctx, key, "")
}
func (*delistedSwapAdapter) PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (*delistedSwapAdapter) CancelOrder(context.Context, exchange.SymbolKey, string) (exchange.Order, error) {
	return exchange.Order{}, nil
}

type fixtureAdapters map[string]execution.ExecutionAdapter

func (a fixtureAdapters) Adapter(accountID string) (execution.ExecutionAdapter, error) {
	return a[accountID], nil
}

// 下架清仓最后一笔 IOC 结果未知、标的随后下架，收尾放行后这笔子单停在 SUBMIT_UNKNOWN：之后的整组清仓、减仓接真实同步，
// 撤它时交易所回“标的不存在”不算撤单失败，撤单后的同步把它按过期结束（写明需人工核对），账户保持就绪，清仓、减仓照常下单。
func TestLaterFlattenWithRealSyncClosesDelistedLeftover(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ratio  string
		placed []string
	}{
		{"整组清仓", "", []string{"BTC-USDT-SPOT:SELL:2"}},
		{"减仓", "0.5", []string{"BTC-USDT-SPOT:SELL:1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newMixedFlattenFixture(t)
			gone := &orderapp.AccountExecutionError{TradingAccountID: "account-a", Operation: "get_order",
				Err: &exchange.Error{Kind: exchange.ErrorRejected, Code: exchange.CodeBinanceInvalidSymbol}}
			service, command := startFinishingWithUnknownChild(t, fixture, "delist-gone", "child-gone", gone)
			clock := func() time.Time { return fixture.now }
			service.Syncer = realSyncer{service: &accountsync.Service{
				Store: fixture.store, Fills: &consumer.Reducer{Store: fixture.store}, Now: clock,
				Adapters: fixtureAdapters{
					"account-a": &delistedSwapAdapter{now: clock, balances: []exchange.AssetBalance{
						{Wallet: exchange.WalletUnified, Asset: "USDT", Available: shared.MustDecimal("1000"), Total: shared.MustDecimal("1000")},
						{Wallet: exchange.WalletUnified, Asset: "BTC", Available: shared.MustDecimal("2"), Total: shared.MustDecimal("2")},
					}},
					"account-b": &delistedSwapAdapter{now: clock, balances: []exchange.AssetBalance{
						{Wallet: exchange.WalletUnified, Asset: "USDT", Available: shared.MustDecimal("100000"), Total: shared.MustDecimal("100000")},
					}},
				},
			}}
			finished, err := service.FlattenLogicalAccount(context.Background(), command)
			require.NoError(t, err)
			require.Equal(t, string(operatordomain.StatusPartial), finished.Action.Status)
			require.Equal(t, []string{"child-gone"}, finished.ManualCheckOrderIDs)
			require.Equal(t, string(orderdomain.SubmitUnknown), childState(t, fixture, "child-gone"), "清仓要在遗留单还没结束时发起")

			fixture.orders.nextID = ""
			later := flattenCommand("later-" + tc.name)
			if tc.ratio != "" {
				later.ReduceRatio = shared.MustDecimal(tc.ratio)
			}
			result, err := service.FlattenLogicalAccount(context.Background(), later)
			require.NoError(t, err)
			require.Equal(t, tc.placed, placed(fixture), "遗留单不能挡住撤单确认，清仓、减仓要照常下单")
			require.NotContains(t, result.Accounts[0].Error, ErrCancelUnconfirmed.Error())

			child, err := fixture.store.GetOrder(context.Background(), "space-1", "child-gone")
			require.NoError(t, err)
			require.Equal(t, "EXPIRED", child.State)
			require.Equal(t, accountsync.InstrumentGoneReason, child.RejectReason)
			account, err := fixture.store.GetTradingAccountByID(context.Background(), "account-a")
			require.NoError(t, err)
			require.True(t, account.Ready, "同步不能因为下架标的的遗留单失败")
		})
	}
}
