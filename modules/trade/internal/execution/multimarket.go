package execution

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// 编译期确认多市场组合适配器实现了全部可选能力（按子适配器是否具备在运行时再判断）。
var (
	_ ExecutionAdapter          = (*MultiMarket)(nil)
	_ ExchangeIdentity          = (*MultiMarket)(nil)
	_ SymbolConfigurer          = (*MultiMarket)(nil)
	_ AccountEventSource        = (*MultiMarket)(nil)
	_ LedgerSource              = (*MultiMarket)(nil)
	_ CapabilityProbe           = (*MultiMarket)(nil)
	_ WalletTransferer          = (*MultiMarket)(nil)
	_ FeeDiscountConfigurer     = (*MultiMarket)(nil)
	_ DustConverter             = (*MultiMarket)(nil)
	_ FundingRateSource         = (*MultiMarket)(nil)
	_ exchange.RateStatusSource = (*MultiMarket)(nil)
)

// MultiMarket 把每个市场一个的单市场适配器合成一个执行账户的适配器：一把密钥同时做现货和合约。
// 所有按标的的调用都按标的所属市场路由，现货和合约同名的代码（BTCUSDT）因此不会串；
// 账户级的读取（余额、持仓、挂单）合并各市场的结果，每条记录自己带市场。
type MultiMarket struct {
	name     exchange.Exchange
	markets  []exchange.MarketType
	adapters map[exchange.MarketType]ExecutionAdapter
}

// NewMultiMarket 按 markets 的顺序组合适配器；每个市场都必须有对应的适配器。
func NewMultiMarket(name exchange.Exchange, markets []exchange.MarketType, adapters map[exchange.MarketType]ExecutionAdapter) (*MultiMarket, error) {
	for _, market := range markets {
		if adapters[market] == nil {
			return nil, fmt.Errorf("execution: 市场 %s 没有适配器", market)
		}
	}
	return &MultiMarket{name: name, markets: append([]exchange.MarketType(nil), markets...), adapters: adapters}, nil
}

func (m *MultiMarket) Exchange() exchange.Exchange { return m.name }

// Markets 返回已开通的市场。
func (m *MultiMarket) Markets() []exchange.MarketType {
	return append([]exchange.MarketType(nil), m.markets...)
}

func (m *MultiMarket) route(market exchange.MarketType) (ExecutionAdapter, error) {
	if adapter := m.adapters[market]; adapter != nil {
		return adapter, nil
	}
	return nil, &exchange.Error{Kind: exchange.ErrorRejected, Err: fmt.Errorf("账户没有开通市场 %q", market)}
}

func (m *MultiMarket) LoadInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	var all []exchange.Instrument
	for _, market := range m.markets {
		items, err := m.adapters[market].LoadInstruments(ctx)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
	}
	return all, nil
}

func (m *MultiMarket) GetQuote(ctx context.Context, key exchange.SymbolKey) (MarketQuote, error) {
	adapter, err := m.route(key.Market)
	if err != nil {
		return MarketQuote{}, err
	}
	return adapter.GetQuote(ctx, key)
}

func (m *MultiMarket) GetReferencePrice(ctx context.Context, key exchange.SymbolKey) (exchange.ReferencePrice, error) {
	adapter, err := m.route(key.Market)
	if err != nil {
		return exchange.ReferencePrice{}, err
	}
	return adapter.GetReferencePrice(ctx, key)
}

func (m *MultiMarket) GetOrderByExchangeID(ctx context.Context, key exchange.SymbolKey, exchangeOrderID string) (exchange.Order, error) {
	adapter, err := m.route(key.Market)
	if err != nil {
		return exchange.Order{}, err
	}
	return adapter.GetOrderByExchangeID(ctx, key, exchangeOrderID)
}

func (m *MultiMarket) GetOrder(ctx context.Context, key exchange.SymbolKey, clientOrderID string) (exchange.Order, error) {
	adapter, err := m.route(key.Market)
	if err != nil {
		return exchange.Order{}, err
	}
	return adapter.GetOrder(ctx, key, clientOrderID)
}

func (m *MultiMarket) CancelOrder(ctx context.Context, key exchange.SymbolKey, clientOrderID string) (exchange.Order, error) {
	adapter, err := m.route(key.Market)
	if err != nil {
		return exchange.Order{}, err
	}
	return adapter.CancelOrder(ctx, key, clientOrderID)
}

func (m *MultiMarket) PlaceOrder(ctx context.Context, request exchange.OrderRequest) (exchange.Order, error) {
	adapter, err := m.route(request.Market)
	if err != nil {
		return exchange.Order{}, err
	}
	return adapter.PlaceOrder(ctx, request)
}

func (m *MultiMarket) ListRecentFills(ctx context.Context, key exchange.SymbolKey, cursor string) ([]exchange.Fill, string, error) {
	adapter, err := m.route(key.Market)
	if err != nil {
		return nil, "", err
	}
	return adapter.ListRecentFills(ctx, key, cursor)
}

func (m *MultiMarket) ListOpenOrders(ctx context.Context) ([]exchange.Order, error) {
	var all []exchange.Order
	for _, market := range m.markets {
		items, err := m.adapters[market].ListOpenOrders(ctx)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
	}
	return all, nil
}

func (m *MultiMarket) ListPositionSnapshots(ctx context.Context) ([]exchange.Position, error) {
	var all []exchange.Position
	for _, market := range m.markets {
		items, err := m.adapters[market].ListPositionSnapshots(ctx)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
	}
	return all, nil
}

// GetAccountSnapshot 合并各市场读到的余额：每个钱包的余额各记各的，每个钱包记自己的读取时间。
// 总体的更新时间取各钱包里最早的，数值字段相加；统一权益由 T10 的公式另行计算。
func (m *MultiMarket) GetAccountSnapshot(ctx context.Context) (exchange.AccountSnapshot, error) {
	merged := exchange.AccountSnapshot{WalletUpdatedAt: map[exchange.Wallet]time.Time{}}
	merged.Present = exchange.AccountSnapshotPresence{
		Balances: true, Equity: true, AvailableFunds: true, UsedMargin: true, MaintenanceMargin: true, UnrealizedPnL: true,
	}
	for _, market := range m.markets {
		part, err := m.adapters[market].GetAccountSnapshot(ctx)
		if err != nil {
			return exchange.AccountSnapshot{}, err
		}
		// 两个市场共用一个钱包（OKX 统一账户）时读到的是同一份账户数据，只计一次，不能相加。
		sharedWallet := slices.ContainsFunc(part.Balances, func(balance exchange.AssetBalance) bool {
			_, seen := merged.WalletUpdatedAt[balance.Wallet]
			return seen
		})
		if !sharedWallet {
			merged.Balances = append(merged.Balances, part.Balances...)
			merged.Equity = merged.Equity.Add(part.Equity)
			merged.AvailableFunds = merged.AvailableFunds.Add(part.AvailableFunds)
			merged.UsedMargin = merged.UsedMargin.Add(part.UsedMargin)
			merged.MaintenanceMargin = merged.MaintenanceMargin.Add(part.MaintenanceMargin)
			merged.UnrealizedPnL = merged.UnrealizedPnL.Add(part.UnrealizedPnL)
			merged.ExchangeEquity = merged.ExchangeEquity.Add(part.ExchangeEquity)
			for _, balance := range part.Balances {
				merged.WalletUpdatedAt[balance.Wallet] = part.ExchangeUpdatedAt
			}
		}
		if merged.ExchangeUpdatedAt.IsZero() || (!part.ExchangeUpdatedAt.IsZero() && part.ExchangeUpdatedAt.Before(merged.ExchangeUpdatedAt)) {
			merged.ExchangeUpdatedAt = part.ExchangeUpdatedAt
		}
		merged.Present = exchange.AccountSnapshotPresence{
			Balances: merged.Present.Balances && part.Present.Balances, Equity: merged.Present.Equity && part.Present.Equity,
			AvailableFunds:    merged.Present.AvailableFunds && part.Present.AvailableFunds,
			UsedMargin:        merged.Present.UsedMargin && part.Present.UsedMargin,
			MaintenanceMargin: merged.Present.MaintenanceMargin && part.Present.MaintenanceMargin,
			UnrealizedPnL:     merged.Present.UnrealizedPnL && part.Present.UnrealizedPnL,
		}
		merged.RequiresSync = merged.RequiresSync || part.RequiresSync
	}
	return merged, nil
}

// SetLeverage、SetMarginMode 只有合约（需要杠杆的市场）的适配器实现，按标的所属市场路由。
func (m *MultiMarket) SetLeverage(ctx context.Context, key exchange.SymbolKey, leverage shared.Decimal) error {
	adapter, err := m.route(key.Market)
	if err != nil {
		return err
	}
	configurer, ok := adapter.(SymbolConfigurer)
	if !ok {
		return &exchange.Error{Kind: exchange.ErrorRejected, Err: fmt.Errorf("市场 %s 不支持设置杠杆", key.Market)}
	}
	return configurer.SetLeverage(ctx, key, leverage)
}

func (m *MultiMarket) SetMarginMode(ctx context.Context, key exchange.SymbolKey, mode exchange.MarginMode) error {
	adapter, err := m.route(key.Market)
	if err != nil {
		return err
	}
	configurer, ok := adapter.(SymbolConfigurer)
	if !ok {
		return &exchange.Error{Kind: exchange.ErrorRejected, Err: fmt.Errorf("市场 %s 不支持设置保证金模式", key.Market)}
	}
	return configurer.SetMarginMode(ctx, key, mode)
}

// RateStatus 取各市场里用量最高的那个，暂停时间取最晚的。
func (m *MultiMarket) RateStatus() exchange.RateStatus {
	var result exchange.RateStatus
	for _, market := range m.markets {
		source, ok := m.adapters[market].(exchange.RateStatusSource)
		if !ok {
			continue
		}
		status := source.RateStatus()
		if status.UsageRatio() > result.UsageRatio() {
			result.UsedWeight, result.WeightLimit = status.UsedWeight, status.WeightLimit
		}
		if status.PausedUntil.After(result.PausedUntil) {
			result.PausedUntil, result.PauseReason = status.PausedUntil, status.PauseReason
		}
		if status.OrdersPausedUntil.After(result.OrdersPausedUntil) {
			result.OrdersPausedUntil = status.OrdersPausedUntil
		}
	}
	return result
}

// LedgerSupported 报告是否有任何一个市场支持资金流水。
func (m *MultiMarket) LedgerSupported() bool {
	for _, market := range m.markets {
		if source, ok := m.adapters[market].(LedgerSource); ok && source.LedgerSupported() {
			return true
		}
	}
	return false
}

// ListLedger 按查询里的市场路由；没写市场时只在一个市场支持流水的情况下取它。
func (m *MultiMarket) ListLedger(ctx context.Context, query exchange.LedgerQuery) (exchange.LedgerPage, error) {
	market := query.Market
	if market == exchange.MarketTypeUnspecified {
		for _, candidate := range m.markets {
			if source, ok := m.adapters[candidate].(LedgerSource); ok && source.LedgerSupported() {
				if market != exchange.MarketTypeUnspecified {
					return exchange.LedgerPage{}, &exchange.Error{Kind: exchange.ErrorRejected, Err: fmt.Errorf("多个市场都有资金流水，请指定市场")}
				}
				market = candidate
			}
		}
	}
	adapter, err := m.route(market)
	if err != nil {
		return exchange.LedgerPage{}, err
	}
	source, ok := adapter.(LedgerSource)
	if !ok || !source.LedgerSupported() {
		return exchange.LedgerPage{}, &exchange.Error{Kind: exchange.ErrorRejected, Err: fmt.Errorf("市场 %s 不支持资金流水", market)}
	}
	query.Market = market
	page, err := source.ListLedger(ctx, query)
	if err != nil {
		return exchange.LedgerPage{}, err
	}
	// 按市场区分的流水带上所属市场，同名的现货和合约代码不会混淆；账户级流水（划转等）不标市场。
	for i := range page.Entries {
		if page.Entries[i].Market == exchange.MarketTypeUnspecified && page.Entries[i].Type.MarketScoped() {
			page.Entries[i].Market = market
		}
	}
	return page, nil
}

// SupportsAccountEvents 报告每个市场的适配器是否都有私有流；任何一个没有就整体没有，会话用轮询同步。
func (m *MultiMarket) SupportsAccountEvents() bool {
	for _, market := range m.markets {
		if _, ok := m.adapters[market].(AccountEventSource); !ok {
			return false
		}
	}
	return true
}

// Subscribe 同时订阅各市场的私有流，任何一路断开都整体结束并上报，由会话重连；
// 全部订阅成功后才通知会话已订阅。
func (m *MultiMarket) Subscribe(ctx context.Context, handler AccountEventHandler) error {
	sources := make([]AccountEventSource, 0, len(m.markets))
	for _, market := range m.markets {
		source, ok := m.adapters[market].(AccountEventSource)
		if !ok {
			return fmt.Errorf("execution: 市场 %s 的适配器没有私有流", market)
		}
		sources = append(sources, source)
	}
	if len(sources) == 1 {
		return sources[0].Subscribe(ctx, handler)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	aggregate := &subscribedOnce{AccountEventHandler: handler, remaining: len(sources)}
	results := make(chan error, len(sources))
	for _, source := range sources {
		go func() { results <- source.Subscribe(ctx, aggregate) }()
	}
	first := <-results
	cancel()
	for range len(sources) - 1 {
		<-results
	}
	return first
}

// subscribedOnce 在所有子流都订阅成功后才转发一次 OnSubscribed。
type subscribedOnce struct {
	AccountEventHandler
	mu        sync.Mutex
	remaining int
}

func (s *subscribedOnce) OnSubscribed() {
	s.mu.Lock()
	s.remaining--
	ready := s.remaining == 0
	s.mu.Unlock()
	if ready {
		s.AccountEventHandler.OnSubscribed()
	}
}

// Supports 报告某个可选能力是否至少有一个市场的适配器具备。
func (m *MultiMarket) Supports(capability string) bool {
	for _, market := range m.markets {
		if m.supporting(market, capability) {
			return true
		}
	}
	return false
}

// supporting 报告某个市场的适配器是否实现并启用了某个可选能力。
func (m *MultiMarket) supporting(market exchange.MarketType, capability string) bool {
	adapter := m.adapters[market]
	switch capability {
	case CapabilityTransfer:
		_, ok := adapter.(WalletTransferer)
		return ok && Supports(adapter, capability)
	case CapabilityFeeDiscount:
		_, ok := adapter.(FeeDiscountConfigurer)
		return ok && Supports(adapter, capability)
	case CapabilityDust:
		_, ok := adapter.(DustConverter)
		return ok && Supports(adapter, capability)
	}
	return false
}

func (m *MultiMarket) unsupported(capability string) error {
	return &exchange.Error{Kind: exchange.ErrorRejected, Err: fmt.Errorf("账户不支持 %s", capability)}
}

// Transfer、ListTransfers 由第一个具备划转能力的市场适配器完成（币安普通账户的划转挂在现货适配器上）。
func (m *MultiMarket) Transfer(ctx context.Context, from, to exchange.Wallet, asset string, amount shared.Decimal, clientRef string) (TransferReceipt, error) {
	for _, market := range m.markets {
		if m.supporting(market, CapabilityTransfer) {
			return m.adapters[market].(WalletTransferer).Transfer(ctx, from, to, asset, amount, clientRef)
		}
	}
	return TransferReceipt{}, m.unsupported(CapabilityTransfer)
}

func (m *MultiMarket) ListTransfers(ctx context.Context, since time.Time) ([]TransferRecord, error) {
	for _, market := range m.markets {
		if m.supporting(market, CapabilityTransfer) {
			return m.adapters[market].(WalletTransferer).ListTransfers(ctx, since)
		}
	}
	return nil, m.unsupported(CapabilityTransfer)
}

// SetBNBFeeDiscount 让每个具备抵扣能力的市场适配器各设自己负责的那部分（现货、合约一起设）。
func (m *MultiMarket) SetBNBFeeDiscount(ctx context.Context, enabled bool) error {
	done := false
	for _, market := range m.markets {
		if !m.supporting(market, CapabilityFeeDiscount) {
			continue
		}
		if err := m.adapters[market].(FeeDiscountConfigurer).SetBNBFeeDiscount(ctx, enabled); err != nil {
			return err
		}
		done = true
	}
	if !done {
		return m.unsupported(CapabilityFeeDiscount)
	}
	return nil
}

// GetBNBFeeDiscount 报告各部分抵扣是否全部开启。
func (m *MultiMarket) GetBNBFeeDiscount(ctx context.Context) (bool, error) {
	seen := false
	for _, market := range m.markets {
		if !m.supporting(market, CapabilityFeeDiscount) {
			continue
		}
		on, err := m.adapters[market].(FeeDiscountConfigurer).GetBNBFeeDiscount(ctx)
		if err != nil || !on {
			return false, err
		}
		seen = true
	}
	if !seen {
		return false, m.unsupported(CapabilityFeeDiscount)
	}
	return true, nil
}

// ListDust、ConvertDust 合并各具备小额兑换能力的市场适配器的结果。
func (m *MultiMarket) ListDust(ctx context.Context) ([]exchange.DustAsset, error) {
	var all []exchange.DustAsset
	for _, market := range m.markets {
		if !m.supporting(market, CapabilityDust) {
			continue
		}
		items, err := m.adapters[market].(DustConverter).ListDust(ctx)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
	}
	return all, nil
}

func (m *MultiMarket) ConvertDust(ctx context.Context, assets []string) (exchange.DustConversion, error) {
	var merged exchange.DustConversion
	for _, market := range m.markets {
		if !m.supporting(market, CapabilityDust) {
			continue
		}
		part, err := m.adapters[market].(DustConverter).ConvertDust(ctx, assets)
		if err != nil {
			return exchange.DustConversion{}, err
		}
		merged.BNBReceived = merged.BNBReceived.Add(part.BNBReceived)
		merged.Converted = append(merged.Converted, part.Converted...)
	}
	return merged, nil
}

// SupportsFundingRates 报告永续市场的适配器是否能查资金费率；没开通永续时没有。
func (m *MultiMarket) SupportsFundingRates() bool {
	return FundingRatesOf(m.adapters[exchange.MarketTypeSwap]) != nil
}

// ListFundingRates 交给永续市场的适配器查。
func (m *MultiMarket) ListFundingRates(ctx context.Context, start, end time.Time) ([]exchange.FundingRate, error) {
	source := FundingRatesOf(m.adapters[exchange.MarketTypeSwap])
	if source == nil {
		return nil, &exchange.Error{Kind: exchange.ErrorRejected, Err: fmt.Errorf("账户的永续市场不支持资金费率查询")}
	}
	return source.ListFundingRates(ctx, start, end)
}
