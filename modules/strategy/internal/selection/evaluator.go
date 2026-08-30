package selection

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

type TargetWeight struct {
	InstrumentID string `json:"instrument_id"`
	TargetWeight string `json:"target_weight"`
}

type DebugInfo struct {
	PoolSize         int              `json:"pool_size"`
	PreCount         map[string]int   `json:"pre_count"`
	SelectedCount    map[string]int   `json:"selected_count"`
	PostCount        map[string]int   `json:"post_count"`
	ScoreRanks       map[string][]int `json:"score_ranks"`
	LongInstruments  []string         `json:"long_instruments"`
	ShortInstruments []string         `json:"short_instruments"`
	Gross            string           `json:"gross"`
	Net              string           `json:"net"`
}

type Evaluation struct {
	Targets []TargetWeight `json:"targets"`
	Debug   DebugInfo      `json:"debug_info"`
}

type scoredItem struct {
	item  input.InstrumentInput
	score quant.Decimal
}

// Evaluate applies the intentionally small built-in coin-selection language.
// It is stateless: all values are supplied in EvaluationInput and all output
// ordering is made deterministic by instrument ID.
func Evaluate(manifest config.Manifest, evaluationInput input.EvaluationInput) (Evaluation, error) {
	cloneSides(&manifest)
	normalizeSideWeights(&manifest)
	rows := evaluationInput.Ordered()
	result := Evaluation{Debug: DebugInfo{
		PoolSize:      len(rows),
		PreCount:      map[string]int{},
		SelectedCount: map[string]int{},
		PostCount:     map[string]int{},
		ScoreRanks:    map[string][]int{},
	}}

	long, err := evaluateSide("long", manifest.Long, rows, &result.Debug)
	if err != nil {
		return Evaluation{}, err
	}
	short, err := evaluateSide("short", manifest.Short, rows, &result.Debug)
	if err != nil {
		return Evaluation{}, err
	}

	weights := make(map[string]quant.Decimal, len(long)+len(short))
	for _, target := range long {
		weights[target.item.InstrumentID] = weights[target.item.InstrumentID].Add(target.weight)
	}
	for _, target := range short {
		weights[target.item.InstrumentID] = weights[target.item.InstrumentID].Add(target.weight.Neg())
	}
	ids := make([]string, 0, len(weights))
	for id, weight := range weights {
		if !weight.IsZero() {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		result.Targets = append(result.Targets, TargetWeight{InstrumentID: id, TargetWeight: weights[id].String()})
	}
	result.Debug.Gross = gross(long, short).String()
	result.Debug.Net = sum(weights).String()
	return result, nil
}

func cloneSides(manifest *config.Manifest) {
	if manifest == nil {
		return
	}
	if manifest.Long != nil {
		clone := *manifest.Long
		clone.Scores = append([]config.ScoreRule(nil), manifest.Long.Scores...)
		clone.Filters = append([]config.FilterRule(nil), manifest.Long.Filters...)
		manifest.Long = &clone
	}
	if manifest.Short != nil {
		clone := *manifest.Short
		clone.Scores = append([]config.ScoreRule(nil), manifest.Short.Scores...)
		clone.Filters = append([]config.FilterRule(nil), manifest.Short.Filters...)
		manifest.Short = &clone
	}
}

func normalizeSideWeights(manifest *config.Manifest) {
	if manifest == nil {
		return
	}
	total := quant.Zero()
	if manifest.Long != nil {
		if value, err := quant.Parse(manifest.Long.SideWeight); err == nil {
			total = total.Add(value)
		}
	}
	if manifest.Short != nil {
		if value, err := quant.Parse(manifest.Short.SideWeight); err == nil {
			total = total.Add(value)
		}
	}
	if total.IsZero() {
		return
	}
	if manifest.Long != nil {
		if value, err := quant.Parse(manifest.Long.SideWeight); err == nil {
			manifest.Long.SideWeight = value.Div(total).String()
		}
	}
	if manifest.Short != nil {
		if value, err := quant.Parse(manifest.Short.SideWeight); err == nil {
			manifest.Short.SideWeight = value.Div(total).String()
		}
	}
}

type sideTarget struct {
	item   input.InstrumentInput
	weight quant.Decimal
}

func evaluateSide(name string, side *config.Side, rows []input.InstrumentInput, debug *DebugInfo) ([]sideTarget, error) {
	if side == nil {
		return nil, nil
	}
	sideWeight, err := quant.Parse(side.SideWeight)
	if err != nil || sideWeight.IsZero() {
		return nil, nil
	}
	fullRanks := rankFactors(rows, side.Scores)
	for key, ranks := range fullRanks {
		factorID := strings.SplitN(key, "\x00", 2)[0]
		debug.ScoreRanks[factorID] = ranks
	}

	candidates := make([]input.InstrumentInput, 0, len(rows))
	for _, row := range rows {
		if name == "short" && strings.EqualFold(row.Market, "spot") {
			continue
		}
		if hasScoreValues(row, side.Scores) && hasFilterValues(row, side.Filters) {
			candidates = append(candidates, row)
		}
	}
	pre, err := filterRows(candidates, rows, side.Filters, "pre")
	if err != nil {
		return nil, fmt.Errorf("%s pre-filter: %w", name, err)
	}
	debug.PreCount[name] = len(pre)

	scored := scoreRows(pre, side.Scores, fullRanks, rows)
	selected, err := selectRows(scored, side.Selection)
	if err != nil {
		return nil, fmt.Errorf("%s selection: %w", name, err)
	}
	debug.SelectedCount[name] = len(selected)
	selectedRows := make([]input.InstrumentInput, len(selected))
	for i := range selected {
		selectedRows[i] = selected[i].item
	}
	post, err := filterRows(selectedRows, rows, side.Filters, "post")
	if err != nil {
		return nil, fmt.Errorf("%s post-filter: %w", name, err)
	}
	debug.PostCount[name] = len(post)
	postSet := make(map[string]struct{}, len(post))
	for _, row := range post {
		postSet[row.InstrumentID] = struct{}{}
	}
	selected = selected[:0]
	for _, candidate := range scored {
		if _, ok := postSet[candidate.item.InstrumentID]; ok {
			selected = append(selected, candidate)
		}
	}
	selectedIDs := make([]string, 0, len(selected))
	for _, candidate := range selected {
		selectedIDs = append(selectedIDs, candidate.item.InstrumentID)
	}
	sort.Strings(selectedIDs)
	if name == "long" {
		debug.LongInstruments = selectedIDs
	} else {
		debug.ShortInstruments = selectedIDs
	}

	weights := quant.DivideStable(sideWeight, selectedIDs)
	targets := make([]sideTarget, 0, len(selected))
	for _, id := range selectedIDs {
		item := findRow(selected, id)
		targets = append(targets, sideTarget{item: item, weight: weights[id]})
	}
	return targets, nil
}

func rankFactors(rows []input.InstrumentInput, scores []config.ScoreRule) map[string][]int {
	result := make(map[string][]int, len(scores))
	ordered := append([]input.InstrumentInput(nil), rows...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].InstrumentID < ordered[j].InstrumentID })
	for _, score := range scores {
		eligible := make([]input.InstrumentInput, 0, len(ordered))
		for _, row := range ordered {
			if _, ok := row.Values[score.FactorID]; ok {
				eligible = append(eligible, row)
			}
		}
		sort.SliceStable(eligible, func(i, j int) bool {
			cmp := eligible[i].Values[score.FactorID].Cmp(eligible[j].Values[score.FactorID])
			if score.Direction == "descending" {
				cmp = -cmp
			}
			if cmp != 0 {
				return cmp < 0
			}
			return eligible[i].InstrumentID < eligible[j].InstrumentID
		})
		ranks := make([]int, 0, len(ordered))
		for _, row := range ordered {
			value, ok := row.Values[score.FactorID]
			if !ok {
				ranks = append(ranks, 0)
				continue
			}
			rank := 0
			for i, candidate := range eligible {
				if candidate.Values[score.FactorID].Cmp(value) == 0 {
					rank = i + 1
					break
				}
			}
			ranks = append(ranks, rank)
		}
		result[rankKey(score)] = ranks
	}
	return result
}

func scoreRows(rows []input.InstrumentInput, rules []config.ScoreRule, ranks map[string][]int, fullRows []input.InstrumentInput) []scoredItem {
	ordered := append([]input.InstrumentInput(nil), rows...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].InstrumentID < ordered[j].InstrumentID })
	weightTotal := quant.Zero()
	parsedWeights := make([]quant.Decimal, len(rules))
	for i, rule := range rules {
		parsedWeights[i], _ = quant.Parse(rule.Weight)
		weightTotal = weightTotal.Add(parsedWeights[i])
	}
	// rankFactors is keyed by the deterministic full-row order. Build an index
	// once so pre-filtering cannot change rank scope.
	fullIndex := make(map[string]int, len(fullRows))
	for i, row := range append([]input.InstrumentInput(nil), fullRows...) {
		fullIndex[row.InstrumentID] = i
	}
	result := make([]scoredItem, 0, len(ordered))
	for _, row := range ordered {
		score := quant.Zero()
		for i, rule := range rules {
			rankList := ranks[rankKey(rule)]
			idx, ok := fullIndex[row.InstrumentID]
			if !ok || idx >= len(rankList) || rankList[idx] == 0 {
				continue
			}
			rank := quant.Must(strconv.Itoa(rankList[idx])).Div(quant.Must(strconv.Itoa(len(fullRows))))
			score = score.Add(rank.Mul(parsedWeights[i].Div(weightTotal)))
		}
		result = append(result, scoredItem{item: row, score: score})
	}
	sort.SliceStable(result, func(i, j int) bool {
		cmp := result[i].score.Cmp(result[j].score)
		if cmp != 0 {
			return cmp < 0
		}
		return result[i].item.InstrumentID < result[j].item.InstrumentID
	})
	return result
}

func rankKey(rule config.ScoreRule) string { return rule.FactorID + "\x00" + rule.Direction }

func selectRows(rows []scoredItem, rule config.SelectionRule) ([]scoredItem, error) {
	count, err := selectionCount(len(rows), rule)
	if err != nil {
		return nil, err
	}
	if count > len(rows) {
		count = len(rows)
	}
	return append([]scoredItem(nil), rows[:count]...), nil
}

func selectionCount(size int, rule config.SelectionRule) (int, error) {
	value := decimalRuleValue(rule.Value)
	if value == "" {
		return 0, errors.New("selection value is invalid")
	}
	if rule.Mode == "count" {
		count, err := strconv.Atoi(value)
		if err != nil || count <= 0 {
			return 0, errors.New("selection count is invalid")
		}
		return count, nil
	}
	rat, ok := new(big.Rat).SetString(value)
	if !ok || rat.Sign() <= 0 {
		return 0, errors.New("selection fraction is invalid")
	}
	countRat := new(big.Rat).Mul(rat, new(big.Rat).SetInt64(int64(size)))
	count := new(big.Int).Quo(countRat.Num(), countRat.Denom())
	if count.Sign() == 0 && size > 0 {
		return 1, nil
	}
	return int(count.Int64()), nil
}

func filterRows(candidates, fullRows []input.InstrumentInput, rules []config.FilterRule, phase string) ([]input.InstrumentInput, error) {
	filtered := append([]input.InstrumentInput(nil), candidates...)
	for _, rule := range rules {
		if rule.Phase != phase {
			continue
		}
		threshold, err := parseRuleDecimal(rule.Value)
		if err != nil {
			return nil, err
		}
		percentiles := map[string]quant.Decimal{}
		if rule.ValueType == "percentile" {
			percentiles = percentileRanks(fullRows, rule.FactorID)
		}
		next := filtered[:0]
		for _, row := range filtered {
			value, ok := row.Values[rule.FactorID]
			if !ok {
				continue
			}
			if rule.ValueType == "percentile" {
				value = percentiles[row.InstrumentID]
			}
			if compare(value, threshold, rule.Op) {
				next = append(next, row)
			}
		}
		filtered = next
	}
	return filtered, nil
}

func percentileRanks(rows []input.InstrumentInput, factorID string) map[string]quant.Decimal {
	eligible := make([]input.InstrumentInput, 0, len(rows))
	for _, row := range rows {
		if _, ok := row.Values[factorID]; ok {
			eligible = append(eligible, row)
		}
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		cmp := eligible[i].Values[factorID].Cmp(eligible[j].Values[factorID])
		if cmp != 0 {
			return cmp < 0
		}
		return eligible[i].InstrumentID < eligible[j].InstrumentID
	})
	result := make(map[string]quant.Decimal, len(eligible))
	for i, row := range eligible {
		rank := i + 1
		for j := i; j >= 0; j-- {
			if eligible[j].Values[factorID].Cmp(row.Values[factorID]) != 0 {
				break
			}
			rank = j + 1
		}
		result[row.InstrumentID] = quant.Must(strconv.Itoa(rank)).Div(quant.Must(strconv.Itoa(len(eligible))))
	}
	return result
}

func compare(left, right quant.Decimal, op string) bool {
	cmp := left.Cmp(right)
	switch op {
	case "lt":
		return cmp < 0
	case "lte":
		return cmp <= 0
	case "gt":
		return cmp > 0
	case "gte":
		return cmp >= 0
	case "eq":
		return cmp == 0
	default:
		return false
	}
}

func hasScoreValues(row input.InstrumentInput, scores []config.ScoreRule) bool {
	for _, rule := range scores {
		if _, ok := row.Values[rule.FactorID]; !ok {
			return false
		}
	}
	return true
}

func hasFilterValues(row input.InstrumentInput, filters []config.FilterRule) bool {
	for _, rule := range filters {
		if _, ok := row.Values[rule.FactorID]; !ok {
			return false
		}
	}
	return true
}

func findRow(rows []scoredItem, id string) input.InstrumentInput {
	for _, row := range rows {
		if row.item.InstrumentID == id {
			return row.item
		}
	}
	return input.InstrumentInput{PoolItem: input.PoolItem{InstrumentID: id}}
}

func gross(long, short []sideTarget) quant.Decimal {
	total := quant.Zero()
	for _, target := range long {
		total = total.Add(target.weight)
	}
	for _, target := range short {
		total = total.Add(target.weight)
	}
	return total
}

func sum(values map[string]quant.Decimal) quant.Decimal {
	total := quant.Zero()
	for _, value := range values {
		total = total.Add(value)
	}
	return total
}

func decimalRuleValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}

func parseRuleDecimal(value any) (quant.Decimal, error) {
	parsed, err := quant.Parse(decimalRuleValue(value))
	if err != nil {
		return quant.Zero(), err
	}
	return parsed, nil
}
