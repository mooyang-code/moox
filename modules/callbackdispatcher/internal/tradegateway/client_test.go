package tradegateway

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// fakeGateway 记录调用（方法、空间头、请求），并按方法返回预设的响应。
type fakeGateway struct {
	calls     []gatewayCall
	responses map[string]any
	err       error
}

type gatewayCall struct {
	service, method, space string
	request                any
}

func (f *fakeGateway) Invoke(ctx context.Context, service, method string, request, response any) error {
	f.calls = append(f.calls, gatewayCall{service: service, method: method, space: gatewayclient.CallMetadataFromContext(ctx).SpaceID, request: request})
	if f.err != nil {
		return f.err
	}
	proto.Merge(response.(proto.Message), f.responses[method].(proto.Message))
	return nil
}

func ok() *tradepb.RetInfo { return &tradepb.RetInfo{Code: tradepb.ErrorCode_SUCCESS} }

var foo = action.InstrumentRef{Exchange: "binance", Market: "SPOT", ExchangeSymbol: "FOOUSDT"}

// 名单的四个全局方法不带空间头；按空间的两个方法带持有者的空间。
func TestSpaceHeaderOnlyForSpaceMethods(t *testing.T) {
	gateway := &fakeGateway{responses: map[string]any{
		"BlockInstrument":        &tradepb.BlockInstrumentRsp{RetInfo: ok(), Created: true},
		"UnblockInstrument":      &tradepb.UnblockInstrumentRsp{RetInfo: ok()},
		"ListBlockedInstruments": &tradepb.ListBlockedInstrumentsRsp{RetInfo: ok()},
		"ListInstrumentHolders":  &tradepb.ListInstrumentHoldersRsp{RetInfo: ok()},
		"FlattenInstrument":      &tradepb.FlattenInstrumentRsp{RetInfo: ok(), Action: &tradepb.OperatorAction{ActionId: "a1", Status: "RUNNING"}},
		"GetOperatorAction":      &tradepb.GetOperatorActionRsp{RetInfo: ok(), Action: &tradepb.OperatorAction{ActionId: "a1", Status: "COMPLETED"}},
	}}
	client := New(gateway)
	// 调用方 ctx 里带着别的空间也不能漏到全局方法上。
	ctx := gatewayclient.WithCallMetadata(context.Background(), gatewayclient.CallMetadata{SpaceID: "other", TraceID: "trace-1"})

	_, err := client.BlockInstrument(ctx, foo, "原因", "event-1")
	require.NoError(t, err)
	require.NoError(t, client.UnblockInstrument(ctx, foo))
	_, err = client.ListBlockedInstruments(ctx)
	require.NoError(t, err)
	_, err = client.ListInstrumentHolders(ctx, foo, false)
	require.NoError(t, err)
	_, err = client.FlattenInstrument(ctx, action.FlattenRequest{ActionID: "a1", SpaceID: "crypto", LogicalAccountID: "la-1", Instrument: foo, Reason: "r"})
	require.NoError(t, err)
	_, err = client.GetOperatorAction(ctx, "crypto", "a1")
	require.NoError(t, err)

	spaces := map[string]string{}
	for _, call := range gateway.calls {
		require.Equal(t, tradeService, call.service)
		spaces[call.method] = call.space
	}
	require.Equal(t, map[string]string{
		"BlockInstrument": "", "UnblockInstrument": "", "ListBlockedInstruments": "", "ListInstrumentHolders": "",
		"FlattenInstrument": "crypto", "GetOperatorAction": "crypto",
	}, spaces)
}

func TestBlockAndHoldersMapping(t *testing.T) {
	gateway := &fakeGateway{responses: map[string]any{
		"BlockInstrument": &tradepb.BlockInstrumentRsp{RetInfo: ok(), InstrumentUnknown: true},
		"ListInstrumentHolders": &tradepb.ListInstrumentHoldersRsp{RetInfo: ok(), Holders: []*tradepb.InstrumentHolder{
			{SpaceId: "crypto", LogicalAccountId: "la-1", TradingAccountId: "ta-1", Quantity: "10", Paper: true, Status: "ENABLED"},
			{SpaceId: "crypto", LogicalAccountId: "la-1", TradingAccountId: "ta-2", Quantity: "0.0001", Dust: true, Status: "ERROR", Note: "取不到报价"},
		}, SkippedAccounts: []*tradepb.SkippedAccount{{TradingAccountId: "ta-9", Reason: "资金设置读不出来"}}, Notes: []string{"计价币不是结算币，清仓不会卖它"}},
		"ListBlockedInstruments": &tradepb.ListBlockedInstrumentsRsp{RetInfo: ok(), Instruments: []*tradepb.BlockedInstrument{
			{Exchange: tradepb.Exchange_EXCHANGE_OKX, MarketType: tradepb.MarketType_MARKET_TYPE_SWAP, ExchangeSymbol: "FOO-USDT-SWAP"},
		}},
	}}
	client := New(gateway)
	result, err := client.BlockInstrument(context.Background(), foo, "原因", "event-1")
	require.NoError(t, err)
	require.True(t, result.InstrumentUnknown)
	request := gateway.calls[0].request.(*tradepb.BlockInstrumentReq)
	require.Equal(t, tradepb.Exchange_EXCHANGE_BINANCE, request.Exchange)
	require.Equal(t, tradepb.MarketType_MARKET_TYPE_SPOT, request.MarketType)
	require.Equal(t, "FOOUSDT", request.ExchangeSymbol)
	require.Equal(t, "event-1", request.EventId)

	holders, err := client.ListInstrumentHolders(context.Background(), foo, true)
	require.NoError(t, err)
	require.True(t, gateway.calls[1].request.(*tradepb.ListInstrumentHoldersReq).PaperOnly)
	require.Equal(t, []action.Holder{
		{SpaceID: "crypto", LogicalAccountID: "la-1", Exchange: "binance", Quantity: "10", Paper: true, Status: "ENABLED"},
		{SpaceID: "crypto", LogicalAccountID: "la-1", Exchange: "binance", Quantity: "0.0001", Dust: true, Status: "ERROR", Note: "取不到报价"},
	}, holders.Holders, "响应里没有交易所，取自请求；账户状态和标注带回")
	require.Equal(t, []action.SkippedAccount{{TradingAccountID: "ta-9", Reason: "资金设置读不出来"}}, holders.Skipped)
	require.Equal(t, []string{"计价币不是结算币，清仓不会卖它"}, holders.Notes)
	require.False(t, holders.InstrumentUnknown)

	blocked, err := client.ListBlockedInstruments(context.Background())
	require.NoError(t, err)
	require.Equal(t, []action.InstrumentRef{{Exchange: "okx", Market: "SWAP", ExchangeSymbol: "FOO-USDT-SWAP"}}, blocked)

	_, err = client.BlockInstrument(context.Background(), action.InstrumentRef{Exchange: "kraken", Market: "SPOT", ExchangeSymbol: "X"}, "r", "e")
	require.ErrorIs(t, err, action.ErrInvalidRequest)
}

// 忙（busy、返回码 CONFLICT、不带动作）翻译成 ErrTradeBusy；别的 CONFLICT 不是忙。
func TestFlattenBusyAndErrors(t *testing.T) {
	gateway := &fakeGateway{responses: map[string]any{
		"FlattenInstrument": &tradepb.FlattenInstrumentRsp{RetInfo: &tradepb.RetInfo{Code: tradepb.ErrorCode_CONFLICT, Msg: "组合正忙"}, Busy: true},
	}}
	client := New(gateway)
	request := action.FlattenRequest{ActionID: "a1", SpaceID: "crypto", LogicalAccountID: "la-1", Instrument: foo, Reason: "r"}
	_, err := client.FlattenInstrument(context.Background(), request)
	require.ErrorIs(t, err, action.ErrTradeBusy)

	gateway.responses["FlattenInstrument"] = &tradepb.FlattenInstrumentRsp{RetInfo: &tradepb.RetInfo{Code: tradepb.ErrorCode_CONFLICT, Msg: "同一个操作号的参数不一致"}}
	_, err = client.FlattenInstrument(context.Background(), request)
	require.Error(t, err)
	require.NotErrorIs(t, err, action.ErrTradeBusy)
	require.Contains(t, err.Error(), "同一个操作号的参数不一致")

	gateway.responses["FlattenInstrument"] = &tradepb.FlattenInstrumentRsp{RetInfo: &tradepb.RetInfo{Code: tradepb.ErrorCode_INVALID_PARAM, Msg: "reason is required"}}
	_, err = client.FlattenInstrument(context.Background(), request)
	require.ErrorIs(t, err, action.ErrInvalidRequest)

	gateway.err = errors.New("网关不可达")
	_, err = client.FlattenInstrument(context.Background(), request)
	require.EqualError(t, err, "网关不可达")
}

// 已解除禁止：响应的 unblocked 或结果 JSON 顶层的 unblocked；PARTIAL 的说明带上失败原因和剩余仓位。
func TestOperatorActionTranslation(t *testing.T) {
	gateway := &fakeGateway{responses: map[string]any{
		"FlattenInstrument": &tradepb.FlattenInstrumentRsp{RetInfo: ok(), Unblocked: true, Action: &tradepb.OperatorAction{ActionId: "a1", Status: "COMPLETED"}},
		"GetOperatorAction": &tradepb.GetOperatorActionRsp{RetInfo: ok(), Action: &tradepb.OperatorAction{
			ActionId: "a1", Status: "PARTIAL", LastError: "最近一笔被交易所拒绝",
			ResultJson: `{"unblocked":true,"accounts":[{"trading_account_id":"ta-1","error":"最近一笔被交易所拒绝","remaining_positions":[{"quantity":"3","reason":"套利底仓对冲腿下架，需人工处理"}]}]}`,
		}},
	}}
	client := New(gateway)
	flatten, err := client.FlattenInstrument(context.Background(), action.FlattenRequest{ActionID: "a1", SpaceID: "crypto", LogicalAccountID: "la-1", Instrument: foo, Reason: "r"})
	require.NoError(t, err)
	require.True(t, flatten.Unblocked)
	require.Equal(t, action.OperatorCompleted, flatten.Status)

	got, err := client.GetOperatorAction(context.Background(), "crypto", "a1")
	require.NoError(t, err)
	require.Equal(t, action.OperatorPartial, got.Status)
	require.True(t, got.Unblocked, "结果 JSON 顶层的 unblocked")
	require.Contains(t, got.Message, "最近一笔被交易所拒绝")
	require.Contains(t, got.Message, "套利底仓对冲腿下架")
}

func TestUnconfiguredClient(t *testing.T) {
	_, err := New(nil).ListBlockedInstruments(context.Background())
	require.ErrorContains(t, err, "未配置")
}
