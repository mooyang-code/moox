package target

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ordersettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// laneEnv 是一轮收敛里各标的共用的下单环境：下单设置、报价、权益和本小时已用额度都在一轮里只取一次，
// Converge、HasDifference（过期告警）和待提交子单的重新判断用同一份，结论一致。
type laneEnv struct {
	executor         *Executor
	spaceID          string
	logicalAccountID string
	settings         ordersettings.Settings
	// quotes 是按需预取的标的报价（键：内部标的 ID）；阈值模式要用金额时才取，取不到的标的没有条目。
	quotes map[string]Quote
	// equity 是组合最近一个权益采样点的权益；equityKnown 为假表示取不到，EQUITY_RATIO 阈值不生效。
	equity      shared.Decimal
	equityKnown bool
	// hourly 是本自然小时已提交的目标子单金额（键：内部标的 ID），配置了每小时上限、第一次要用时才查订单表。
	hourly       map[string]shared.Decimal
	hourlyLoaded bool
}

// errOrderSettingsUnreadable 表示下单设置读不出来：这个组合不下单，原因写进目标。
var errOrderSettingsUnreadable = errors.New("下单设置无法解析")

// loadOrderSettings 读取并严格解析组合的下单设置。保存时已经校验过，读不出来说明数据被改坏。
func loadOrderSettings(logical store.LogicalAccountRecord) (ordersettings.Settings, error) {
	settings, err := ordersettings.Parse(logical.OrderSettingsJSON)
	if err != nil {
		return ordersettings.Settings{}, fmt.Errorf("%w：组合 %s：%v", errOrderSettingsUnreadable, logical.LogicalAccountID, err)
	}
	return settings, nil
}

// newLaneEnv 建立这一轮的下单环境。
func (e *Executor) newLaneEnv(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	settings ordersettings.Settings,
	members []memberState,
	desired map[string]desiredTarget,
) *laneEnv {
	env := &laneEnv{executor: e, spaceID: spaceID, logicalAccountID: logicalAccountID, settings: settings, quotes: make(map[string]Quote)}
	needsValue := settings.ThresholdMode == ordersettings.ThresholdPositionRatio ||
		settings.ThresholdMode == ordersettings.ThresholdEquityRatio
	if !needsValue {
		return env
	}
	if settings.ThresholdMode == ordersettings.ThresholdEquityRatio {
		// 没有采样点（新组合）时取不到权益，阈值不生效。
		if point, err := e.Store.LatestLogicalAccountEquityPoint(ctx, spaceID, logicalAccountID); err == nil {
			if equity, parseErr := shared.ParseDecimal(point.Equity); parseErr == nil && equity.Cmp(shared.Zero()) > 0 {
				env.equity, env.equityKnown = equity, true
			}
		}
	}
	// 只有目标和持仓都不为 0 的标的才会用到阈值，其余标的不取报价。
	for _, instrumentID := range laneIDs(desired, members) {
		target, ok := desired[instrumentID]
		if !ok || target.Hold || target.Quantity.IsZero() {
			continue
		}
		for _, member := range members {
			if member.positions[instrumentID].IsZero() {
				continue
			}
			instrument, known := member.instruments[instrumentID]
			if !known {
				continue
			}
			quote, err := e.Prices.LatestPrice(ctx, member.account.TradingAccountID, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol))
			if err == nil && quote.Price.Cmp(shared.Zero()) > 0 {
				env.quotes[instrumentID] = quote
			}
			break
		}
	}
	return env
}

// withinRebalanceThreshold 判断整条标的的差额是否小到不用调。四种模式：
//   - TARGET_RATIO：|目标 − 合计持仓| 不超过 |目标| × r；
//   - POSITION_RATIO：调仓金额低于当前持仓金额 × r；
//   - EQUITY_RATIO：调仓金额低于组合权益 × r；
//   - ALWAYS：每期都调。
//
// 建仓（合计持仓为 0）和清仓（目标为 0）在任何模式下都不受阈值限制；只要有成员持仓和目标反向，也不跳过。
// 按整条标的比较，反向持仓和多成员对冲不会被跳过。需要金额而取不到报价（或权益）时不跳过，宁可多调。
func (env *laneEnv) withinRebalanceThreshold(
	desired shared.Decimal,
	confirmed shared.Decimal,
	members []memberState,
	instrumentID string,
) bool {
	settings := env.settings
	if settings.ThresholdMode == ordersettings.ThresholdAlways {
		return false
	}
	if desired.IsZero() || confirmed.IsZero() {
		return false
	}
	for _, member := range members {
		position := member.positions[instrumentID]
		if !position.IsZero() && !sameSign(position, desired) {
			return false
		}
	}
	diff := desired.Sub(confirmed).Abs()
	switch settings.ThresholdMode {
	case ordersettings.ThresholdTargetRatio:
		return diff.Cmp(desired.Abs().Mul(settings.ThresholdRatio)) <= 0
	case ordersettings.ThresholdPositionRatio:
		quote, ok := env.quotes[instrumentID]
		if !ok {
			return false
		}
		return diff.Mul(quote.Price).Cmp(confirmed.Abs().Mul(quote.Price).Mul(settings.ThresholdRatio)) < 0
	case ordersettings.ThresholdEquityRatio:
		quote, ok := env.quotes[instrumentID]
		if !ok || !env.equityKnown {
			return false
		}
		return diff.Mul(quote.Price).Cmp(env.equity.Mul(settings.ThresholdRatio)) < 0
	}
	return false
}

// belowConfiguredMinimum 判断子单金额是否低于设置的最小下单额（现货、合约各一个值）。
// 合约的减仓和清仓不受它限制：低于门槛的剩余仓位也要能平掉。交易所自己的最小名义金额另行判断，
// OKX 没有最小名义金额，它的最小数量（minSz）在取整时已经保证金额不低于 minSz × 价格。
func (env *laneEnv) belowConfiguredMinimum(candidate laneAction, quantity, price shared.Decimal) bool {
	if candidate.reducing && exchange.RuleFor(candidate.instrument.MarketType).ReduceOnly {
		return false
	}
	minimum := env.settings.MinNotional(candidate.instrument.MarketType)
	return minimum.Cmp(shared.Zero()) > 0 && quantity.Mul(price).Cmp(minimum) < 0
}

// hourWindow 返回当前时刻所在自然小时（UTC）的起止。
func hourWindow(now time.Time) (start, next time.Time) {
	start = now.UTC().Truncate(time.Hour)
	return start, start.Add(time.Hour)
}

// nextHourRetryDelay 是到下一个整点的等待时间；多等一秒，保证唤醒时已经进入新的一小时。
func nextHourRetryDelay(now time.Time) time.Duration {
	_, next := hourWindow(now)
	return next.Sub(now) + time.Second
}

// submittedNotional 是一笔已提交目标子单计入每小时上限的金额：结束的单按实际成交额，没结束的按全部数量 × 参考价。
// 金额解析失败返回错误：按 0 计会让上限静默放宽。
func submittedNotional(row store.SubmittedTargetOrder) (shared.Decimal, error) {
	price, err := shared.ParseDecimal(row.ReferencePrice)
	if err != nil {
		return shared.Zero(), fmt.Errorf("%s 的已提交子单参考价 %q 无法解析：%w", row.InstrumentID, row.ReferencePrice, err)
	}
	if orderdomain.State(row.State).Terminal() {
		filled, filledErr := shared.ParseDecimal(row.FilledQuantity)
		if filledErr != nil {
			return shared.Zero(), fmt.Errorf("%s 的已提交子单成交量 %q 无法解析：%w", row.InstrumentID, row.FilledQuantity, filledErr)
		}
		if average, averageErr := shared.ParseDecimal(row.AveragePrice); averageErr == nil && average.Cmp(shared.Zero()) > 0 {
			price = average
		}
		return filled.Mul(price), nil
	}
	quantity, err := shared.ParseDecimal(row.Quantity)
	if err != nil {
		return shared.Zero(), fmt.Errorf("%s 的已提交子单数量 %q 无法解析：%w", row.InstrumentID, row.Quantity, err)
	}
	return quantity.Mul(price), nil
}

// hourlyUsed 返回标的本自然小时已提交的金额，第一次调用时按订单表汇总一次。
func (env *laneEnv) hourlyUsed(ctx context.Context, instrumentID string) (shared.Decimal, error) {
	if !env.hourlyLoaded {
		start, next := hourWindow(env.executor.now())
		rows, err := env.executor.Store.ListSubmittedTargetOrders(ctx, env.spaceID, env.logicalAccountID, start.UnixMilli(), next.UnixMilli())
		if err != nil {
			return shared.Zero(), err
		}
		env.hourly = make(map[string]shared.Decimal)
		for _, row := range rows {
			notional, err := submittedNotional(row)
			if err != nil {
				return shared.Zero(), err
			}
			env.hourly[row.InstrumentID] = env.hourly[row.InstrumentID].Add(notional)
		}
		env.hourlyLoaded = true
	}
	return env.hourly[instrumentID], nil
}

// applyHourlyCap 按单币每小时上限收窄数量：本小时剩余额度不够整笔时只下剩下的部分。
// clipped 为真表示数量被额度收窄了；收窄后下不了单的，调用方把这个标的顺延到下一个整点。
func (env *laneEnv) applyHourlyCap(
	ctx context.Context,
	candidate laneAction,
	quantity shared.Decimal,
	price shared.Decimal,
) (shared.Decimal, bool, error) {
	// 上限只限制加仓和建仓：减仓（包括目标改成 0、受管但不在目标里按 0 卖出）一律不查上限，
	// 否则本小时先买满额度后，策略想清仓也要等到下一个整点。减仓的金额照常计入汇总。
	if candidate.reducing {
		return quantity, false, nil
	}
	limit, capped := env.settings.HourlyCap(candidate.instrument.MarketType, candidate.instrument.InstrumentID)
	if !capped {
		return quantity, false, nil
	}
	used, err := env.hourlyUsed(ctx, candidate.instrument.InstrumentID)
	if err != nil {
		return shared.Zero(), false, err
	}
	remaining := limit.Sub(used)
	if remaining.Cmp(shared.Zero()) <= 0 {
		return shared.Zero(), true, nil
	}
	allowed := floorToStep(remaining.Div(price), mustBaseStep(candidate.instrument))
	if allowed.Cmp(quantity) < 0 {
		return allowed, true, nil
	}
	return quantity, false, nil
}

// noteSubmitted 把刚建的子单金额记进本小时已用额度，同一轮后面的判断要用。
func (env *laneEnv) noteSubmitted(instrumentID string, notional shared.Decimal) {
	if env.hourlyLoaded {
		env.hourly[instrumentID] = env.hourly[instrumentID].Add(notional)
	}
}

// tooSmallToPlace 判断数量（已按步长取整）是否下不了单：不到交易所最小数量、不到交易所最小名义金额（合约全平例外）、
// 或不到配置的最小下单额（合约减仓、清仓例外）。每小时上限把数量收窄后用它区分“额度不够”和“本来就太小”。
func (env *laneEnv) tooSmallToPlace(candidate laneAction, quantity, price shared.Decimal) bool {
	if quantity.Cmp(shared.Zero()) <= 0 {
		return true
	}
	if _, minimum, err := baseQuantityRules(candidate.instrument); err != nil || quantity.Cmp(minimum) < 0 {
		return true
	}
	shortfall, err := env.minimumShortfall(candidate, quantity, price, false)
	return err != nil || shortfall != ""
}

// 数量不够哪一项最小金额：交易所最小名义金额，或设置的最小下单额。
const (
	shortfallExchange   = "exchange"
	shortfallConfigured = "configured"
)

// minimumShortfall 返回数量按给定价格不够哪一项最小金额，够了返回空串。skipExchange 为真时不查交易所最小名义金额
// （合约只减仓单被单笔上限截小后）。合约全平、合约减仓的例外见 belowExchangeMinimum 和 belowConfiguredMinimum。
func (env *laneEnv) minimumShortfall(candidate laneAction, quantity, price shared.Decimal, skipExchange bool) (string, error) {
	if !skipExchange {
		below, err := belowExchangeMinimum(candidate, quantity, price)
		if err != nil {
			return "", err
		}
		if below {
			return shortfallExchange, nil
		}
	}
	if env.belowConfiguredMinimum(candidate, quantity, price) {
		return shortfallConfigured, nil
	}
	return "", nil
}

// liftToMinimum 把被单笔上限截小、不够最小金额的子单抬到刚好够：拆单随机化可能抽到偏低的比例，而配置的单笔上限本身是够的。
// 抬高后不超过截量前的数量，也不超过配置的单笔上限（按参考价折算）；够不上就原样返回，由调用方阻塞。
func (env *laneEnv) liftToMinimum(candidate laneAction, quantity, beforeSlice, price, referencePrice shared.Decimal, skipExchange bool) shared.Decimal {
	need := shared.Zero()
	if !skipExchange {
		if raw, err := candidate.instrument.MinNotionalAt(price); err == nil && raw != "" {
			if minimum, parseErr := shared.ParseDecimal(raw); parseErr == nil && minimum.Cmp(need) > 0 {
				need = minimum
			}
		}
	}
	if !(candidate.reducing && exchange.RuleFor(candidate.instrument.MarketType).ReduceOnly) {
		if configured := env.settings.MinNotional(candidate.instrument.MarketType); configured.Cmp(need) > 0 {
			need = configured
		}
	}
	if need.IsZero() {
		return quantity
	}
	step := mustBaseStep(candidate.instrument)
	want := ceilToStep(need.Div(price), step)
	ceiling := floorToStep(env.settings.MaxChildNotional.Div(referencePrice), step)
	if beforeSlice.Cmp(ceiling) < 0 {
		ceiling = beforeSlice
	}
	if want.Cmp(ceiling) <= 0 && want.Cmp(quantity) > 0 {
		return want
	}
	return quantity
}

// sliceLimitReason 是单笔上限把子单截到不够最小金额时的阻塞原因，显示配置的单笔上限。
func (env *laneEnv) sliceLimitReason(candidate laneAction, shortfall, exchangeMinimum string) string {
	limit := env.settings.MaxChildNotional
	what, hint, minimum := "该标的最小下单额", "请调高单笔上限", exchangeMinimum
	if shortfall == shortfallConfigured {
		configured := env.settings.MinNotional(candidate.instrument.MarketType)
		what, hint, minimum = "设置的最小下单额", "请调高单笔上限或降低最小下单额", configured.String()
	}
	// 单笔上限本身就不到最小额：直说；本身够、按下单价折算后才不够：说明是折算造成的。
	if value, err := shared.ParseDecimal(minimum); err == nil && limit.Cmp(value) >= 0 {
		return fmt.Sprintf("单笔上限 %s 按下单价折算后低于%s %s，%s", limit, what, minimum, hint)
	}
	return fmt.Sprintf("单笔上限 %s 低于%s %s，%s", limit, what, minimum, hint)
}

// splitFraction 由种子算出 [0.5, 1] 之间的比例（精确小数，千分位）。同一个种子永远得到同一个比例，结果可复现。
func splitFraction(seed string) shared.Decimal {
	sum := sha256.Sum256([]byte(seed))
	thousandths := binary.BigEndian.Uint64(sum[:8]) % 501 // 0..500
	return shared.MustDecimal("0.5").Add(shared.MustDecimal(fmt.Sprint(thousandths)).Div(shared.MustDecimal("1000")))
}

// sliceNotionalLimit 是这一笔子单的名义金额上限：单笔上限；开了随机化时取其中随机的一部分（50%～100%），
// 种子是组合、目标、标的和这个标的本目标已发出的子单数，所以逐笔变化、整体可复现。
func (env *laneEnv) sliceNotionalLimit(target store.LogicalAccountTargetRecord, instrumentID string, sent int) shared.Decimal {
	limit := env.settings.MaxChildNotional
	if !env.settings.RandomizeSplit {
		return limit
	}
	return limit.Mul(splitFraction(fmt.Sprintf("%s|%s|%s|%d", target.LogicalAccountID, target.TargetID, instrumentID, sent)))
}
