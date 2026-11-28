package input

import (
	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"

	"github.com/mooyang-code/moox/modules/strategy/internal/period"
	"github.com/mooyang-code/moox/modules/strategy/internal/upstream"
)

// MarketRows 是装配一个市场的输入：本期与上一根的行（按标的归并）、失败证据。实时由 LoadBar 读出，回放由分段读取给出。
type MarketRows struct {
	Market   string
	Current  map[string]upstream.Row
	Previous map[string]upstream.Row
	// Failed 是该市场的失败证据（列名 → 标的 → 原因）；回放没有就绪信息，为空。
	Failed map[string]map[string]string
}

// Assembly 是一期装配引擎帧所需的全部输入，由调用方各自读取、构造：基础集合（Sets）实时与回放的口径不同（见 docs/模块/策略.md
// 第 10 节），这里不关心它从哪来。
type Assembly struct {
	Program  *dsl.Program
	Boundary period.PeriodBoundaries
	Sets     Sets
	Holdings map[string]engine.HoldingClock
	// Listed 是各市场本期列出的标的（年龄判定的口径）；MarketPassed 是策略级 min_age_bars 下各市场通过年龄判定的标的
	// （没做年龄判定的市场不出现）。
	Listed       AgeListed
	MarketPassed map[string]map[string]struct{}
	Markets      []MarketRows
}

// AssembleFrame 把一期的集合划分、年龄判定结果与各市场的行装配成引擎帧（纯函数，不读 Storage）。实时、试算、研究回放、
// 策略池回放与预热都调它，附加来源的列之后由调用方用 MergeSourceRows 并进各市场的行。
//
// 规则：只取基础集合里的标的的行；用到 bars[-1] 时带上上一根的值。多个市场时，某个市场里年龄不够的标的不用这个市场的行
// （老币刚上线永续，不能用现货的历史直接通过永续的年龄检查）：只记进该市场的 AgedOut，由选币池构造决定（已持仓的豁免）。
func AssembleFrame(in Assembly) engine.Frame {
	sets := in.Sets
	frame := engine.Frame{BarEnd: in.Boundary.BarEnd, BarIndex: in.Boundary.BarIndex, Markets: make(map[string]engine.MarketInput, len(in.Markets)),
		Universe: sets.Universe, Expected: sets.Expected, AgedOut: sets.AgedOut, AgedOutReasons: sets.AgedOutReasons, Holdings: in.Holdings}
	manyMarkets := len(in.Markets) > 1
	for _, market := range in.Markets {
		input := engine.MarketInput{Rows: make(map[string]engine.Row, len(market.Current)), Failed: market.Failed}
		passed, probed := in.MarketPassed[market.Market]
		for subjectID, row := range market.Current {
			if _, ok := sets.Subjects[subjectID]; !ok {
				continue
			}
			if manyMarkets && probed {
				if _, listed := in.Listed[market.Market][subjectID]; listed {
					if _, ok := passed[subjectID]; !ok {
						if input.AgedOut == nil {
							input.AgedOut = make(map[string]struct{})
						}
						input.AgedOut[subjectID] = struct{}{}
					}
				}
			}
			// 来源读了 __params 时带上各可变成员这一行实际用的参数，params() 读它；没读时为空，params() 取到 NaN。
			engineRow := engine.Row{Values: row.Values, Params: row.Params}
			if in.Program.UsesPreviousBar {
				if prev, ok := market.Previous[subjectID]; ok {
					engineRow.Previous = prev.Values
				}
			}
			input.Rows[subjectID] = engineRow
		}
		frame.Markets[market.Market] = input
	}
	return frame
}
