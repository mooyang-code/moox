// Package tradegenproto 在 trade 主模块内执行 proto/tradegen 覆盖测试。
// proto/tradegen 为独立 go.mod，标准 ./... 不会执行其子模块测试，故在此补测。
package tradegenproto

import (
	"context"
	"reflect"
	"testing"

	mooxpb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-go/server"
)

func noopFilter(req interface{}) (filter.ServerChain, error) {
	return filter.ServerChain{filter.NoopServerFilter}, nil
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
	account := &mooxpb.UnimplementedAccountSvc{}
	balance := &mooxpb.UnimplementedBalanceSvc{}
	fund := &mooxpb.UnimplementedFundSvc{}
	apiKey := &mooxpb.UnimplementedApiKeySvc{}
	channel := &mooxpb.UnimplementedChannelSvc{}
	tradeOp := &mooxpb.UnimplementedTradeOpSvc{}
	order := &mooxpb.UnimplementedOrderSvc{}
	query := &mooxpb.UnimplementedTradeQuerySvc{}
	position := &mooxpb.UnimplementedPositionSvc{}
	rebalance := &mooxpb.UnimplementedRebalanceSvc{}
	tradeOps := &mooxpb.UnimplementedTradeOpsSvc{}

	cases := []struct {
		name    string
		handler interface{}
		svr     interface{}
	}{
		{"CreateAccount", mooxpb.AccountSvcService_CreateAccount_Handler, account},
		{"UpdateAccount", mooxpb.AccountSvcService_UpdateAccount_Handler, account},
		{"DeleteAccount", mooxpb.AccountSvcService_DeleteAccount_Handler, account},
		{"GetAccount", mooxpb.AccountSvcService_GetAccount_Handler, account},
		{"ListAccounts", mooxpb.AccountSvcService_ListAccounts_Handler, account},
		{"SyncExchangeAccounts", mooxpb.AccountSvcService_SyncExchangeAccounts_Handler, account},
		{"GetBalances", mooxpb.BalanceSvcService_GetBalances_Handler, balance},
		{"SyncBalances", mooxpb.BalanceSvcService_SyncBalances_Handler, balance},
		{"ListFundFlows", mooxpb.FundSvcService_ListFundFlows_Handler, fund},
		{"Transfer", mooxpb.FundSvcService_Transfer_Handler, fund},
		{"CreateApiKey", mooxpb.ApiKeySvcService_CreateApiKey_Handler, apiKey},
		{"DeleteApiKey", mooxpb.ApiKeySvcService_DeleteApiKey_Handler, apiKey},
		{"ListApiKeys", mooxpb.ApiKeySvcService_ListApiKeys_Handler, apiKey},
		{"CreateChannel", mooxpb.ChannelSvcService_CreateChannel_Handler, channel},
		{"UpdateChannel", mooxpb.ChannelSvcService_UpdateChannel_Handler, channel},
		{"DeleteChannel", mooxpb.ChannelSvcService_DeleteChannel_Handler, channel},
		{"ListChannels", mooxpb.ChannelSvcService_ListChannels_Handler, channel},
		{"TestChannel", mooxpb.ChannelSvcService_TestChannel_Handler, channel},
		{"ListInstruments", mooxpb.ChannelSvcService_ListInstruments_Handler, channel},
		{"PlaceOrder", mooxpb.TradeOpSvcService_PlaceOrder_Handler, tradeOp},
		{"CancelOrder", mooxpb.TradeOpSvcService_CancelOrder_Handler, tradeOp},
		{"CancelAllOrders", mooxpb.TradeOpSvcService_CancelAllOrders_Handler, tradeOp},
		{"AmendOrder", mooxpb.TradeOpSvcService_AmendOrder_Handler, tradeOp},
		{"SetLeverage", mooxpb.TradeOpSvcService_SetLeverage_Handler, tradeOp},
		{"ConvertDust", mooxpb.TradeOpSvcService_ConvertDust_Handler, tradeOp},
		{"GetOrder", mooxpb.OrderSvcService_GetOrder_Handler, order},
		{"ListOrders", mooxpb.OrderSvcService_ListOrders_Handler, order},
		{"SyncOrders", mooxpb.OrderSvcService_SyncOrders_Handler, order},
		{"ListTrades", mooxpb.TradeQuerySvcService_ListTrades_Handler, query},
		{"SyncTrades", mooxpb.TradeQuerySvcService_SyncTrades_Handler, query},
		{"ListPositions", mooxpb.PositionSvcService_ListPositions_Handler, position},
		{"SyncPositions", mooxpb.PositionSvcService_SyncPositions_Handler, position},
		{"CreateRebalance", mooxpb.RebalanceSvcService_CreateRebalance_Handler, rebalance},
		{"AdvanceRebalance", mooxpb.RebalanceSvcService_AdvanceRebalance_Handler, rebalance},
		{"SetPause", mooxpb.TradeOpsSvcService_SetPause_Handler, tradeOps},
		{"ReconcileNow", mooxpb.TradeOpsSvcService_ReconcileNow_Handler, tradeOps},
		{"InspectSaga", mooxpb.TradeOpsSvcService_InspectSaga_Handler, tradeOps},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dispatchHandler(t, tc.handler, tc.svr)
		})
	}
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

func TestUnimplementedTradeServices_ShouldReturnErrors(t *testing.T) {
	ctx := context.Background()
	services := []interface{}{
		&mooxpb.UnimplementedAccountSvc{},
		&mooxpb.UnimplementedBalanceSvc{},
		&mooxpb.UnimplementedFundSvc{},
		&mooxpb.UnimplementedApiKeySvc{},
		&mooxpb.UnimplementedChannelSvc{},
		&mooxpb.UnimplementedTradeOpSvc{},
		&mooxpb.UnimplementedOrderSvc{},
		&mooxpb.UnimplementedTradeQuerySvc{},
		&mooxpb.UnimplementedPositionSvc{},
		&mooxpb.UnimplementedRebalanceSvc{},
		&mooxpb.UnimplementedTradeOpsSvc{},
	}
	for _, svc := range services {
		t.Run(reflect.TypeOf(svc).Elem().Name(), func(t *testing.T) {
			callAllRPCMethods(t, svc)
		})
	}
	_, err := (&mooxpb.UnimplementedAccountSvc{}).CreateAccount(ctx, &mooxpb.CreateAccountReq{})
	assert.Error(t, err)
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
		mooxpb.RegisterAccountSvcService(s, &mooxpb.UnimplementedAccountSvc{})
		mooxpb.RegisterBalanceSvcService(s, &mooxpb.UnimplementedBalanceSvc{})
		mooxpb.RegisterFundSvcService(s, &mooxpb.UnimplementedFundSvc{})
		mooxpb.RegisterApiKeySvcService(s, &mooxpb.UnimplementedApiKeySvc{})
		mooxpb.RegisterChannelSvcService(s, &mooxpb.UnimplementedChannelSvc{})
		mooxpb.RegisterTradeOpSvcService(s, &mooxpb.UnimplementedTradeOpSvc{})
		mooxpb.RegisterOrderSvcService(s, &mooxpb.UnimplementedOrderSvc{})
		mooxpb.RegisterTradeQuerySvcService(s, &mooxpb.UnimplementedTradeQuerySvc{})
		mooxpb.RegisterPositionSvcService(s, &mooxpb.UnimplementedPositionSvc{})
		mooxpb.RegisterRebalanceSvcService(s, &mooxpb.UnimplementedRebalanceSvc{})
		mooxpb.RegisterTradeOpsSvcService(s, &mooxpb.UnimplementedTradeOpsSvc{})
	})
	assert.True(t, s.registered)
}

func TestClientProxies_AllMethods_ShouldFailWithoutBackend(t *testing.T) {
	ctx := context.Background()
	calls := []func() error{
		func() error { _, err := mooxpb.NewAccountSvcClientProxy().CreateAccount(ctx, &mooxpb.CreateAccountReq{}); return err },
		func() error { _, err := mooxpb.NewBalanceSvcClientProxy().GetBalances(ctx, &mooxpb.GetBalancesReq{}); return err },
		func() error { _, err := mooxpb.NewFundSvcClientProxy().ListFundFlows(ctx, &mooxpb.ListFundFlowsReq{}); return err },
		func() error { _, err := mooxpb.NewApiKeySvcClientProxy().ListApiKeys(ctx, &mooxpb.ListApiKeysReq{}); return err },
		func() error { _, err := mooxpb.NewChannelSvcClientProxy().ListChannels(ctx, &mooxpb.ListChannelsReq{}); return err },
		func() error { _, err := mooxpb.NewTradeOpSvcClientProxy().PlaceOrder(ctx, &mooxpb.PlaceOrderReq{}); return err },
		func() error { _, err := mooxpb.NewOrderSvcClientProxy().ListOrders(ctx, &mooxpb.ListOrdersReq{}); return err },
		func() error { _, err := mooxpb.NewTradeQuerySvcClientProxy().ListTrades(ctx, &mooxpb.ListTradesReq{}); return err },
		func() error { _, err := mooxpb.NewPositionSvcClientProxy().ListPositions(ctx, &mooxpb.ListPositionsReq{}); return err },
		func() error { _, err := mooxpb.NewRebalanceSvcClientProxy().CreateRebalance(ctx, &mooxpb.CreateRebalanceReq{}); return err },
		func() error { _, err := mooxpb.NewTradeOpsSvcClientProxy().SetPause(ctx, &mooxpb.SetTradePauseReq{}); return err },
	}
	for i, call := range calls {
		assert.Error(t, call(), "call %d should fail without backend", i)
	}
}

func TestTypedProtoMessages_GettersShouldBeSafe(t *testing.T) {
	messages := []interface{}{
		&mooxpb.Account{}, &mooxpb.Balance{}, &mooxpb.FundFlow{}, &mooxpb.ApiKey{},
		&mooxpb.CreateAccountReq{}, &mooxpb.CreateAccountRsp{}, &mooxpb.UpdateAccountReq{}, &mooxpb.UpdateAccountRsp{},
		&mooxpb.DeleteAccountReq{}, &mooxpb.DeleteAccountRsp{}, &mooxpb.GetAccountReq{}, &mooxpb.GetAccountRsp{},
		&mooxpb.ListAccountsReq{}, &mooxpb.ListAccountsRsp{}, &mooxpb.SyncExchangeAccountsReq{}, &mooxpb.SyncExchangeAccountsRsp{},
		&mooxpb.GetBalancesReq{}, &mooxpb.GetBalancesRsp{}, &mooxpb.SyncBalancesReq{}, &mooxpb.SyncBalancesRsp{},
		&mooxpb.ListFundFlowsReq{}, &mooxpb.ListFundFlowsRsp{}, &mooxpb.TransferReq{}, &mooxpb.TransferRsp{},
		&mooxpb.CreateApiKeyReq{}, &mooxpb.CreateApiKeyRsp{}, &mooxpb.DeleteApiKeyReq{}, &mooxpb.DeleteApiKeyRsp{},
		&mooxpb.ListApiKeysReq{}, &mooxpb.ListApiKeysRsp{}, &mooxpb.TradeChannel{}, &mooxpb.Instrument{},
		&mooxpb.Order{}, &mooxpb.Trade{}, &mooxpb.Position{}, &mooxpb.CreateChannelReq{}, &mooxpb.CreateChannelRsp{},
		&mooxpb.UpdateChannelReq{}, &mooxpb.UpdateChannelRsp{}, &mooxpb.DeleteChannelReq{}, &mooxpb.DeleteChannelRsp{},
		&mooxpb.ListChannelsReq{}, &mooxpb.ListChannelsRsp{}, &mooxpb.TestChannelReq{}, &mooxpb.TestChannelRsp{},
		&mooxpb.ListInstrumentsReq{}, &mooxpb.ListInstrumentsRsp{}, &mooxpb.PlaceOrderReq{}, &mooxpb.PlaceOrderRsp{},
		&mooxpb.CancelOrderReq{}, &mooxpb.CancelOrderRsp{}, &mooxpb.CancelAllOrdersReq{}, &mooxpb.CancelAllOrdersRsp{},
		&mooxpb.AmendOrderReq{}, &mooxpb.AmendOrderRsp{}, &mooxpb.SetLeverageReq{}, &mooxpb.SetLeverageRsp{},
		&mooxpb.DustTransferItem{}, &mooxpb.DustTransferSkippedItem{}, &mooxpb.ConvertDustReq{}, &mooxpb.ConvertDustRsp{},
		&mooxpb.GetOrderReq{}, &mooxpb.GetOrderRsp{}, &mooxpb.ListOrdersReq{}, &mooxpb.ListOrdersRsp{},
		&mooxpb.SyncOrdersReq{}, &mooxpb.SyncOrdersRsp{}, &mooxpb.ListTradesReq{}, &mooxpb.ListTradesRsp{},
		&mooxpb.SyncTradesReq{}, &mooxpb.SyncTradesRsp{}, &mooxpb.ListPositionsReq{}, &mooxpb.ListPositionsRsp{},
		&mooxpb.SyncPositionsReq{}, &mooxpb.SyncPositionsRsp{}, &mooxpb.TargetPosition{}, &mooxpb.CurrentPosition{},
		&mooxpb.RebalanceMarket{}, &mooxpb.CreateRebalanceReq{}, &mooxpb.CreateRebalanceRsp{}, &mooxpb.AdvanceRebalanceReq{},
		&mooxpb.AdvanceRebalanceRsp{}, &mooxpb.SetTradePauseReq{}, &mooxpb.SetTradePauseRsp{}, &mooxpb.ReconcileNowReq{},
		&mooxpb.ReconcileNowRsp{}, &mooxpb.InspectSagaReq{}, &mooxpb.InspectSagaRsp{},
	}
	for _, msg := range messages {
		m := msg.(proto.Message)
		invokeGetters(t, m)
		exerciseProtoMessage(t, m)
	}
}

func exerciseProtoMessage(t *testing.T, msg proto.Message) {
	t.Helper()
	if exerciser, ok := msg.(interface {
		Reset()
		String() string
		ProtoMessage()
	}); ok {
		exerciser.Reset()
		_ = exerciser.String()
		exerciser.ProtoMessage()
	}
}

func TestGeneratedProtoMessages_GettersShouldBeSafe(t *testing.T) {
	if mooxpb.File_trade_service_proto == nil {
		t.Skip("trade proto file descriptor unavailable")
	}
	for i := 0; i < mooxpb.File_trade_service_proto.Messages().Len(); i++ {
		msg := dynamicpb.NewMessage(mooxpb.File_trade_service_proto.Messages().Get(i))
		invokeGetters(t, msg)
	}
}

func invokeGetters(t *testing.T, msg proto.Message) {
	t.Helper()
	rv := reflect.ValueOf(msg)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	rt := rv.Type()
	for i := 0; i < rt.NumMethod(); i++ {
		m := rt.Method(i)
		if len(m.Name) < 3 || m.Name[:3] != "Get" {
			continue
		}
		if m.Type.NumIn() != 1 || m.Type.NumOut() == 0 {
			continue
		}
		m.Func.Call([]reflect.Value{rv})
	}
}

func TestGeneratedProtoEnums_MethodsShouldBeSafe(t *testing.T) {
	accountType := mooxpb.AccountType_ACCOUNT_TYPE_MARGIN
	assert.Equal(t, accountType, *accountType.Enum())
	assert.NotEmpty(t, accountType.String())
	assert.NotNil(t, accountType.Descriptor())

	orderStatus := mooxpb.OrderStatus_ORDER_STATUS_FILLED
	assert.Equal(t, orderStatus, *orderStatus.Enum())
	assert.NotEmpty(t, orderStatus.String())
	assert.Equal(t, protoreflect.EnumNumber(orderStatus), orderStatus.Number())

	channelStatus := mooxpb.ChannelStatus_CHANNEL_STATUS_ONLINE
	assert.NotEmpty(t, channelStatus.String())
	assert.NotNil(t, channelStatus.Descriptor())
}

func TestGeneratedProtoMessagesViaReflect_ShouldMarshal(t *testing.T) {
	if mooxpb.File_trade_service_proto == nil {
		t.Skip("trade proto file descriptor unavailable")
	}
	for i := 0; i < mooxpb.File_trade_service_proto.Messages().Len(); i++ {
		populateDynamicMessage(t, dynamicpb.NewMessage(mooxpb.File_trade_service_proto.Messages().Get(i)))
	}
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
