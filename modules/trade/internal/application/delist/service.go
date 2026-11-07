// Package delist 是交易计划 4.7 里供事件中心调用的下架名单与持有者查询。
// 名单是全局的、不分空间，一律用（交易所、市场、交易所代码）定位标的，由 Trade 在使用时按标的表换算内部 ID。
package delist

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/exposure"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

var (
	ErrServiceConfig  = errors.New("trade delist: service is not configured")
	ErrInvalidRequest = errors.New("trade delist: invalid request")
	// ErrNoMemberOfExchange 表示组合里没有属于这个交易所的成员：没有可清仓的对象。
	ErrNoMemberOfExchange = errors.New("组合里没有属于该交易所的成员")
)

// PriceSource 取标的的参考价，持有者查询用它按最小金额判断零头；为空或取不到时只按最小数量判断（并标注）。
type PriceSource interface {
	LatestPrice(ctx context.Context, tradingAccountID string, key exchange.SymbolKey) (targetapp.Quote, error)
}

type Service struct {
	Store  *store.Store
	Prices PriceSource
	// OnChanged 在名单变化后调用：已收敛、已阻塞的目标不会被定时器扫描，需要唤醒它们重新判断。
	OnChanged func()
	Now       func() time.Time
}

func (s *Service) ready() error {
	if s == nil || s.Store == nil {
		return ErrServiceConfig
	}
	return nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) changed() {
	if s.OnChanged != nil {
		s.OnChanged()
	}
}

// Key 是（交易所、市场、交易所代码）：交易所、市场、交易所代码一律转大写（BINANCE、OKX；SPOT、SWAP；FOOUSDT、FOO-USDT-SWAP），
// 调用方传小写也能对上，不会被判成“Trade 无此标的”。
type Key struct {
	Exchange       string
	MarketType     string
	ExchangeSymbol string
}

func normalize(key Key) (Key, error) {
	key.Exchange = strings.ToUpper(strings.TrimSpace(key.Exchange))
	key.MarketType = strings.ToUpper(strings.TrimSpace(key.MarketType))
	key.ExchangeSymbol = strings.ToUpper(strings.TrimSpace(key.ExchangeSymbol))
	if key.Exchange == "" || key.ExchangeSymbol == "" ||
		(key.MarketType != string(exchange.MarketTypeSpot) && key.MarketType != string(exchange.MarketTypeSwap)) {
		return Key{}, fmt.Errorf("%w: 需要交易所、市场（SPOT 或 SWAP）和交易所代码", ErrInvalidRequest)
	}
	return key, nil
}

// lookup 在标的表里按（交易所、市场、交易所代码）找标的，找不到说明 Trade 没有这个标的。
func (s *Service) lookup(ctx context.Context, key Key) (store.InstrumentRecord, bool, error) {
	record, err := s.Store.GetInstrument(ctx, key.Exchange, key.MarketType, key.ExchangeSymbol)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.InstrumentRecord{}, false, nil
	}
	if err != nil {
		return store.InstrumentRecord{}, false, err
	}
	return record, true, nil
}

// BlockResult 是写入名单的结果。
type BlockResult struct {
	// Created 为假表示已经在名单里，没有改动。
	Created bool
	// InstrumentUnknown 为真表示 Trade 无此标的（用户没开通对应市场的账户）：照常写入名单，不算失败。
	InstrumentUnknown bool
}

// Block 把标的写进下架名单。能从标的表查到的记下内部标的 ID 和基础资产（只用于展示和审计），查不到的留空。
func (s *Service) Block(ctx context.Context, key Key, reason, eventID string) (BlockResult, error) {
	if err := s.ready(); err != nil {
		return BlockResult{}, err
	}
	key, err := normalize(key)
	if err != nil {
		return BlockResult{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return BlockResult{}, fmt.Errorf("%w: reason 必填", ErrInvalidRequest)
	}
	instrument, known, err := s.lookup(ctx, key)
	if err != nil {
		return BlockResult{}, err
	}
	created, err := s.Store.BlockInstrument(ctx, store.BlockedInstrumentRecord{
		Exchange: key.Exchange, MarketType: key.MarketType, ExchangeSymbol: key.ExchangeSymbol,
		InstrumentID: instrument.InstrumentID, BaseAsset: instrument.BaseAsset,
		Reason: reason, SourceEventID: strings.TrimSpace(eventID), BlockedAt: s.now().UnixMilli(),
	})
	if err != nil {
		return BlockResult{}, err
	}
	if created {
		s.changed()
		if known {
			s.warnSpecialHolders(ctx, key, instrument)
		}
	}
	return BlockResult{Created: created, InstrumentUnknown: !known}, nil
}

// warnSpecialHolders 在写入名单时检查：这个标的是不是某个组合的套利底仓对冲腿，或排除资产（BNB 保留、稳定币保证金、保证金币、底仓）。
// 这些资产清仓不会自动处理（也不算策略持有），下架时没有任何动作会碰它们，所以要提醒人工处理。固定文字，不带变化的数字。
func (s *Service) warnSpecialHolders(ctx context.Context, key Key, instrument store.InstrumentRecord) {
	accounts, err := s.Store.ListAllTradingAccounts(ctx)
	if err != nil {
		return
	}
	groups := map[string]struct{}{}
	for _, account := range accounts {
		if account.Exchange != key.Exchange || !account.SupportsMarket(key.MarketType) {
			continue
		}
		logical, _, err := s.Store.FindLogicalAccountMembership(ctx, account.SpaceID, account.TradingAccountID)
		if err != nil {
			continue
		}
		settings, err := s.Store.FundSettingsForAccount(ctx, account.SpaceID, account.TradingAccountID)
		if err != nil {
			continue
		}
		special := false
		switch key.MarketType {
		case string(exchange.MarketTypeSwap):
			special = settings.IsArbitrageBase(instrument.BaseAsset)
		case string(exchange.MarketTypeSpot):
			special = settings.ExcludedFromManaged(instrument.BaseAsset)
		}
		if special {
			groups[logical.Name] = struct{}{}
		}
	}
	for name := range groups {
		log.Warnf("交易关注项：下架名单里的标的 %s %s %s 是组合 %s 的套利底仓对冲腿或排除资产，下架时不会被自动清仓，需要人工处理",
			key.Exchange, key.MarketType, key.ExchangeSymbol, name)
	}
}

// Unblock 解除禁止；不在名单里不算错误。
func (s *Service) Unblock(ctx context.Context, key Key) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	key, err := normalize(key)
	if err != nil {
		return false, err
	}
	removed, err := s.Store.UnblockInstrument(ctx, key.Exchange, key.MarketType, key.ExchangeSymbol)
	if err != nil {
		return false, err
	}
	if removed {
		s.changed()
	}
	return removed, nil
}

// List 返回整张名单。
func (s *Service) List(ctx context.Context) ([]store.BlockedInstrumentRecord, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.Store.ListBlockedInstruments(ctx)
}

// Holder 是策略持有某个标的的一个执行账户（每个执行账户一行，调用方按（空间、组合）聚合）。
type Holder struct {
	SpaceID          string
	LogicalAccountID string
	TradingAccountID string
	// Quantity 是持有数量，保留原精度：现货是余额，永续是带符号的持仓。
	Quantity string
	// Dust 为真表示零头：低于交易所最小数量，下不了单，清仓也不会处理它。
	Dust bool
	// Paper 为真表示模拟盘账户。
	Paper bool
	// Status 是执行账户状态（ENABLED、DISABLED、ERROR）；停用和出错的账户照常列出。
	Status string
	// Note 是对这一项的标注（例如零头只按最小数量判断）。
	Note string
}

// SkippedAccount 是查询时出错、没能判断的账户。
type SkippedAccount struct {
	TradingAccountID string
	Reason           string
}

// HoldersResult 是持有者查询的结果：Holders 为空且 InstrumentUnknown 为假，表示确认没有策略持有。
type HoldersResult struct {
	Holders           []Holder
	InstrumentUnknown bool
	// SkippedAccounts 是出错、没能判断的账户：不能当作没有持有。
	SkippedAccounts []SkippedAccount
	// Notes 是明确不算持有的说明（例如标的计价币不是账户的结算币）。
	Notes []string
}

// Holders 跨空间列出策略持有该标的的执行账户。只算策略持有：
//   - 现货必须是受管资产（MooX 买过或在目标里，排除资产除外）且余额大于 0；永续持仓不为 0（套利底仓的对冲腿不算）；
//   - 只看该交易所、在组合里的执行账户（停用、出错的照常列出并带状态）；测试网不算；标的计价币不是账户结算币的不算（清仓不会卖它），写进说明；
//   - paperOnly 只返回模拟盘。
func (s *Service) Holders(ctx context.Context, key Key, paperOnly bool) (HoldersResult, error) {
	if err := s.ready(); err != nil {
		return HoldersResult{}, err
	}
	key, err := normalize(key)
	if err != nil {
		return HoldersResult{}, err
	}
	if _, known, lookupErr := s.lookup(ctx, key); lookupErr != nil {
		return HoldersResult{}, lookupErr
	} else if !known {
		return HoldersResult{Holders: []Holder{}, InstrumentUnknown: true}, nil
	}
	accounts, err := s.Store.ListAllTradingAccounts(ctx)
	if err != nil {
		return HoldersResult{}, err
	}
	result := HoldersResult{Holders: []Holder{}}
	for _, account := range accounts {
		paper := account.ExecutionMode == string(exchange.ExecutionModePaper)
		if account.Exchange != key.Exchange ||
			account.Environment == string(exchange.AccountEnvironmentTestnet) || (paperOnly && !paper) ||
			!account.SupportsMarket(key.MarketType) {
			continue
		}
		logical, _, membershipErr := s.Store.FindLogicalAccountMembership(ctx, account.SpaceID, account.TradingAccountID)
		if errors.Is(membershipErr, gorm.ErrRecordNotFound) {
			continue
		}
		// 一个账户出错不能让整个跨空间查询失败（所有空间的下架处理都会卡住）：跳过它，在响应里标出，调用方不能把它当作没有持有。
		if membershipErr != nil {
			result.SkippedAccounts = append(result.SkippedAccounts, SkippedAccount{account.TradingAccountID, membershipErr.Error()})
			continue
		}
		holder, found, note, holdErr := s.holderOf(ctx, account, logical, key)
		if holdErr != nil {
			result.SkippedAccounts = append(result.SkippedAccounts, SkippedAccount{account.TradingAccountID, holdErr.Error()})
			continue
		}
		if note != "" && !found {
			result.Notes = append(result.Notes, account.Name+"："+note)
		}
		if found {
			holder.Paper, holder.Status = paper, account.Status
			result.Holders = append(result.Holders, holder)
		}
	}
	return result, nil
}

// holderOf 判断一个账户是否持有这个标的。found 为假且 note 非空表示明确不算持有（带说明）。
func (s *Service) holderOf(ctx context.Context, account store.TradingAccountRecord, logical store.LogicalAccountRecord, key Key) (holder Holder, found bool, note string, err error) {
	instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
	if err != nil {
		return Holder{}, false, "", err
	}
	var instrument *store.InstrumentRecord
	for i := range instruments {
		if instruments[i].MarketType == key.MarketType && instruments[i].ExchangeSymbol == key.ExchangeSymbol {
			instrument = &instruments[i]
			break
		}
	}
	if instrument == nil {
		return Holder{}, false, "", nil
	}
	// 标的的计价币不是账户的结算币（例如屏蔽 FOOBTC，账户按 USDT 结算）：清仓解析不到它、什么也不卖，不能把同名基础币的余额算成持有。
	if quote := instrument.QuoteAsset; exchange.RuleFor(key.MarketType).PositionFromBalance && quote != account.SettlementAsset ||
		!exchange.RuleFor(key.MarketType).PositionFromBalance && instrument.SettlementAsset != "" && instrument.SettlementAsset != account.SettlementAsset {
		return Holder{}, false, fmt.Sprintf("标的 %s 的计价币不是结算币 %s，清仓不会卖它，不算持有", key.ExchangeSymbol, account.SettlementAsset), nil
	}
	holder = Holder{SpaceID: account.SpaceID, LogicalAccountID: logical.LogicalAccountID, TradingAccountID: account.TradingAccountID}
	if exchange.RuleFor(key.MarketType).PositionFromBalance {
		managed, err := s.Store.ManagedAssets(ctx, account.SpaceID, account.TradingAccountID)
		if err != nil {
			return Holder{}, false, "", err
		}
		if _, ok := managed[instrument.BaseAsset]; !ok || instrument.BaseAsset == account.SettlementAsset {
			return Holder{}, false, "", nil
		}
		total := shared.Zero()
		for _, balance := range account.SpotBalances() {
			if balance.Asset != instrument.BaseAsset {
				continue
			}
			quantity, err := shared.ParseDecimal(balance.Total)
			if err != nil {
				return Holder{}, false, "", err
			}
			total = total.Add(quantity)
		}
		if total.Cmp(shared.Zero()) <= 0 {
			return Holder{}, false, "", nil
		}
		// 零头和清仓同一个口径：有报价时按最小数量和最小金额判断（价格暴跌的下架币常常高于最小数量、低于最小金额）；
		// 取不到报价时只能按最小数量判断，在标注里说明。
		price := shared.Zero()
		if s.Prices != nil {
			if quote, quoteErr := s.Prices.LatestPrice(ctx, account.TradingAccountID, exchange.NewSymbolKey(key.MarketType, key.ExchangeSymbol)); quoteErr == nil {
				price = quote.Price
			}
		}
		if price.Cmp(shared.Zero()) <= 0 {
			holder.Note = "取不到报价，零头只按最小数量判断"
		}
		holder.Quantity, holder.Dust = total.String(), exposure.SpotDust(total, price, *instrument)
		return holder, true, "", nil
	}
	settings, err := s.Store.FundSettingsForAccount(ctx, account.SpaceID, account.TradingAccountID)
	if err != nil {
		return Holder{}, false, "", err
	}
	// 套利底仓的永续对冲腿不是策略持有，清仓也不会平它。
	if settings.IsArbitrageBase(instrument.BaseAsset) {
		return Holder{}, false, "", nil
	}
	positions, err := s.Store.ListPositions(ctx, account.SpaceID, account.TradingAccountID, exchange.NewSymbolKey(key.MarketType, key.ExchangeSymbol))
	if err != nil {
		return Holder{}, false, "", err
	}
	total := shared.Zero()
	for _, position := range positions {
		quantity, err := shared.ParseDecimal(position.SignedQuantity)
		if err != nil {
			return Holder{}, false, "", err
		}
		total = total.Add(quantity)
	}
	if total.IsZero() {
		return Holder{}, false, "", nil
	}
	// 永续平仓单是只减仓单，清仓不查最小金额，零头只看最小数量。
	holder.Quantity, holder.Dust = total.String(), swapDust(total.Abs(), *instrument)
	return holder, true, "", nil
}

// swapDust 判断永续持仓是不是下不了单的零头：按步长取整后低于最小数量（合约按面值折成基础币数量）。
func swapDust(quantity shared.Decimal, instrument store.InstrumentRecord) bool {
	step, err := shared.ParseDecimal(instrument.ExchangeQuantityStep)
	if err != nil || step.Cmp(shared.Zero()) <= 0 {
		return false
	}
	minimum := shared.Zero()
	if raw := strings.TrimSpace(instrument.MinExchangeQuantity); raw != "" {
		if minimum, err = shared.ParseDecimal(raw); err != nil {
			return false
		}
	}
	if exchange.RuleFor(instrument.MarketType).ContractValue {
		contractValue, err := shared.ParseDecimal(instrument.ContractValue)
		if err != nil || contractValue.Cmp(shared.Zero()) <= 0 {
			return false
		}
		step, minimum = step.Mul(contractValue), minimum.Mul(contractValue)
	}
	floored := exposure.FloorToStep(quantity, step)
	return floored.IsZero() || floored.Cmp(minimum) < 0
}

// ResolveInstrumentID 把（交易所、市场、交易所代码）换算成组合里这个交易所成员的内部标的 ID。
// 组合里没有该交易所的成员，或所有这个交易所的成员标的表里都没有它，返回错误，不能静默当作平完了。
func (s *Service) ResolveInstrumentID(ctx context.Context, spaceID, logicalAccountID string, key Key) (string, error) {
	if err := s.ready(); err != nil {
		return "", err
	}
	key, err := normalize(key)
	if err != nil {
		return "", err
	}
	members, err := s.Store.ListLogicalAccountMembers(ctx, spaceID, logicalAccountID, true)
	if err != nil {
		return "", err
	}
	sawExchange := false
	for _, member := range members {
		account, err := s.Store.GetTradingAccountByID(ctx, member.TradingAccountID)
		if err != nil {
			return "", err
		}
		if account.Exchange != key.Exchange {
			continue
		}
		sawExchange = true
		instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
		if err != nil {
			return "", err
		}
		for _, instrument := range instruments {
			if instrument.MarketType == key.MarketType && instrument.ExchangeSymbol == key.ExchangeSymbol {
				return instrument.InstrumentID, nil
			}
		}
	}
	if !sawExchange {
		return "", ErrNoMemberOfExchange
	}
	return "", fmt.Errorf("%w: 标的 %s %s %s 在该交易所成员的标的表里解析不到", ErrInvalidRequest, key.Exchange, key.MarketType, key.ExchangeSymbol)
}
