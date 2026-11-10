package target

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/fundmaint"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/fundsettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

type EquitySource interface {
	ResolveLogicalAccountEquity(context.Context, string, string) (shared.Decimal, int64, error)
}

type WeightConversion struct {
	RequestHash            string
	SignalTime             int64
	Equity                 shared.Decimal
	EquitySourceTime       int64
	ReferencePrices        map[string]string
	ReferencePriceEvidence []ReferencePriceEvidence
	WeightsJSON            string
	QuantityTargets        []store.InstrumentTarget
}

// ReferencePriceEvidence records the exact account and quote selected for a
// weight-to-quantity conversion. It is persisted with the immutable target
// receipt so a later audit can reconstruct the conversion inputs.
type ReferencePriceEvidence struct {
	InstrumentID     string `json:"instrument_id"`
	TradingAccountID string `json:"trading_account_id"`
	ExchangeSymbol   string `json:"exchange_symbol"`
	Price            string `json:"price"`
	UpdatedAt        int64  `json:"updated_at"`
}

type WeightResolver struct {
	Store  *store.Store
	Prices PriceSource
	Equity EquitySource
	// Exchanges 提供场所的交易时段，参考价新鲜度按它判断；为空时全天按时段内处理。
	Exchanges   execution.ExchangeCatalog
	Now         func() time.Time
	MaxPriceAge time.Duration

	// warned 记录排除资产告警的日期（组合|资产 -> 日期），同一个组合、同一个资产每天只告警一次。
	warnMu sync.Mutex
	warned map[string]string
}

// ErrPermanent marks a target request that cannot become valid by retrying
// market or account state (for example malformed weights or a negative Spot
// allocation). The EventBus consumer TERM's these messages rather than
// redelivering them forever.
var ErrPermanent = errors.New("permanent target weight request")

// These are Trade-owned safety ceilings. Strategy is free to emit partial
// gross exposure, but a malformed or compromised producer must not be able to
// turn one message into an unbounded position request.
var (
	defaultMaxSingleWeight = shared.MustDecimal("10")
	defaultMaxGrossWeight  = shared.MustDecimal("20")
	// Logical targets are expressed in base units, before the executor fans
	// them out to exchange-specific child orders. Keep a exchange-independent finite
	// precision for the persisted snapshot; child orders apply each exchange's
	// own quantity step later.
	logicalTargetQuantityStep = shared.MustDecimal("0.000000000000000001")
)

// heldConversion 是全部标的都保持时的换算结果：不换算任何数量，不带权益和报价。
func heldConversion(requestHash string, signalTime int64, weightsJSON string, orderedWeights []orderedWeight) WeightConversion {
	held := make([]store.InstrumentTarget, 0, len(orderedWeights))
	for _, weight := range orderedWeights {
		held = append(held, store.InstrumentTarget{InstrumentID: weight.InstrumentID, Hold: true})
	}
	return WeightConversion{RequestHash: requestHash, SignalTime: signalTime, Equity: shared.Zero(), WeightsJSON: weightsJSON, ReferencePrices: map[string]string{}, ReferencePriceEvidence: []ReferencePriceEvidence{}, QuantityTargets: held}
}

func (r *WeightResolver) Resolve(ctx context.Context, _ int64, request *tradeeventpb.LogicalAccountTargetWeightRequested, spaceID string) (WeightConversion, error) {
	if r == nil || r.Store == nil || r.Prices == nil || r.Equity == nil {
		return WeightConversion{}, fmt.Errorf("%w: target weight resolver is not configured", ErrPermanent)
	}
	if request == nil || request.GetTargetId() == "" || request.GetLogicalAccountId() == "" || request.GetInstanceId() == "" || request.GetSessionId() == "" || request.GetStrategyId() == "" || request.GetBarEndTime() == nil || request.GetEffectiveAt() == nil || request.GetValidUntil() == nil {
		return WeightConversion{}, fmt.Errorf("%w: target weight request is incomplete", ErrPermanent)
	}
	if err := request.GetBarEndTime().CheckValid(); err != nil {
		return WeightConversion{}, fmt.Errorf("%w: invalid bar_end_time: %v", ErrPermanent, err)
	}
	if err := request.GetEffectiveAt().CheckValid(); err != nil || !request.GetEffectiveAt().AsTime().Equal(request.GetBarEndTime().AsTime()) {
		return WeightConversion{}, fmt.Errorf("%w: effective_at must equal bar_end_time", ErrPermanent)
	}
	if err := request.GetValidUntil().CheckValid(); err != nil || !request.GetValidUntil().AsTime().After(request.GetEffectiveAt().AsTime()) {
		return WeightConversion{}, fmt.Errorf("%w: valid_until must be after effective_at", ErrPermanent)
	}
	requestHash, weightsJSON, orderedWeights, err := canonicalWeights(request)
	if err != nil {
		return WeightConversion{}, fmt.Errorf("%w: %v", ErrPermanent, err)
	}
	if err := validateWeightRisk(orderedWeights); err != nil {
		return WeightConversion{}, fmt.Errorf("%w: %v", ErrPermanent, err)
	}
	// The period carried by the event is the sole business timestamp. Do not
	// fall back to exchange or local processing time when it is malformed.
	signalTime := request.GetBarEndTime().AsTime().UnixMilli()
	// An empty FULL target is an explicit flatten command. It must not depend
	// on an equity snapshot or a quote for an instrument that no longer exists.
	if len(orderedWeights) == 0 {
		return WeightConversion{RequestHash: requestHash, SignalTime: signalTime, Equity: shared.Zero(), WeightsJSON: weightsJSON, ReferencePrices: map[string]string{}, ReferencePriceEvidence: []ReferencePriceEvidence{}, QuantityTargets: []store.InstrumentTarget{}}, nil
	}
	// 全部是保持标的的目标不换算任何数量，不依赖权益快照与报价：权益读取暂时失败时保持标记也必须被接受，
	// 否则旧的调仓目标会继续执行，保持保护失效。
	if allHold(orderedWeights) {
		return heldConversion(requestHash, signalTime, weightsJSON, orderedWeights), nil
	}
	// 资金设置在空目标（清仓）和全保持的提前返回之后才读：设置解析失败不能挡住清仓。
	logical, err := r.Store.GetLogicalAccount(ctx, spaceID, request.GetLogicalAccountId())
	if err != nil {
		return WeightConversion{}, err
	}
	// 与执行器、受管资产同一个读取入口：数值字段坏了取默认值，排除字段读不出来才报错；
	// 不用严格解析，不然后来加的校验会让早先保存的设置整个读不出来。
	settings, err := r.Store.ResolveFundSettings(logical)
	if err != nil {
		return WeightConversion{}, err
	}
	members, err := r.Store.ListLogicalAccountMembers(ctx, spaceID, request.GetLogicalAccountId(), false)
	if err != nil {
		return WeightConversion{}, err
	}
	if len(members) == 0 {
		return WeightConversion{}, fmt.Errorf("target weight resolver: logical account has no members")
	}
	// 排除资产（BNB 保留、稳定币保证金、保证金币、底仓）只跳过这一项并告警，其余照常执行。
	// 跳过的标的按“保持”处理：既不加仓也不减仓，整份目标全被跳过时也不会变成清仓指令，也不需要权益和报价。
	if settings.HasExclusions() {
		if err := r.skipExcludedTargets(ctx, spaceID, request.GetLogicalAccountId(), settings, members, orderedWeights); err != nil {
			return WeightConversion{}, err
		}
	}
	if allHold(orderedWeights) {
		return heldConversion(requestHash, signalTime, weightsJSON, orderedWeights), nil
	}
	equity, equityTime, err := r.Equity.ResolveLogicalAccountEquity(ctx, spaceID, request.GetLogicalAccountId())
	if err != nil {
		return WeightConversion{}, err
	}
	prices := make(map[string]string, len(orderedWeights))
	evidence := make([]ReferencePriceEvidence, 0, len(orderedWeights))
	quantities := make([]store.InstrumentTarget, 0, len(orderedWeights))
	quotes := make(map[string]resolvedQuote, len(orderedWeights))
	// 先定每个标的由哪个成员报价，再按账户批量预取（A 股腾讯快照一次 60 只），之后逐个取价命中缓存。
	for _, weight := range orderedWeights {
		if weight.Hold {
			continue
		}
		instrument, tradingAccountID, err := r.instrumentForMembers(ctx, spaceID, members, weight.InstrumentID)
		if err != nil {
			return WeightConversion{}, err
		}
		quotes[weight.InstrumentID] = resolvedQuote{instrument: instrument, tradingAccountID: tradingAccountID}
	}
	if prefetcher, ok := r.Prices.(QuotePrefetchSource); ok {
		byAccount := make(map[string][]exchange.SymbolKey)
		for _, resolved := range quotes {
			byAccount[resolved.tradingAccountID] = append(byAccount[resolved.tradingAccountID],
				exchange.NewSymbolKey(resolved.instrument.MarketType, resolved.instrument.ExchangeSymbol))
		}
		for tradingAccountID, keys := range byAccount {
			prefetcher.PrefetchQuotes(ctx, tradingAccountID, keys)
		}
	}
	for _, weight := range orderedWeights {
		if weight.Hold {
			continue
		}
		resolved := quotes[weight.InstrumentID]
		quote, err := r.Prices.LatestPrice(ctx, resolved.tradingAccountID, exchange.NewSymbolKey(resolved.instrument.MarketType, resolved.instrument.ExchangeSymbol))
		if err != nil {
			return WeightConversion{}, err
		}
		if quote.Price.Cmp(shared.Zero()) <= 0 || quote.UpdatedAt.IsZero() || !r.priceFresh(resolved.instrument.Exchange, quote.UpdatedAt) {
			return WeightConversion{}, fmt.Errorf("target weight resolver: invalid reference price for %s", weight.InstrumentID)
		}
		resolved.quote = quote
		quotes[weight.InstrumentID] = resolved
	}
	unmanaged, err := r.unmanagedTargetValue(ctx, spaceID, members, quotes)
	if err != nil {
		return WeightConversion{}, err
	}
	equity = equity.Add(unmanaged)
	// 换算用的权益再扣资金预留比例，以及 BNB 保留金额还差的部分：BNB 本身不计入权益，所以买到位后不再扣。
	equity = equity.Sub(equity.Mul(settings.ReserveRatio))
	if settings.BNBReserveEnabled() {
		shortfall, bnbErr := r.bnbShortfall(ctx, members, settings.BNBReserveAmount)
		if bnbErr != nil {
			return WeightConversion{}, bnbErr
		}
		equity = equity.Sub(shortfall)
	}
	if equity.Cmp(shared.Zero()) <= 0 {
		return WeightConversion{}, fmt.Errorf("target weight resolver: 扣除资金预留和 BNB 保留金额后可用权益不为正")
	}
	for _, weight := range orderedWeights {
		if weight.Hold {
			// 保持标的不取报价、不换算数量；执行器对它既不加仓也不减仓。
			quantities = append(quantities, store.InstrumentTarget{InstrumentID: weight.InstrumentID, Hold: true})
			continue
		}
		resolved := quotes[weight.InstrumentID]
		instrument, tradingAccountID, quote := resolved.instrument, resolved.tradingAccountID, resolved.quote
		targetWeight, _ := shared.ParseDecimal(weight.TargetWeight)
		quantity := equity.Mul(targetWeight).Div(quote.Price)
		if !exchange.RuleFor(instrument.MarketType).CanShort && quantity.IsNegative() {
			return WeightConversion{}, fmt.Errorf("%w: target weight resolver: Spot target cannot be negative for %s", ErrPermanent, weight.InstrumentID)
		}
		// Division of exact rationals often produces a repeating quotient (for
		// example 100000*0.1/79999.5). Persist a finite, exchange-independent
		// snapshot. Exchange-specific quantity steps are applied only when the
		// executor creates child orders; quantizing here would let the first
		// member's step silently erase a quantity that another member can fill.
		negative := quantity.IsNegative()
		quantity = floorToStep(quantity.Abs(), logicalTargetQuantityStep)
		if negative {
			quantity = quantity.Neg()
		}
		prices[weight.InstrumentID] = quote.Price.String()
		evidence = append(evidence, ReferencePriceEvidence{
			InstrumentID: weight.InstrumentID, TradingAccountID: tradingAccountID,
			ExchangeSymbol: instrument.ExchangeSymbol, Price: quote.Price.String(),
			UpdatedAt: quote.UpdatedAt.UTC().UnixMilli(),
		})
		quantities = append(quantities, store.InstrumentTarget{
			InstrumentID: weight.InstrumentID, Quantity: quantity.String(),
		})
	}
	return WeightConversion{RequestHash: requestHash, SignalTime: signalTime, Equity: equity, EquitySourceTime: equityTime, ReferencePrices: prices, ReferencePriceEvidence: evidence, WeightsJSON: weightsJSON, QuantityTargets: quantities}, nil
}

type resolvedQuote struct {
	instrument       store.InstrumentRecord
	tradingAccountID string
	quote            Quote
}

// skipExcludedTargets 把目标里的排除资产标记为保持并告警（同一个组合、同一个资产每天一次）。
// BNB 保留、稳定币保证金、保证金币只排除现货标的；套利底仓的现货和对应永续都排除。
// 基础资产取标的表里的 BaseAsset（1000PEPE 与 PEPE 是两个资产），查不到的标的不排除，后面按“没有可交易成员”报错。
func (r *WeightResolver) skipExcludedTargets(
	ctx context.Context,
	spaceID, logicalAccountID string,
	settings fundsettings.Settings,
	members []store.LogicalAccountMemberRecord,
	weights []orderedWeight,
) error {
	for i := range weights {
		if weights[i].Hold {
			continue
		}
		var instrument store.InstrumentRecord
		found := false
		for _, member := range members {
			record, err := r.Store.GetInstrumentByIDForAccount(ctx, spaceID, member.TradingAccountID, weights[i].InstrumentID)
			if err == nil {
				instrument, found = record, true
				break
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		if !found {
			continue
		}
		excluded := false
		switch exchange.MarketType(instrument.MarketType) {
		case exchange.MarketTypeSpot:
			excluded = settings.ExcludedFromManaged(instrument.BaseAsset)
		case exchange.MarketTypeSwap:
			excluded = settings.IsArbitrageBase(instrument.BaseAsset)
		}
		if !excluded {
			continue
		}
		weights[i].Hold = true
		r.warnExcluded(ctx, logicalAccountID, strings.ToUpper(instrument.BaseAsset), weights[i].InstrumentID)
	}
	return nil
}

func (r *WeightResolver) warnExcluded(ctx context.Context, logicalAccountID, asset, instrumentID string) bool {
	today := r.now().Format("2006-01-02")
	key := logicalAccountID + "|" + asset
	r.warnMu.Lock()
	if r.warned == nil {
		r.warned = make(map[string]string)
	}
	repeated := r.warned[key] == today
	r.warned[key] = today
	r.warnMu.Unlock()
	if repeated {
		return false
	}
	log.WarnContextf(ctx, "交易关注项：组合 %s 的目标里出现排除资产 %s（%s），已跳过这一项，其余照常执行", logicalAccountID, asset, instrumentID)
	return true
}

// bnbShortfall 返回 BNB 保留金额还差的部分：每个会补买的执行账户各算一个缺口再求和，
// 和资金维护任务的补买口径一致（M 个账户实际要买 M 份，不是只扣一份）：普通账户只数现货钱包的 BNB、目标是 N/2，
// 统一账户一个钱包、目标是 N（数量与目标直接用 fundmaint.BNBQuantity、BNBBounds，和补买、归集同一份算法）。
// 缺口不扣触发容差：多留一点只是保守。
// 只算资金维护生效的账户（币安生产环境实盘）且开通了现货、有 BNB 现货标的的：其他账户没有补买，也就不用为它留钱。
// 没有 BNB 余额时不取报价；取到的报价要新鲜。
func (r *WeightResolver) bnbShortfall(ctx context.Context, members []store.LogicalAccountMemberRecord, reserve shared.Decimal) (shared.Decimal, error) {
	total := shared.Zero()
	for _, member := range members {
		account, err := r.Store.GetTradingAccountByID(ctx, member.TradingAccountID)
		if err != nil {
			return shared.Zero(), err
		}
		// 口径和资金维护任务一致：它不处理停用、未就绪的账户，也不会在非交易状态的标的上补买，这些账户不用为补买留钱。
		if !account.FundMaintenanceApplies() || !account.HasBalanceMarket() ||
			account.Status != string(exchange.AccountStatusEnabled) || !account.Ready {
			continue
		}
		instruments, err := r.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
		if err != nil {
			return shared.Zero(), err
		}
		var bnb *store.InstrumentRecord
		for i := range instruments {
			if exchange.RuleFor(instruments[i].MarketType).PositionFromBalance &&
				strings.EqualFold(instruments[i].BaseAsset, fundsettings.BNBAsset) &&
				strings.EqualFold(instruments[i].QuoteAsset, account.SettlementAsset) &&
				exchange.StatusTradable(instruments[i].Status) {
				bnb = &instruments[i]
				break
			}
		}
		if bnb == nil {
			continue
		}
		quantity := fundmaint.BNBQuantity(account)
		value := shared.Zero()
		if quantity.Cmp(shared.Zero()) > 0 {
			quote, err := r.Prices.LatestPrice(ctx, account.TradingAccountID, exchange.NewSymbolKey(bnb.MarketType, bnb.ExchangeSymbol))
			if err != nil {
				return shared.Zero(), err
			}
			if quote.Price.Cmp(shared.Zero()) <= 0 || quote.UpdatedAt.IsZero() || !r.priceFresh(bnb.Exchange, quote.UpdatedAt) {
				return shared.Zero(), fmt.Errorf("target weight resolver: invalid reference price for BNB")
			}
			value = quantity.Mul(quote.Price)
		}
		target, _ := fundmaint.BNBBounds(account, reserve)
		if shortfall := target.Sub(value); shortfall.Cmp(shared.Zero()) > 0 {
			total = total.Add(shortfall)
		}
	}
	return total, nil
}

// unmanagedTargetValue 计算目标里尚未受管的现货资产市值。权益点只算受管资产，而目标一落库
// 这些资产就受管了，收敛按全部余额算持仓。不计入的话数量按偏小的权益换算，会多卖一大截。
// 只看现货标的：合约标的的 BTC 不是现货资产；同一基础资产只算一次。
func (r *WeightResolver) unmanagedTargetValue(
	ctx context.Context,
	spaceID string,
	members []store.LogicalAccountMemberRecord,
	quotes map[string]resolvedQuote,
) (shared.Decimal, error) {
	total := shared.Zero()
	for _, member := range members {
		account, err := r.Store.GetTradingAccountByID(ctx, member.TradingAccountID)
		if err != nil {
			return shared.Zero(), err
		}
		if !account.HasBalanceMarket() {
			continue
		}
		managed, err := r.Store.ManagedAssets(ctx, spaceID, account.TradingAccountID)
		if err != nil {
			return shared.Zero(), err
		}
		counted := make(map[string]struct{}, len(quotes))
		for _, resolved := range quotes {
			if !exchange.RuleFor(resolved.instrument.MarketType).PositionFromBalance {
				continue
			}
			if _, ok := managed[resolved.instrument.BaseAsset]; ok {
				continue
			}
			if _, done := counted[resolved.instrument.BaseAsset]; done {
				continue
			}
			counted[resolved.instrument.BaseAsset] = struct{}{}
			for _, balance := range account.SpotBalances() {
				if balance.Asset != resolved.instrument.BaseAsset {
					continue
				}
				quantity, err := shared.ParseDecimal(balance.Total)
				if err != nil {
					return shared.Zero(), err
				}
				total = total.Add(quantity.Mul(resolved.quote.Price))
			}
		}
	}
	return total, nil
}

func allHold(weights []orderedWeight) bool {
	for _, weight := range weights {
		if !weight.Hold {
			return false
		}
	}
	return len(weights) > 0
}

func validateWeightRisk(weights []orderedWeight) error {
	gross := shared.Zero()
	for _, weight := range weights {
		value, err := shared.ParseDecimal(weight.TargetWeight)
		if err != nil {
			return fmt.Errorf("target weight resolver: invalid weight for %s", weight.InstrumentID)
		}
		if value.Abs().Cmp(defaultMaxSingleWeight) > 0 {
			return fmt.Errorf("target weight resolver: single target weight exceeds %s for %s", defaultMaxSingleWeight.String(), weight.InstrumentID)
		}
		gross = gross.Add(value.Abs())
	}
	if gross.Cmp(defaultMaxGrossWeight) > 0 {
		return fmt.Errorf("target weight resolver: gross target weight exceeds %s", defaultMaxGrossWeight.String())
	}
	return nil
}

func (r *WeightResolver) instrumentForMembers(ctx context.Context, spaceID string, members []store.LogicalAccountMemberRecord, instrumentID string) (store.InstrumentRecord, string, error) {
	for _, member := range members {
		account, accountErr := r.Store.GetTradingAccountByID(ctx, member.TradingAccountID)
		if accountErr != nil {
			return store.InstrumentRecord{}, "", accountErr
		}
		if account.Status != string(exchange.AccountStatusEnabled) || !account.Ready {
			continue
		}
		instrument, err := r.Store.GetInstrumentByIDForAccount(ctx, spaceID, member.TradingAccountID, instrumentID)
		if err == nil {
			if !exchange.StatusTradable(instrument.Status) {
				continue
			}
			if instrument.SettlementAsset != "" && instrument.SettlementAsset != account.SettlementAsset {
				continue
			}
			return instrument, member.TradingAccountID, nil
		}
	}
	return store.InstrumentRecord{}, "", fmt.Errorf("target weight resolver: instrument %s is not supported by a ready account member", instrumentID)
}

func (r *WeightResolver) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// priceFresh 判断换算用的参考价是否新鲜：时段内 30 秒；A 股时段外（夜里、午休、周末）用收盘价，见 execution.QuoteFresh。
func (r *WeightResolver) priceFresh(exchangeName string, updatedAt time.Time) bool {
	maxAge := r.MaxPriceAge
	if maxAge <= 0 {
		maxAge = 30 * time.Second
	}
	return execution.QuoteFresh(r.Exchanges, exchangeName, updatedAt.UTC(), r.now(), maxAge)
}

type orderedWeight struct {
	InstrumentID string `json:"instrument_id"`
	TargetWeight string `json:"target_weight"`
	// Hold 为 true 的标的保持当前持仓数量不动，权重只是预留的预算占用，不换算数量。
	Hold bool `json:"hold,omitempty"`
}

func canonicalWeights(request *tradeeventpb.LogicalAccountTargetWeightRequested) (string, string, []orderedWeight, error) {
	weights := make([]orderedWeight, 0, len(request.GetTargets()))
	seen := make(map[string]struct{}, len(request.GetTargets()))
	for _, target := range request.GetTargets() {
		if target == nil || target.GetInstrumentId() == "" {
			return "", "", nil, fmt.Errorf("target weight resolver: invalid target")
		}
		if _, ok := seen[target.GetInstrumentId()]; ok {
			return "", "", nil, fmt.Errorf("target weight resolver: duplicate instrument %s", target.GetInstrumentId())
		}
		seen[target.GetInstrumentId()] = struct{}{}
		weight, err := shared.ParseDecimal(target.GetTargetWeight())
		if err != nil {
			return "", "", nil, fmt.Errorf("target weight resolver: invalid weight for %s", target.GetInstrumentId())
		}
		weights = append(weights, orderedWeight{InstrumentID: target.GetInstrumentId(), TargetWeight: weight.String(), Hold: target.GetHold()})
	}
	sort.Slice(weights, func(i, j int) bool { return weights[i].InstrumentID < weights[j].InstrumentID })
	payload := struct {
		TargetID         string          `json:"target_id"`
		InstanceID       string          `json:"instance_id"`
		LogicalAccountID string          `json:"logical_account_id"`
		SessionID        string          `json:"session_id"`
		StrategyID       string          `json:"strategy_id"`
		BarEndTime       string          `json:"bar_end_time"`
		EffectiveAt      string          `json:"effective_at"`
		ValidUntil       string          `json:"valid_until"`
		Targets          []orderedWeight `json:"targets"`
	}{TargetID: request.GetTargetId(), InstanceID: request.GetInstanceId(), LogicalAccountID: request.GetLogicalAccountId(), SessionID: request.GetSessionId(), StrategyID: request.GetStrategyId(), BarEndTime: request.GetBarEndTime().AsTime().UTC().Format(time.RFC3339Nano), EffectiveAt: request.GetEffectiveAt().AsTime().UTC().Format(time.RFC3339Nano), ValidUntil: request.GetValidUntil().AsTime().UTC().Format(time.RFC3339Nano), Targets: weights}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", "", nil, err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), string(mustJSON(weights)), weights, nil
}

// RequestHash returns the canonical logical request hash used by TargetReceipt.
func RequestHash(request *tradeeventpb.LogicalAccountTargetWeightRequested) (string, error) {
	hash, _, _, err := canonicalWeights(request)
	return hash, err
}

func mustJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}
