package tradepb

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
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
