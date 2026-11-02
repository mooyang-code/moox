package execution

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// Factory constructs an execution adapter for one account binding. The
// exchange package only owns transport/domain types; execution owns binding
// and adapter selection so application code depends on one port.
type AdapterFactory func(exchange.AccountConfig, exchange.Credential) (ExecutionAdapter, error)

type ExchangeIdentity interface {
	Exchange() exchange.Exchange
}

// VenueDescriptor 是场所注册时登记的静态信息，不依赖适配器实例，未就绪的账户也能取到。
type VenueDescriptor struct {
	Exchange     exchange.Exchange
	OrderTypes   []exchange.OrderType
	FillPolicies []exchange.FillPolicy
	// UnifiedAccount 表示现货和合约共用一个资金账户，同一把密钥只能建一个执行账户。
	UnifiedAccount bool
	// TradingSession 判断当前是否在交易时段；nil 表示全天可交易。
	TradingSession func(time.Time) bool
}

// InTradingSession 报告场所当前是否可以下单。
func (d VenueDescriptor) InTradingSession(now time.Time) bool {
	return d.TradingSession == nil || d.TradingSession(now)
}

// VenueCatalog 按场所名查询静态信息。
type VenueCatalog interface {
	Descriptor(name exchange.Exchange) (VenueDescriptor, bool)
}

// InTradingSession 报告账户所在场所当前是否可以下单；没有登记的场所视为全天可交易。
func InTradingSession(venues VenueCatalog, name string, now time.Time) bool {
	if venues == nil {
		return true
	}
	descriptor, ok := venues.Descriptor(exchange.Exchange(name))
	return !ok || descriptor.InTradingSession(now)
}

type Registry struct {
	mu          sync.RWMutex
	factories   map[exchange.Exchange]AdapterFactory
	descriptors map[exchange.Exchange]VenueDescriptor
}

func NewRegistry() *Registry {
	return &Registry{
		factories:   make(map[exchange.Exchange]AdapterFactory),
		descriptors: make(map[exchange.Exchange]VenueDescriptor),
	}
}

func (r *Registry) Register(descriptor VenueDescriptor, factory AdapterFactory) {
	name := descriptor.Exchange
	if !name.Valid() || factory == nil {
		panic("execution: Register requires a supported Exchange and factory")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[name]; exists {
		panic(fmt.Sprintf("execution: duplicate registration for %q", name))
	}
	r.factories[name] = factory
	r.descriptors[name] = descriptor
}

// Descriptor 返回场所的静态信息。
func (r *Registry) Descriptor(name exchange.Exchange) (VenueDescriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	descriptor, ok := r.descriptors[name]
	return descriptor, ok
}

func (r *Registry) Bind(config exchange.AccountConfig, credential exchange.Credential) (ExecutionAdapter, error) {
	if err := validateBinding(config); err != nil {
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

func validateBinding(config exchange.AccountConfig) error {
	if strings.TrimSpace(config.TradingAccountID) == "" ||
		!config.Exchange.Valid() ||
		!config.MarketType.Valid() ||
		!config.ExecutionMode.Valid() ||
		strings.TrimSpace(config.SettlementAsset) == "" {
		return fmt.Errorf("execution: invalid account binding")
	}
	if !exchange.RuleFor(config.MarketType).Leverage {
		if config.MarginMode != exchange.MarginModeUnspecified {
			return fmt.Errorf("execution: SPOT account cannot configure margin mode")
		}
		return nil
	}
	if config.MarginMode != exchange.MarginModeCross {
		return fmt.Errorf("execution: SWAP account requires CROSS margin mode")
	}
	if !strings.EqualFold(strings.TrimSpace(config.SettlementAsset), "USDT") {
		return fmt.Errorf("execution: SWAP account requires USDT settlement")
	}
	return nil
}
