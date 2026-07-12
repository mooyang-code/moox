package mooxpb

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-go/server"
)

func noopFilter(req interface{}) (filter.ServerChain, error) {
	return filter.ServerChain{filter.NoopServerFilter}, nil
}

func exerciseMessage(t *testing.T, msg interface {
	Reset()
	String() string
	ProtoMessage()
}) {
	t.Helper()
	assert.NotPanics(t, func() {
		_ = msg.String()
	})
	msg.Reset()
	assert.NotPanics(t, func() {
		_ = msg.String()
	})
	msg.ProtoMessage()
}

func TestProtoMessages_ShouldSupportResetAndString(t *testing.T) {
	exerciseMessage(t, &Account{})
	exerciseMessage(t, &CreateAccountReq{AccountName: "main"})
	exerciseMessage(t, &CreateAccountRsp{RetInfo: &RetInfo{Code: ErrorCode_SUCCESS}})
	exerciseMessage(t, &Page{Page: 1, Size: 10})
	exerciseMessage(t, &PlaceOrderReq{Symbol: "BTCUSDT", Quantity: "1"})
	exerciseMessage(t, &PlaceOrderRsp{RetInfo: &RetInfo{}})
	exerciseMessage(t, &Order{OrderId: "o-1"})
	exerciseMessage(t, &TradeChannel{ChannelName: "default"})
	exerciseMessage(t, &TargetPosition{Symbol: "BTCUSDT", Quantity: "1"})
	exerciseMessage(t, &CreateRebalanceReq{RunId: "run-1"})
	exerciseMessage(t, &ReconcileNowReq{})
}

func TestGeneratedProtoMessagesViaReflect_ShouldMarshal(t *testing.T) {
	if File_trade_service_proto == nil {
		t.Skip("trade proto file descriptor unavailable")
	}
	for i := 0; i < File_trade_service_proto.Messages().Len(); i++ {
		populateDynamicMessage(t, dynamicpb.NewMessage(File_trade_service_proto.Messages().Get(i)))
	}
}

func TestGeneratedProtoMessages_GettersShouldBeSafe(t *testing.T) {
	messages := []interface{}{
		&Account{}, &Balance{}, &FundFlow{}, &ApiKey{},
		&CreateAccountReq{}, &CreateAccountRsp{}, &UpdateAccountReq{}, &UpdateAccountRsp{},
		&DeleteAccountReq{}, &DeleteAccountRsp{}, &GetAccountReq{}, &GetAccountRsp{},
		&ListAccountsReq{}, &ListAccountsRsp{}, &SyncExchangeAccountsReq{}, &SyncExchangeAccountsRsp{},
		&GetBalancesReq{}, &GetBalancesRsp{}, &SyncBalancesReq{}, &SyncBalancesRsp{},
		&ListFundFlowsReq{}, &ListFundFlowsRsp{}, &TransferReq{}, &TransferRsp{},
		&CreateApiKeyReq{}, &CreateApiKeyRsp{}, &DeleteApiKeyReq{}, &DeleteApiKeyRsp{},
		&ListApiKeysReq{}, &ListApiKeysRsp{}, &TradeChannel{}, &Instrument{},
		&Order{}, &Trade{}, &Position{}, &CreateChannelReq{}, &CreateChannelRsp{},
		&UpdateChannelReq{}, &UpdateChannelRsp{}, &DeleteChannelReq{}, &DeleteChannelRsp{},
		&ListChannelsReq{}, &ListChannelsRsp{}, &TestChannelReq{}, &TestChannelRsp{},
		&ListInstrumentsReq{}, &ListInstrumentsRsp{}, &PlaceOrderReq{}, &PlaceOrderRsp{},
		&CancelOrderReq{}, &CancelOrderRsp{}, &CancelAllOrdersReq{}, &CancelAllOrdersRsp{},
		&AmendOrderReq{}, &AmendOrderRsp{}, &SetLeverageReq{}, &SetLeverageRsp{},
		&DustTransferItem{}, &DustTransferSkippedItem{}, &ConvertDustReq{}, &ConvertDustRsp{},
		&GetOrderReq{}, &GetOrderRsp{}, &ListOrdersReq{}, &ListOrdersRsp{},
		&SyncOrdersReq{}, &SyncOrdersRsp{}, &ListTradesReq{}, &ListTradesRsp{},
		&SyncTradesReq{}, &SyncTradesRsp{}, &ListPositionsReq{}, &ListPositionsRsp{},
		&SyncPositionsReq{}, &SyncPositionsRsp{}, &TargetPosition{}, &CurrentPosition{},
		&RebalanceMarket{}, &CreateRebalanceReq{}, &CreateRebalanceRsp{}, &AdvanceRebalanceReq{},
		&AdvanceRebalanceRsp{}, &SetTradePauseReq{}, &SetTradePauseRsp{}, &ReconcileNowReq{},
		&ReconcileNowRsp{}, &InspectSagaReq{}, &InspectSagaRsp{},
	}

	for _, msg := range messages {
		v := reflect.ValueOf(msg)
		typ := v.Type()
		for i := 0; i < typ.NumMethod(); i++ {
			method := typ.Method(i)
			if method.Type.NumIn() != 1 {
				continue
			}
			assert.NotPanics(t, func() {
				method.Func.Call([]reflect.Value{v})
			}, "%s.%s", typ.Elem().Name(), method.Name)
		}
	}
}

func TestGeneratedProtoEnums_MethodsShouldBeSafe(t *testing.T) {
	accountType := AccountType_ACCOUNT_TYPE_MARGIN
	assert.Equal(t, accountType, *accountType.Enum())
	assert.NotEmpty(t, accountType.String())
	assert.NotNil(t, accountType.Descriptor())
	assert.NotNil(t, accountType.Type())
	assert.Equal(t, protoreflect.EnumNumber(accountType), accountType.Number())
	raw, path := accountType.EnumDescriptor()
	assert.NotEmpty(t, raw)
	assert.Equal(t, []int{0}, path)

	accountStatus := AccountStatus_ACCOUNT_STATUS_FROZEN
	assert.Equal(t, accountStatus, *accountStatus.Enum())
	assert.NotEmpty(t, accountStatus.String())
	assert.NotNil(t, accountStatus.Descriptor())
	assert.NotNil(t, accountStatus.Type())
	assert.Equal(t, protoreflect.EnumNumber(accountStatus), accountStatus.Number())

	marketType := MarketType_MARKET_TYPE_SWAP
	assert.Equal(t, marketType, *marketType.Enum())
	assert.NotEmpty(t, marketType.String())
	assert.NotNil(t, marketType.Descriptor())
	assert.NotNil(t, marketType.Type())
	assert.Equal(t, protoreflect.EnumNumber(marketType), marketType.Number())

	orderSide := OrderSide_ORDER_SIDE_SELL
	assert.Equal(t, orderSide, *orderSide.Enum())
	assert.NotEmpty(t, orderSide.String())
	assert.NotNil(t, orderSide.Descriptor())
	assert.NotNil(t, orderSide.Type())
	assert.Equal(t, protoreflect.EnumNumber(orderSide), orderSide.Number())

	orderType := OrderType_ORDER_TYPE_IOC
	assert.Equal(t, orderType, *orderType.Enum())
	assert.NotEmpty(t, orderType.String())
	assert.NotNil(t, orderType.Descriptor())
	assert.NotNil(t, orderType.Type())
	assert.Equal(t, protoreflect.EnumNumber(orderType), orderType.Number())

	orderStatus := OrderStatus_ORDER_STATUS_FILLED
	assert.Equal(t, orderStatus, *orderStatus.Enum())
	assert.NotEmpty(t, orderStatus.String())
	assert.NotNil(t, orderStatus.Descriptor())
	assert.NotNil(t, orderStatus.Type())
	assert.Equal(t, protoreflect.EnumNumber(orderStatus), orderStatus.Number())

	channelStatus := ChannelStatus_CHANNEL_STATUS_ONLINE
	assert.Equal(t, channelStatus, *channelStatus.Enum())
	assert.NotEmpty(t, channelStatus.String())
	assert.NotNil(t, channelStatus.Descriptor())
	assert.NotNil(t, channelStatus.Type())
	assert.Equal(t, protoreflect.EnumNumber(channelStatus), channelStatus.Number())
}

func populateDynamicMessage(t *testing.T, msg *dynamicpb.Message) {
	t.Helper()
	pr := msg.ProtoReflect()
	_, err := proto.Marshal(msg)
	require.NoError(t, err)
	for i := 0; i < pr.Descriptor().Fields().Len(); i++ {
		fd := pr.Descriptor().Fields().Get(i)
		switch {
		case fd.IsMap() && fd.MapKey().Kind() == protoreflect.StringKind:
			pr.Mutable(fd).Map().Set(
				protoreflect.MapKey(protoreflect.ValueOfString("k")),
				protoreflect.ValueOfString("v"),
			)
		case fd.IsList() && fd.Kind() == protoreflect.StringKind:
			pr.Mutable(fd).List().Append(protoreflect.ValueOfString("item"))
		case fd.Message() != nil && !fd.IsList() && !fd.IsMap():
			pr.Set(fd, protoreflect.ValueOfMessage(dynamicpb.NewMessage(fd.Message())))
		default:
			setScalarField(pr, fd)
		}
		_ = pr.Get(fd)
	}
	_, err = proto.Marshal(msg)
	require.NoError(t, err)
}

func setScalarField(pr protoreflect.Message, fd protoreflect.FieldDescriptor) {
	switch fd.Kind() {
	case protoreflect.StringKind:
		pr.Set(fd, protoreflect.ValueOfString("test"))
	case protoreflect.BoolKind:
		pr.Set(fd, protoreflect.ValueOfBool(true))
	case protoreflect.EnumKind:
		if fd.Enum().Values().Len() > 0 {
			pr.Set(fd, protoreflect.ValueOfEnum(fd.Enum().Values().Get(0).Number()))
		}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		pr.Set(fd, protoreflect.ValueOfInt32(1))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		pr.Set(fd, protoreflect.ValueOfInt64(1))
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		pr.Set(fd, protoreflect.ValueOfUint32(1))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		pr.Set(fd, protoreflect.ValueOfUint64(1))
	case protoreflect.FloatKind:
		pr.Set(fd, protoreflect.ValueOfFloat32(1))
	case protoreflect.DoubleKind:
		pr.Set(fd, protoreflect.ValueOfFloat64(1))
	case protoreflect.BytesKind:
		pr.Set(fd, protoreflect.ValueOfBytes([]byte("x")))
	}
}

func dispatchHandler(t *testing.T, handler interface{}, svr interface{}) {
	t.Helper()
	hv := reflect.ValueOf(handler)
	require.Equal(t, reflect.Func, hv.Kind())
	out := hv.Call([]reflect.Value{
		reflect.ValueOf(svr),
		reflect.ValueOf(context.Background()),
		reflect.ValueOf(server.FilterFunc(noopFilter)),
	})
	require.Len(t, out, 2)
	if err, ok := out[1].Interface().(error); ok && err != nil {
		assert.Error(t, err)
	}
}

func TestAllTradeServiceHandlers_ShouldExecute(t *testing.T) {
	ctx := context.Background()
	_ = ctx
	account := &UnimplementedAccountSvc{}
	balance := &UnimplementedBalanceSvc{}
	fund := &UnimplementedFundSvc{}
	apiKey := &UnimplementedApiKeySvc{}
	channel := &UnimplementedChannelSvc{}
	tradeOp := &UnimplementedTradeOpSvc{}
	order := &UnimplementedOrderSvc{}
	query := &UnimplementedTradeQuerySvc{}
	position := &UnimplementedPositionSvc{}
	rebalance := &UnimplementedRebalanceSvc{}
	tradeOps := &UnimplementedTradeOpsSvc{}

	cases := []struct {
		name    string
		handler interface{}
		svr     interface{}
	}{
		{"CreateAccount", AccountSvcService_CreateAccount_Handler, account},
		{"UpdateAccount", AccountSvcService_UpdateAccount_Handler, account},
		{"DeleteAccount", AccountSvcService_DeleteAccount_Handler, account},
		{"GetAccount", AccountSvcService_GetAccount_Handler, account},
		{"ListAccounts", AccountSvcService_ListAccounts_Handler, account},
		{"SyncExchangeAccounts", AccountSvcService_SyncExchangeAccounts_Handler, account},
		{"GetBalances", BalanceSvcService_GetBalances_Handler, balance},
		{"SyncBalances", BalanceSvcService_SyncBalances_Handler, balance},
		{"ListFundFlows", FundSvcService_ListFundFlows_Handler, fund},
		{"Transfer", FundSvcService_Transfer_Handler, fund},
		{"CreateApiKey", ApiKeySvcService_CreateApiKey_Handler, apiKey},
		{"DeleteApiKey", ApiKeySvcService_DeleteApiKey_Handler, apiKey},
		{"ListApiKeys", ApiKeySvcService_ListApiKeys_Handler, apiKey},
		{"CreateChannel", ChannelSvcService_CreateChannel_Handler, channel},
		{"UpdateChannel", ChannelSvcService_UpdateChannel_Handler, channel},
		{"DeleteChannel", ChannelSvcService_DeleteChannel_Handler, channel},
		{"ListChannels", ChannelSvcService_ListChannels_Handler, channel},
		{"TestChannel", ChannelSvcService_TestChannel_Handler, channel},
		{"ListInstruments", ChannelSvcService_ListInstruments_Handler, channel},
		{"PlaceOrder", TradeOpSvcService_PlaceOrder_Handler, tradeOp},
		{"CancelOrder", TradeOpSvcService_CancelOrder_Handler, tradeOp},
		{"CancelAllOrders", TradeOpSvcService_CancelAllOrders_Handler, tradeOp},
		{"AmendOrder", TradeOpSvcService_AmendOrder_Handler, tradeOp},
		{"SetLeverage", TradeOpSvcService_SetLeverage_Handler, tradeOp},
		{"ConvertDust", TradeOpSvcService_ConvertDust_Handler, tradeOp},
		{"GetOrder", OrderSvcService_GetOrder_Handler, order},
		{"ListOrders", OrderSvcService_ListOrders_Handler, order},
		{"SyncOrders", OrderSvcService_SyncOrders_Handler, order},
		{"ListTrades", TradeQuerySvcService_ListTrades_Handler, query},
		{"SyncTrades", TradeQuerySvcService_SyncTrades_Handler, query},
		{"ListPositions", PositionSvcService_ListPositions_Handler, position},
		{"SyncPositions", PositionSvcService_SyncPositions_Handler, position},
		{"CreateRebalance", RebalanceSvcService_CreateRebalance_Handler, rebalance},
		{"AdvanceRebalance", RebalanceSvcService_AdvanceRebalance_Handler, rebalance},
		{"SetPause", TradeOpsSvcService_SetPause_Handler, tradeOps},
		{"ReconcileNow", TradeOpsSvcService_ReconcileNow_Handler, tradeOps},
		{"InspectSaga", TradeOpsSvcService_InspectSaga_Handler, tradeOps},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dispatchHandler(t, tc.handler, tc.svr)
		})
	}
}

func TestUnimplementedTradeServices_ShouldReturnErrors(t *testing.T) {
	ctx := context.Background()
	services := []interface{}{
		&UnimplementedAccountSvc{},
		&UnimplementedBalanceSvc{},
		&UnimplementedFundSvc{},
		&UnimplementedApiKeySvc{},
		&UnimplementedChannelSvc{},
		&UnimplementedTradeOpSvc{},
		&UnimplementedOrderSvc{},
		&UnimplementedTradeQuerySvc{},
		&UnimplementedPositionSvc{},
		&UnimplementedRebalanceSvc{},
		&UnimplementedTradeOpsSvc{},
	}
	for _, svc := range services {
		t.Run(reflect.TypeOf(svc).Elem().Name(), func(t *testing.T) {
			callAllRPCMethods(t, svc)
		})
	}
	_, err := (&UnimplementedAccountSvc{}).CreateAccount(ctx, &CreateAccountReq{})
	assert.Error(t, err)
}

func callAllRPCMethods(t *testing.T, svc interface{}) {
	t.Helper()
	typ := reflect.TypeOf(svc)
	for i := 0; i < typ.NumMethod(); i++ {
		method := typ.Method(i)
		if method.Type.NumIn() != 3 || method.Type.NumOut() != 2 {
			continue
		}
		reqType := method.Type.In(2)
		if reqType.Kind() != reflect.Ptr || reqType.Elem().Kind() != reflect.Struct {
			continue
		}
		req := reflect.New(reqType.Elem())
		out := method.Func.Call([]reflect.Value{reflect.ValueOf(svc), reflect.ValueOf(context.Background()), req})
		err, ok := out[1].Interface().(error)
		require.True(t, ok, "%s should return error", method.Name)
		require.Error(t, err, method.Name)
	}
}

type fakeTRPCService struct{ registered bool }

func (f *fakeTRPCService) Register(serviceDesc interface{}, serviceImpl interface{}) error {
	f.registered = true
	return nil
}
func (f *fakeTRPCService) Serve() error              { return nil }
func (f *fakeTRPCService) Close(chan struct{}) error { return nil }

func TestRegisterTradeServices_ShouldRegisterWithoutPanic(t *testing.T) {
	s := &fakeTRPCService{}
	require.NotPanics(t, func() {
		RegisterAccountSvcService(s, &UnimplementedAccountSvc{})
		RegisterBalanceSvcService(s, &UnimplementedBalanceSvc{})
		RegisterFundSvcService(s, &UnimplementedFundSvc{})
		RegisterApiKeySvcService(s, &UnimplementedApiKeySvc{})
		RegisterChannelSvcService(s, &UnimplementedChannelSvc{})
		RegisterTradeOpSvcService(s, &UnimplementedTradeOpSvc{})
		RegisterOrderSvcService(s, &UnimplementedOrderSvc{})
		RegisterTradeQuerySvcService(s, &UnimplementedTradeQuerySvc{})
		RegisterPositionSvcService(s, &UnimplementedPositionSvc{})
		RegisterRebalanceSvcService(s, &UnimplementedRebalanceSvc{})
		RegisterTradeOpsSvcService(s, &UnimplementedTradeOpsSvc{})
	})
	assert.True(t, s.registered)
}

type fakeClient struct{ calls int }

func (f *fakeClient) Invoke(ctx context.Context, reqBody interface{}, rspBody interface{}, opt ...client.Option) error {
	f.calls++
	return nil
}

func TestClientProxies_AllMethods_ShouldInvoke(t *testing.T) {
	ctx := context.Background()
	fc := &fakeClient{}
	calls := []func() (interface{}, error){
		func() (interface{}, error) {
			return (&AccountSvcClientProxyImpl{client: fc}).CreateAccount(ctx, &CreateAccountReq{})
		},
		func() (interface{}, error) {
			return (&AccountSvcClientProxyImpl{client: fc}).UpdateAccount(ctx, &UpdateAccountReq{})
		},
		func() (interface{}, error) {
			return (&AccountSvcClientProxyImpl{client: fc}).DeleteAccount(ctx, &DeleteAccountReq{})
		},
		func() (interface{}, error) {
			return (&AccountSvcClientProxyImpl{client: fc}).GetAccount(ctx, &GetAccountReq{})
		},
		func() (interface{}, error) {
			return (&AccountSvcClientProxyImpl{client: fc}).ListAccounts(ctx, &ListAccountsReq{})
		},
		func() (interface{}, error) {
			return (&AccountSvcClientProxyImpl{client: fc}).SyncExchangeAccounts(ctx, &SyncExchangeAccountsReq{})
		},
		func() (interface{}, error) {
			return (&BalanceSvcClientProxyImpl{client: fc}).GetBalances(ctx, &GetBalancesReq{})
		},
		func() (interface{}, error) {
			return (&BalanceSvcClientProxyImpl{client: fc}).SyncBalances(ctx, &SyncBalancesReq{})
		},
		func() (interface{}, error) {
			return (&FundSvcClientProxyImpl{client: fc}).ListFundFlows(ctx, &ListFundFlowsReq{})
		},
		func() (interface{}, error) {
			return (&FundSvcClientProxyImpl{client: fc}).Transfer(ctx, &TransferReq{})
		},
		func() (interface{}, error) {
			return (&ApiKeySvcClientProxyImpl{client: fc}).CreateApiKey(ctx, &CreateApiKeyReq{})
		},
		func() (interface{}, error) {
			return (&ApiKeySvcClientProxyImpl{client: fc}).DeleteApiKey(ctx, &DeleteApiKeyReq{})
		},
		func() (interface{}, error) {
			return (&ApiKeySvcClientProxyImpl{client: fc}).ListApiKeys(ctx, &ListApiKeysReq{})
		},
		func() (interface{}, error) {
			return (&ChannelSvcClientProxyImpl{client: fc}).CreateChannel(ctx, &CreateChannelReq{})
		},
		func() (interface{}, error) {
			return (&ChannelSvcClientProxyImpl{client: fc}).UpdateChannel(ctx, &UpdateChannelReq{})
		},
		func() (interface{}, error) {
			return (&ChannelSvcClientProxyImpl{client: fc}).DeleteChannel(ctx, &DeleteChannelReq{})
		},
		func() (interface{}, error) {
			return (&ChannelSvcClientProxyImpl{client: fc}).ListChannels(ctx, &ListChannelsReq{})
		},
		func() (interface{}, error) {
			return (&ChannelSvcClientProxyImpl{client: fc}).TestChannel(ctx, &TestChannelReq{})
		},
		func() (interface{}, error) {
			return (&ChannelSvcClientProxyImpl{client: fc}).ListInstruments(ctx, &ListInstrumentsReq{})
		},
		func() (interface{}, error) {
			return (&TradeOpSvcClientProxyImpl{client: fc}).PlaceOrder(ctx, &PlaceOrderReq{})
		},
		func() (interface{}, error) {
			return (&TradeOpSvcClientProxyImpl{client: fc}).CancelOrder(ctx, &CancelOrderReq{})
		},
		func() (interface{}, error) {
			return (&TradeOpSvcClientProxyImpl{client: fc}).CancelAllOrders(ctx, &CancelAllOrdersReq{})
		},
		func() (interface{}, error) {
			return (&TradeOpSvcClientProxyImpl{client: fc}).AmendOrder(ctx, &AmendOrderReq{})
		},
		func() (interface{}, error) {
			return (&TradeOpSvcClientProxyImpl{client: fc}).SetLeverage(ctx, &SetLeverageReq{})
		},
		func() (interface{}, error) {
			return (&TradeOpSvcClientProxyImpl{client: fc}).ConvertDust(ctx, &ConvertDustReq{})
		},
		func() (interface{}, error) {
			return (&OrderSvcClientProxyImpl{client: fc}).GetOrder(ctx, &GetOrderReq{})
		},
		func() (interface{}, error) {
			return (&OrderSvcClientProxyImpl{client: fc}).ListOrders(ctx, &ListOrdersReq{})
		},
		func() (interface{}, error) {
			return (&OrderSvcClientProxyImpl{client: fc}).SyncOrders(ctx, &SyncOrdersReq{})
		},
		func() (interface{}, error) {
			return (&TradeQuerySvcClientProxyImpl{client: fc}).ListTrades(ctx, &ListTradesReq{})
		},
		func() (interface{}, error) {
			return (&TradeQuerySvcClientProxyImpl{client: fc}).SyncTrades(ctx, &SyncTradesReq{})
		},
		func() (interface{}, error) {
			return (&PositionSvcClientProxyImpl{client: fc}).ListPositions(ctx, &ListPositionsReq{})
		},
		func() (interface{}, error) {
			return (&PositionSvcClientProxyImpl{client: fc}).SyncPositions(ctx, &SyncPositionsReq{})
		},
		func() (interface{}, error) {
			return (&RebalanceSvcClientProxyImpl{client: fc}).CreateRebalance(ctx, &CreateRebalanceReq{})
		},
		func() (interface{}, error) {
			return (&RebalanceSvcClientProxyImpl{client: fc}).AdvanceRebalance(ctx, &AdvanceRebalanceReq{})
		},
		func() (interface{}, error) {
			return (&TradeOpsSvcClientProxyImpl{client: fc}).SetPause(ctx, &SetTradePauseReq{})
		},
		func() (interface{}, error) {
			return (&TradeOpsSvcClientProxyImpl{client: fc}).ReconcileNow(ctx, &ReconcileNowReq{})
		},
		func() (interface{}, error) {
			return (&TradeOpsSvcClientProxyImpl{client: fc}).InspectSaga(ctx, &InspectSagaReq{})
		},
	}
	for i, call := range calls {
		rsp, err := call()
		require.NoError(t, err, "call %d", i)
		assert.NotNil(t, rsp, fmt.Sprintf("call %d should return response", i))
	}
	assert.Equal(t, len(calls), fc.calls)
}
