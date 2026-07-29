package tradepb

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

func TestTradeRPCContractHasOnlyApprovedServices(t *testing.T) {
	want := map[protoreflect.FullName][]protoreflect.Name{
		"trpc.moox.trade.ExchangeAccountService": {
			"CreateAccount", "UpdateAccount", "GetAccount", "ListAccounts",
			"SetLeverage", "PauseAccount", "SyncAccount",
		},
		"trpc.moox.trade.TradeExecutionService": {
			"PlaceOrder", "CancelOrder", "CancelAllOrders", "SubmitTarget",
			"GetExecution", "ListExecutions", "GetOrder", "ListOrders",
			"ListFills", "ListPositions",
		},
	}

	services := File_trade_service_proto.Services()
	if services.Len() != len(want) {
		t.Fatalf("service count = %d, want %d", services.Len(), len(want))
	}
	for serviceName, wantMethods := range want {
		service := services.ByName(serviceName.Name())
		if service == nil || service.FullName() != serviceName {
			t.Fatalf("missing service %s", serviceName)
		}
		var gotMethods []protoreflect.Name
		for i := 0; i < service.Methods().Len(); i++ {
			gotMethods = append(gotMethods, service.Methods().Get(i).Name())
		}
		if !reflect.DeepEqual(gotMethods, wantMethods) {
			t.Errorf("%s methods = %v, want %v", serviceName, gotMethods, wantMethods)
		}
	}
}

func TestTradeRPCContractUsesApprovedEnums(t *testing.T) {
	want := map[string][]string{
		"Exchange":      {"EXCHANGE_UNSPECIFIED", "EXCHANGE_BINANCE", "EXCHANGE_OKX"},
		"MarketType":    {"MARKET_TYPE_UNSPECIFIED", "MARKET_TYPE_SPOT", "MARKET_TYPE_SWAP"},
		"ExecutionMode": {"EXECUTION_MODE_UNSPECIFIED", "EXECUTION_MODE_PAPER", "EXECUTION_MODE_LIVE"},
		"OrderType":     {"ORDER_TYPE_UNSPECIFIED", "ORDER_TYPE_MARKET", "ORDER_TYPE_LIMIT"},
		"TimeInForce":   {"TIME_IN_FORCE_UNSPECIFIED", "TIME_IN_FORCE_GTC", "TIME_IN_FORCE_IOC", "TIME_IN_FORCE_FOK"},
		"PositionSide":  {"POSITION_SIDE_UNSPECIFIED", "POSITION_SIDE_NET"},
	}
	for enumName, wantValues := range want {
		enum := File_trade_service_proto.Enums().ByName(protoreflect.Name(enumName))
		if enum == nil {
			t.Fatalf("missing enum %s", enumName)
		}
		var gotValues []string
		for i := 0; i < enum.Values().Len(); i++ {
			gotValues = append(gotValues, string(enum.Values().Get(i).Name()))
		}
		if !reflect.DeepEqual(gotValues, wantValues) {
			t.Errorf("%s values = %v, want %v", enumName, gotValues, wantValues)
		}
	}
}

func TestPlaceOrderRequestHasExactApprovedShape(t *testing.T) {
	want := map[string]protoreflect.FieldNumber{
		"exchange_account_id":   1,
		"client_order_id":       2,
		"symbol":                3,
		"order_type":            4,
		"time_in_force":         5,
		"side":                  6,
		"position_side":         7,
		"quantity":              8,
		"limit_price":           9,
		"reduce_only":           10,
		"source":                11,
		"strategy_execution_id": 12,
	}
	fields := (&PlaceOrderReq{}).ProtoReflect().Descriptor().Fields()
	if fields.Len() != len(want) {
		t.Fatalf("PlaceOrderReq field count = %d, want %d", fields.Len(), len(want))
	}
	for name, number := range want {
		field := fields.ByName(protoreflect.Name(name))
		if field == nil || field.Number() != number {
			t.Errorf("PlaceOrderReq.%s = %v, want field %d", name, field, number)
		}
	}
	limitPrice := fields.ByName("limit_price")
	if limitPrice == nil || !limitPrice.HasOptionalKeyword() {
		t.Error("PlaceOrderReq.limit_price must be proto3 optional")
	}
}

func TestExchangeAccountExposesOwnedIdentityAndReadiness(t *testing.T) {
	fields := (&ExchangeAccount{}).ProtoReflect().Descriptor().Fields()
	for name, number := range map[protoreflect.Name]protoreflect.FieldNumber{
		"exchange_account_id": 1,
		"space_id":            2,
		"ready":               13,
	} {
		field := fields.ByName(name)
		if field == nil || field.Number() != number {
			t.Errorf("ExchangeAccount.%s = %v, want field %d", name, field, number)
		}
	}

	// Space ownership always comes from authenticated context. Account responses
	// expose it, but public requests must never accept a caller-supplied space.
	requests := []protoreflect.MessageDescriptor{
		(&CreateAccountReq{}).ProtoReflect().Descriptor(),
		(&UpdateAccountReq{}).ProtoReflect().Descriptor(),
		(&GetAccountReq{}).ProtoReflect().Descriptor(),
		(&ListAccountsReq{}).ProtoReflect().Descriptor(),
	}
	for _, request := range requests {
		if field := request.Fields().ByName("space_id"); field != nil {
			t.Errorf("%s must not accept caller-supplied space_id", request.FullName())
		}
	}

	accountDescriptor := (&ExchangeAccount{}).ProtoReflect().Descriptor()
	responses := []struct {
		message protoreflect.MessageDescriptor
		field   protoreflect.Name
	}{
		{(&CreateAccountRsp{}).ProtoReflect().Descriptor(), "account"},
		{(&UpdateAccountRsp{}).ProtoReflect().Descriptor(), "account"},
		{(&GetAccountRsp{}).ProtoReflect().Descriptor(), "account"},
		{(&ListAccountsRsp{}).ProtoReflect().Descriptor(), "accounts"},
		{(&SetLeverageRsp{}).ProtoReflect().Descriptor(), "account"},
		{(&PauseAccountRsp{}).ProtoReflect().Descriptor(), "account"},
	}
	for _, response := range responses {
		field := response.message.Fields().ByName(response.field)
		if field == nil || field.Message() != accountDescriptor {
			t.Errorf("%s.%s must return the full ExchangeAccount entity", response.message.FullName(), response.field)
		}
	}
}

func TestTradeRPCContractDoesNotExposeRemovedVocabulary(t *testing.T) {
	oldServices := []protoreflect.Name{
		"AccountSvc", "BalanceSvc", "FundSvc", "ApiKeySvc", "ChannelSvc",
		"TradeOpSvc", "OrderSvc", "TradeQuerySvc", "PositionSvc",
		"RebalanceSvc", "TradeOpsSvc",
	}
	for _, name := range oldServices {
		if service := File_trade_service_proto.Services().ByName(name); service != nil {
			t.Errorf("removed service still declared: %s", service.FullName())
		}
	}

	forbiddenFields := map[protoreflect.Name]bool{
		"provider": true, "channel_id": true, "quote_amount": true,
		"api_key": true, "api_secret": true, "passphrase": true,
	}
	messages := File_trade_service_proto.Messages()
	for i := 0; i < messages.Len(); i++ {
		message := messages.Get(i)
		for j := 0; j < message.Fields().Len(); j++ {
			field := message.Fields().Get(j)
			if forbiddenFields[field.Name()] {
				t.Errorf("removed field still declared: %s.%s", message.FullName(), field.Name())
			}
		}
		if strings.Contains(string(message.Name()), "Saga") {
			t.Errorf("removed Saga message still declared: %s", message.FullName())
		}
	}
	placeOrderFields := (&PlaceOrderReq{}).ProtoReflect().Descriptor().Fields()
	if field := placeOrderFields.ByName("amount"); field != nil {
		t.Errorf("removed quote-amount field still declared: %s", field.FullName())
	}

	if method := findMethod("SyncPositions"); method != nil {
		t.Errorf("removed method still declared: %s", method.FullName())
	}
	for i := 0; i < File_trade_service_proto.Enums().Len(); i++ {
		enum := File_trade_service_proto.Enums().Get(i)
		for j := 0; j < enum.Values().Len(); j++ {
			if strings.Contains(string(enum.Values().Get(j).Name()), "STOP") {
				t.Errorf("removed STOP enum value still declared: %s", enum.Values().Get(j).FullName())
			}
		}
	}

	protoPath := filepath.Join("..", "trade_service.proto")
	source, err := os.ReadFile(protoPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"provider", "channel_id", "quote_amount", "Saga", "SyncPositions", "STOP"} {
		if strings.Contains(string(source), forbidden) {
			t.Errorf("removed declaration %q still appears in trade_service.proto", forbidden)
		}
	}
}

func TestPublicRequestValidation(t *testing.T) {
	t.Run("live account requires secret reference", func(t *testing.T) {
		req := &CreateAccountReq{
			Name:          "primary",
			Exchange:      Exchange_EXCHANGE_BINANCE,
			MarketType:    MarketType_MARKET_TYPE_SPOT,
			ExecutionMode: ExecutionMode_EXECUTION_MODE_LIVE,
		}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() succeeded without credential_secret_id")
		}
		req.CredentialSecretId = "secret-1"
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("market order rejects limit semantics", func(t *testing.T) {
		price := "100"
		req := &PlaceOrderReq{
			ExchangeAccountId: "account-1",
			ClientOrderId:     "client-1",
			Symbol:            "BTCUSDT",
			OrderType:         OrderType_ORDER_TYPE_MARKET,
			Side:              OrderSide_ORDER_SIDE_BUY,
			Quantity:          "0.1",
			LimitPrice:        &price,
		}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() accepted limit_price for a market order")
		}
		req.LimitPrice = nil
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("limit order requires price and time in force", func(t *testing.T) {
		price := "100"
		req := &PlaceOrderReq{
			ExchangeAccountId: "account-1",
			ClientOrderId:     "client-1",
			Symbol:            "BTCUSDT",
			OrderType:         OrderType_ORDER_TYPE_LIMIT,
			Side:              OrderSide_ORDER_SIDE_SELL,
			Quantity:          "0.1",
			LimitPrice:        &price,
		}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() accepted a limit order without time_in_force")
		}
		req.TimeInForce = TimeInForce_TIME_IN_FORCE_GTC
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("rejects unknown enum numbers", func(t *testing.T) {
		newValidAccount := func() *CreateAccountReq {
			return &CreateAccountReq{
				Name:               "primary",
				Exchange:           Exchange_EXCHANGE_BINANCE,
				MarketType:         MarketType_MARKET_TYPE_SPOT,
				ExecutionMode:      ExecutionMode_EXECUTION_MODE_LIVE,
				CredentialSecretId: "secret-1",
			}
		}
		accountCases := []struct {
			name   string
			mutate func(*CreateAccountReq)
		}{
			{"exchange", func(req *CreateAccountReq) { req.Exchange = Exchange(99) }},
			{"market type", func(req *CreateAccountReq) { req.MarketType = MarketType(99) }},
			{"execution mode", func(req *CreateAccountReq) { req.ExecutionMode = ExecutionMode(99) }},
			{"execution mode cannot bypass live credential guard", func(req *CreateAccountReq) {
				req.ExecutionMode = ExecutionMode(99)
				req.CredentialSecretId = ""
			}},
		}
		for _, tc := range accountCases {
			t.Run(tc.name, func(t *testing.T) {
				req := newValidAccount()
				tc.mutate(req)
				if err := req.Validate(); err == nil {
					t.Fatal("Validate() accepted unknown enum value")
				}
			})
		}

		newValidOrder := func() *PlaceOrderReq {
			return &PlaceOrderReq{
				ExchangeAccountId: "account-1",
				ClientOrderId:     "client-1",
				Symbol:            "BTCUSDT",
				OrderType:         OrderType_ORDER_TYPE_MARKET,
				Side:              OrderSide_ORDER_SIDE_BUY,
				Quantity:          "0.1",
			}
		}
		orderCases := []struct {
			name   string
			mutate func(*PlaceOrderReq)
		}{
			{"order type", func(req *PlaceOrderReq) { req.OrderType = OrderType(99) }},
			{"side", func(req *PlaceOrderReq) { req.Side = OrderSide(99) }},
			{"time in force", func(req *PlaceOrderReq) { req.TimeInForce = TimeInForce(99) }},
			{"position side", func(req *PlaceOrderReq) { req.PositionSide = PositionSide(99) }},
		}
		for _, tc := range orderCases {
			t.Run(tc.name, func(t *testing.T) {
				req := newValidOrder()
				tc.mutate(req)
				if err := req.Validate(); err == nil {
					t.Fatal("Validate() accepted unknown enum value")
				}
			})
		}

		exchange := Exchange(99)
		marketType := MarketType(99)
		executionMode := ExecutionMode(99)
		for name, req := range map[string]*ListAccountsReq{
			"list exchange":       {Exchange: &exchange},
			"list market type":    {MarketType: &marketType},
			"list execution mode": {ExecutionMode: &executionMode},
		} {
			t.Run(name, func(t *testing.T) {
				if err := req.Validate(); err == nil {
					t.Fatal("Validate() accepted unknown enum filter")
				}
			})
		}
	})
}

func TestEveryPublicRPCRequestImplementsValidation(t *testing.T) {
	services := File_trade_service_proto.Services()
	for i := 0; i < services.Len(); i++ {
		service := services.Get(i)
		for j := 0; j < service.Methods().Len(); j++ {
			method := service.Methods().Get(j)
			messageType, err := protoregistry.GlobalTypes.FindMessageByName(method.Input().FullName())
			if err != nil {
				t.Fatalf("%s input type: %v", method.FullName(), err)
			}
			request := messageType.New().Interface()
			if _, ok := request.(interface{ Validate() error }); !ok {
				t.Errorf("%s input %T does not implement Validate() error", method.FullName(), request)
			}
		}
	}
}

func TestScopedRequestsRejectMissingIdentity(t *testing.T) {
	requests := []any{
		&UpdateAccountReq{},
		&GetAccountReq{},
		&SetLeverageReq{},
		&PauseAccountReq{},
		&SyncAccountReq{},
		&CancelOrderReq{},
		&CancelAllOrdersReq{},
		&SubmitTargetReq{},
		&GetExecutionReq{},
		&ListExecutionsReq{},
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
		&ListExecutionsReq{ExchangeAccountId: "account-1", Page: &Page{Size: 1001}},
		&ListOrdersReq{ExchangeAccountId: "account-1", Page: &Page{Size: 1001}},
		&ListFillsReq{ExchangeAccountId: "account-1", Page: &Page{Size: 1001}},
	}
	for _, request := range pagedRequests {
		t.Run(reflect.TypeOf(request).Elem().Name()+"Page", func(t *testing.T) {
			if err := validateRequest(request); err == nil {
				t.Fatal("oversized page was accepted")
			}
		})
	}
	if err := validateRequest(&ListOrdersReq{
		ExchangeAccountId: "account-1",
		StartTime:         20,
		EndTime:           10,
	}); err == nil {
		t.Fatal("reversed time range was accepted")
	}
}

func TestPlaceOrderRejectsNonCanonicalOrNonPositiveDecimals(t *testing.T) {
	newMarketOrder := func() *PlaceOrderReq {
		return &PlaceOrderReq{
			ExchangeAccountId: "account-1",
			ClientOrderId:     "client-1",
			Symbol:            "BTCUSDT",
			OrderType:         OrderType_ORDER_TYPE_MARKET,
			Side:              OrderSide_ORDER_SIDE_BUY,
			Quantity:          "1",
		}
	}
	for _, quantity := range []string{
		"", "0", "-0", "-1", "+1", ".5", "1.", "01", "1e3",
		"1/2", "NaN", "Inf", " 1", strings.Repeat("9", 257),
	} {
		t.Run("quantity "+quantity, func(t *testing.T) {
			req := newMarketOrder()
			req.Quantity = quantity
			if err := req.Validate(); err == nil {
				t.Fatalf("non-positive or non-canonical quantity %q was accepted", quantity)
			}
		})
	}
	for _, quantity := range []string{"0.0001", "1", "10.25"} {
		t.Run("valid quantity "+quantity, func(t *testing.T) {
			req := newMarketOrder()
			req.Quantity = quantity
			if err := req.Validate(); err != nil {
				t.Fatalf("canonical quantity %q rejected: %v", quantity, err)
			}
		})
	}

	for _, price := range []string{
		"", "0", "-1", "+1", ".5", "1.", "01", "1e3",
		"1/2", "NaN", "Inf", " 1", strings.Repeat("9", 257),
	} {
		t.Run("limit price "+price, func(t *testing.T) {
			req := newMarketOrder()
			req.OrderType = OrderType_ORDER_TYPE_LIMIT
			req.TimeInForce = TimeInForce_TIME_IN_FORCE_GTC
			req.LimitPrice = &price
			if err := req.Validate(); err == nil {
				t.Fatalf("non-positive or non-canonical limit price %q was accepted", price)
			}
		})
	}
}

func TestSubmitTargetMatchesEventTargetIntentValidation(t *testing.T) {
	newRequest := func() *SubmitTargetReq {
		return &SubmitTargetReq{
			EventId:            "execution-1",
			ExecutionId:        "execution-1",
			StrategyRunId:      "run-1",
			ExecutionBindingId: "binding-1",
			ExchangeAccountId:  "account-1",
			CommandSequence:    1,
			NotAfter:           time.Now().Add(time.Hour).UnixMilli(),
			DataRevision:       "revision-1",
			Targets: []*TargetPosition{{
				InstrumentId:   "BTC-USDT",
				Symbol:         "BTCUSDT",
				TargetQuantity: "1.25",
			}},
		}
	}
	tests := []struct {
		name   string
		mutate func(*SubmitTargetReq)
	}{
		{"empty event", func(req *SubmitTargetReq) { req.EventId = "" }},
		{"event identity mismatch", func(req *SubmitTargetReq) { req.EventId = "other" }},
		{"empty execution", func(req *SubmitTargetReq) { req.ExecutionId = "" }},
		{"empty strategy run", func(req *SubmitTargetReq) { req.StrategyRunId = "" }},
		{"empty binding", func(req *SubmitTargetReq) { req.ExecutionBindingId = "" }},
		{"empty account", func(req *SubmitTargetReq) { req.ExchangeAccountId = "" }},
		{"empty data revision", func(req *SubmitTargetReq) { req.DataRevision = "" }},
		{"zero sequence", func(req *SubmitTargetReq) { req.CommandSequence = 0 }},
		{"sequence overflow", func(req *SubmitTargetReq) { req.CommandSequence = uint64(math.MaxInt64) + 1 }},
		{"non-positive expiry", func(req *SubmitTargetReq) { req.NotAfter = 0 }},
		{"expired", func(req *SubmitTargetReq) { req.NotAfter = time.Now().Add(-time.Minute).UnixMilli() }},
		{"empty targets", func(req *SubmitTargetReq) { req.Targets = nil }},
		{"nil target", func(req *SubmitTargetReq) { req.Targets[0] = nil }},
		{"blank instrument", func(req *SubmitTargetReq) { req.Targets[0].InstrumentId = " " }},
		{"blank symbol", func(req *SubmitTargetReq) { req.Targets[0].Symbol = " " }},
		{"duplicate symbol", func(req *SubmitTargetReq) {
			req.Targets = append(req.Targets, &TargetPosition{
				InstrumentId: "other", Symbol: req.Targets[0].Symbol, TargetQuantity: "2",
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := newRequest()
			test.mutate(req)
			if err := req.Validate(); err == nil {
				t.Fatal("invalid target request was accepted")
			}
		})
	}

	for _, quantity := range []string{"", "+1", ".5", "1.", "01", "1e3", "1/2", "NaN", "Inf", " 1", strings.Repeat("9", 257)} {
		t.Run("invalid quantity "+quantity, func(t *testing.T) {
			req := newRequest()
			req.Targets[0].TargetQuantity = quantity
			if err := req.Validate(); err == nil {
				t.Fatalf("non-canonical target quantity %q was accepted", quantity)
			}
		})
	}
	for _, quantity := range []string{"0", "-0", "1", "-1", "1.25", "-0.0001"} {
		t.Run("valid quantity "+quantity, func(t *testing.T) {
			req := newRequest()
			req.CommandSequence = uint64(math.MaxInt64)
			req.Targets[0].TargetQuantity = quantity
			if err := req.Validate(); err != nil {
				t.Fatalf("canonical target quantity %q rejected: %v", quantity, err)
			}
		})
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
