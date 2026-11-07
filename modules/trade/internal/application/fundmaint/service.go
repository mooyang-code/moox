// Package fundmaint 是组合账户资金设置里的后台维护任务：BNB 抵扣开关、BNB 保留补买、小额资产兑 BNB、多稳定币调平。
// 它们由一个定时任务统一调度（每小时一次，设置变更时马上一次）；补买和调平下的单归属是 SYSTEM，
// 不暂停组合，也不算执行器的子单；成交不会让资产变成受管资产。
package fundmaint

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/rs/xid"
	"trpc.group/trpc-go/trpc-go/log"
)

// AdapterSource 按执行账户取适配器。
type AdapterSource interface {
	Adapter(tradingAccountID string) (execution.ExecutionAdapter, error)
}

// OrderService 是下系统单要用的订单服务。
type OrderService interface {
	Place(ctx context.Context, spaceID string, spec orderdomain.OrderSpec) (orderdomain.Order, error)
	Submit(ctx context.Context, spaceID, orderID string) (orderdomain.Order, error)
	Cancel(ctx context.Context, spaceID, orderID string) (orderdomain.Order, error)
}

// 系统单的归属 ID：同一类任务的订单用同一个，下一次检查时据此找到上次没成交的单。
const (
	ownerBNBReserve      = "bnb-reserve"
	ownerStableRebalance = "stable-rebalance"
)

// 约定的数值：BNB 市值低于“保留金额 − fundsettings.BNBReserveSlack”才补买；稳定币偏离目标超过 stableDeviation（占稳定币总价值的比例，
// 取绝对差）才调平；零头资产价值低于 dustValueLimit（结算币计）才兑换。
var (
	stableDeviation = shared.MustDecimal("0.05")
	dustValueLimit  = shared.MustDecimal("5")
)

// lockWait 是等组合账户锁的最长时间：收敛可能正占着它，等不到就留给下一轮，不挡别的组合。
const lockWait = 5 * time.Second

type Service struct {
	Store    *store.Store
	Adapters AdapterSource
	Orders   OrderService
	Now      func() time.Time

	mu sync.Mutex
	// discountApplied 记录已确认开启 BNB 抵扣的执行账户；设置变更时清掉，重新确认。
	discountApplied map[string]bool
	warned          map[string]struct{}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Invalidate 在资金设置变更后清掉组合成员的“已开启抵扣”记录，下一次检查重新确认交易所上的开关。
func (s *Service) Invalidate(ctx context.Context, spaceID, logicalAccountID string) {
	members, err := s.Store.ListLogicalAccountMembers(ctx, spaceID, logicalAccountID, true)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, member := range members {
		delete(s.discountApplied, member.TradingAccountID)
	}
}

// RunAll 检查所有空间的所有组合账户；单个组合失败不影响其他组合。
func (s *Service) RunAll(ctx context.Context) error {
	accounts, err := s.Store.ListAllTradingAccounts(ctx)
	if err != nil {
		return err
	}
	spaces := make(map[string]struct{})
	for _, account := range accounts {
		spaces[account.SpaceID] = struct{}{}
	}
	ordered := make([]string, 0, len(spaces))
	for spaceID := range spaces {
		ordered = append(ordered, spaceID)
	}
	sort.Strings(ordered)
	var runErrors []error
	for _, spaceID := range ordered {
		logicals, err := s.Store.ListLogicalAccounts(ctx, spaceID)
		if err != nil {
			runErrors = append(runErrors, err)
			continue
		}
		for _, logical := range logicals {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := s.RunLogicalAccount(ctx, logical); err != nil {
				runErrors = append(runErrors, fmt.Errorf("组合 %s：%w", logical.LogicalAccountID, err))
			}
		}
	}
	return errors.Join(runErrors...)
}

// RunLogicalAccount 对一个组合账户做一遍资金维护。设置里没有开任何项时什么都不做。
func (s *Service) RunLogicalAccount(ctx context.Context, logical store.LogicalAccountRecord) error {
	settings, err := s.Store.ResolveFundSettings(logical)
	if err != nil {
		// 排除字段读不出来：执行器同样不下单，这里也不动。
		s.warnOnce("settings|"+logical.LogicalAccountID, fmt.Sprintf("组合 %s 的资金设置无法解析，资金维护任务跳过：%v", logical.LogicalAccountID, err))
		return nil
	}
	if !settings.BNBDiscount && !settings.BNBReserveEnabled() && !settings.DustConvert && len(settings.StablecoinTargets) < 2 {
		return nil
	}
	lockCtx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	unlock, err := s.Store.LockLogicalAccountContext(lockCtx, logical.SpaceID, logical.LogicalAccountID)
	if err != nil {
		return nil
	}
	defer unlock()
	// 锁里重新读：等锁期间设置、自动执行状态可能变了。
	logical, err = s.Store.GetLogicalAccount(ctx, logical.SpaceID, logical.LogicalAccountID)
	if err != nil {
		return err
	}
	if settings, err = s.Store.ResolveFundSettings(logical); err != nil {
		return nil
	}
	members, err := s.Store.ListLogicalAccountMembers(ctx, logical.SpaceID, logical.LogicalAccountID, false)
	if err != nil {
		return err
	}
	var runErrors []error
	// 没有目标时（组合还没收到策略目标）按没有目标处理。
	tradingAllowed, err := s.tradingAllowed(ctx, logical)
	if err != nil {
		return err
	}
	for _, member := range members {
		account, err := s.Store.GetTradingAccountByID(ctx, member.TradingAccountID)
		if err != nil {
			runErrors = append(runErrors, err)
			continue
		}
		// 只对币安生产环境的实盘账户做：OKX 没有 BNB，测试网的币没有真实价值。
		if !account.FundMaintenanceApplies() || account.Status != string(exchange.AccountStatusEnabled) || !account.Ready {
			continue
		}
		adapter, err := s.Adapters.Adapter(account.TradingAccountID)
		if err != nil {
			runErrors = append(runErrors, err)
			continue
		}
		if settings.BNBDiscount {
			if err := s.ensureBNBDiscount(ctx, account, adapter); err != nil {
				runErrors = append(runErrors, fmt.Errorf("账户 %s 开启 BNB 抵扣：%w", account.TradingAccountID, err))
			}
		}
		// 补买、兑换、调平会下单或动资产：组合暂停、正在清仓或减仓时不做。
		if !tradingAllowed {
			continue
		}
		if settings.BNBReserveEnabled() {
			if err := s.topUpBNB(ctx, logical, settings, account, adapter); err != nil {
				runErrors = append(runErrors, fmt.Errorf("账户 %s 补买 BNB：%w", account.TradingAccountID, err))
			}
		}
		if settings.DustConvert {
			if err := s.convertDust(ctx, logical, settings, account, adapter); err != nil {
				runErrors = append(runErrors, fmt.Errorf("账户 %s 小额兑换 BNB：%w", account.TradingAccountID, err))
			}
		}
		if len(settings.StablecoinTargets) > 1 {
			if err := s.rebalanceStablecoins(ctx, logical, settings, account, adapter); err != nil {
				runErrors = append(runErrors, fmt.Errorf("账户 %s 稳定币调平：%w", account.TradingAccountID, err))
			}
		}
	}
	return errors.Join(runErrors...)
}

// tradingAllowed 判断能不能做会下单的维护：自动执行没暂停，且没有运行中的清仓、减仓、下架清仓。
func (s *Service) tradingAllowed(ctx context.Context, logical store.LogicalAccountRecord) (bool, error) {
	if logical.AutomationState != "ACTIVE" {
		return false, nil
	}
	running, err := s.Store.ListRunningOperatorActions(ctx, logical.SpaceID, logical.LogicalAccountID)
	if err != nil {
		return false, err
	}
	for _, action := range running {
		switch action.ActionType {
		case "FLATTEN", "REDUCE", "DELIST_FLATTEN":
			return false, nil
		}
	}
	return true, nil
}

func (s *Service) warnOnce(key, message string) {
	s.mu.Lock()
	if s.warned == nil {
		s.warned = make(map[string]struct{})
	}
	_, repeated := s.warned[key]
	s.warned[key] = struct{}{}
	s.mu.Unlock()
	if !repeated {
		log.Warnf("交易关注项：%s", strings.TrimSpace(message))
	}
}

// ensureBNBDiscount 只会把 BNB 抵扣打开，不会关：用户在交易所里自己开的开关，这里不动。
func (s *Service) ensureBNBDiscount(ctx context.Context, account store.TradingAccountRecord, adapter execution.ExecutionAdapter) error {
	configurer, ok := adapter.(execution.FeeDiscountConfigurer)
	if !ok {
		return nil
	}
	s.mu.Lock()
	applied := s.discountApplied[account.TradingAccountID]
	s.mu.Unlock()
	if applied {
		return nil
	}
	enabled, err := configurer.GetBNBFeeDiscount(ctx)
	if err != nil {
		return err
	}
	if !enabled {
		if err := configurer.SetBNBFeeDiscount(ctx, true); err != nil {
			return err
		}
		log.Infof("执行账户 %s 已开启 BNB 抵扣手续费", account.TradingAccountID)
	}
	s.mu.Lock()
	if s.discountApplied == nil {
		s.discountApplied = make(map[string]bool)
	}
	s.discountApplied[account.TradingAccountID] = true
	s.mu.Unlock()
	return nil
}

// spotInstrument 找账户上 base/结算币的现货标的。
func (s *Service) spotInstrument(ctx context.Context, account store.TradingAccountRecord, base string) (store.InstrumentRecord, bool, error) {
	instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
	if err != nil {
		return store.InstrumentRecord{}, false, err
	}
	for _, instrument := range instruments {
		if exchange.RuleFor(instrument.MarketType).PositionFromBalance &&
			strings.EqualFold(instrument.BaseAsset, base) &&
			strings.EqualFold(instrument.QuoteAsset, account.SettlementAsset) &&
			(instrument.Status == "TRADING" || instrument.Status == "live") {
			return instrument, true, nil
		}
	}
	return store.InstrumentRecord{}, false, nil
}

// referencePrice 取标的的参考价；要求报价不超过 maxQuoteAge。
func (s *Service) referencePrice(ctx context.Context, adapter execution.ExecutionAdapter, instrument store.InstrumentRecord) (shared.Decimal, time.Time, error) {
	quote, err := adapter.GetReferencePrice(ctx, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol))
	if err != nil {
		return shared.Zero(), time.Time{}, err
	}
	if quote.Price.Cmp(shared.Zero()) <= 0 || quote.UpdatedAt.IsZero() || s.now().Sub(quote.UpdatedAt) > maxQuoteAge {
		return shared.Zero(), time.Time{}, fmt.Errorf("%s 的报价不可用或已过期", instrument.InstrumentID)
	}
	return quote.Price, quote.UpdatedAt, nil
}

// maxQuoteAge 是系统单参考价的最长时延：和下单校验的 10 秒一致。
const maxQuoteAge = 10 * time.Second

// quantityRules 返回标的的数量步长、最小数量和最小名义金额（现货标的，面值为 1）。
func quantityRules(instrument store.InstrumentRecord) (step, minimum, minNotional shared.Decimal, err error) {
	if step, err = shared.ParseDecimal(instrument.ExchangeQuantityStep); err != nil || step.Cmp(shared.Zero()) <= 0 {
		return shared.Zero(), shared.Zero(), shared.Zero(), fmt.Errorf("%s 的数量步长不合法", instrument.InstrumentID)
	}
	if minimum, err = shared.ParseDecimal(instrument.MinExchangeQuantity); err != nil {
		return shared.Zero(), shared.Zero(), shared.Zero(), fmt.Errorf("%s 的最小数量不合法", instrument.InstrumentID)
	}
	minNotional = shared.Zero()
	if strings.TrimSpace(instrument.MinNotional) != "" {
		if minNotional, err = shared.ParseDecimal(instrument.MinNotional); err != nil {
			return shared.Zero(), shared.Zero(), shared.Zero(), fmt.Errorf("%s 的最小名义金额不合法", instrument.InstrumentID)
		}
	}
	return step, minimum, minNotional, nil
}

// placeSystemOrder 下一笔系统单并提交。
func (s *Service) placeSystemOrder(
	ctx context.Context,
	logical store.LogicalAccountRecord,
	account store.TradingAccountRecord,
	instrument store.InstrumentRecord,
	ownerID string,
	side exchange.Side,
	quantity shared.Decimal,
	reference shared.Decimal,
	referenceAt time.Time,
	limit *shared.Decimal,
	policy exchange.FillPolicy,
) error {
	spec := orderdomain.OrderSpec{
		ClientOrderSpec: orderdomain.ClientOrderSpec{
			TradingAccountID: account.TradingAccountID,
			ClientOrderID:    "sys" + xid.New().String(),
			InstrumentID:     instrument.InstrumentID,
			Type:             exchange.OrderTypeMarket,
			Side:             side,
			Quantity:         quantity,
		},
		ReferencePrice: reference, ReferencePriceAt: referenceAt,
		Owner: orderdomain.OrderOwner{Type: orderdomain.OwnerSystem, OwnerID: ownerID, LogicalAccountID: logical.LogicalAccountID},
	}
	if limit != nil {
		spec.Type, spec.FillPolicy, spec.LimitPrice = exchange.OrderTypeLimit, policy, limit
	}
	placed, err := s.Orders.Place(ctx, logical.SpaceID, spec)
	if err != nil {
		return err
	}
	_, err = s.Orders.Submit(ctx, logical.SpaceID, string(placed.ID))
	return err
}

// openSystemOrders 返回账户上这类系统单里还没结束的订单。
func (s *Service) openSystemOrders(ctx context.Context, account store.TradingAccountRecord, ownerID string) ([]store.OrderRecord, error) {
	active, err := s.Store.ListActiveOrdersForAccount(ctx, account.SpaceID, account.TradingAccountID)
	if err != nil {
		return nil, err
	}
	var open []store.OrderRecord
	for _, record := range active {
		if record.OwnerType == string(orderdomain.OwnerSystem) && record.OwnerID == ownerID {
			open = append(open, record)
		}
	}
	return open, nil
}
