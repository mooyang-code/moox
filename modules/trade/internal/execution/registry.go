package execution

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// MarketDataFactory 构造一个场所的公共行情源（不需要密钥），供模拟盘与公共行情查询使用。
type MarketDataFactory func(exchange.AccountConfig) (PublicMarketData, error)

// AdapterFactory 构造一个场所的实盘下单通道适配器（需要密钥）；不支持实盘的场所登记为 nil。
type AdapterFactory func(exchange.AccountConfig, exchange.Credential) (ExecutionAdapter, error)

type ExchangeIdentity interface {
	Exchange() exchange.Exchange
}

// ExchangeDescriptor 是场所注册时登记的静态信息，不依赖适配器实例，未就绪的账户也能取到。
type ExchangeDescriptor struct {
	Exchange     exchange.Exchange
	OrderTypes   []exchange.OrderType
	FillPolicies []exchange.FillPolicy
	// Markets 是场所支持开通的市场，建账户时校验；BINANCE、OKX 是现货和永续，ASHARE、HKEX 只有股票。
	Markets []exchange.MarketType
	// SettlementAssets 是场所允许的结算资产；为空表示不限制（由市场规则自己校验）。
	SettlementAssets []string
	// LiveUnavailable 非空时这个场所还不能建实盘账户，内容是给人看的原因。
	LiveUnavailable string
	// Session 是交易时段；nil 表示全天可交易。
	Session TradingSession
	// SessionHours 是给人看的下单时段说明，手动下单不在时段内时写进提示。
	SessionHours string
	// InstrumentScope 是给人看的标的范围说明：场所的标的表不是全市场时填（港股模拟盘只收录恒生指数成分），
	// 目标里的标的在各成员的标的表里都找不到时写进原因；为空表示标的表就是场所的全部标的。
	InstrumentScope string
	// Inspect 检测这个场所的密钥（账户类型、权限）；nil 表示不支持检测（券商、模拟盘）。
	Inspect exchange.Inspector
	// WholeAccount 报告某个账户配置是否由一个适配器同时服务全部市场（币安统一账户：一个余额接口、一条私有流），
	// 为真时注册表只调用一次工厂并把全部市场交给它，不再按市场拆成多个单市场适配器；nil 表示按市场拆分。
	WholeAccount func(exchange.AccountConfig) bool
}

// ExchangeCatalog 按场所名查询静态信息。
type ExchangeCatalog interface {
	Descriptor(name exchange.Exchange) (ExchangeDescriptor, bool)
}

// ValidateAccount 校验场所能不能建这种账户：场所已登记、支持这些市场、结算资产对得上、实盘已经接入。
// 模拟账户和实盘账户建立时共用；只在建账户（和改结算资产）的路径上调用，不进会话绑定，
// 库里已有的旧账户不会因此在启动时全部加载失败。目录为空（只在单元测试里）时不校验。
func ValidateAccount(
	exchanges ExchangeCatalog,
	name exchange.Exchange,
	markets []exchange.MarketType,
	settlementAsset string,
	mode exchange.ExecutionMode,
) error {
	if exchanges == nil {
		return nil
	}
	descriptor, ok := exchanges.Descriptor(name)
	if !ok {
		return fmt.Errorf("不支持的场所 %q", name)
	}
	for _, market := range markets {
		if len(descriptor.Markets) != 0 && !slices.Contains(descriptor.Markets, market) {
			return fmt.Errorf("场所 %s 不支持开通市场 %s", name, market)
		}
	}
	if len(descriptor.SettlementAssets) != 0 &&
		!slices.Contains(descriptor.SettlementAssets, strings.ToUpper(strings.TrimSpace(settlementAsset))) {
		return fmt.Errorf("场所 %s 的结算资产只能是 %s", name, strings.Join(descriptor.SettlementAssets, "、"))
	}
	if mode == exchange.ExecutionModeLive && descriptor.LiveUnavailable != "" {
		return errors.New(descriptor.LiveUnavailable)
	}
	return nil
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
	marketData  map[exchange.Exchange]MarketDataFactory
	live        map[exchange.Exchange]AdapterFactory
	descriptors map[exchange.Exchange]ExchangeDescriptor
}

func NewRegistry() *Registry {
	return &Registry{
		marketData:  make(map[exchange.Exchange]MarketDataFactory),
		live:        make(map[exchange.Exchange]AdapterFactory),
		descriptors: make(map[exchange.Exchange]ExchangeDescriptor),
	}
}

// Register 登记一个场所：公共行情工厂必填；下单通道工厂可为空，为空表示这个场所不能建实盘账户。
func (r *Registry) Register(descriptor ExchangeDescriptor, marketData MarketDataFactory, live AdapterFactory) {
	name := descriptor.Exchange
	if !name.Valid() || marketData == nil {
		panic("execution: Register requires a supported Exchange and market data factory")
	}
	if live == nil && strings.TrimSpace(descriptor.LiveUnavailable) == "" {
		descriptor.LiveUnavailable = fmt.Sprintf("场所 %s 没有实盘下单通道", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.marketData[name]; exists {
		panic(fmt.Sprintf("execution: duplicate registration for %q", name))
	}
	r.marketData[name] = marketData
	if live != nil {
		r.live[name] = live
	}
	r.descriptors[name] = descriptor
}

// Descriptor 返回场所的静态信息。
func (r *Registry) Descriptor(name exchange.Exchange) (ExchangeDescriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	descriptor, ok := r.descriptors[name]
	return descriptor, ok
}

// BindMarketData 按账户配置绑定场所的公共行情源（供模拟盘使用，不校验也不传递交易凭据）。
func (r *Registry) BindMarketData(config exchange.AccountConfig) (PublicMarketData, error) {
	if err := validateBinding(config); err != nil {
		return nil, err
	}
	r.mu.RLock()
	factory, ok := r.marketData[config.Exchange]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("execution: unregistered Exchange %q", config.Exchange)
	}
	sources := make(map[exchange.MarketType]PublicMarketData, len(config.Markets))
	for _, market := range config.Markets {
		single := config
		single.Markets = []exchange.MarketType{market}
		if !exchange.RuleFor(market).Leverage {
			single.MarginMode = exchange.MarginModeUnspecified
		}
		source, err := factory(single)
		if err != nil {
			return nil, fmt.Errorf("execution: bind %s market data for account %q market %s: %w", config.Exchange, config.TradingAccountID, market, err)
		}
		if source == nil {
			return nil, fmt.Errorf("execution: %s market data factory returned nil source", config.Exchange)
		}
		identity, ok := source.(ExchangeIdentity)
		if !ok || identity.Exchange() != config.Exchange {
			return nil, fmt.Errorf("execution: market data identity does not match account Exchange %q", config.Exchange)
		}
		sources[market] = source
	}
	if len(config.Markets) == 1 {
		return sources[config.Markets[0]], nil
	}
	return NewMultiMarketData(config.Exchange, config.Markets, sources)
}

// Bind 按账户配置与凭据绑定场所的实盘下单通道适配器；没有登记下单通道的场所直接拒绝。
func (r *Registry) Bind(config exchange.AccountConfig, credential exchange.Credential) (ExecutionAdapter, error) {
	if err := validateBinding(config); err != nil {
		return nil, err
	}
	if config.ExecutionMode != exchange.ExecutionModeLive {
		return nil, fmt.Errorf("execution: Bind 只用于实盘账户，模拟盘请使用 BindMarketData")
	}
	r.mu.RLock()
	_, registered := r.marketData[config.Exchange]
	factory := r.live[config.Exchange]
	descriptor := r.descriptors[config.Exchange]
	r.mu.RUnlock()
	if !registered {
		return nil, fmt.Errorf("execution: unregistered Exchange %q", config.Exchange)
	}
	if factory == nil {
		return nil, errors.New(descriptor.LiveUnavailable)
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
