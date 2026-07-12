package rpc

import (
	"errors"
	"testing"
	"time"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	mooxpb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/assert"
	domainorder "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

func TestErrToRetInfo_ServiceErrors_ShouldMapCodes(t *testing.T) {
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, errToRetInfo(nil).Code)
	assert.Equal(t, mooxpb.ErrorCode_INVALID_PARAM, errToRetInfo(service.ErrInvalidParam).Code)
	assert.Equal(t, mooxpb.ErrorCode_NOT_FOUND, errToRetInfo(service.ErrNotFound).Code)
	assert.Equal(t, mooxpb.ErrorCode_INVALID_PARAM, errToRetInfo(service.ErrConflict).Code)
	assert.Equal(t, mooxpb.ErrorCode_INVALID_PARAM, errToRetInfo(service.ErrInsufficient).Code)
	assert.Equal(t, mooxpb.ErrorCode_INNER_ERR, errToRetInfo(assert.AnError).Code)
	assert.Equal(t, mooxpb.ErrorCode_INNER_ERR, errToRetInfo(errors.New("wrapped")).Code)
}

func TestPageFromPB_NilAndValues_ShouldNormalize(t *testing.T) {
	assert.Equal(t, service.Page{}, pageFromPB(nil))
	got := pageFromPB(&mooxpb.Page{Page: 2, Size: 50})
	assert.Equal(t, service.Page{PageNo: 2, PageSize: 50}, got)
}

func TestPageResult_HasMore_ShouldReflectTotal(t *testing.T) {
	r := pageResult(service.Page{PageNo: 1, PageSize: 10}, 25)
	assert.True(t, r.HasMore)
	assert.Equal(t, uint32(25), r.Total)
	r = pageResult(service.Page{PageNo: 3, PageSize: 10}, 25)
	assert.False(t, r.HasMore)
}

func TestUnixOrZero_ZeroTime_ShouldReturnZero(t *testing.T) {
	assert.Equal(t, int64(0), unixOrZero(time.Time{}))
	ts := time.Unix(1700000000, 0)
	assert.Equal(t, ts.Unix(), unixOrZero(ts))
}

func TestAccountTypeMappings_ShouldRoundTrip(t *testing.T) {
	assert.Equal(t, service.AccountMargin, accountTypeToDomain(mooxpb.AccountType_ACCOUNT_TYPE_MARGIN))
	assert.Equal(t, service.AccountSwap, accountTypeToDomain(mooxpb.AccountType_ACCOUNT_TYPE_SWAP))
	assert.Equal(t, service.AccountSim, accountTypeToDomain(mooxpb.AccountType_ACCOUNT_TYPE_SIM))
	assert.Equal(t, service.AccountSpot, accountTypeToDomain(mooxpb.AccountType(-1)))
	assert.Equal(t, mooxpb.AccountType_ACCOUNT_TYPE_MARGIN, accountTypeToPB(service.AccountMargin))
	assert.Equal(t, mooxpb.AccountType_ACCOUNT_TYPE_SWAP, accountTypeToPB(service.AccountSwap))
	assert.Equal(t, mooxpb.AccountType_ACCOUNT_TYPE_SIM, accountTypeToPB(service.AccountSim))
	assert.Equal(t, mooxpb.AccountType_ACCOUNT_TYPE_SPOT, accountTypeToPB(service.AccountSpot))
}

func TestMarketTypeMappings_ShouldMapKnownValues(t *testing.T) {
	assert.Equal(t, "margin", marketTypeToDomain(mooxpb.MarketType_MARKET_TYPE_MARGIN))
	assert.Equal(t, "swap", marketTypeToDomain(mooxpb.MarketType_MARKET_TYPE_SWAP))
	assert.Equal(t, "futures", marketTypeToDomain(mooxpb.MarketType_MARKET_TYPE_FUTURES))
	assert.Equal(t, "spot", marketTypeToDomain(mooxpb.MarketType(-1)))
	assert.Equal(t, mooxpb.MarketType_MARKET_TYPE_MARGIN, marketTypeToPB("margin"))
	assert.Equal(t, mooxpb.MarketType_MARKET_TYPE_SWAP, marketTypeToPB("swap"))
	assert.Equal(t, mooxpb.MarketType_MARKET_TYPE_FUTURES, marketTypeToPB("futures"))
	assert.Equal(t, mooxpb.MarketType_MARKET_TYPE_SPOT, marketTypeToPB("spot"))
}

func TestOrderSideAndTypeMappings_ShouldMapKnownValues(t *testing.T) {
	assert.Equal(t, "sell", orderSideToDomain(mooxpb.OrderSide_ORDER_SIDE_SELL))
	assert.Equal(t, "buy", orderSideToDomain(mooxpb.OrderSide_ORDER_SIDE_BUY))
	assert.Equal(t, "market", orderTypeToDomain(mooxpb.OrderType_ORDER_TYPE_MARKET))
	assert.Equal(t, "stop", orderTypeToDomain(mooxpb.OrderType_ORDER_TYPE_STOP))
	assert.Equal(t, "stop_limit", orderTypeToDomain(mooxpb.OrderType_ORDER_TYPE_STOP_LIMIT))
	assert.Equal(t, "post_only", orderTypeToDomain(mooxpb.OrderType_ORDER_TYPE_POST_ONLY))
	assert.Equal(t, "ioc", orderTypeToDomain(mooxpb.OrderType_ORDER_TYPE_IOC))
	assert.Equal(t, "fok", orderTypeToDomain(mooxpb.OrderType_ORDER_TYPE_FOK))
	assert.Equal(t, "limit", orderTypeToDomain(mooxpb.OrderType_ORDER_TYPE_LIMIT))
	assert.Equal(t, int32(mooxpb.OrderSide_ORDER_SIDE_SELL), orderSidePB("sell"))
	assert.Equal(t, int32(mooxpb.OrderSide_ORDER_SIDE_BUY), orderSidePB("buy"))
	assert.Equal(t, int32(mooxpb.OrderType_ORDER_TYPE_MARKET), orderTypePB("market"))
	assert.Equal(t, int32(mooxpb.OrderType_ORDER_TYPE_STOP), orderTypePB("stop"))
	assert.Equal(t, int32(mooxpb.OrderType_ORDER_TYPE_STOP_LIMIT), orderTypePB("stop_limit"))
	assert.Equal(t, int32(mooxpb.OrderType_ORDER_TYPE_POST_ONLY), orderTypePB("post_only"))
	assert.Equal(t, int32(mooxpb.OrderType_ORDER_TYPE_IOC), orderTypePB("ioc"))
	assert.Equal(t, int32(mooxpb.OrderType_ORDER_TYPE_FOK), orderTypePB("fok"))
	assert.Equal(t, int32(mooxpb.OrderType_ORDER_TYPE_LIMIT), orderTypePB("limit"))
}

func TestStatusMappings_ShouldCastDomainValues(t *testing.T) {
	assert.Equal(t, mooxpb.AccountStatus_ACCOUNT_STATUS_DISABLED, accountStatusToPB(service.AccountDisabled))
	assert.Equal(t, mooxpb.AccountStatus_ACCOUNT_STATUS_FROZEN, accountStatusToPB(service.AccountFrozen))
	assert.Equal(t, mooxpb.AccountStatus_ACCOUNT_STATUS_READONLY, accountStatusToPB(service.AccountReadonly))
	assert.Equal(t, mooxpb.AccountStatus_ACCOUNT_STATUS_NORMAL, accountStatusToPB(service.AccountNormal))
	assert.Equal(t, mooxpb.OrderStatus_ORDER_STATUS_FILLED, orderStatusToPB(int(mooxpb.OrderStatus_ORDER_STATUS_FILLED)))
	assert.Equal(t, mooxpb.ChannelStatus_CHANNEL_STATUS_DISABLED, channelStatusToPB(int(mooxpb.ChannelStatus_CHANNEL_STATUS_DISABLED)))
}

func TestModelToPB_NilInputs_ShouldReturnNil(t *testing.T) {
	assert.Nil(t, accountToPB(nil))
	assert.Nil(t, balanceToPB(nil))
	assert.Nil(t, fundFlowToPB(nil))
	assert.Nil(t, apiKeyToPB(nil))
	assert.Nil(t, channelToPB(nil))
	assert.Nil(t, orderToPB(nil))
	assert.Nil(t, tradeToPB(nil))
	assert.Nil(t, positionToPB(nil))
}

func TestModelToPB_ValidAccount_ShouldPopulateFields(t *testing.T) {
	now := time.Unix(100, 0)
	a := &service.Account{
		AccountID: "acc-1", UserID: "u1", AccountName: "main",
		AccountType: service.AccountSpot, ChannelID: "ch-1",
		BaseCurrency: "USDT", Status: service.AccountNormal, IsDefault: true,
		CreatedAt: now, UpdatedAt: now,
	}
	got := accountToPB(a)
	assert.Equal(t, "acc-1", got.AccountId)
	assert.Equal(t, mooxpb.AccountType_ACCOUNT_TYPE_SPOT, got.AccountType)
	assert.Equal(t, int64(100), got.CreatedAt)
}

func TestModelToPB_ValidModels_ShouldPopulateFields(t *testing.T) {
	now := time.Unix(200, 0)

	balance := balanceToPB(&service.Balance{AccountID: "acc-1", Currency: "USDT", Available: "1", Frozen: "2", Total: "3"})
	assert.Equal(t, "USDT", balance.Currency)
	assert.Equal(t, "2", balance.Frozen)

	flow := fundFlowToPB(&service.FundFlow{FlowID: "flow-1", AccountID: "acc-1", Currency: "USDT", BizType: "transfer", Direction: -1, Amount: "5", BalanceAfter: "95", RefType: "order", RefID: "ord-1", Remark: "done", CreatedAt: now})
	assert.Equal(t, int32(-1), flow.Direction)
	assert.Equal(t, int64(200), flow.CreatedAt)

	key := apiKeyToPB(&service.APIKey{APIKeyID: "key-1", AccountID: "acc-1", Exchange: "binance", APIKey: "masked", PermissionsRaw: []string{"read"}, Status: 1, CreatedAt: now})
	assert.Equal(t, []string{"read"}, key.Permissions)
	assert.Equal(t, int64(200), key.CreatedAt)

	channel := channelToPB(&service.TradeChannel{ChannelID: "ch-1", ChannelName: "main", Exchange: "okx", MarketType: "swap", AccountID: "acc-1", APIKeyID: "key-1", Endpoint: "prod", IsSimulated: true, Status: int(mooxpb.ChannelStatus_CHANNEL_STATUS_ONLINE), RateLimit: 10, LastHeartbeat: now, CreatedAt: now, UpdatedAt: now})
	assert.Equal(t, mooxpb.MarketType_MARKET_TYPE_SWAP, channel.MarketType)
	assert.True(t, channel.IsSimulated)

	order := orderToPB(&service.Order{OrderID: "ord-1", ClientOrderID: "cli-1", ExchangeOrderID: "ex-1", AccountID: "acc-1", ChannelID: "ch-1", Exchange: "binance", Symbol: "BTCUSDT", MarketType: "spot", Side: "sell", PosSide: "net", OrderType: "ioc", TimeInForce: "IOC", Price: "100", Quantity: "2", Amount: "200", FilledQty: "1", FilledAmount: "100", AvgPrice: "100", Fee: "0.1", FeeCurrency: "USDT", Status: int(mooxpb.OrderStatus_ORDER_STATUS_PARTIALLY_FILLED), ReduceOnly: true, TriggerPrice: "90", Source: "manual", StrategyID: "st-1", RejectReason: "none", SubmittedAt: now, FinishedAt: now, CreatedAt: now, UpdatedAt: now})
	assert.Equal(t, mooxpb.OrderSide_ORDER_SIDE_SELL, order.Side)
	assert.Equal(t, mooxpb.OrderType_ORDER_TYPE_IOC, order.OrderType)
	assert.True(t, order.ReduceOnly)

	trade := tradeToPB(&service.Trade{TradeID: "tr-1", ExchangeTradeID: "ex-tr-1", OrderID: "ord-1", ExchangeOrderID: "ex-1", AccountID: "acc-1", ChannelID: "ch-1", Exchange: "okx", Symbol: "ETH-USDT", Side: "buy", Price: "10", Quantity: "3", Amount: "30", Fee: "0.01", FeeCurrency: "USDT", Role: "maker", TradedAt: now})
	assert.Equal(t, mooxpb.OrderSide_ORDER_SIDE_BUY, trade.Side)
	assert.Equal(t, int64(200), trade.TradedAt)

	position := positionToPB(&service.Position{PositionID: "pos-1", AccountID: "acc-1", ChannelID: "ch-1", Exchange: "okx", Symbol: "BTC-USDT-SWAP", PosSide: "long", Quantity: "1", AvgPrice: "50000", Leverage: "10", Margin: "5000", LiqPrice: "40000", UnrealizedPnl: "12", RealizedPnl: "3", UpdatedAt: now})
	assert.Equal(t, "BTC-USDT-SWAP", position.Symbol)
	assert.Equal(t, "12", position.UnrealizedPnl)
}

func TestInstrumentAndDustToPB_ShouldPopulateFields(t *testing.T) {
	ins := instrumentToPB(exchange.Instrument{Symbol: "BTCUSDT", Market: exchange.MarketSpot, BaseCcy: "BTC", QuoteCcy: "USDT"})
	assert.Equal(t, "BTCUSDT", ins.Symbol)
	assert.Equal(t, mooxpb.MarketType_MARKET_TYPE_SPOT, ins.MarketType)
	dust := dustTransferItemToPB(exchange.DustTransferItem{Asset: "BNB", Amount: "1"})
	assert.Equal(t, "BNB", dust.Asset)
	skipped := dustTransferSkippedItemToPB(exchange.DustTransferSkippedItem{Asset: "X", Reason: "low"})
	assert.Equal(t, "low", skipped.Reason)
}

func TestKernelStatusToPB_AllStates_ShouldMap(t *testing.T) {
	cases := []struct {
		state domainorder.State
		want  mooxpb.OrderStatus
	}{
		{domainorder.Open, mooxpb.OrderStatus_ORDER_STATUS_SUBMITTED},
		{domainorder.Submitting, mooxpb.OrderStatus_ORDER_STATUS_SUBMITTED},
		{domainorder.SubmitUnknown, mooxpb.OrderStatus_ORDER_STATUS_SUBMITTED},
		{domainorder.Canceling, mooxpb.OrderStatus_ORDER_STATUS_SUBMITTED},
		{domainorder.CancelUnknown, mooxpb.OrderStatus_ORDER_STATUS_SUBMITTED},
		{domainorder.PartiallyFilled, mooxpb.OrderStatus_ORDER_STATUS_PARTIALLY_FILLED},
		{domainorder.Filled, mooxpb.OrderStatus_ORDER_STATUS_FILLED},
		{domainorder.Canceled, mooxpb.OrderStatus_ORDER_STATUS_CANCELED},
		{domainorder.PartiallyCanceled, mooxpb.OrderStatus_ORDER_STATUS_PARTIAL_CANCELED},
		{domainorder.Rejected, mooxpb.OrderStatus_ORDER_STATUS_REJECTED},
		{domainorder.Expired, mooxpb.OrderStatus_ORDER_STATUS_EXPIRED},
		{domainorder.State("unknown"), mooxpb.OrderStatus_ORDER_STATUS_PENDING},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, kernelStatusToPB(tc.state), "state=%s", tc.state)
	}
}

func TestKernelOrderToPB_BuyAndSell_ShouldPopulateFields(t *testing.T) {
	buy := kernelOrderToPB(store.OrderRecord{
		OrderID: "ord-1", ClientOrderID: "cli-1", ExchangeOrderID: "ex-1",
		AccountID: "acc-1", ChannelID: "ch-1", Symbol: "BTCUSDT",
		Side: "BUY", Price: "100", Quantity: "2", FilledQuantity: "1",
		State: string(domainorder.PartiallyFilled), ReduceOnly: true,
	})
	assert.Equal(t, mooxpb.OrderSide_ORDER_SIDE_BUY, buy.Side)
	assert.Equal(t, mooxpb.OrderStatus_ORDER_STATUS_PARTIALLY_FILLED, buy.Status)
	assert.True(t, buy.ReduceOnly)

	sell := kernelOrderToPB(store.OrderRecord{OrderID: "ord-2", Side: "SELL", State: string(domainorder.Filled)})
	assert.Equal(t, mooxpb.OrderSide_ORDER_SIDE_SELL, sell.Side)
	assert.Equal(t, mooxpb.OrderStatus_ORDER_STATUS_FILLED, sell.Status)
}
