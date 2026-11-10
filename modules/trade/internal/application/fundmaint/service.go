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
	DiscardPending(ctx context.Context, spaceID, orderID string) (orderdomain.Order, error)
	ResolveUnknown(ctx context.Context, spaceID, orderID string) (orderdomain.Order, error)
	RecoverCancel(ctx context.Context, spaceID, orderID string) (orderdomain.Order, error)
}

// 系统单的归属 ID：同一类任务的订单用同一个，下一次检查时据此找到上次没成交的单。
const (
	ownerBNBReserve      = "bnb-reserve"
	ownerStableRebalance = "stable-rebalance"
)

// 约定的数值：BNB 市值低于“目标 − 容差”才补买（统一账户目标是保留金额、容差 fundsettings.BNBReserveSlack，普通账户只补现货钱包、两者都减半，见 BNBBounds）；稳定币偏离目标超过 stableDeviation（占稳定币总价值的比例，
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
	// dustRetryAt 是小额兑换失败后的最早重试时间（执行账户 → 时间）：失败后至少等 dustRetryBackoff，
	// 不然每小时、每次改设置、每个账户首次就绪都会再调一次交易所。进程重启后清零。
	dustRetryAt map[string]time.Time
}

// dustRetryBackoff 是小额兑换失败后的退避时间。
const dustRetryBackoff = 6 * time.Hour

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
	var runErrors []error
	// BNB 抵扣开关只查询和设置交易所上的开关，不碰订单和资产，放在组合锁外：交易所慢的时候不挡收敛。
	if settings.BNBDiscount {
		runErrors = append(runErrors, s.runDiscount(ctx, logical)...)
	}
	if !settings.BNBReserveEnabled() && !settings.DustConvert && len(settings.StablecoinTargets) < 2 {
		return errors.Join(runErrors...)
	}
	lockCtx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	unlock, err := s.Store.LockLogicalAccountContext(lockCtx, logical.SpaceID, logical.LogicalAccountID)
	if err != nil {
		return errors.Join(runErrors...)
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
		// 补买、兑换、调平会下单或动资产：组合暂停、正在清仓或减仓时不做。
		if !tradingAllowed {
			continue
		}
		bnbBusy := false
		if settings.BNBReserveEnabled() {
			busy, err := s.topUpBNB(ctx, logical, settings, account, adapter)
			bnbBusy = busy
			if err != nil {
				runErrors = append(runErrors, fmt.Errorf("账户 %s 补买 BNB：%w", account.TradingAccountID, err))
			}
		}
		if settings.DustConvert {
			if err := s.convertDust(ctx, logical, settings, account, adapter); err != nil {
				runErrors = append(runErrors, fmt.Errorf("账户 %s 小额兑换 BNB：%w", account.TradingAccountID, err))
			}
		}
		if len(settings.StablecoinTargets) > 1 && stableApplies(account) {
			// 上一轮的买单先处理（过期的撤掉）；这和 BNB 补买无关，补买让路时旧买单照样要撤。
			hasOpen, err := s.settleStableOrders(ctx, account)
			if err != nil {
				runErrors = append(runErrors, fmt.Errorf("账户 %s 稳定币调平：%w", account.TradingAccountID, err))
			}
			// BNB 补买单刚下出（或还没结束）时调平先让路：补买要花 USDT，余额要等同步才更新。
			if !hasOpen && !bnbBusy && err == nil {
				// 过期的买单撤掉的同一轮就重算重挂（定时器每小时才一次，账户就绪唤醒每个账户每个进程只一次，不能等下一轮）：
				// 撤单前可能有部分成交，重读账户；成交还没进快照时账户未就绪，等下一轮。
				fresh, err := s.Store.GetTradingAccountByID(ctx, account.TradingAccountID)
				if err == nil && fresh.Ready {
					err = s.rebalanceStablecoins(ctx, logical, settings, fresh, adapter)
				}
				if err != nil {
					runErrors = append(runErrors, fmt.Errorf("账户 %s 稳定币调平：%w", account.TradingAccountID, err))
				}
			}
		}
	}
	return errors.Join(runErrors...)
}

// runDiscount 确认组合里每个币安生产环境成员都打开了 BNB 抵扣（只开不关）。
func (s *Service) runDiscount(ctx context.Context, logical store.LogicalAccountRecord) []error {
	members, err := s.Store.ListLogicalAccountMembers(ctx, logical.SpaceID, logical.LogicalAccountID, false)
	if err != nil {
		return []error{err}
	}
	var errs []error
	for _, member := range members {
		account, err := s.Store.GetTradingAccountByID(ctx, member.TradingAccountID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !account.FundMaintenanceApplies() || account.Status != string(exchange.AccountStatusEnabled) || !account.Ready {
			continue
		}
		adapter, err := s.Adapters.Adapter(account.TradingAccountID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := s.ensureBNBDiscount(ctx, account, adapter); err != nil {
			errs = append(errs, fmt.Errorf("账户 %s 开启 BNB 抵扣：%w", account.TradingAccountID, err))
		}
	}
	return errs
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
			exchange.StatusTradable(instrument.Status) {
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

// quantityRules 返回标的的数量步长、最小数量和在这个价格下的最小名义金额（现货标的，面值为 1）：
// 交易所给了最小名义金额就用它，没给（OKX）用最小下单量 × 价格折算。
func quantityRules(instrument store.InstrumentRecord, price shared.Decimal) (step, minimum, minNotional shared.Decimal, err error) {
	if step, err = shared.ParseDecimal(instrument.ExchangeQuantityStep); err != nil || step.Cmp(shared.Zero()) <= 0 {
		return shared.Zero(), shared.Zero(), shared.Zero(), fmt.Errorf("%s 的数量步长不合法", instrument.InstrumentID)
	}
	if minimum, err = shared.ParseDecimal(instrument.MinExchangeQuantity); err != nil {
		return shared.Zero(), shared.Zero(), shared.Zero(), fmt.Errorf("%s 的最小数量不合法", instrument.InstrumentID)
	}
	raw, err := instrument.MinNotionalAt(price)
	if err != nil {
		return shared.Zero(), shared.Zero(), shared.Zero(), fmt.Errorf("%s 的最小名义金额不合法", instrument.InstrumentID)
	}
	minNotional = shared.Zero()
	if strings.TrimSpace(raw) != "" {
		if minNotional, err = shared.ParseDecimal(raw); err != nil {
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

// advanceUnconfirmed 推进还没有交易所结果的旧系统单：待提交的丢弃；提交中、结果未知的先解析，
// 解析后退回待提交的接着丢弃。系统单由组合锁串行下出，在锁里看到这样的旧单，要么是提交失败留下的，
// 要么是进程中途退出留下的，没有别的代码会推进它们；不处理的话它们一直算“未结束”，补买和调平永远让路。
// 解析后仍然未知（还在查询窗口内）的不报错，下一轮再解析。
func (s *Service) advanceUnconfirmed(ctx context.Context, record store.OrderRecord) error {
	switch orderdomain.State(record.State) {
	case orderdomain.Pending:
		_, err := s.Orders.DiscardPending(ctx, record.SpaceID, record.OrderID)
		return err
	case orderdomain.Submitting, orderdomain.SubmitUnknown:
		resolved, err := s.Orders.ResolveUnknown(ctx, record.SpaceID, record.OrderID)
		if err != nil || resolved.State != orderdomain.Pending {
			return err
		}
		_, err = s.Orders.DiscardPending(ctx, record.SpaceID, record.OrderID)
		return err
	}
	return nil
}

// stopOrder 结束一笔还没成交完的系统单，写法与人工操作的 stopOrder 一致：待提交丢弃；提交中、结果未知先解析
// （解析成挂单的接着撤）；撤单中走撤单恢复；其余才撤。提交结果未知的单不能直接撤，撤单被拒后会得到一个没有交易所单号的挂单。
func (s *Service) stopOrder(ctx context.Context, record store.OrderRecord) error {
	switch orderdomain.State(record.State) {
	case orderdomain.Pending, orderdomain.Submitting, orderdomain.SubmitUnknown:
		if err := s.advanceUnconfirmed(ctx, record); err != nil {
			return err
		}
		latest, err := s.Store.GetOrder(ctx, record.SpaceID, record.OrderID)
		if err != nil {
			return err
		}
		switch orderdomain.State(latest.State) {
		case orderdomain.Open, orderdomain.PartiallyFilled, orderdomain.Canceling, orderdomain.CancelUnknown:
			return s.stopOrder(ctx, latest)
		}
		return nil
	case orderdomain.Canceling, orderdomain.CancelUnknown:
		_, err := s.Orders.RecoverCancel(ctx, record.SpaceID, record.OrderID)
		return err
	default:
		_, err := s.Orders.Cancel(ctx, record.SpaceID, record.OrderID)
		return err
	}
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
