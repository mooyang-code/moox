package execution

import (
	"context"
	"fmt"
	"slices"
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

// ExchangeDescriptor 是场所注册时登记的静态信息，不依赖适配器实例，未就绪的账户也能取到。
type ExchangeDescriptor struct {
	Exchange     exchange.Exchange
	OrderTypes   []exchange.OrderType
	FillPolicies []exchange.FillPolicy
	// TradingSession 判断当前是否在交易时段；nil 表示全天可交易。
	TradingSession func(time.Time) bool
	// Inspect 检测这个场所的密钥（账户类型、权限）；nil 表示不支持检测（券商、模拟盘）。
	Inspect exchange.Inspector
	// WholeAccount 报告某个账户配置是否由一个适配器同时服务全部市场（币安统一账户：一个余额接口、一条私有流），
	// 为真时注册表只调用一次工厂并把全部市场交给它，不再按市场拆成多个单市场适配器；nil 表示按市场拆分。
	WholeAccount func(exchange.AccountConfig) bool
}

// InTradingSession 报告场所当前是否可以下单。
func (d ExchangeDescriptor) InTradingSession(now time.Time) bool {
	return d.TradingSession == nil || d.TradingSession(now)
}

// ExchangeCatalog 按场所名查询静态信息。
type ExchangeCatalog interface {
	Descriptor(name exchange.Exchange) (ExchangeDescriptor, bool)
}

// InTradingSession 报告账户所在场所当前是否可以下单；没有登记的场所视为全天可交易。
func InTradingSession(exchanges ExchangeCatalog, name string, now time.Time) bool {
	if exchanges == nil {
		return true
	}
	descriptor, ok := exchanges.Descriptor(exchange.Exchange(name))
	return !ok || descriptor.InTradingSession(now)
}

// Inspect 用场所登记的检测函数检测一把密钥；场所没有登记检测函数时返回错误。
func (r *Registry) Inspect(ctx context.Context, name exchange.Exchange, request exchange.InspectRequest) (exchange.Inspection, error) {
	descriptor, ok := r.Descriptor(name)
	if !ok || descriptor.Inspect == nil {
		return exchange.Inspection{}, fmt.Errorf("execution: 场所 %q 不支持密钥检测", name)
	}
	return descriptor.Inspect(ctx, request)
}

type Registry struct {
	mu          sync.RWMutex
	factories   map[exchange.Exchange]AdapterFactory
	descriptors map[exchange.Exchange]ExchangeDescriptor
}

func NewRegistry() *Registry {
	return &Registry{
		factories:   make(map[exchange.Exchange]AdapterFactory),
		descriptors: make(map[exchange.Exchange]ExchangeDescriptor),
	}
}

func (r *Registry) Register(descriptor ExchangeDescriptor, factory AdapterFactory) {
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
func (r *Registry) Descriptor(name exchange.Exchange) (ExchangeDescriptor, bool) {
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
	descriptor := r.descriptors[config.Exchange]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("execution: unregistered Exchange %q", config.Exchange)
	}
	if descriptor.WholeAccount != nil && descriptor.WholeAccount(config) {
		adapter, err := factory(config, credential)
		if err != nil {
			return nil, fmt.Errorf("execution: bind %s account %q: %w", config.Exchange, config.TradingAccountID, err)
		}
		if adapter == nil {
			return nil, fmt.Errorf("execution: %s factory returned nil adapter", config.Exchange)
		}
		if identity, ok := adapter.(ExchangeIdentity); !ok || identity.Exchange() != config.Exchange {
			return nil, fmt.Errorf("execution: adapter identity does not match account Exchange %q", config.Exchange)
		}
		return adapter, nil
	}
	// 每个市场绑一个单市场适配器；开通多个市场时合成一个按市场路由的适配器。
	adapters := make(map[exchange.MarketType]ExecutionAdapter, len(config.Markets))
	for _, market := range config.Markets {
		single := config
		single.Markets = []exchange.MarketType{market}
		if !exchange.RuleFor(market).Leverage {
			single.MarginMode = exchange.MarginModeUnspecified
		}
		adapter, err := factory(single, credential)
		if err != nil {
			return nil, fmt.Errorf("execution: bind %s account %q market %s: %w", config.Exchange, config.TradingAccountID, market, err)
		}
		if adapter == nil {
			return nil, fmt.Errorf("execution: %s factory returned nil adapter", config.Exchange)
		}
		identity, ok := adapter.(ExchangeIdentity)
		if !ok || identity.Exchange() != config.Exchange {
			return nil, fmt.Errorf("execution: adapter identity does not match account Exchange %q", config.Exchange)
		}
		adapters[market] = adapter
	}
	if len(config.Markets) == 1 {
		return adapters[config.Markets[0]], nil
	}
	return NewMultiMarket(config.Exchange, config.Markets, adapters)
}

func validateBinding(config exchange.AccountConfig) error {
	if strings.TrimSpace(config.TradingAccountID) == "" ||
		!config.Exchange.Valid() ||
		!config.ExecutionMode.Valid() ||
		strings.TrimSpace(config.SettlementAsset) == "" {
		return fmt.Errorf("execution: invalid account binding")
	}
	normalized, err := exchange.NormalizeMarkets(config.Markets)
	if err != nil || !slices.Equal(normalized, config.Markets) {
		return fmt.Errorf("execution: invalid account markets %v", config.Markets)
	}
	if !slices.ContainsFunc(config.Markets, func(market exchange.MarketType) bool { return exchange.RuleFor(market).Leverage }) {
		if config.MarginMode != exchange.MarginModeUnspecified {
			return fmt.Errorf("execution: account without SWAP cannot configure margin mode")
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
