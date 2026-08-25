package tradepb

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

func TestTradeRPCExposesOnlyApprovedServicesAndMethods(t *testing.T) {
	want := map[protoreflect.FullName][]protoreflect.Name{
		"trpc.moox.trade.TradeConsoleService": {
			"CreateAccount", "UpdateAccount", "GetAccount", "ListAccounts",
			"SetLeverage", "SyncAccount",
			"CreateLogicalAccount", "GetLogicalAccount", "ListLogicalAccounts",
			"UpdateLogicalAccount", "AddLogicalAccountMember",
			"RemoveLogicalAccountMember", "ClaimLogicalAccountOwner",
			"ReleaseLogicalAccountOwner", "PauseLogicalAccount",
			"ResumeLogicalAccount", "FlattenLogicalAccount",
			"PlaceManualOrder", "CancelOrder", "GetOperatorAction",
			"GetLogicalAccountTarget", "GetOrder", "ListOrders", "ListFills",
			"ListPositions", "CreatePaperSimulation", "ClosePaperSimulation",
			"GetExecutionCapabilities", "QueryEquityCurve", "ListHoldings",
		},
		"trpc.moox.trade.TradeDNSResolverService": {
			"ResolveDomains",
		},
	}
	services := File_trade_service_proto.Services()
	if services.Len() != len(want) {
		t.Fatalf("service count = %d, want %d", services.Len(), len(want))
	}
	for serviceName, methods := range want {
		service := services.ByName(serviceName.Name())
		if service == nil || service.FullName() != serviceName {
			t.Fatalf("missing service %s", serviceName)
		}
		if service.Methods().Len() != len(methods) {
			t.Fatalf(
				"%s method count = %d, want %d",
				serviceName,
				service.Methods().Len(),
				len(methods),
			)
		}
		for _, method := range methods {
			if service.Methods().ByName(method) == nil {
				t.Errorf("%s is missing method %s", serviceName, method)
			}
		}
	}
}

func TestTradeRPCUsesApprovedEnums(t *testing.T) {
	want := map[protoreflect.Name][]protoreflect.Name{
		"Exchange": {
			"EXCHANGE_UNSPECIFIED", "EXCHANGE_BINANCE", "EXCHANGE_OKX",
		},
		"MarketType": {
			"MARKET_TYPE_UNSPECIFIED", "MARKET_TYPE_SPOT", "MARKET_TYPE_SWAP",
		},
		"ExecutionMode": {
			"EXECUTION_MODE_UNSPECIFIED", "EXECUTION_MODE_PAPER",
			"EXECUTION_MODE_LIVE",
		},
		"AccountEnvironment": {
			"ACCOUNT_ENVIRONMENT_UNSPECIFIED", "ACCOUNT_ENVIRONMENT_PAPER",
			"ACCOUNT_ENVIRONMENT_TESTNET", "ACCOUNT_ENVIRONMENT_PRODUCTION",
		},
		"OrderType": {
			"ORDER_TYPE_UNSPECIFIED", "ORDER_TYPE_MARKET", "ORDER_TYPE_LIMIT",
		},
		"FillPolicy": {
			"FILL_POLICY_UNSPECIFIED", "FILL_POLICY_GTC", "FILL_POLICY_IOC",
			"FILL_POLICY_FOK",
		},
		"PositionSide": {
			"POSITION_SIDE_UNSPECIFIED", "POSITION_SIDE_NET",
		},
		"OrderSide": {
			"ORDER_SIDE_UNSPECIFIED", "ORDER_SIDE_BUY", "ORDER_SIDE_SELL",
		},
	}
	enums := File_trade_service_proto.Enums()
	if enums.Len() != len(want) {
		t.Fatalf("enum count = %d, want %d", enums.Len(), len(want))
	}
	for name, values := range want {
		enum := enums.ByName(name)
		if enum == nil {
			t.Fatalf("missing enum %s", name)
		}
		if enum.Values().Len() != len(values) {
			t.Fatalf(
				"%s value count = %d, want %d",
				name,
				enum.Values().Len(),
				len(values),
			)
		}
		for index, value := range values {
			if enum.Values().Get(index).Name() != value {
				t.Errorf(
					"%s value %d = %s, want %s",
					name,
					index,
					enum.Values().Get(index).Name(),
					value,
				)
			}
		}
	}
}

func TestManualOrderRequestHasExactApprovedShape(t *testing.T) {
	want := map[protoreflect.Name]protoreflect.FieldNumber{
		"action_id":           1,
		"exchange_account_id": 2,
		"client_order_id":     3,
		"symbol":              4,
		"order_type":          5,
		"fill_policy":         6,
		"side":                7,
		"position_side":       8,
		"quantity":            9,
		"limit_price":         10,
		"reason":              11,
	}
	fields := (&PlaceManualOrderReq{}).ProtoReflect().Descriptor().Fields()
	if fields.Len() != len(want) {
		t.Fatalf("field count = %d, want %d", fields.Len(), len(want))
	}
	for name, number := range want {
		field := fields.ByName(name)
		if field == nil || field.Number() != number {
			t.Errorf("%s = %v, want field %d", name, field, number)
		}
	}
	if !fields.ByName("limit_price").HasOptionalKeyword() {
		t.Error("limit_price must remain proto3 optional")
	}
	for _, forbidden := range []protoreflect.Name{
		"source", "owner_type", "owner_id", "runner_id",
		"strategy_result_id", "reduce_only", "reduce_position_only",
	} {
		if fields.ByName(forbidden) != nil {
			t.Errorf("manual order exposes trusted field %s", forbidden)
		}
	}
}

func TestTradeRPCDoesNotExposeRemovedVocabulary(t *testing.T) {
	for _, method := range []protoreflect.Name{
		"PauseAccount", "PlaceOrder", "CancelAllOrders", "SubmitTarget",
		"GetExecution", "ListExecutions",
	} {
		if descriptor := findMethod(method); descriptor != nil {
			t.Errorf("removed method still declared: %s", descriptor.FullName())
		}
	}

	forbiddenFields := map[protoreflect.Name]bool{
		"provider":              true,
		"channel_id":            true,
		"quote_amount":          true,
		"api_key":               true,
		"api_secret":            true,
		"passphrase":            true,
		"source":                true,
		"strategy_execution_id": true,
		"execution_binding_id":  true,
		"control_state":         true,
		"control_revision":      true,
	}
	messages := File_trade_service_proto.Messages()
	for i := 0; i < messages.Len(); i++ {
		message := messages.Get(i)
		for j := 0; j < message.Fields().Len(); j++ {
			field := message.Fields().Get(j)
			if forbiddenFields[field.Name()] {
				t.Errorf("removed field still declared: %s", field.FullName())
			}
		}
	}

	source, err := os.ReadFile(filepath.Join("..", "trade_service.proto"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"TimeInForce", "PauseAccount", "PlaceOrder", "SubmitTarget",
		"TargetIntent", "TradeTarget", "target_quantity",
	} {
		if strings.Contains(string(source), forbidden) {
			t.Errorf("removed declaration %q still appears in proto", forbidden)
		}
	}
}

func TestEveryPublicRPCRequestImplementsValidation(t *testing.T) {
	services := File_trade_service_proto.Services()
	for i := 0; i < services.Len(); i++ {
		service := services.Get(i)
		for j := 0; j < service.Methods().Len(); j++ {
			method := service.Methods().Get(j)
			messageType, err := protoregistry.GlobalTypes.FindMessageByName(
				method.Input().FullName(),
			)
			if err != nil {
				t.Fatalf("%s input type: %v", method.FullName(), err)
			}
			request := messageType.New().Interface()
			if _, ok := request.(interface{ Validate() error }); !ok {
				t.Errorf(
					"%s input %T does not implement Validate() error",
					method.FullName(),
					request,
				)
			}
		}
	}
}

func TestScopedRequestsRejectMissingIdentity(t *testing.T) {
	requests := []any{
		&UpdateAccountReq{},
		&GetAccountReq{},
		&SetLeverageReq{},
		&SyncAccountReq{},
		&GetLogicalAccountReq{},
		&UpdateLogicalAccountReq{},
		&AddLogicalAccountMemberReq{},
		&RemoveLogicalAccountMemberReq{},
		&ClaimLogicalAccountOwnerReq{},
		&ReleaseLogicalAccountOwnerReq{},
		&PauseLogicalAccountReq{},
		&ResumeLogicalAccountReq{},
		&FlattenLogicalAccountReq{},
		&PlaceManualOrderReq{},
		&CancelOrderReq{},
		&GetOperatorActionReq{},
		&GetLogicalAccountTargetReq{},
		&GetOrderReq{},
		&ListOrdersReq{},
		&ListFillsReq{},
		&ListPositionsReq{},
	}
	for _, request := range requests {
		t.Run(reflect.TypeOf(request).Elem().Name(), func(t *testing.T) {
			if err := validateRequest(request); err == nil {
				t.Fatal("zero request was accepted")
			}
		})
	}

	pagedRequests := []any{
		&ListAccountsReq{Page: &Page{Size: 1001}},
		&ListLogicalAccountsReq{Page: &Page{Size: 1001}},
		&ListOrdersReq{
			ExchangeAccountId: "account-1",
			Page:              &Page{Size: 1001},
		},
		&ListFillsReq{
			ExchangeAccountId: "account-1",
			Page:              &Page{Size: 1001},
		},
	}
	for _, request := range pagedRequests {
		t.Run(reflect.TypeOf(request).Elem().Name()+"Page", func(t *testing.T) {
			if err := validateRequest(request); err == nil {
				t.Fatal("oversized page was accepted")
			}
		})
	}
	if err := (&ListOrdersReq{
		ExchangeAccountId: "account-1",
		StartTime:         20,
		EndTime:           10,
	}).Validate(); err == nil {
		t.Fatal("reversed time range was accepted")
	}
}

func TestCreateAccountRejectsPaperLifecycleBypass(t *testing.T) {
	valid := &CreateAccountReq{
		Name:          "paper",
		Exchange:      Exchange_EXCHANGE_BINANCE,
		MarketType:    MarketType_MARKET_TYPE_SPOT,
		ExecutionMode: ExecutionMode_EXECUTION_MODE_PAPER,
		Environment:   AccountEnvironment_ACCOUNT_ENVIRONMENT_PAPER,
	}
	if err := valid.Validate(); err == nil {
		t.Fatal("generic CreateAccount accepted a paper lifecycle request")
	}

	cases := []struct {
		name string
		req  *CreateAccountReq
	}{
		{
			name: "paper cannot use testnet",
			req: &CreateAccountReq{
				Name:          "paper",
				Exchange:      Exchange_EXCHANGE_BINANCE,
				MarketType:    MarketType_MARKET_TYPE_SPOT,
				ExecutionMode: ExecutionMode_EXECUTION_MODE_PAPER,
				Environment:   AccountEnvironment_ACCOUNT_ENVIRONMENT_TESTNET,
			},
		},
		{
			name: "live requires secret",
			req: &CreateAccountReq{
				Name:          "live",
				Exchange:      Exchange_EXCHANGE_OKX,
				MarketType:    MarketType_MARKET_TYPE_SWAP,
				ExecutionMode: ExecutionMode_EXECUTION_MODE_LIVE,
				Environment:   AccountEnvironment_ACCOUNT_ENVIRONMENT_TESTNET,
			},
		},
		{
			name: "unknown environment",
			req: &CreateAccountReq{
				Name:               "live",
				Exchange:           Exchange_EXCHANGE_OKX,
				MarketType:         MarketType_MARKET_TYPE_SWAP,
				ExecutionMode:      ExecutionMode_EXECUTION_MODE_LIVE,
				Environment:        AccountEnvironment(99),
				CredentialSecretId: "secret-1",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.req.Validate(); err == nil {
				t.Fatal("invalid environment profile was accepted")
			}
		})
	}
}

func TestManualOrderValidation(t *testing.T) {
	newMarketOrder := func() *PlaceManualOrderReq {
		return &PlaceManualOrderReq{
			ActionId:          "action-1",
			ExchangeAccountId: "account-1",
			ClientOrderId:     "client-1",
			Symbol:            "BTCUSDT",
			OrderType:         OrderType_ORDER_TYPE_MARKET,
			FillPolicy:        FillPolicy_FILL_POLICY_UNSPECIFIED,
			Side:              OrderSide_ORDER_SIDE_BUY,
			PositionSide:      PositionSide_POSITION_SIDE_NET,
			Quantity:          "1",
			Reason:            "operator intervention",
		}
	}

	if err := newMarketOrder().Validate(); err != nil {
		t.Fatalf("valid market order rejected: %v", err)
	}
	price := "100"
	marketWithLimit := newMarketOrder()
	marketWithLimit.LimitPrice = &price
	if err := marketWithLimit.Validate(); err == nil {
		t.Fatal("market order accepted limit_price")
	}

	limit := newMarketOrder()
	limit.OrderType = OrderType_ORDER_TYPE_LIMIT
	limit.FillPolicy = FillPolicy_FILL_POLICY_GTC
	limit.LimitPrice = &price
	if err := limit.Validate(); err != nil {
		t.Fatalf("valid limit order rejected: %v", err)
	}

	for _, quantity := range []string{
		"", "0", "-1", "+1", ".5", "1.", "01", "1e3", "NaN",
		strings.Repeat("9", 257),
	} {
		t.Run("quantity "+quantity, func(t *testing.T) {
			req := newMarketOrder()
			req.Quantity = quantity
			if err := req.Validate(); err == nil {
				t.Fatalf("invalid quantity %q was accepted", quantity)
			}
		})
	}

	for _, mutate := range []func(*PlaceManualOrderReq){
		func(req *PlaceManualOrderReq) { req.OrderType = OrderType(99) },
		func(req *PlaceManualOrderReq) { req.FillPolicy = FillPolicy(99) },
		func(req *PlaceManualOrderReq) { req.Side = OrderSide(99) },
		func(req *PlaceManualOrderReq) { req.PositionSide = PositionSide(99) },
	} {
		req := newMarketOrder()
		mutate(req)
		if err := req.Validate(); err == nil {
			t.Fatal("unknown enum value was accepted")
		}
	}
}

func TestLogicalOperationValidationRequiresActionAndReason(t *testing.T) {
	if err := (&PauseLogicalAccountReq{
		LogicalAccountId: "logical-1",
		Reason:           "manual intervention",
	}).Validate(); err != nil {
		t.Fatalf("valid pause rejected: %v", err)
	}
	if err := (&FlattenLogicalAccountReq{
		ActionId:         "action-1",
		LogicalAccountId: "logical-1",
		Reason:           "close every member",
	}).Validate(); err != nil {
		t.Fatalf("valid flatten rejected: %v", err)
	}
	if err := (&FlattenLogicalAccountReq{
		ActionId:         "action-1",
		LogicalAccountId: "logical-1",
	}).Validate(); err == nil {
		t.Fatal("flatten without reason was accepted")
	}
}

func validateRequest(request any) error {
	validator, ok := request.(interface{ Validate() error })
	if !ok {
		return nil
	}
	return validator.Validate()
}

func findMethod(name protoreflect.Name) protoreflect.MethodDescriptor {
	services := File_trade_service_proto.Services()
	for i := 0; i < services.Len(); i++ {
		if method := services.Get(i).Methods().ByName(name); method != nil {
			return method
		}
	}
	return nil
}
