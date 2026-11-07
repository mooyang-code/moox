package operator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/application/exposure"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/fundsettings"
	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

// actionType 返回清仓命令对应的动作类型：比例小于 1 是 REDUCE；只平一个标的是 DELIST_FLATTEN；否则是整组 FLATTEN。
func (c FlattenCommand) actionType() operatordomain.ActionType {
	switch {
	case c.reducing():
		return operatordomain.ActionReduce
	case strings.TrimSpace(c.InstrumentID) != "":
		return operatordomain.ActionDelistFlatten
	default:
		return operatordomain.ActionFlatten
	}
}

// reducing 报告命令是否只减一部分仓位：比例在 0 到 1 之间。比例为零表示没填，按全部清掉处理。
func (c FlattenCommand) reducing() bool {
	return c.ReduceRatio.Cmp(shared.Zero()) > 0 && c.ReduceRatio.Cmp(shared.MustDecimal("1")) < 0
}

// pausesLogicalAccount 报告命令是否先暂停组合账户：整组清仓和减仓都要暂停；只平一个标的不动组合的自动执行状态。
func (c FlattenCommand) pausesLogicalAccount() bool {
	return c.actionType() != operatordomain.ActionDelistFlatten
}

// validateScope 校验比例和标的：比例必须在 (0, 1] 内（零表示没填）。
func (c FlattenCommand) validateScope() error {
	if c.ReduceRatio.IsNegative() || c.ReduceRatio.Cmp(shared.MustDecimal("1")) > 0 {
		return fmt.Errorf("%w: 减仓比例必须在 0 到 1 之间", ErrInvalidCommand)
	}
	if c.reducing() && strings.TrimSpace(c.InstrumentID) != "" {
		return fmt.Errorf("%w: 减仓比例和单个标的不能同时给", ErrInvalidCommand)
	}
	return nil
}

// fundSettingsOf 读取执行账户所属组合的资金设置，和执行器、受管资产同一个入口：账户不在任何组合里才取默认值，
// 数值字段坏了取默认值，排除字段坏了或其他读错误一律返回错误，清仓这一轮不下单。每个账户每轮只读一次。
func (s *Service) fundSettingsOf(ctx context.Context, spaceID, tradingAccountID string) (fundsettings.Settings, error) {
	return s.Store.FundSettingsForAccount(ctx, spaceID, tradingAccountID)
}

// instrumentIndex 是账户标的的索引：不带可交易过滤，已下架的标的也能判断是否在清仓范围内。
type instrumentIndex struct {
	// bySymbol 按（市场，交易所代码）找标的，所有状态。
	bySymbol map[exchange.SymbolKey]store.InstrumentRecord
	// spot 按基础资产找现货卖出标的（计价资产等于结算资产），有可交易的优先。
	spot map[string]store.InstrumentRecord
}

func newInstrumentIndex(instruments []store.InstrumentRecord, settlementAsset string) instrumentIndex {
	index := instrumentIndex{
		bySymbol: make(map[exchange.SymbolKey]store.InstrumentRecord, len(instruments)),
		spot:     make(map[string]store.InstrumentRecord),
	}
	for _, instrument := range instruments {
		index.bySymbol[exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol)] = instrument
		if !exchange.RuleFor(instrument.MarketType).PositionFromBalance ||
			instrument.QuoteAsset != settlementAsset || instrument.BaseAsset == settlementAsset {
			continue
		}
		current, exists := index.spot[instrument.BaseAsset]
		if !exists || (!exposure.Tradable(current, settlementAsset) && exposure.Tradable(instrument, settlementAsset)) {
			index.spot[instrument.BaseAsset] = instrument
		}
	}
	return index
}

// flattenScope 是一次清仓或减仓要处理的范围：只看哪个标的，以及减仓时每个持仓要减到多少。
type flattenScope struct {
	// instrumentID 非空时清仓的每一步（撤单、平仓、剩余统计）都只看这个标的。
	instrumentID string
	// targets 是减仓要收敛到的持仓数量（键：执行账户|标的 ID，值带符号）。为空表示全部清到零；
	// 减仓发起时一次写定，之后各轮都收敛到这个数量，重试不会再减一次。
	targets map[string]shared.Decimal
	// reducing 为真时只处理 targets 里有的持仓。
	reducing bool
	// ensureTargets 在账户撤单、确认、同步之后计算并落库这个账户的减仓目标，只在第一次计算，之后沿用。
	ensureTargets func(ctx context.Context, tradingAccountID string, settings fundsettings.Settings) error
	// settings 是这个账户这一轮读到的资金设置（flattenAccount 读一次，平仓、剩余统计、减仓目标共用）。
	settings fundsettings.Settings
}

// narrow 报告范围是否被收窄：只平一个标的，或减仓（只处理登记过的持仓）。收窄时范围外的资产和持仓一律跳过。
func (s flattenScope) narrow() bool { return s.instrumentID != "" || s.reducing }

func (s flattenScope) includes(instrumentID string) bool {
	return s.instrumentID == "" || s.instrumentID == instrumentID
}

func scopeKey(tradingAccountID, instrumentID string) string {
	return tradingAccountID + "|" + instrumentID
}

// target 返回持仓要收敛到的数量；减仓里没有登记的持仓（发起之后才出现的）不在范围内。
func (s flattenScope) target(tradingAccountID, instrumentID string) (shared.Decimal, bool) {
	if !s.reducing {
		return shared.Zero(), true
	}
	value, found := s.targets[scopeKey(tradingAccountID, instrumentID)]
	return value, found
}

// excess 返回持仓超出目标的数量（保持持仓方向，绝对值为要减掉的量）；没有超出时返回零。
func excess(current, target shared.Decimal) shared.Decimal {
	if current.Abs().Cmp(target.Abs()) <= 0 {
		return shared.Zero()
	}
	return current.Sub(target)
}

// orderInScope 报告订单是否在清仓范围内：只平一个标的时，别的标的的订单不动。
func (s flattenScope) orderInScope(order store.OrderRecord) bool {
	return s.includes(order.InstrumentID)
}

// reduceTargetsForAccount 为一个账户的减仓算出每个持仓的目标数量：减掉 ratio 比例（按交易所数量步长向下取整），
// 余下的就是目标。现货按受管资产的余额，合约按持仓；减不动一个最小数量的持仓（零头）不登记，不参与减仓。
// 调用方要在撤单确认和同步之后再算，在途子单的成交才不会让目标算偏。
func (s *Service) reduceTargetsForAccount(
	ctx context.Context,
	command FlattenCommand,
	tradingAccountID string,
	settings fundsettings.Settings,
) (map[string]shared.Decimal, error) {
	targets := make(map[string]shared.Decimal)
	account, err := s.Store.GetTradingAccount(ctx, command.SpaceID, tradingAccountID)
	if err != nil {
		return nil, err
	}
	instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
	if err != nil {
		return nil, err
	}
	index := newInstrumentIndex(instruments, account.SettlementAsset)
	add := func(instrument store.InstrumentRecord, current shared.Decimal) error {
		step, minimum, ruleErr := baseQuantityRules(instrument)
		if ruleErr != nil {
			return fmt.Errorf("标的 %s 的数量规则无法解析：%w", instrument.InstrumentID, ruleErr)
		}
		reduce := exposure.FloorToStep(current.Abs().Mul(command.ReduceRatio), step)
		// 现货零头和合约减仓量低于最小数量用同一个口径：减不动，不登记。
		if reduce.IsZero() || reduce.Cmp(minimum) < 0 {
			return nil
		}
		signed := reduce
		if current.IsNegative() {
			signed = reduce.Neg()
		}
		targets[scopeKey(account.TradingAccountID, instrument.InstrumentID)] = current.Sub(signed)
		return nil
	}
	if account.HasPositionMarket() {
		positions, err := s.Store.ListPositions(ctx, command.SpaceID, account.TradingAccountID, exchange.SymbolKey{})
		if err != nil {
			return nil, err
		}
		for _, position := range positions {
			quantity, err := decimalOrZero(position.SignedQuantity)
			if err != nil {
				return nil, err
			}
			instrument, found := index.bySymbol[exchange.NewSymbolKey(position.MarketType, position.ExchangeSymbol)]
			// 套利底仓的永续对冲腿不参与清仓和减仓，与执行器一致：平掉它会留下没对冲的底仓现货。
			if quantity.IsZero() || !found || !exposure.Tradable(instrument, account.SettlementAsset) || settings.IsArbitrageBase(instrument.BaseAsset) {
				continue
			}
			if err := add(instrument, quantity); err != nil {
				return nil, err
			}
		}
	}
	if account.HasBalanceMarket() {
		managed, err := s.Store.ManagedAssets(ctx, command.SpaceID, account.TradingAccountID)
		if err != nil {
			return nil, err
		}
		for _, balance := range account.SpotBalances() {
			if _, ok := managed[balance.Asset]; !ok || balance.Asset == account.SettlementAsset {
				continue
			}
			total, err := decimalOrZero(balance.Total)
			if err != nil {
				return nil, err
			}
			instrument, found := index.spot[balance.Asset]
			if total.Cmp(shared.Zero()) <= 0 || !found || !exposure.Tradable(instrument, account.SettlementAsset) {
				continue
			}
			if err := add(instrument, total); err != nil {
				return nil, err
			}
		}
	}
	return targets, nil
}

// encodeTargets / decodeTargets 把减仓目标存进动作结果里（键：执行账户|标的 ID，值为数量字符串）。
func encodeTargets(targets map[string]shared.Decimal) map[string]string {
	encoded := make(map[string]string, len(targets))
	for key, value := range targets {
		if strings.HasSuffix(key, "|") {
			continue // “已算过目标”的账户标记单独存放，不混进目标
		}
		encoded[key] = value.String()
	}
	if len(encoded) == 0 {
		return nil
	}
	return encoded
}

func decodeTargets(encoded map[string]string) (map[string]shared.Decimal, error) {
	targets := make(map[string]shared.Decimal, len(encoded))
	for key, raw := range encoded {
		value, err := shared.ParseDecimal(raw)
		if err != nil {
			return nil, fmt.Errorf("减仓目标 %s 不是合法数字：%w", key, err)
		}
		targets[key] = value
	}
	return targets, nil
}

// requireInstrumentInMembers 要求只平的标的至少能在一个被选中的成员的标的表里解析到：
// 任何成员都解析不到时不能静默当作平完了，按无效命令失败。
func (s *Service) requireInstrumentInMembers(ctx context.Context, command FlattenCommand, members []store.LogicalAccountMemberRecord) error {
	instrumentID := strings.TrimSpace(command.InstrumentID)
	for _, member := range members {
		_, err := s.Store.GetInstrumentByIDForAccount(ctx, command.SpaceID, member.TradingAccountID, instrumentID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	return fmt.Errorf("%w: 标的 %s 在所有成员的标的表里都解析不到", ErrInvalidCommand, instrumentID)
}
