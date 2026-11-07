package operator

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/application/exposure"
	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
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
	return nil
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
}

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

// reduceTargets 为减仓算出每个持仓的目标数量：减掉 ratio 比例（按交易所数量步长向下取整），余下的就是目标。
// 现货按受管资产的余额，合约按持仓；零头（减不动一个步长）的持仓不登记。
func (s *Service) reduceTargets(
	ctx context.Context,
	command FlattenCommand,
	members []store.LogicalAccountMemberRecord,
) (map[string]shared.Decimal, error) {
	targets := make(map[string]shared.Decimal)
	for _, member := range members {
		account, err := s.Store.GetTradingAccount(ctx, command.SpaceID, member.TradingAccountID)
		if err != nil {
			return nil, err
		}
		instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
		if err != nil {
			return nil, err
		}
		bySymbol := make(map[exchange.SymbolKey]store.InstrumentRecord, len(instruments))
		for _, instrument := range instruments {
			bySymbol[exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol)] = instrument
		}
		add := func(instrument store.InstrumentRecord, current shared.Decimal) {
			if strings.TrimSpace(command.InstrumentID) != "" && instrument.InstrumentID != command.InstrumentID {
				return
			}
			step, _, ruleErr := baseQuantityRules(instrument)
			if ruleErr != nil {
				return
			}
			reduce := exposure.FloorToStep(current.Abs().Mul(command.ReduceRatio), step)
			if reduce.IsZero() {
				return
			}
			signed := reduce
			if current.IsNegative() {
				signed = reduce.Neg()
			}
			targets[scopeKey(account.TradingAccountID, instrument.InstrumentID)] = current.Sub(signed)
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
				instrument, found := bySymbol[exchange.NewSymbolKey(position.MarketType, position.ExchangeSymbol)]
				if quantity.IsZero() || !found {
					continue
				}
				add(instrument, quantity)
			}
		}
		if account.HasBalanceMarket() {
			managed, err := s.Store.ManagedAssets(ctx, command.SpaceID, account.TradingAccountID)
			if err != nil {
				return nil, err
			}
			byAsset := exposure.SpotInstruments(instruments, account.SettlementAsset)
			for _, balance := range account.SpotBalances() {
				if _, ok := managed[balance.Asset]; !ok || balance.Asset == account.SettlementAsset {
					continue
				}
				total, err := decimalOrZero(balance.Total)
				if err != nil {
					return nil, err
				}
				instrument, found := byAsset[balance.Asset]
				if total.Cmp(shared.Zero()) <= 0 || !found {
					continue
				}
				add(instrument, total)
			}
		}
	}
	return targets, nil
}

// encodeTargets / decodeTargets 把减仓目标存进动作结果里（键：执行账户|标的 ID，值为数量字符串）。
func encodeTargets(targets map[string]shared.Decimal) map[string]string {
	if len(targets) == 0 {
		return nil
	}
	encoded := make(map[string]string, len(targets))
	for key, value := range targets {
		encoded[key] = value.String()
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
