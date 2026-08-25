package exchange

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

type stubAdapter struct {
	config     AccountConfig
	credential Credential
}

func (s *stubAdapter) Exchange() Exchange { return s.config.Exchange }
func (*stubAdapter) LoadInstruments(context.Context) ([]Instrument, error) {
	return nil, nil
}
func (*stubAdapter) GetAccountSnapshot(context.Context) (AccountSnapshot, error) {
	return AccountSnapshot{}, nil
}
func (*stubAdapter) ListPositionSnapshots(context.Context) ([]Position, error) {
	return nil, nil
}
func (*stubAdapter) ListOpenOrders(context.Context) ([]Order, error) {
	return nil, nil
}
func (*stubAdapter) ListRecentFills(context.Context, string, string) ([]Fill, string, error) {
	return nil, "", nil
}
func (*stubAdapter) GetOrder(_ context.Context, _ string, clientOrderID string) (Order, error) {
	if clientOrderID == "terminal-client" {
		return Order{
			ClientOrderID: clientOrderID,
			Status:        OrderStatusFilled,
			ReduceOnly:    true,
		}, nil
	}
	return Order{}, &Error{Kind: ErrorOrderNotFound, Code: "missing"}
}
func (*stubAdapter) PlaceOrder(_ context.Context, request OrderRequest) (Order, error) {
	return Order{
		ClientOrderID: request.ClientOrderID,
		Status:        OrderStatusOpen,
		ReduceOnly:    request.ReduceOnly,
	}, nil
}
func (*stubAdapter) CancelOrder(context.Context, string, string) (Order, error) {
	return Order{}, nil
}
func (*stubAdapter) SetLeverage(context.Context, string, shared.Decimal) error {
	return nil
}
func (*stubAdapter) SetMarginMode(context.Context, string, MarginMode) error {
	return nil
}
func (*stubAdapter) SubscribePrivate(context.Context, EventHandler) error {
	return nil
}

func TestRegistryBindsAccountConfigurationAndCredential(t *testing.T) {
	registry := NewRegistry()
	registry.Register(ExchangeBinance, func(config AccountConfig, credential Credential) (Adapter, error) {
		return &stubAdapter{config: config, credential: credential}, nil
	})
	config := AccountConfig{
		TradingAccountID: "account-1",
		Exchange:         ExchangeBinance,
		MarketType:       MarketTypeSpot,
		ExecutionMode:    ExecutionModeLive,
		SettlementAsset:  "USDT",
	}
	credential := Credential{APIKey: "key", APISecret: "secret"}

	got, err := registry.Bind(config, credential)
	if err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	adapter, ok := got.(*stubAdapter)
	if !ok {
		t.Fatalf("Bind() adapter type = %T", got)
	}
	if adapter.config != config || adapter.credential != credential {
		t.Fatalf("factory bindings = %+v, %+v", adapter.config, adapter.credential)
	}
}

func TestRegistryRejectsInvalidBinding(t *testing.T) {
	registry := NewRegistry()
	registry.Register(ExchangeOKX, func(config AccountConfig, credential Credential) (Adapter, error) {
		return &stubAdapter{config: config, credential: credential}, nil
	})

	tests := []struct {
		name       string
		config     AccountConfig
		credential Credential
	}{
		{
			name: "unknown Exchange",
			config: AccountConfig{
				TradingAccountID: "account-1",
				Exchange:         Exchange("OTHER"),
				MarketType:       MarketTypeSpot,
				ExecutionMode:    ExecutionModeLive,
				SettlementAsset:  "USDT",
			},
			credential: Credential{APIKey: "key", APISecret: "secret"},
		},
		{
			name: "blank account",
			config: AccountConfig{
				Exchange:        ExchangeOKX,
				MarketType:      MarketTypeSpot,
				ExecutionMode:   ExecutionModeLive,
				SettlementAsset: "USDT",
			},
			credential: Credential{APIKey: "key", APISecret: "secret"},
		},
		{
			name: "live missing credential",
			config: AccountConfig{
				TradingAccountID: "account-1",
				Exchange:         ExchangeOKX,
				MarketType:       MarketTypeSpot,
				ExecutionMode:    ExecutionModeLive,
				SettlementAsset:  "USDT",
			},
		},
		{
			name: "non USDT SWAP settlement",
			config: AccountConfig{
				TradingAccountID: "account-1",
				Exchange:         ExchangeOKX,
				MarketType:       MarketTypeSwap,
				ExecutionMode:    ExecutionModePaper,
				SettlementAsset:  "USDC",
				MarginMode:       MarginModeCross,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := registry.Bind(tt.config, tt.credential); err == nil {
				t.Fatal("Bind() error = nil")
			}
		})
	}
}

func TestRegistryRejectsDuplicateRegistration(t *testing.T) {
	registry := NewRegistry()
	factory := func(AccountConfig, Credential) (Adapter, error) { return &stubAdapter{}, nil }
	registry.Register(ExchangeBinance, factory)
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate Register() did not panic")
		}
	}()
	registry.Register(ExchangeBinance, factory)
}

func TestAccountBoundAdapterLooksUpTerminalOrderByClientOrderID(t *testing.T) {
	registry := NewRegistry()
	registry.Register(ExchangeBinance, func(config AccountConfig, credential Credential) (Adapter, error) {
		return &stubAdapter{config: config, credential: credential}, nil
	})
	adapter, err := registry.Bind(AccountConfig{
		TradingAccountID: "account-1",
		Exchange:         ExchangeBinance,
		MarketType:       MarketTypeSpot,
		ExecutionMode:    ExecutionModePaper,
		SettlementAsset:  "USDT",
	}, Credential{})
	if err != nil {
		t.Fatalf("Bind() error = %v", err)
	}

	order, err := adapter.GetOrder(context.Background(), "BTC-USDT", "terminal-client")
	if err != nil {
		t.Fatalf("GetOrder() error = %v", err)
	}
	if order.Status != OrderStatusFilled || order.ClientOrderID != "terminal-client" {
		t.Fatalf("GetOrder() = %+v", order)
	}
	_, err = adapter.GetOrder(context.Background(), "BTC-USDT", "missing-client")
	if !IsKind(err, ErrorOrderNotFound) {
		t.Fatalf("GetOrder() error = %v, want ORDER_NOT_FOUND", err)
	}
}

func TestReduceOnlySurvivesRequestResponse(t *testing.T) {
	adapter := &stubAdapter{config: AccountConfig{Exchange: ExchangeBinance}}
	response, err := adapter.PlaceOrder(context.Background(), OrderRequest{
		ClientOrderID: "reduce-client",
		ReduceOnly:    true,
	})
	if err != nil {
		t.Fatalf("PlaceOrder() error = %v", err)
	}
	if !response.ReduceOnly {
		t.Fatal("PlaceOrder() response lost ReduceOnly")
	}
}
