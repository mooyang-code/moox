package execution

import (
	"fmt"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// Factory constructs an execution adapter for one account binding. The
// exchange package only owns transport/domain types; execution owns binding
// and adapter selection so application code depends on one port.
type AdapterFactory func(exchange.AccountConfig, exchange.Credential) (ExecutionAdapter, error)

type ExchangeIdentity interface {
	Exchange() exchange.Exchange
}

type Registry struct {
	mu        sync.RWMutex
	factories map[exchange.Exchange]AdapterFactory
}

func NewRegistry() *Registry {
	return &Registry{factories: make(map[exchange.Exchange]AdapterFactory)}
}

func (r *Registry) Register(name exchange.Exchange, factory AdapterFactory) {
	if !name.Valid() || factory == nil {
		panic("execution: Register requires a supported Exchange and factory")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[name]; exists {
		panic(fmt.Sprintf("execution: duplicate registration for %q", name))
	}
	r.factories[name] = factory
}

func (r *Registry) Bind(config exchange.AccountConfig, credential exchange.Credential) (ExecutionAdapter, error) {
	if err := validateBinding(config, credential); err != nil {
		return nil, err
	}
	r.mu.RLock()
	factory, ok := r.factories[config.Exchange]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("execution: unregistered Exchange %q", config.Exchange)
	}
	adapter, err := factory(config, credential)
	if err != nil {
		return nil, fmt.Errorf("execution: bind %s account %q: %w", config.Exchange, config.TradingAccountID, err)
	}
	if adapter == nil {
		return nil, fmt.Errorf("execution: %s factory returned nil adapter", config.Exchange)
	}
	identity, ok := adapter.(ExchangeIdentity)
	if !ok || identity.Exchange() != config.Exchange {
		return nil, fmt.Errorf("execution: adapter identity does not match account Exchange %q", config.Exchange)
	}
	return adapter, nil
}

func validateBinding(config exchange.AccountConfig, credential exchange.Credential) error {
	if strings.TrimSpace(config.TradingAccountID) == "" ||
		!config.Exchange.Valid() ||
		!config.MarketType.Valid() ||
		!config.ExecutionMode.Valid() ||
		strings.TrimSpace(config.SettlementAsset) == "" {
		return fmt.Errorf("execution: invalid account binding")
	}
	switch config.MarketType {
	case exchange.MarketTypeSpot:
		if config.MarginMode != exchange.MarginModeUnspecified {
			return fmt.Errorf("execution: SPOT account cannot configure margin mode")
		}
	case exchange.MarketTypeSwap:
		if config.MarginMode != exchange.MarginModeCross {
			return fmt.Errorf("execution: SWAP account requires CROSS margin mode")
		}
		if !strings.EqualFold(strings.TrimSpace(config.SettlementAsset), "USDT") {
			return fmt.Errorf("execution: SWAP account requires USDT settlement")
		}
	}
	if config.ExecutionMode == exchange.ExecutionModeLive &&
		(strings.TrimSpace(credential.APIKey) == "" || strings.TrimSpace(credential.APISecret) == "") {
		return fmt.Errorf("execution: live account requires credential")
	}
	return nil
}
