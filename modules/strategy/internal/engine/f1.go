package engine

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// f1 规则的求值：语义与 alphalib F1 框架一致（docs/计划/F1策略迁移/F1策略迁移设计.md 第 1、7 节，F1-1～F1-14）。

const (
	reasonF1FilterAfter = "f1_filter_after"
	f1ReasonPrefix      = "f1:"
	// f1StateNoteBytes 是 f1 状态的观察阈值：超过时在本期摘要里说明（不截断）。
	f1StateNoteBytes = 256 << 10
)

// F1State 是 f1 规则下一期需要的状态：按排名签名分组的开仓记录（设计 7.4）。
type F1State struct {
	Groups []F1GroupState `json:"groups"`
}

// F1GroupState 是一个排名签名的开仓记录；StateKey 同时覆盖签名与组内子策略，组成员变化时旧记录不再使用。
type F1GroupState struct {
	Key   string   `json:"key"`
	Picks []F1Pick `json:"picks"`
}

// F1Pick 是一条开仓记录：开仓 bar、标的、方向、开仓 close、资金分母、自开仓以来的候选行数，以及组内各子策略的后置过滤通过位图。
type F1Pick struct {
	Bar   int64  `json:"bar"`
	ID    string `json:"id"`
	Short bool   `json:"short,omitempty"`
	Close string `json:"close"`
	Denom int    `json:"denom"`
	Rows  int    `json:"rows"`
	Pass  string `json:"pass"`
}

// partitionF1 是 f1 规则专用的集合划分：缺数只算没有行、close 或 volume 为空、或 K 线对该标的失败；
// 因子列为空不算缺数（F1 用中位数名次补上，过滤按 between 语义计算）。volume 为 0 的标的既不可用也不缺数（inactive），
// 所以可用集合就是候选行 B，min_universe 按它守门。
func partitionF1(rule *dsl.CompiledRule, frame Frame, held []string) ruleSets {
	sets := ruleSets{agedOut: make(map[string]struct{}), missing: make(map[string]string), frozen: make(map[string]struct{})}
	heldSet := make(map[string]struct{}, len(held))
	for _, id := range held {
		heldSet[id] = struct{}{}
	}
	for _, id := range frame.AgedOut[rule.Rule.ID] {
		if _, isHeld := heldSet[id]; !isHeld {
			sets.agedOut[id] = struct{}{}
		}
	}
	expected := append([]string(nil), frame.Expected[rule.Rule.ID]...)
	sort.Strings(expected)
	sets.expected = expected
	for _, id := range expected {
		if _, aged := sets.agedOut[id]; aged {
			continue
		}
		row, ok := frame.Rows[id]
		if !ok {
			sets.missing[id] = "no_row"
			continue
		}
		reason := ""
		for _, column := range []string{"close", "volume"} {
			if failure, failed := frame.FailedColumns[column][id]; failed {
				reason = failure + ":" + column
				break
			}
			if _, present := row.Values[column]; !present {
				reason = "missing:" + column
				break
			}
		}
		if reason != "" {
			sets.missing[id] = reason
			continue
		}
		if row.Values["volume"] <= 0 {
			sets.inactive = append(sets.inactive, id)
			continue
		}
		sets.available = append(sets.available, id)
	}
	return sets
}

// f1Key 是同分时的排序键：F1 按 symbol（BTCUSDT）排序，moox 的标的 ID 是 BTC-USDT，去掉 "-" 后比较。
func f1Key(id string) string { return strings.ReplaceAll(id, "-", "") }

// f1Sample 是一期的样本：按 F1 的行顺序（symbol 升序）排好的标的，以及取列值的方法。
type f1Sample struct {
	ids   []string
	frame Frame
}

func (s f1Sample) value(id, column string) float64 {
	if v, ok := s.frame.Rows[id].Values[column]; ok {
		return v
	}
	return math.NaN()
}

// averageRanks 是 pandas rank(method="average", ascending=True)：并列取平均名次，NaN 保持 NaN。
func averageRanks(values []float64) []float64 {
	order := make([]int, 0, len(values))
	for i, v := range values {
		if !math.IsNaN(v) {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool { return values[order[a]] < values[order[b]] })
	ranks := make([]float64, len(values))
	for i := range ranks {
		ranks[i] = math.NaN()
	}
	for start := 0; start < len(order); {
		end := start
		for end+1 < len(order) && values[order[end+1]] == values[order[start]] {
			end++
		}
		rank := float64(start+end+2) / 2 // 第 start+1 名到第 end+1 名的平均
		for k := start; k <= end; k++ {
			ranks[order[k]] = rank
		}
		start = end + 1
	}
	return ranks
}

// firstRanks 是 pandas rank(method="first")：并列按出现顺序先到先得，NaN 保持 NaN；pct 为真时除以非 NaN 个数。
func firstRanks(values []float64, ascending, pct bool) []float64 {
	order := make([]int, 0, len(values))
	for i, v := range values {
		if !math.IsNaN(v) {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		if ascending {
			return values[order[a]] < values[order[b]]
		}
		return values[order[a]] > values[order[b]]
	})
	ranks := make([]float64, len(values))
	for i := range ranks {
		ranks[i] = math.NaN()
	}
	for position, index := range order {
		rank := float64(position + 1)
		if pct {
			rank /= float64(len(order))
		}
		ranks[index] = rank
	}
	return ranks
}

// fillMedian 用非 NaN 值的中位数补上 NaN（F1：groupby(...).transform('median') 后 fillna）；全是 NaN 时保持 NaN。
func fillMedian(values []float64) []float64 {
	valid := make([]float64, 0, len(values))
	for _, v := range values {
		if !math.IsNaN(v) {
			valid = append(valid, v)
		}
	}
	out := append([]float64(nil), values...)
	if len(valid) == 0 {
		return out
	}
	sort.Float64s(valid)
	median := valid[len(valid)/2]
	if len(valid)%2 == 0 {
		median = (valid[len(valid)/2-1] + valid[len(valid)/2]) / 2
	}
	for i, v := range out {
		if math.IsNaN(v) {
			out[i] = median
		}
	}
	return out
}

// f1Scores 按 F1-2、F1-3 计算多空两边的合成分数（在全部候选行 B 上）。cross 模式下多空因子不同时分两遍原地改写：
// 先排多头列表的列并算多头分数，再在当前值（共用的列已经是名次）上排空头列表的列并算空头分数。
func f1Scores(sample f1Sample, sub dsl.F1Sub) (long, short []float64) {
	current := make(map[string][]float64)
	column := func(name string) []float64 {
		if values, ok := current[name]; ok {
			return values
		}
		values := make([]float64, len(sample.ids))
		for i, id := range sample.ids {
			values[i] = sample.value(id, name)
		}
		current[name] = values
		return values
	}
	pass := func(factors []dsl.F1Factor) []float64 {
		if sub.CalcType == "cross" {
			ranked := make(map[string]bool)
			for _, factor := range factors {
				if ranked[factor.Column] {
					continue // 同一列表里重复的列只排一次（pandas 对 df[factor_list] 整体排名）
				}
				ranked[factor.Column] = true
				current[factor.Column] = fillMedian(averageRanks(column(factor.Column)))
			}
		}
		score := make([]float64, len(sample.ids))
		for i := range score {
			total := 0.0
			for _, factor := range factors {
				coefficient := factor.Weight
				if factor.Descending {
					coefficient = -coefficient
				}
				total += column(factor.Column)[i] * coefficient
			}
			score[i] = total
		}
		return score
	}
	if sameF1Factors(sub.LongFactors, sub.ShortFactors) {
		score := pass(sub.LongFactors)
		return score, score
	}
	long = pass(sub.LongFactors)
	short = pass(sub.ShortFactors)
	return long, short
}

func sameF1Factors(a, b []dsl.F1Factor) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// f1Condition 按 F1-8、F1-9 在样本上计算一个条件：rank / pct 在样本内排名后比较，between 的开闭与取反已在 DSL 里规范化。
func f1Condition(sample []string, values func(id string) float64, filter dsl.F1Filter) []bool {
	raw := make([]float64, len(sample))
	for i, id := range sample {
		raw[i] = values(id)
	}
	switch filter.Type {
	case dsl.F1FilterRank:
		raw = firstRanks(raw, filter.RankAscending, false)
	case dsl.F1FilterPct:
		raw = firstRanks(raw, true, true)
	}
	out := make([]bool, len(sample))
	for i, v := range raw {
		in := false
		if !math.IsNaN(v) {
			if filter.Inclusive {
				in = v >= filter.Lo && v <= filter.Hi
			} else {
				in = v > filter.Lo && v < filter.Hi
			}
		}
		out[i] = in != filter.Negate
	}
	return out
}

// f1Match 计算一个过滤项（单个条件或逻辑组合）在样本上的结果。
func f1Match(sample []string, values func(column string) func(id string) float64, filter dsl.F1Filter) []bool {
	if filter.Logic == "" {
		return f1Condition(sample, values(filter.Column), filter)
	}
	results := make([][]bool, len(filter.Items))
	for i, item := range filter.Items {
		results[i] = f1Condition(sample, values(item.Column), item)
	}
	tree := filter.LogicTree()
	out := make([]bool, len(sample))
	row := make([]bool, len(filter.Items))
	for k := range sample {
		for i := range filter.Items {
			row[i] = results[i][k]
		}
		out[k] = tree.Eval(row)
	}
	return out
}

func listFilter(ids []string, white, black []string) []string {
	blocked := make(map[string]struct{}, len(black))
	for _, id := range black {
		blocked[id] = struct{}{}
	}
	allowed := make(map[string]struct{}, len(white))
	for _, id := range white {
		allowed[id] = struct{}{}
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, no := blocked[id]; no {
			continue
		}
		if len(white) > 0 {
			if _, yes := allowed[id]; !yes {
				continue
			}
		}
		out = append(out, id)
	}
	return out
}

// f1Selection 是一个排名签名本期的选币结果（F1-4～F1-10）。
type f1Selection struct {
	picks        []F1Pick
	beforeFilter int // 被前置过滤（含名单）去掉的候选数，多空合计
}

func selectF1Group(sample f1Sample, sub dsl.F1Sub, barIndex int64) f1Selection {
	longScore, shortScore := f1Scores(sample, sub)
	position := make(map[string]int, len(sample.ids))
	for i, id := range sample.ids {
		position[id] = i
	}
	columnValues := func(column string) func(id string) float64 {
		return func(id string) float64 { return sample.value(id, column) }
	}
	longIDs := listFilter(sample.ids, sub.LongWhite, sub.LongBlack)
	shortIDs := listFilter(sample.ids, sub.ShortWhite, sub.ShortBlack)
	removed := len(sample.ids)*2 - len(longIDs) - len(shortIDs)
	for _, filter := range sub.FilterBefore {
		target := &longIDs
		if filter.Short {
			target = &shortIDs
		}
		keep := f1Match(*target, columnValues, filter)
		kept := make([]string, 0, len(*target))
		for i, id := range *target {
			if keep[i] {
				kept = append(kept, id)
			}
		}
		removed += len(*target) - len(kept)
		*target = kept
	}
	selection := f1Selection{beforeFilter: removed}
	pick := func(ids []string, scores []float64, coinNum float64, short bool) {
		values := make([]float64, len(ids))
		for i, id := range ids {
			values[i] = scores[position[id]]
		}
		pct := coinNum < 1
		ranks := firstRanks(values, !short, pct)
		var chosen []string
		for i, id := range ids {
			if !math.IsNaN(ranks[i]) && ranks[i] <= coinNum {
				chosen = append(chosen, id)
			}
		}
		denom := int(coinNum)
		if pct {
			denom = len(chosen)
		}
		for _, id := range chosen {
			closeValue := sample.value(id, "close")
			selection.picks = append(selection.picks, F1Pick{
				Bar: barIndex, ID: id, Short: short, Close: strconv.FormatFloat(closeValue, 'g', -1, 64), Denom: denom, Rows: 1,
			})
		}
	}
	if sub.LongWeight > 0 {
		pick(longIDs, longScore, sub.LongCoinNum, false)
	}
	if sub.ShortWeight > 0 {
		pick(shortIDs, shortScore, sub.ShortCoinNum, true)
	}
	return selection
}

// passAfter 计算组内每个子策略的后置过滤（F1-11）：在本期选中的样本上一次性判断，任一条件成立就剔除。
// 返回每个开仓记录的通过位图（组内子策略按 group.Subs 的顺序编号）。
func passAfter(sample f1Sample, picks []F1Pick, subs []dsl.F1Sub, members []int) []string {
	bits := make([][]byte, len(picks))
	for i := range bits {
		bits[i] = make([]byte, (len(members)+7)/8)
	}
	var longIdx, shortIdx []int
	for i, p := range picks {
		if p.Short {
			shortIdx = append(shortIdx, i)
		} else {
			longIdx = append(longIdx, i)
		}
	}
	columnValues := func(column string) func(id string) float64 {
		return func(id string) float64 { return sample.value(id, column) }
	}
	for position, subIndex := range members {
		sub := subs[subIndex]
		removed := make([]bool, len(picks))
		for _, side := range []struct {
			short bool
			index []int
		}{{false, longIdx}, {true, shortIdx}} {
			ids := make([]string, len(side.index))
			for k, i := range side.index {
				ids[k] = picks[i].ID
			}
			for _, filter := range sub.FilterAfter {
				if filter.Short != side.short || len(ids) == 0 {
					continue
				}
				match := f1Match(ids, columnValues, filter)
				for k, i := range side.index {
					if match[k] {
						removed[i] = true
					}
				}
			}
		}
		for i := range picks {
			if !removed[i] {
				bits[i][position/8] |= 1 << (position % 8)
			}
		}
	}
	out := make([]string, len(picks))
	for i := range bits {
		out[i] = hex.EncodeToString(bits[i])
	}
	return out
}

func passBit(pass string, position int) bool {
	raw, err := hex.DecodeString(pass)
	if err != nil || position/8 >= len(raw) {
		return false
	}
	return raw[position/8]&(1<<(position%8)) != 0
}

func ratFloat(v float64) *big.Rat { return new(big.Rat).SetFloat64(v) }

// evaluateF1 实现 f1 规则一期的求值（设计 7.2 第 1～7 步）。
func evaluateF1(rule *dsl.CompiledRule, frame Frame, sets ruleSets, previous RuleState, tolerance quant.Decimal) (ruleResult, error) {
	result := ruleResult{weights: map[string]quant.Decimal{}}
	explain := newExplanation(rule.Rule.ID, tolerance)
	subs := rule.Rule.F1.Subs
	expected := make(map[string]struct{}, len(sets.expected))
	for _, id := range sets.expected {
		expected[id] = struct{}{}
	}
	// 1. 候选行 B：可用集合（有行且 volume > 0），按 F1 的行顺序（symbol 升序）排列。
	candidates := append([]string(nil), sets.available...)
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := f1Key(candidates[i]), f1Key(candidates[j])
		if a != b {
			return a < b
		}
		return candidates[i] < candidates[j]
	})
	inB := make(map[string]struct{}, len(candidates))
	for _, id := range candidates {
		inB[id] = struct{}{}
	}
	sample := f1Sample{ids: candidates, frame: frame}
	result.scored = len(candidates)

	previousGroups := make(map[string]F1GroupState)
	if previous.F1 != nil {
		for _, group := range previous.F1.Groups {
			previousGroups[group.Key] = group
		}
	}
	totalStg := new(big.Rat)
	for _, sub := range subs {
		totalStg.Add(totalStg, ratFloat(sub.StgWeight))
	}
	budget := new(big.Rat).Quo(rule.Rule.Weight.Total.Rat(), totalStg)
	contributions := make(map[string]*big.Rat)
	holders := make(map[string]map[int]struct{}) // 标的 → 持有它的子策略
	newlyPicked := make(map[string]bool)         // 本期选中的标的 → 是否至少一个子策略通过后置过滤
	state := &F1State{}
	for _, group := range rule.F1Groups {
		representative := subs[group.Subs[0]]
		maxHold := 0
		for _, index := range group.Subs {
			maxHold = max(maxHold, subs[index].HoldPeriod)
		}
		// 2～5. 排名、候选、前置过滤、选币（签名相同的子策略只算一次）。
		selection := selectF1Group(sample, representative, frame.BarIndex)
		result.filtered += selection.beforeFilter
		// 6. 后置过滤：每个子策略在本期选中的样本上判断。
		passes := passAfter(sample, selection.picks, subs, group.Subs)
		for i := range selection.picks {
			selection.picks[i].Pass = passes[i]
			passed := false
			for position := range group.Subs {
				if passBit(passes[i], position) {
					passed = true
					break
				}
			}
			newlyPicked[selection.picks[i].ID] = newlyPicked[selection.picks[i].ID] || passed
		}
		// 7.1 计数：旧记录的币本期有候选行就加 1；不在预期集合里的删除。
		picks := make([]F1Pick, 0)
		for _, old := range previousGroups[group.StateKey].Picks {
			if _, ok := expected[old.ID]; !ok {
				continue
			}
			if _, ok := inB[old.ID]; ok {
				old.Rows++
			}
			picks = append(picks, old)
		}
		picks = append(picks, selection.picks...)
		// 7.2 贡献：Rows ≤ h 且本期有候选行。
		for _, p := range picks {
			if _, ok := inB[p.ID]; !ok {
				continue
			}
			openClose, err := strconv.ParseFloat(p.Close, 64)
			if err != nil || openClose <= 0 || p.Denom <= 0 {
				return ruleResult{}, fmt.Errorf("开仓记录 %s@%d 的 close 或分母无效", p.ID, p.Bar)
			}
			nowClose := frame.Rows[p.ID].Values["close"]
			for position, index := range group.Subs {
				sub := subs[index]
				if p.Rows > sub.HoldPeriod || !passBit(p.Pass, position) {
					continue
				}
				sideWeight := sub.LongWeight
				if p.Short {
					sideWeight = sub.ShortWeight
				}
				// 数量 = 0.5 × side_weight / h / 分母 / 开仓 close；贡献 = 数量 × 本期 close × stg_weight。
				amount := new(big.Rat).Mul(ratFloat(0.5*sideWeight), ratFloat(nowClose))
				amount.Mul(amount, ratFloat(sub.StgWeight))
				amount.Quo(amount, new(big.Rat).SetInt64(int64(sub.HoldPeriod)*int64(p.Denom)))
				amount.Quo(amount, ratFloat(openClose))
				if p.Short {
					amount.Neg(amount)
				}
				if contributions[p.ID] == nil {
					contributions[p.ID] = new(big.Rat)
					holders[p.ID] = make(map[int]struct{})
				}
				contributions[p.ID].Add(contributions[p.ID], amount)
				holders[p.ID][index] = struct{}{}
			}
		}
		// 7.4 清理：Rows ≥ 组内最大 h 的记录不再有贡献。
		kept := picks[:0]
		for _, p := range picks {
			if p.Rows < maxHold {
				kept = append(kept, p)
			}
		}
		sort.Slice(kept, func(i, j int) bool {
			if kept[i].Bar != kept[j].Bar {
				return kept[i].Bar < kept[j].Bar
			}
			if kept[i].ID != kept[j].ID {
				return kept[i].ID < kept[j].ID
			}
			return !kept[i].Short && kept[j].Short
		})
		state.Groups = append(state.Groups, F1GroupState{Key: group.StateKey, Picks: append([]F1Pick(nil), kept...)})
	}
	// 7.3 合成：Σ 子策略 stg_weight × 贡献 / Σ stg_weight × weight.total。
	ids := make([]string, 0, len(contributions))
	for id := range contributions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	held := make([]string, 0, len(ids))
	for _, id := range ids {
		weight := quant.FromRat(new(big.Rat).Mul(contributions[id], budget))
		if weight.IsZero() {
			continue
		}
		result.weights[id] = weight
		held = append(held, id)
		explain.weight(id, weight, nil, 0, true)
		item := explain.items[id]
		item.Reason = f1ReasonPrefix + strconv.Itoa(len(holders[id]))
		explain.items[id] = item
	}
	for id, passed := range newlyPicked {
		if _, weighted := result.weights[id]; !weighted && !passed {
			explain.reject(id, StageDropped, reasonF1FilterAfter, nil, 0)
		}
	}
	result.selected = len(newlyPicked)
	result.state = RuleState{Held: held, F1: state}
	// 开仓记录只在该币累计满 h 个候选行后删除：长期没有候选行的币会让记录一直保留（截断会改变结果，所以只提示）。
	if raw, err := json.Marshal(state); err == nil && len(raw) > f1StateNoteBytes {
		result.notes = append(result.notes, fmt.Sprintf("规则 %s 的 f1 状态有 %d 字节，超过 %d 字节：有标的长期没有候选行，开仓记录一直保留", rule.Rule.ID, len(raw), f1StateNoteBytes))
	}
	result.items = explain.list()
	return result, nil
}
