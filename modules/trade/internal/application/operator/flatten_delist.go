package operator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/exposure"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// 下架清仓专属：截止时间、子单历史、IOC 限价与间隔、被拒与连续未成交的终结判断。

// unblockedEverywhere 报告下架清仓的标的是否已解除禁止：标的表里有这个标的的成员上都不在名单里了（至少有一个这样的成员）。
// 不是下架清仓、查名单失败都算没解除。
func (s *Service) unblockedEverywhere(ctx context.Context, command FlattenCommand, scope flattenScope, members []store.LogicalAccountMemberRecord) bool {
	if scope.stillBlocked == nil {
		return false
	}
	present := 0
	for _, member := range members {
		blocked, found, err := s.delistBlockState(ctx, command.SpaceID, member.TradingAccountID, scope.instrumentID)
		if err != nil || found && blocked {
			return false
		}
		if found {
			present++
		}
	}
	return present > 0
}

// pastDeadline 报告下架清仓是否已过截止时间（下架时间）。
func (s *Service) pastDeadline(command FlattenCommand) bool {
	if command.DeadlineAt <= 0 {
		return false
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	return now.UnixMilli() >= command.DeadlineAt
}

// errDelistDeadlinePassed：已过下架时间（截止时间），不再下单也不再提交待提交的子单，属终结原因。
var errDelistDeadlinePassed = errors.New("已过下架时间，不再下单")

// notePacing 记下“间隔未到”要等到的时间；err 不是这个情况返回 false。
func (r *FlattenAccountResult) notePacing(err error) bool {
	var pacing delistPacingError
	if !errors.As(err, &pacing) {
		return false
	}
	if pacing.until.After(r.pacingUntil) {
		r.pacingUntil = pacing.until
	}
	r.pacedOnly = true
	return true
}

// delistSlippage 是下架清仓伪市价单的滑点：买入向上、卖出向下 3%。
var delistSlippage = shared.MustDecimal("0.03")

// delistLimitPrice 返回伪市价的限价：参考价 ×（1 ± 3%），买入向上、卖出向下取整到价格精度；价格精度不明时不取整。
func delistLimitPrice(reference shared.Decimal, side exchange.Side, priceTick string) shared.Decimal {
	one := shared.MustDecimal("1")
	tick, tickErr := shared.ParseDecimal(priceTick)
	validTick := tickErr == nil && tick.Cmp(shared.Zero()) > 0
	if side == exchange.SideBuy {
		price := reference.Mul(one.Add(delistSlippage))
		if validTick {
			floored := exposure.FloorToStep(price, tick)
			if floored.Cmp(price) != 0 {
				price = floored.Add(tick)
			} else {
				price = floored
			}
		}
		return price
	}
	price := reference.Mul(one.Sub(delistSlippage))
	if validTick {
		price = exposure.FloorToStep(price, tick)
	}
	return price
}

// delistPacingError 表示离上一笔 IOC 单结束太近，要等到 until 才能下下一笔：不算终结，也不是错误。
// 轮内循环见到它不能空转同步（每轮两次轻量同步，对币安现货加永续要发 3 个接口，会打满 IP 限频），直接睡到 until 再继续。
type delistPacingError struct{ until time.Time }

func (e delistPacingError) Error() string {
	return "下架清仓的限价单刚结束，间隔几秒再下下一笔"
}

// delistHistory 是下架清仓在一个标的上的子单历史。
type delistHistory struct {
	// finished 是本动作在这个标的上已结束的子单总数（不分起点）：并进子单号的哈希，保证每笔的号不同。
	finished int
	// lastRejected、rejectReason 说明这一轮（起点之后）最近一笔是否被交易所拒绝；unfilled 是这一轮最近连续一股都没成交的笔数。
	lastRejected bool
	rejectReason string
	unfilled     int
	// lastFinishedAt 是最近一笔结束的时间（任何一轮），用来控制两笔 IOC 之间的最小间隔。
	lastFinishedAt time.Time
}

// loadDelistHistory 读本动作在这个标的上已结束的子单。被拒和连续未成交只统计这一轮起点之后创建的子单：
// 同一个动作号在 FAILED、PARTIAL 之后重试会重新记起点，让重试真的重新来过；起点之前的子单只影响号和间隔。
func (s *Service) loadDelistHistory(ctx context.Context, command FlattenCommand, tradingAccountID string, key exchange.SymbolKey) (delistHistory, error) {
	records, err := s.Store.ListOrdersForLane(ctx, command.SpaceID, tradingAccountID, key)
	if err != nil {
		return delistHistory{}, err
	}
	var finished []store.OrderRecord
	for _, current := range records {
		if current.OwnerType == string(orderdomain.OwnerOperator) && current.OwnerID == command.ActionID &&
			orderdomain.State(current.State).Terminal() {
			finished = append(finished, current)
		}
	}
	sort.SliceStable(finished, func(i, j int) bool {
		if !finished[i].CreatedAt.Equal(finished[j].CreatedAt) {
			return finished[i].CreatedAt.Before(finished[j].CreatedAt)
		}
		return finished[i].OrderID < finished[j].OrderID
	})
	history := delistHistory{finished: len(finished)}
	var thisRound []store.OrderRecord
	for _, current := range finished {
		at := current.CreatedAt
		if current.FinishedAt > 0 {
			at = time.UnixMilli(current.FinishedAt)
		}
		if at.After(history.lastFinishedAt) {
			history.lastFinishedAt = at
		}
		// 起点取到秒、订单创建时间也只到秒（向下取整）：同一秒内创建的子单算这一轮的，否则这一轮刚下的单会被漏数。
		// 代价是上一轮结束后不到一秒就用同一个动作号重试，会看到上一轮同一秒的被拒单（事件中心至少隔 30 秒才重试，碰不到）。
		if current.CreatedAt.UnixMilli() >= command.attemptStart {
			thisRound = append(thisRound, current)
		}
	}
	if n := len(thisRound); n > 0 && thisRound[n-1].State == string(orderdomain.Rejected) && !localCapacityReject(thisRound[n-1]) {
		history.lastRejected, history.rejectReason = true, thisRound[n-1].RejectReason
	}
	for index := len(thisRound) - 1; index >= 0; index-- {
		filled, parseErr := decimalOrZero(thisRound[index].FilledQuantity)
		if parseErr != nil || filled.Cmp(shared.Zero()) > 0 {
			break
		}
		// 休市前一刻发出、休市时被模拟撮合撤掉的单不是一次真正的尝试，下个时段继续。
		if thisRound[index].RejectReason == paper.OutOfSessionReason {
			continue
		}
		history.unfilled++
	}
	return history, nil
}

// localCapacityReject 报告拒单是否是提交前在本地判定的资金不足、只减仓无效（还没发给交易所，SubmittedAt 为 0；原因文字就是这两个错误的原文）。
// 它们与 Place 的口径一致：持仓、余额可能只是还没同步，按零成交换号重试、到上限才终结，不当成交易所的确定性拒绝。
func localCapacityReject(record store.OrderRecord) bool {
	return record.SubmittedAt == 0 &&
		(record.RejectReason == orderapp.ErrInsufficientFunds.Error() || record.RejectReason == orderapp.ErrReduceOnly.Error())
}

// flattenMaxUnfilled 是清仓、减仓、下架清仓在一个标的上连续零成交的子单上限，到了就按终结处理、要人工看，不无限下单。
const flattenMaxUnfilled = 20

// checkFlattenAttempt 判断加密市场清仓、减仓这一笔能不能下：最近一笔被交易所拒绝（同样的单再发还是拒绝），
// 或这一轮连续 flattenMaxUnfilled 笔零成交（已过期、已撤销、本地校验没过）是终结错误；其余零成交的换号重试。
func checkFlattenAttempt(history delistHistory) error {
	switch {
	case history.lastRejected:
		return flattenFinal{fmt.Errorf("清仓的订单被拒绝：%s", history.rejectReason)}
	case history.unfilled >= flattenMaxUnfilled:
		return flattenFinal{fmt.Errorf("清仓连续 %d 笔订单一股都没成交，需要人工处理", history.unfilled)}
	}
	return nil
}

// checkStockFlattenAttempt 判断 A 股清仓、减仓这一笔能不能下：
//   - 停牌、卖出时跌停（现价在跌停价且没有买一）：一股都卖不出，按可重试处理，下一轮再看，由清仓动作的 30 分钟期限兜底；
//     和“当日买入，次日可卖”不同，后者在当天怎么重试都卖不了，按终结处理；
//   - 最近一笔被拒绝，或这一轮连续 flattenMaxUnfilled 笔零成交（休市时被撤掉的不算）：终结。
func checkStockFlattenAttempt(history delistHistory, side exchange.Side, quote Quote) error {
	switch {
	case quote.Halted:
		return manualFailureError{message: "停牌，复牌后再卖", cause: ErrInvalidCommand}
	case side == exchange.SideSell && quote.LimitDown.Cmp(shared.Zero()) > 0 && quote.Price.Cmp(quote.LimitDown) <= 0 &&
		quote.Bid.Cmp(shared.Zero()) <= 0:
		return manualFailureError{message: "跌停，没有买一，打开后再卖", cause: ErrInvalidCommand}
	case side == exchange.SideBuy && quote.LimitUp.Cmp(shared.Zero()) > 0 && quote.Price.Cmp(quote.LimitUp) >= 0 &&
		quote.Ask.Cmp(shared.Zero()) <= 0:
		return manualFailureError{message: "涨停，没有卖一，打开后再买", cause: ErrInvalidCommand}
	case history.lastRejected:
		return flattenFinal{fmt.Errorf("清仓的订单被拒绝：%s", history.rejectReason)}
	case history.unfilled >= flattenMaxUnfilled:
		return flattenFinal{fmt.Errorf("清仓连续 %d 笔限价单一股都没成交，需要人工处理", history.unfilled)}
	}
	return nil
}

// checkDelistAttempt 判断能不能再下一笔：最近一笔被交易所拒绝（同样的单再发还是拒绝），或这一轮连续 flattenMaxUnfilled 笔 IOC 一股都没成交
// （部分成交说明在推进，不算）是终结错误，这一轮停下；同一个动作号重试会重新来过。两笔之间至少隔 delistAttemptInterval。
func (s *Service) checkDelistAttempt(history delistHistory) error {
	if history.lastRejected {
		return flattenFinal{fmt.Errorf("下架清仓的订单被交易所拒绝：%s", history.rejectReason)}
	}
	if history.unfilled >= flattenMaxUnfilled {
		return flattenFinal{fmt.Errorf("下架清仓连续 %d 次限价单（滑点 3%%）一股都没成交，需要人工处理", history.unfilled)}
	}
	if interval := s.delistAttemptInterval(); interval > 0 && !history.lastFinishedAt.IsZero() && time.Since(history.lastFinishedAt) < interval {
		return delistPacingError{until: history.lastFinishedAt.Add(interval)}
	}
	return nil
}

// defaultDelistAttemptInterval 是两笔下架清仓 IOC 单之间的最小间隔：薄盘口下一笔打完马上再打只会在同一个价位上空转。
const defaultDelistAttemptInterval = 3 * time.Second

func (s *Service) delistAttemptInterval() time.Duration {
	switch {
	case s.DelistAttemptInterval < 0:
		return 0
	case s.DelistAttemptInterval == 0:
		return defaultDelistAttemptInterval
	}
	return s.DelistAttemptInterval
}
