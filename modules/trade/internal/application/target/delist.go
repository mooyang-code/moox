package target

import (
	"context"
	"encoding/json"
	"fmt"

	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// 下架名单与下架清仓在执行器里的三个效果（计划 4.7）：
//   - 名单里还能交易的标的“只减不增”：目标截到不超过当前持仓，策略减仓、卖出照常，加仓的部分去掉；
//   - 名单里已经不能交易的标的整项跳过：不买不卖，也不当作“没有可交易标的”把整份目标置为 BLOCKED；
//   - 正在做下架清仓（DELIST_FLATTEN 运行中）的标的，执行器不碰：它的单不算冲突，也不和它同时下单。

// delistFlattenScope 是一个运行中的下架清仓动作的范围，从动作的请求 JSON 读出。
// 键名与 operator 包的 flattenRequest 一致（instrument_id、exchange、trading_account_id）。
type delistFlattenScope struct {
	InstrumentID     string `json:"instrument_id"`
	Exchange         string `json:"exchange"`
	TradingAccountID string `json:"trading_account_id"`
}

// delistFlattens 是一个组合里运行中的下架清仓：动作号集合（它们的 OPERATOR 单不算冲突）和各自的范围。
type delistFlattens struct {
	actionIDs map[string]struct{}
	scopes    []delistFlattenScope
}

func (d delistFlattens) owns(actionID string) bool {
	_, found := d.actionIDs[actionID]
	return found
}

// covers 报告这个成员上的标的是否在运行中的下架清仓范围内：范围写了交易所或执行账户时，只覆盖对应的成员。
func (d delistFlattens) covers(account store.TradingAccountRecord, instrumentID string) bool {
	for _, scope := range d.scopes {
		if scope.InstrumentID != instrumentID ||
			(scope.Exchange != "" && scope.Exchange != account.Exchange) ||
			(scope.TradingAccountID != "" && scope.TradingAccountID != account.TradingAccountID) {
			continue
		}
		return true
	}
	return false
}

func (e *Executor) runningDelistFlattens(ctx context.Context, spaceID, logicalAccountID string) (delistFlattens, error) {
	running, err := e.Store.ListRunningOperatorActions(ctx, spaceID, logicalAccountID)
	if err != nil {
		return delistFlattens{}, err
	}
	result := delistFlattens{actionIDs: make(map[string]struct{})}
	for _, action := range running {
		if action.ActionType != string(operatordomain.ActionDelistFlatten) {
			continue
		}
		var scope delistFlattenScope
		if err := json.Unmarshal([]byte(action.RequestJSON), &scope); err != nil {
			return delistFlattens{}, fmt.Errorf("下架清仓 %s 的请求无法解析：%w", action.ActionID, err)
		}
		result.actionIDs[action.ActionID] = struct{}{}
		result.scopes = append(result.scopes, scope)
	}
	return result, nil
}

// restricted 报告执行器是否不能对这个成员的这个标的加仓（只减不增）或根本不碰（跳过）。
func (m memberState) restricted(instrumentID string) bool {
	return m.skip[instrumentID] || m.reduceOnly[instrumentID]
}

// constrainDesired 在 desiredTargets 之后、算差额之前调用，Converge 和 HasDifference 共用：
//   - 所有支持这个标的的成员都被跳过时，整项去掉；
//   - 所有可交易成员都是“只减不增”时，目标截到不超过合计持仓（同号取小，反号截成 0）；
//   - 只有部分成员受限时目标不动，受限成员不会被选来加仓（见 nextLaneAction、placementCandidates）。
//
// 返回被截掉了加仓的标的。
func constrainDesired(desired map[string]desiredTarget, members []memberState) []string {
	var truncated []string
	for instrumentID, want := range desired {
		if want.Hold {
			continue
		}
		restrictedAny := false
		for _, member := range members {
			restrictedAny = restrictedAny || member.restricted(instrumentID)
		}
		if !restrictedAny {
			continue
		}
		supporting, allReduceOnly := 0, true
		confirmed := shared.Zero()
		for _, member := range members {
			if _, ok := member.instruments[instrumentID]; !ok || member.skip[instrumentID] {
				continue
			}
			supporting++
			confirmed = confirmed.Add(member.positions[instrumentID])
			allReduceOnly = allReduceOnly && member.reduceOnly[instrumentID]
		}
		switch {
		case supporting == 0:
			delete(desired, instrumentID)
		case allReduceOnly:
			capped := capReduceOnly(want.Quantity, confirmed)
			if capped.Cmp(want.Quantity) != 0 {
				truncated = append(truncated, instrumentID)
				desired[instrumentID] = desiredTarget{Quantity: capped}
			}
		}
	}
	return truncated
}

// capReduceOnly 把目标截成只减不增：同号时不超过持仓（多头不超过当前持仓，空头绝对值不超过当前持仓），
// 反号或当前没有持仓时截成 0（反向开仓的部分去掉）。
func capReduceOnly(target, position shared.Decimal) shared.Decimal {
	if position.IsZero() || target.IsZero() || position.IsNegative() != target.IsNegative() {
		return shared.Zero()
	}
	if target.Abs().Cmp(position.Abs()) > 0 {
		return position
	}
	return target
}

// warnReduceOnly 对截掉了加仓的标的告警，同一个组合、同一个标的每天一次。
// 只在截掉的买入量确实会下出去时才告警：目标比持仓只多一点点（低于调仓阈值、低于最小下单额）本来也不会下单，不算。
func (e *Executor) warnReduceOnly(ctx context.Context, env *laneEnv, logicalAccountID string, original map[string]desiredTarget, truncated []string, members []memberState) {
	day := e.now().Format("2006-01-02")
	for _, instrumentID := range truncated {
		key := "reduce-only|" + logicalAccountID + "|" + instrumentID + "|" + day
		// 先查当天是否已经告警过，再去判断（判断要取一次报价，每轮都取太多余）。
		if e.alreadyWarned(key) || !e.truncationMatters(ctx, env, instrumentID, original[instrumentID], members) {
			continue
		}
		e.warnOnce(
			key,
			fmt.Sprintf("组合 %s 的标的 %s 在下架名单里，目标里加仓的部分已去掉，只减不增", logicalAccountID, instrumentID),
		)
	}
}

// truncationMatters 判断截断之前的目标会不会下出买单：超过调仓阈值，且按步长取整后的金额不低于设置的最小下单额。
func (e *Executor) truncationMatters(ctx context.Context, env *laneEnv, instrumentID string, want desiredTarget, members []memberState) bool {
	confirmed := shared.Zero()
	for _, member := range members {
		if !member.skip[instrumentID] {
			confirmed = confirmed.Add(member.positions[instrumentID])
		}
	}
	if env.withinRebalanceThreshold(want.Quantity, confirmed, members, instrumentID) {
		return false
	}
	for _, member := range members {
		instrument, ok := member.instruments[instrumentID]
		if !ok || member.skip[instrumentID] {
			continue
		}
		delta := want.Quantity.Sub(confirmed)
		quantity := floorToStep(delta.Abs(), mustBaseStep(instrument))
		if quantity.IsZero() {
			return false
		}
		quote, err := e.Prices.LatestPrice(ctx, member.account.TradingAccountID, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol))
		if err != nil || quote.Price.Cmp(shared.Zero()) <= 0 {
			return true // 拿不到报价，判断不了，宁可提醒
		}
		return !env.belowConfiguredMinimum(laneAction{member: member, instrument: instrument, delta: delta}, quantity, quote.Price)
	}
	return false
}
