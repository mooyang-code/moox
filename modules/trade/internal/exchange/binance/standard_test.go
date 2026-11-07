package binance

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/stretchr/testify/require"
)

// 普通账户（现货 api/v3 + 合约 fapi）的接口样例测试：响应用官方样例（来源见 testdata/README.md），
// 官方样例里合约订单的 positionSide 是 SHORT（双向持仓），单向持仓的账户收到的是 BOTH，改样例时只改这一个字段。

func TestStandardAccountOrdersUseOfficialSampleShapes(t *testing.T) {
	futuresNew := withFields(t, "fapi_order_new.json", map[string]any{"positionSide": "BOTH", "type": "MARKET"})
	futuresQuery := withFields(t, "fapi_order_query.json", map[string]any{"positionSide": "BOTH"})
	state, server := newPMServer(t, map[string]string{
		"POST /api/v3/order":      samplePath(t, "spot_order_new_result.json"),
		"POST /fapi/v1/order":     futuresNew,
		"GET /fapi/v1/order":      futuresQuery,
		"DELETE /api/v3/order":    samplePath(t, "gct_spot_cancel_order.json"),
		"GET /api/v3/openOrders":  samplePath(t, "gct_spot_open_orders.json"),
		"GET /fapi/v1/openOrders": `[` + futuresQuery + `]`,
	})
	spot, swap := testAdapter(exchange.MarketTypeSpot, server.URL), testAdapter(exchange.MarketTypeSwap, server.URL)
	ctx := context.Background()

	placed, err := spot.PlaceOrder(ctx, marketOrderRequest(exchange.MarketTypeSpot, "BTCUSDT"))
	require.NoError(t, err)
	require.Equal(t, exchange.MarketTypeSpot, placed.Market)
	require.Equal(t, exchange.OrderStatusFilled, placed.Status)
	require.Equal(t, "28", placed.ExchangeOrderID)
	sent := state.find(http.MethodPost, "/api/v3/order")
	require.Len(t, sent, 1)
	require.Empty(t, sent[0].Query.Get("sideEffectType"), "普通账户的现货下单没有借币参数")
	require.Equal(t, "RESULT", sent[0].Query.Get("newOrderRespType"))

	request := marketOrderRequest(exchange.MarketTypeSwap, "BTCUSDT")
	request.ReduceOnly = true
	placedSwap, err := swap.PlaceOrder(ctx, request)
	require.NoError(t, err)
	require.Equal(t, exchange.MarketTypeSwap, placedSwap.Market)
	require.Equal(t, "BOTH", state.find(http.MethodPost, "/fapi/v1/order")[0].Query.Get("positionSide"))

	queried, err := swap.GetOrder(ctx, exchange.NewSymbolKey(exchange.MarketTypeSwap, "BTCUSDT"), "abc")
	require.NoError(t, err)
	require.Equal(t, exchange.OrderStatusOpen, queried.Status)

	cancelled, err := spot.CancelOrder(ctx, exchange.NewSymbolKey(exchange.MarketTypeSpot, "LTCBTC"), "myOrder1")
	require.NoError(t, err)
	require.Equal(t, "myOrder1", cancelled.ClientOrderID)
	require.Equal(t, exchange.OrderStatusPartiallyCanceled, cancelled.Status)

	openSpot, err := spot.ListOpenOrders(ctx)
	require.NoError(t, err)
	require.Len(t, openSpot, 1)
	openSwap, err := swap.ListOpenOrders(ctx)
	require.NoError(t, err)
	require.Len(t, openSwap, 1)
	require.NotEqual(t, openSpot[0].Market, openSwap[0].Market)
}

// 官方样例里合约订单 positionSide 是 SHORT（双向持仓）：MooX 只支持单向，必须拒绝而不是当成单向处理。
func TestStandardSwapOrderWithHedgePositionSideIsRejected(t *testing.T) {
	_, server := newPMServer(t, map[string]string{"GET /fapi/v1/order": samplePath(t, "fapi_order_query.json")})
	_, err := testAdapter(exchange.MarketTypeSwap, server.URL).GetOrder(context.Background(), exchange.NewSymbolKey(exchange.MarketTypeSwap, "BTCUSDT"), "abc")
	require.True(t, exchange.IsKind(err, exchange.ErrorRejected), "%v", err)
}

func TestStandardSwapPositionRiskUsesOfficialFieldNames(t *testing.T) {
	// 官方 v3 positionRisk 样例：字段名 unRealizedProfit（大写 R）、positionAmt、initialMargin、notional。
	_, server := newPMServer(t, map[string]string{"GET /fapi/v3/positionRisk": samplePath(t, "fapi_position_risk_v3.json")})
	positions, err := testAdapter(exchange.MarketTypeSwap, server.URL).ListPositionSnapshots(context.Background())
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.Equal(t, "ADAUSDT", positions[0].ExchangeSymbol)
	require.Equal(t, "30", positions[0].SignedQuantity.String())
	require.Equal(t, "0.764277", positions[0].UnrealizedPnL.String())
	require.Equal(t, exchange.MarketTypeSwap, positions[0].Market)
	require.Equal(t, "0.764277", sumUnrealizedPnL(positions).String())
}

// 账户快照：现货和合约各记各的钱包，未实现盈亏取持仓接口。
func TestStandardAccountSnapshotsUseSeparateWallets(t *testing.T) {
	_, server := newPMServer(t, map[string]string{
		"GET /api/v3/account":            samplePath(t, "spot_account.json"),
		"GET /fapi/v3/account":           samplePath(t, "fapi_account_v3.json"),
		"GET /fapi/v3/positionRisk":      `[]`,
		"GET /fapi/v1/positionSide/dual": `{"dualSidePosition": false}`,
	})
	spot, err := testAdapter(exchange.MarketTypeSpot, server.URL).GetAccountSnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, exchange.WalletSpot, spot.Balances[0].Wallet)
	require.Equal(t, "4723846.89208129", spot.Balances[0].Available.String())
	swap, err := testAdapter(exchange.MarketTypeSwap, server.URL).GetAccountSnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, exchange.WalletFutures, swap.Balances[0].Wallet)
	// 合约钱包余额取 walletBalance（官方样例 23.72469206），不含未实现盈亏。
	require.Equal(t, "23.72469206", swap.Balances[0].Total.String())
	// 保证金率监控的读数：总权益取 totalMarginBalance，维持保证金取 totalMaintMargin（官方 fapi v3 样例）。
	require.Equal(t, "126.72469206", swap.ExchangeEquity.String())
	require.Equal(t, "0", swap.MaintenanceMargin.String())
	// 可划转额度取 maxWithdrawAmount（官方：Maximum transferable/withdrawable amount），不是 availableBalance。
	require.Equal(t, "23.72469206", swap.Balances[0].Transferable.String())
	var info map[string]any
	require.NoError(t, json.Unmarshal(sample(t, "fapi_account_v3.json"), &info))
	info["assets"].([]any)[0].(map[string]any)["maxWithdrawAmount"] = "5.5"
	changed, err := json.Marshal(info)
	require.NoError(t, err)
	_, other := newPMServer(t, map[string]string{
		"GET /fapi/v3/account": string(changed), "GET /fapi/v3/positionRisk": `[]`, "GET /fapi/v1/positionSide/dual": `{"dualSidePosition": false}`,
	})
	limited, err := testAdapter(exchange.MarketTypeSwap, other.URL).GetAccountSnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, "23.72469206", limited.Balances[0].Available.String())
	require.Equal(t, "5.5", limited.Balances[0].Transferable.String(), "可用余额和可转出额度不一致时，划转用后者")
}

func TestStandardWalletTransferUsesUniversalTransfer(t *testing.T) {
	state, server := newPMServer(t, map[string]string{
		"POST /sapi/v1/asset/transfer": samplePath(t, "sapi_transfer_new.json"),
		"GET /sapi/v1/asset/transfer":  samplePath(t, "sapi_transfer_history.json"),
	})
	adapter := testAdapter(exchange.MarketTypeSpot, server.URL)
	ctx := context.Background()
	receipt, err := adapter.Transfer(ctx, exchange.WalletFutures, exchange.WalletSpot, "USDT", shared.MustDecimal("12.5"), "local-ref-1")
	require.NoError(t, err)
	require.Equal(t, "13526853623", receipt.ExchangeTransferID)
	sent := state.find(http.MethodPost, "/sapi/v1/asset/transfer")
	require.Len(t, sent, 1)
	require.Equal(t, "UMFUTURE_MAIN", sent[0].Query.Get("type"))
	require.Equal(t, "12.5", sent[0].Query.Get("amount"))
	require.Empty(t, sent[0].Query.Get("clientRef"), "万向划转没有客户端流水号，本地引用号不发给交易所")
	require.Equal(t, 1, strings.Count(sent[0].Query.Encode(), "type="))

	_, err = adapter.Transfer(ctx, exchange.WalletSpot, exchange.WalletMargin, "USDT", shared.MustDecimal("1"), "ref")
	require.True(t, exchange.IsKind(err, exchange.ErrorRejected), "只支持现货与合约钱包之间划转")
	_, err = adapter.Transfer(ctx, exchange.WalletSpot, exchange.WalletFutures, "USDT", shared.Zero(), "ref")
	require.True(t, exchange.IsKind(err, exchange.ErrorRejected))

	records, err := adapter.ListTransfers(ctx, time.UnixMilli(1544000000000))
	require.NoError(t, err)
	require.Len(t, records, 2, "两个方向各查一次")
	require.Equal(t, execution.TransferConfirmed, records[0].Status)
	require.Equal(t, "11415955596", records[0].ExchangeTransferID)
	require.Equal(t, exchange.WalletSpot, records[0].From)
	history := state.find(http.MethodGet, "/sapi/v1/asset/transfer")
	require.ElementsMatch(t, []string{"MAIN_UMFUTURE", "UMFUTURE_MAIN"}, []string{history[0].Query.Get("type"), history[1].Query.Get("type")})
	require.Equal(t, "1544000000000", history[0].Query.Get("startTime"))
}

func TestStandardTransferStatusMapping(t *testing.T) {
	require.Equal(t, execution.TransferPending, transferStatus("PENDING"), "PENDING 不等于没到账")
	require.Equal(t, execution.TransferConfirmed, transferStatus("CONFIRMED"))
	require.Equal(t, execution.TransferFailed, transferStatus("FAILED"))
}

// 普通账户的现货、合约适配器各设自己负责的那部分 BNB 抵扣；划转和小额兑换只挂在现货适配器上。
func TestStandardFeeDiscountAndDustPerMarket(t *testing.T) {
	state, server := newPMServer(t, map[string]string{
		"POST /sapi/v1/bnbBurn":        samplePath(t, "sapi_bnb_burn.json"),
		"GET /sapi/v1/bnbBurn":         samplePath(t, "sapi_bnb_burn_status.json"),
		"POST /fapi/v1/feeBurn":        samplePath(t, "fapi_fee_burn_toggle.json"),
		"GET /fapi/v1/feeBurn":         samplePath(t, "fapi_fee_burn_status.json"),
		"POST /sapi/v1/asset/dust-btc": samplePath(t, "sapi_dust_btc.json"),
		"POST /sapi/v1/asset/dust":     samplePath(t, "sapi_dust.json"),
	})
	spot, swap := testAdapter(exchange.MarketTypeSpot, server.URL), testAdapter(exchange.MarketTypeSwap, server.URL)
	ctx := context.Background()
	require.NoError(t, spot.SetBNBFeeDiscount(ctx, true))
	require.NoError(t, swap.SetBNBFeeDiscount(ctx, true))
	burn := state.find(http.MethodPost, "/sapi/v1/bnbBurn")
	require.Equal(t, "true", burn[0].Query.Get("spotBNBBurn"))
	require.Empty(t, burn[0].Query.Get("interestBNBBurn"), "普通账户的现货不设借币利息抵扣")
	require.Equal(t, "true", state.find(http.MethodPost, "/fapi/v1/feeBurn")[0].Query.Get("feeBurn"))
	on, err := spot.GetBNBFeeDiscount(ctx)
	require.NoError(t, err)
	require.True(t, on)
	on, err = swap.GetBNBFeeDiscount(ctx)
	require.NoError(t, err)
	require.True(t, on)

	require.True(t, spot.Supports(execution.CapabilityTransfer))
	require.False(t, swap.Supports(execution.CapabilityTransfer))
	require.True(t, spot.Supports(execution.CapabilityDust))
	require.False(t, swap.Supports(execution.CapabilityDust))
	dust, err := spot.ListDust(ctx)
	require.NoError(t, err)
	require.Equal(t, "ADA", dust[0].Asset)
	require.Equal(t, "6.21", dust[0].Quantity.String())
	require.Equal(t, "0.00016848", dust[0].ValueBTC.String(), "官方样例 toBTC")
	require.Equal(t, "SPOT", state.find(http.MethodPost, "/sapi/v1/asset/dust-btc")[0].Query.Get("accountType"))
	swapDust, err := swap.ListDust(ctx)
	require.NoError(t, err)
	require.Empty(t, swapDust)
	converted, err := spot.ConvertDust(ctx, []string{"ETH"})
	require.NoError(t, err)
	require.Equal(t, []string{"ETH"}, converted.Converted)
}

// 双市场账户通过 MultiMarket 使用这些能力：划转交给现货适配器，抵扣两边各设，小额兑换只现货。
func TestMultiMarketForwardsOptionalCapabilities(t *testing.T) {
	state, server := newPMServer(t, map[string]string{
		"POST /sapi/v1/asset/transfer": samplePath(t, "sapi_transfer_new.json"),
		"POST /sapi/v1/bnbBurn":        samplePath(t, "sapi_bnb_burn.json"),
		"POST /fapi/v1/feeBurn":        samplePath(t, "fapi_fee_burn_toggle.json"),
		"POST /sapi/v1/asset/dust-btc": samplePath(t, "sapi_dust_btc.json"),
	})
	multi, err := execution.NewMultiMarket(exchange.ExchangeBinance,
		[]exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap},
		map[exchange.MarketType]execution.ExecutionAdapter{
			exchange.MarketTypeSpot: testAdapter(exchange.MarketTypeSpot, server.URL),
			exchange.MarketTypeSwap: testAdapter(exchange.MarketTypeSwap, server.URL),
		})
	require.NoError(t, err)
	ctx := context.Background()
	for _, capability := range []string{execution.CapabilityTransfer, execution.CapabilityFeeDiscount, execution.CapabilityDust} {
		require.True(t, multi.Supports(capability), capability)
	}
	_, err = multi.Transfer(ctx, exchange.WalletSpot, exchange.WalletFutures, "USDT", shared.MustDecimal("5"), "ref")
	require.NoError(t, err)
	require.Len(t, state.find(http.MethodPost, "/sapi/v1/asset/transfer"), 1)
	require.NoError(t, multi.SetBNBFeeDiscount(ctx, true))
	require.Len(t, state.find(http.MethodPost, "/sapi/v1/bnbBurn"), 1)
	require.Len(t, state.find(http.MethodPost, "/fapi/v1/feeBurn"), 1)
	dust, err := multi.ListDust(ctx)
	require.NoError(t, err)
	require.Len(t, dust, 1)

	// 只开合约的账户没有划转和小额兑换。
	swapOnly, err := execution.NewMultiMarket(exchange.ExchangeBinance, []exchange.MarketType{exchange.MarketTypeSwap},
		map[exchange.MarketType]execution.ExecutionAdapter{exchange.MarketTypeSwap: testAdapter(exchange.MarketTypeSwap, server.URL)})
	require.NoError(t, err)
	require.False(t, swapOnly.Supports(execution.CapabilityTransfer))
	require.False(t, swapOnly.Supports(execution.CapabilityDust))
	_, err = swapOnly.Transfer(ctx, exchange.WalletSpot, exchange.WalletFutures, "USDT", shared.MustDecimal("5"), "ref")
	require.True(t, exchange.IsKind(err, exchange.ErrorRejected))
}
