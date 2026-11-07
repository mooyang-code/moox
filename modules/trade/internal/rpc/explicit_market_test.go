package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/require"
)

func openDualMarketAccount(t *testing.T) *store.Store {
	t.Helper()
	tradeStore := openRPCStore(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "dual-1", Name: "dual", Exchange: "BINANCE", Markets: []string{"SPOT", "SWAP"},
			ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: "USDT", MarginMode: "CROSS", Status: "ENABLED",
		})
	}))
	return tradeStore
}

// 人工下单请求显式带市场：账户开通多个市场时，同名的 BTC-USDT 按请求的市场规范化，不再要求手写后缀；
// 账户没开通的市场被拒绝。
func TestRequestInstrumentIDUsesExplicitMarket(t *testing.T) {
	ctx := context.Background()
	handler := &ExecutionServer{Store: openDualMarketAccount(t)}

	got, err := handler.requestInstrumentID(ctx, "space-1", "dual-1", "BTC-USDT", tradepb.MarketType_MARKET_TYPE_SWAP)
	require.NoError(t, err)
	require.Equal(t, "BTC-USDT-SWAP", got)
	got, err = handler.requestInstrumentID(ctx, "space-1", "dual-1", "BTC-USDT", tradepb.MarketType_MARKET_TYPE_SPOT)
	require.NoError(t, err)
	require.Equal(t, "BTC-USDT-SPOT", got)

	// 没给市场时回到账户规则：开通多个市场必须带后缀。
	_, err = handler.requestInstrumentID(ctx, "space-1", "dual-1", "BTC-USDT", tradepb.MarketType_MARKET_TYPE_UNSPECIFIED)
	require.ErrorIs(t, err, exchange.ErrInvalidInstrumentID)
	got, err = handler.requestInstrumentID(ctx, "space-1", "dual-1", "BTC-USDT-SWAP", tradepb.MarketType_MARKET_TYPE_UNSPECIFIED)
	require.NoError(t, err)
	require.Equal(t, "BTC-USDT-SWAP", got)

	// 请求的市场账户没开通，或后缀与请求的市场矛盾。
	_, err = handler.requestInstrumentID(ctx, "space-1", "dual-1", "600519", tradepb.MarketType_MARKET_TYPE_STOCK)
	require.ErrorIs(t, err, exchange.ErrInvalidInstrumentID)
	_, err = handler.requestInstrumentID(ctx, "space-1", "dual-1", "BTC-USDT-SWAP", tradepb.MarketType_MARKET_TYPE_SPOT)
	require.Error(t, err)
}

type marketLedgerAdapter struct {
	execution.ExecutionAdapter
	seen exchange.MarketType
}

func (a *marketLedgerAdapter) LedgerSupported() bool { return true }
func (a *marketLedgerAdapter) ListLedger(_ context.Context, query exchange.LedgerQuery) (exchange.LedgerPage, error) {
	a.seen = query.Market
	return exchange.LedgerPage{Entries: []exchange.LedgerEntry{
		{ID: "1", Time: time.UnixMilli(1000), Type: exchange.LedgerFundingFee, Asset: "USDT", Amount: shared.MustDecimal("-1"), ExchangeSymbol: "BTCUSDT"},
		{ID: "2", Time: time.UnixMilli(900), Type: exchange.LedgerTransfer, Asset: "USDT", Amount: shared.MustDecimal("100")},
	}}, nil
}

type singleLedgerSource struct{ adapter execution.ExecutionAdapter }

func (s singleLedgerSource) Adapter(string) (execution.ExecutionAdapter, error) {
	return s.adapter, nil
}

// 资金流水按请求的市场查，返回的每条流水带所属市场。
func TestListAccountLedgerPassesMarketAndStampsEntries(t *testing.T) {
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	adapter := &marketLedgerAdapter{}
	multi, err := execution.NewMultiMarket("BINANCE", []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap}, map[exchange.MarketType]execution.ExecutionAdapter{
		exchange.MarketTypeSpot: &marketLedgerAdapter{}, exchange.MarketTypeSwap: adapter,
	})
	require.NoError(t, err)
	handler := &ConsoleServer{Store: openDualMarketAccount(t), Adapters: singleLedgerSource{adapter: multi}}

	rsp, err := handler.ListAccountLedger(ctx, &tradepb.ListAccountLedgerReq{
		TradingAccountId: "dual-1", MarketType: tradepb.MarketType_MARKET_TYPE_SWAP,
	})

	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().String())
	require.Equal(t, exchange.MarketTypeSwap, adapter.seen)
	require.Len(t, rsp.GetEntries(), 2)
	require.Equal(t, tradepb.MarketType_MARKET_TYPE_SWAP, rsp.GetEntries()[0].GetMarketType())
	require.Equal(t, tradepb.MarketType_MARKET_TYPE_UNSPECIFIED, rsp.GetEntries()[1].GetMarketType(), "划转是账户级流水，不属于某个市场")

	// 账户没开通的市场被拒绝。
	rejected, err := handler.ListAccountLedger(ctx, &tradepb.ListAccountLedgerReq{
		TradingAccountId: "dual-1", MarketType: tradepb.MarketType_MARKET_TYPE_STOCK,
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_INVALID_PARAM, rejected.GetRetInfo().GetCode())

	// 市场枚举不合法在校验里就被拒绝。
	require.Error(t, (&tradepb.ListAccountLedgerReq{TradingAccountId: "dual-1", MarketType: tradepb.MarketType(99)}).Validate())
}
