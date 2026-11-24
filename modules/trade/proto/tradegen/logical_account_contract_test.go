package tradepb

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestTradeConsoleIsOnlyBusinessHTTPService(t *testing.T) {
	services := File_trade_service_proto.Services()
	require.NotNil(t, services.ByName("TradeConsoleService"))
	require.Nil(t, services.ByName("Exchange"+"AccountService"))
	require.Nil(t, services.ByName("TradingAccountService"))
	require.Nil(t, services.ByName("TradeExecutionService"))
	require.Nil(t, services.ByName("LogicalAccountService"))
	account := File_trade_service_proto.Messages().ByName("TradingAccount")
	require.NotNil(t, account)
	require.NotNil(t, account.Fields().ByName("trading_account_id"))
	require.NotNil(t, account.Oneofs().ByName("execution_config"))
	require.NotNil(t, account.Fields().ByName("live"))
	require.NotNil(t, account.Fields().ByName("paper"))
	for _, forbidden := range []protoreflect.Name{
		protoreflect.Name("exchange_" + "account_id"), "environment", "credential_" + "secret_id",
	} {
		require.Nil(t, account.Fields().ByName(forbidden), forbidden)
	}
}

func TestTradeProtoExposesLogicalAccountMethodsOnConsole(t *testing.T) {
	services := File_trade_service_proto.Services()
	logical := services.ByName("TradeConsoleService")
	require.NotNil(t, logical)
	for _, method := range []protoreflect.Name{
		"CreateLogicalAccount",
		"GetLogicalAccount",
		"ListLogicalAccounts",
		"UpdateLogicalAccount",
		"AddLogicalAccountMember",
		"RemoveLogicalAccountMember",
		"CloseTargetSession",
		"PauseLogicalAccount",
		"ResumeLogicalAccount",
		"FlattenLogicalAccount",
	} {
		require.NotNil(t, logical.Methods().ByName(method), method)
	}
	// 实例与组合账户的归属只记在 Strategy：Trade 没有认领、释放归属的方法，组合账户也不带归属字段。
	for _, retired := range []protoreflect.Name{"ClaimLogicalAccountOwner", "ReleaseLogicalAccountOwner"} {
		require.Nil(t, logical.Methods().ByName(retired), retired)
	}
	account := File_trade_service_proto.Messages().ByName("LogicalAccount")
	require.NotNil(t, account)
	for _, retired := range []protoreflect.Name{"owner_instance_id", "owner_session_id", "auth_fence"} {
		require.Nil(t, account.Fields().ByName(retired), retired)
	}
}

func TestPhysicalAccountRPCDoesNotExposePause(t *testing.T) {
	service := File_trade_service_proto.Services().ByName("TradeConsoleService")
	require.NotNil(t, service)
	require.NotNil(t, service.Methods().ByName("CreateTradingAccount"))
	require.Nil(t, service.Methods().ByName("PauseAccount"))
}

func TestTradeExecutionRPCUsesManualOrderAndLogicalTarget(t *testing.T) {
	service := File_trade_service_proto.Services().ByName("TradeConsoleService")
	require.NotNil(t, service)
	require.NotNil(t, service.Methods().ByName("SubmitOrder"))
	require.Nil(t, service.Methods().ByName("PlaceManualOrder"), "人工下单只留 SubmitOrder 一个入口（动作类型 MANUAL_ORDER）")
	require.NotNil(t, service.Methods().ByName("GetOperatorAction"))
	require.NotNil(t, service.Methods().ByName("GetLogicalAccountTarget"))
	require.Nil(t, service.Methods().ByName("PlaceOrder"))
	require.Nil(t, service.Methods().ByName("SubmitTarget"))

	request := service.Methods().ByName("SubmitOrder").Input()
	for _, forbidden := range []protoreflect.Name{
		"source",
		"owner_type",
		"owner_id",
		"instance_id",
		"strategy_result_id",
		"reduce_position_only",
		"reduce_only",
	} {
		require.Nil(t, request.Fields().ByName(forbidden), forbidden)
	}
	for _, required := range []protoreflect.Name{
		"action_id",
		"trading_account_id",
		"client_order_id",
		"fill_policy",
		"reason",
	} {
		require.NotNil(t, request.Fields().ByName(required), required)
	}
}

func TestLogicalAccountModelUsesAutomationVocabulary(t *testing.T) {
	message := File_trade_service_proto.Messages().ByName("LogicalAccount")
	require.NotNil(t, message)
	for _, required := range []protoreflect.Name{
		"logical_account_id",
		"settlement_asset",
		"automation_state",
		"pause_reason",
	} {
		require.NotNil(t, message.Fields().ByName(required), required)
	}
	for _, forbidden := range []protoreflect.Name{
		"control_state",
		"control_revision",
	} {
		require.Nil(t, message.Fields().ByName(forbidden), forbidden)
	}
}
