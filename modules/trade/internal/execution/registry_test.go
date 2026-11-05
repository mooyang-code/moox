package execution

import (
	"context"
	"reflect"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type registryStub struct {
	config     exchange.AccountConfig
	credential exchange.Credential
}

func (s *registryStub) Exchange() exchange.Exchange { return s.config.Exchange }
func (*registryStub) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	return exchange.AccountSnapshot{}, nil
}
func (*registryStub) ListPositionSnapshots(context.Context) ([]exchange.Position, error) {
	return nil, nil
}
func (*registryStub) ListOpenOrders(context.Context) ([]exchange.Order, error) { return nil, nil }
func (*registryStub) ListRecentFills(context.Context, exchange.SymbolKey, string) ([]exchange.Fill, string, error) {
	return nil, "", nil
}
func (*registryStub) GetOrder(_ context.Context, _ exchange.SymbolKey, clientOrderID string) (exchange.Order, error) {
	if clientOrderID == "terminal-client" {
		return exchange.Order{Market: exchange.MarketTypeSwap, ClientOrderID: clientOrderID, Status: exchange.OrderStatusFilled, ReduceOnly: true}, nil
	}
	return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorOrderNotFound}
}
func (*registryStub) PlaceOrder(_ context.Context, request exchange.OrderRequest) (exchange.Order, error) {
	return exchange.Order{Market: exchange.MarketTypeSwap, ClientOrderID: request.ClientOrderID, Status: exchange.OrderStatusOpen, ReduceOnly: request.ReduceOnly}, nil
}
func (*registryStub) CancelOrder(context.Context, exchange.SymbolKey, string) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (*registryStub) SetLeverage(context.Context, exchange.SymbolKey, shared.Decimal) error {
	return nil
}
func (*registryStub) SetMarginMode(context.Context, exchange.SymbolKey, exchange.MarginMode) error {
	return nil
}

func TestRegistryBindsExecutionAdapter(t *testing.T) {
	registry := NewRegistry()
	registry.Register(ExchangeDescriptor{Exchange: exchange.ExchangeBinance}, func(config exchange.AccountConfig, credential exchange.Credential) (ExecutionAdapter, error) {
		return &registryStub{config: config, credential: credential}, nil
	})
	config := exchange.AccountConfig{TradingAccountID: "account-1", Exchange: exchange.ExchangeBinance, Markets: []exchange.MarketType{exchange.MarketTypeSpot}, ExecutionMode: exchange.ExecutionModeLive, SettlementAsset: "USDT"}
	credential := exchange.Credential{APIKey: "key", APISecret: "secret"}
	got, err := registry.Bind(config, credential)
	if err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	adapter, ok := got.(*registryStub)
	if !ok || !reflect.DeepEqual(adapter.config, config) || adapter.credential.APIKey != credential.APIKey {
		t.Fatalf("factory binding = %#v", got)
	}
	order, err := got.GetOrder(context.Background(), exchange.NewSymbolKey(exchange.MarketTypeSpot, "BTC-USDT-SPOT"), "terminal-client")
	if err != nil || order.Status != exchange.OrderStatusFilled {
		t.Fatalf("GetOrder() = %#v, %v", order, err)
	}
}

func TestRegistryRejectsInvalidBindingAndDuplicate(t *testing.T) {
	registry := NewRegistry()
	factory := func(exchange.AccountConfig, exchange.Credential) (ExecutionAdapter, error) {
		return &registryStub{}, nil
	}
	registry.Register(ExchangeDescriptor{Exchange: exchange.ExchangeBinance}, factory)
	if _, err := registry.Bind(exchange.AccountConfig{Exchange: exchange.ExchangeBinance, Markets: []exchange.MarketType{exchange.MarketTypeSpot}, ExecutionMode: exchange.ExecutionModeLive, SettlementAsset: "USDT"}, exchange.Credential{}); err == nil {
		t.Fatal("Bind() accepted missing account and credential")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate Register() did not panic")
		}
	}()
	registry.Register(ExchangeDescriptor{Exchange: exchange.ExchangeBinance}, factory)
}

func (*registryStub) LoadInstruments(context.Context) ([]exchange.Instrument, error) { return nil, nil }
func (*registryStub) GetQuote(context.Context, exchange.SymbolKey) (MarketQuote, error) {
	return MarketQuote{}, nil
}
func (*registryStub) GetReferencePrice(context.Context, exchange.SymbolKey) (exchange.ReferencePrice, error) {
	return exchange.ReferencePrice{}, nil
}
func (*registryStub) GetOrderByExchangeID(context.Context, exchange.SymbolKey, string) (exchange.Order, error) {
	return exchange.Order{}, nil
}
