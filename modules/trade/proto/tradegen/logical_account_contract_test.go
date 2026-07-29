package tradepb

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestTradeProtoExposesLogicalAccountService(t *testing.T) {
	services := File_trade_service_proto.Services()
	logical := services.ByName("LogicalAccountService")
	require.NotNil(t, logical)
	for _, method := range []protoreflect.Name{
		"CreateLogicalAccount",
		"GetLogicalAccount",
		"ListLogicalAccounts",
		"UpdateLogicalAccount",
		"AddLogicalAccountMember",
		"RemoveLogicalAccountMember",
		"ClaimLogicalAccountOwner",
		"ReleaseLogicalAccountOwner",
		"PauseLogicalAccount",
		"ResumeLogicalAccount",
		"FlattenLogicalAccount",
	} {
		require.NotNil(t, logical.Methods().ByName(method), method)
	}
}

func TestPhysicalAccountRPCDoesNotExposePause(t *testing.T) {
	service := File_trade_service_proto.Services().ByName("ExchangeAccountService")
	require.NotNil(t, service)
	require.Nil(t, service.Methods().ByName("PauseAccount"))
}

func TestTradeExecutionRPCUsesManualOrderAndLogicalTarget(t *testing.T) {
	service := File_trade_service_proto.Services().ByName("TradeExecutionService")
	require.NotNil(t, service)
	require.NotNil(t, service.Methods().ByName("PlaceManualOrder"))
	require.NotNil(t, service.Methods().ByName("GetOperatorAction"))
	require.NotNil(t, service.Methods().ByName("GetLogicalAccountTarget"))
	require.Nil(t, service.Methods().ByName("PlaceOrder"))
	require.Nil(t, service.Methods().ByName("SubmitTarget"))

	request := service.Methods().ByName("PlaceManualOrder").Input()
	for _, forbidden := range []protoreflect.Name{
		"source",
		"owner_type",
		"owner_id",
		"runner_id",
		"strategy_result_id",
		"reduce_position_only",
		"reduce_only",
	} {
		require.Nil(t, request.Fields().ByName(forbidden), forbidden)
	}
	for _, required := range []protoreflect.Name{
		"action_id",
		"exchange_account_id",
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
