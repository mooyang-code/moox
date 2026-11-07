package okx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/stretchr/testify/require"
)

// OKX 官方文档的响应样例（testdata，来源见 testdata/README.md）。

func readOfficialSample(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return raw
}

// officialData 取官方响应信封里的 data 数组，按需改第一行的字段。
func officialData(t *testing.T, name string, mutate func(map[string]any)) string {
	t.Helper()
	var envelope struct {
		Code string           `json:"code"`
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(readOfficialSample(t, name), &envelope))
	require.Equal(t, "0", envelope.Code)
	if mutate != nil {
		mutate(envelope.Data[0])
	}
	raw, err := json.Marshal(envelope.Data)
	require.NoError(t, err)
	return string(raw)
}

func spotAdapter(baseURL string) *Adapter {
	return newWithClient(exchange.AccountConfig{
		TradingAccountID: "account-1", Exchange: exchange.ExchangeOKX,
		Markets: []exchange.MarketType{exchange.MarketTypeSpot}, ExecutionMode: exchange.ExecutionModeLive,
		SettlementAsset: "USDT",
	}, exchange.Credential{APIKey: "key", APISecret: "secret", Passphrase: "pass"}, httpclient.New(baseURL))
}

// 现货下单一律 tdMode=cash（不借币）：市价买、市价卖、限价各档生效期都断言请求体。
func TestOfficialSpotOrdersAlwaysUseCashTradeMode(t *testing.T) {
	var bodies []map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]string
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		bodies = append(bodies, body)
		// 官方下单响应样例。
		ok(w, officialData(t, "order_place.json", nil))
	}))
	defer server.Close()
	adapter := spotAdapter(server.URL)
	price := shared.MustDecimal("50000")
	requests := []exchange.OrderRequest{
		{ClientOrderID: "c1", ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket, Side: exchange.SideBuy, Quantity: shared.MustDecimal("0.01")},
		{ClientOrderID: "c2", ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket, Side: exchange.SideSell, Quantity: shared.MustDecimal("0.01")},
	}
	for _, policy := range []exchange.FillPolicy{exchange.FillPolicyGTC, exchange.FillPolicyIOC, exchange.FillPolicyFOK} {
		requests = append(requests, exchange.OrderRequest{
			ClientOrderID: "c3", ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeLimit, FillPolicy: policy,
			Side: exchange.SideBuy, Quantity: shared.MustDecimal("0.01"), LimitPrice: &price,
		})
	}
	for _, request := range requests {
		order, err := adapter.PlaceOrder(context.Background(), request)
		require.NoError(t, err)
		require.Equal(t, "312269865356374016", order.ExchangeOrderID, "官方下单响应样例的 ordId")
		require.Equal(t, exchange.MarketTypeSpot, order.Market)
	}
	require.Len(t, bodies, len(requests))
	for _, body := range bodies {
		require.Equal(t, "cash", body["tdMode"], "现货下单必须 tdMode=cash：%v", body)
		require.NotContains(t, body, "posSide")
		require.NotContains(t, body, "reduceOnly")
	}
}

func TestOfficialOrderDetailsListAndFillsParse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v5/trade/order":
			if request.Method == http.MethodDelete {
				return
			}
			ok(w, officialData(t, "order_details.json", nil))
		case "/api/v5/trade/orders-pending":
			ok(w, officialData(t, "order_list.json", nil))
		case "/api/v5/trade/fills-history":
			ok(w, officialData(t, "fills.json", nil))
		default:
			t.Errorf("意外的路径 %s", request.URL.Path)
		}
	}))
	defer server.Close()
	adapter := spotAdapter(server.URL)
	ctx := context.Background()
	key := exchange.NewSymbolKey(exchange.MarketTypeSpot, "BTC-USDT")

	order, err := adapter.GetOrderByExchangeID(ctx, key, "680800019749904384")
	require.NoError(t, err)
	require.Equal(t, exchange.OrderStatusFilled, order.Status)
	require.Equal(t, "0.00192834", order.FilledQuantity.String())
	require.Equal(t, "51858", order.AveragePrice.String())
	require.Equal(t, exchange.SideBuy, order.Side)

	open, err := adapter.ListOpenOrders(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, open)
	require.Equal(t, exchange.MarketTypeSpot, open[0].Market)

	fills, cursor, err := adapter.ListRecentFills(ctx, key, "")
	require.NoError(t, err)
	require.Len(t, fills, 1)
	require.Equal(t, "744876980", fills[0].ExchangeTradeID)
	require.Equal(t, "0.00192834", fills[0].Quantity.String())
	require.Equal(t, "0.00000192834", fills[0].Fee.String(), "费用取绝对值，官方样例里是负数（扣费）")
	require.Equal(t, "BTC", fills[0].FeeAsset)
	require.Equal(t, exchange.MarketTypeSpot, fills[0].Market)
	require.NotEmpty(t, cursor)
}

// 余额：结算资产的数量取 cashBal（官方定义“Cash balance”，不含未实现盈亏），未实现盈亏取该币种的 upl
// （官方定义“该币种所有保证金与衍生品持仓的未实现盈亏之和”）；账户级的 upl 是折算 USD 的全账户值，不用；
// totalEq 是全币种折合 USD，只记入 ExchangeEquity 供展示核对。
func TestOfficialBalanceSampleUsesCashBalanceAndPerCurrencyUnrealizedPnL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/api/v5/account/balance", request.URL.Path)
		ok(w, officialData(t, "account_balance.json", nil))
	}))
	defer server.Close()
	snapshot, err := spotAdapter(server.URL).GetAccountSnapshot(context.Background())
	require.NoError(t, err)

	var usdt exchange.AssetBalance
	for _, balance := range snapshot.Balances {
		require.Equal(t, exchange.WalletUnified, balance.Wallet, "OKX 统一账户只有一个钱包")
		if balance.Asset == "USDT" {
			usdt = balance
		}
	}
	require.Equal(t, "4850.435693622894", usdt.Total.String(), "数量取 cashBal，不取含未实现盈亏的 eq（4992.89）")
	require.Equal(t, "-7.545600000000006", snapshot.UnrealizedPnL.String(), "未实现盈亏取 USDT 明细的 upl，不取账户级 upl")
	require.Equal(t, "55837.43556134779", snapshot.ExchangeEquity.String())
	require.True(t, snapshot.Present.UnrealizedPnL)
}

// 同一个 OKX 账户同时做现货和永续：两个适配器读到的是同一份账户余额（同一个钱包），合并后只计一次，
// 权益和未实现盈亏不会翻倍。
func TestOneOKXAccountWithBothMarketsCountsTheSharedWalletOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v5/account/balance":
			ok(w, officialData(t, "account_balance.json", nil))
		case "/api/v5/account/config":
			ok(w, officialData(t, "account_config.json", func(row map[string]any) { row["posMode"] = "net_mode" }))
		default:
			t.Errorf("意外的路径 %s", request.URL.Path)
		}
	}))
	defer server.Close()
	multi, err := execution.NewMultiMarket(exchange.ExchangeOKX,
		[]exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap},
		map[exchange.MarketType]execution.ExecutionAdapter{
			exchange.MarketTypeSpot: spotAdapter(server.URL), exchange.MarketTypeSwap: swapAdapter(server.URL),
		})
	require.NoError(t, err)
	snapshot, err := multi.GetAccountSnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, "-7.545600000000006", snapshot.UnrealizedPnL.String(), "共用钱包只计一次，不翻倍")
	usdtCount := 0
	for _, balance := range snapshot.Balances {
		if balance.Asset == "USDT" {
			usdtCount++
		}
	}
	require.Equal(t, 1, usdtCount)
}

// 官方持仓样例是保证金（MARGIN）逐仓的，改成永续全仓后核对字段：pos、avgPx、markPx、lever、upl 的名字和含义。
func TestOfficialPositionSampleFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v5/public/instruments":
			ok(w, `[{"instId":"BTC-USDT-SWAP","instType":"SWAP","baseCcy":"","quoteCcy":"","settleCcy":"USDT","tickSz":"0.1","lotSz":"0.01","minSz":"0.01","ctVal":"1","ctValCcy":"BTC","ctType":"linear","state":"live"}]`)
		case "/api/v5/account/positions":
			require.Equal(t, "SWAP", request.URL.Query().Get("instType"))
			ok(w, officialData(t, "account_positions.json", func(row map[string]any) {
				row["instType"], row["instId"], row["mgnMode"] = "SWAP", "BTC-USDT-SWAP", "cross"
			}))
		default:
			t.Errorf("意外的路径 %s", request.URL.Path)
		}
	}))
	defer server.Close()
	adapter := swapAdapter(server.URL)
	_, err := adapter.LoadInstruments(context.Background())
	require.NoError(t, err)
	positions, err := adapter.ListPositionSnapshots(context.Background())
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.Equal(t, "0.00190433573", positions[0].SignedQuantity.String())
	require.Equal(t, "62961.4", positions[0].EntryPrice.String())
	require.Equal(t, "62891.9", positions[0].MarkPrice.String())
	require.Equal(t, "5", positions[0].Leverage.String())
	require.Equal(t, "-0.0000033452492717", positions[0].UnrealizedPnL.String())
	require.Equal(t, exchange.MarketTypeSwap, positions[0].Market)
}
