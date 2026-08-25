package exchange

import (
	"fmt"
	"strings"
	"sync"
)

type Factory func(AccountConfig, Credential) (Adapter, error)

type Registry struct {
	mu        sync.RWMutex
	factories map[Exchange]Factory
}

func NewRegistry() *Registry {
	return &Registry{factories: make(map[Exchange]Factory)}
}

func (r *Registry) Register(name Exchange, factory Factory) {
	if !name.Valid() || factory == nil {
		panic("exchange: Register requires a supported Exchange and factory")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[name]; exists {
		panic(fmt.Sprintf("exchange: duplicate registration for %q", name))
	}
	r.factories[name] = factory
}

func (r *Registry) Bind(config AccountConfig, credential Credential) (Adapter, error) {
	if err := validateBinding(config, credential); err != nil {
		return nil, err
	}
	r.mu.RLock()
	factory, ok := r.factories[config.Exchange]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("exchange: unregistered Exchange %q", config.Exchange)
	}
	adapter, err := factory(config, credential)
	if err != nil {
		return nil, fmt.Errorf("exchange: bind %s account %q: %w", config.Exchange, config.TradingAccountID, err)
	}
	if adapter == nil {
		return nil, fmt.Errorf("exchange: %s factory returned nil adapter", config.Exchange)
	}
	if adapter.Exchange() != config.Exchange {
		return nil, fmt.Errorf(
			"exchange: adapter identity %q does not match account Exchange %q",
			adapter.Exchange(),
			config.Exchange,
		)
	}
	return adapter, nil
}

func validateBinding(config AccountConfig, credential Credential) error {
	if strings.TrimSpace(config.TradingAccountID) == "" ||
		!config.Exchange.Valid() ||
		!config.MarketType.Valid() ||
		!config.ExecutionMode.Valid() ||
		strings.TrimSpace(config.SettlementAsset) == "" {
		return fmt.Errorf("exchange: invalid account binding")
	}
	switch config.MarketType {
	case MarketTypeSpot:
		if config.MarginMode != MarginModeUnspecified {
			return fmt.Errorf("exchange: SPOT account cannot configure margin mode")
		}
	case MarketTypeSwap:
		if config.MarginMode != MarginModeCross {
			return fmt.Errorf("exchange: SWAP account requires CROSS margin mode")
		}
		if !strings.EqualFold(strings.TrimSpace(config.SettlementAsset), "USDT") {
			return fmt.Errorf("exchange: SWAP account requires USDT settlement")
		}
	}
	if config.ExecutionMode == ExecutionModeLive &&
		(strings.TrimSpace(credential.APIKey) == "" ||
			strings.TrimSpace(credential.APISecret) == "") {
		return fmt.Errorf("exchange: live account requires credential")
	}
	return nil
}
