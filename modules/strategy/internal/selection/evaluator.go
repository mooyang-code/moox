package selection

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	"github.com/mooyang-code/moox/packages/report"
)

// Definition is the evaluator-facing form of the validated strategy DSL.
// The compiler is responsible for resolving pool UDFs and input bindings;
// this package only evaluates the resulting rows.
type Definition struct {
	Rules map[string]Rule
}

type Rule struct {
	Pool []string
	// PoolSet distinguishes an explicitly empty pool (which must produce no
	// targets) from the zero value used by legacy in-memory callers to mean
	// "all loaded rows".
	PoolSet      bool
	FilterBefore string
	Score        string
	Select       Select
	Weight       string
	WeightEach   string
	Side         string
	FilterAfter  string
	Signals      *Signals
	Holding      *Holding
}

type Select struct {
	Where string
	Top   int
	Tail  int
}

type Signals struct {
	Entry string
	Exit  string
}

type Holding struct {
	Bars    int
	Offsets []int
}

// EvaluationInput is useful to callers that already have a period snapshot
// and need to provide the previous bar for bars[-1]. The trigger package can
// use the adapter for input.EvaluationInput directly.
type EvaluationInput struct {
	Rows        []Row
	PeriodEnd   time.Time
	BarIndex    int64
	BarDuration time.Duration
	BarEndAt    func(int) time.Time
}

type Row struct {
	InstrumentID   string
	Market         string
	Values         map[string]quant.Decimal
	PreviousValues map[string]quant.Decimal
}

type TargetWeight struct {
	InstrumentID string `json:"instrument_id"`
	TargetWeight string `json:"target_weight"`
}

type DebugInfo struct {
	PoolSize         int                          `json:"pool_size"`
	PreCount         map[string]int               `json:"pre_count"`
	SelectedCount    map[string]int               `json:"selected_count"`
	PostCount        map[string]int               `json:"post_count"`
	Scores           map[string]map[string]string `json:"scores,omitempty"`
	LongInstruments  []string                     `json:"long_instruments,omitempty"`
	ShortInstruments []string                     `json:"short_instruments,omitempty"`
	Gross            string                       `json:"gross,omitempty"`
	Net              string                       `json:"net,omitempty"`
}

type Evaluation struct {
	Targets    []TargetWeight              `json:"targets"`
	RuleStates map[string]domain.RuleState `json:"rule_states"`
	Debug      DebugInfo                   `json:"debug_info"`
}

type row struct {
	id       string
	market   string
	values   map[string]quant.Decimal
	previous map[string]quant.Decimal
}

type scoredRow struct {
	row
	score float64
}

var (
	normalizerCall = regexp.MustCompile(`\b(pct_rank|zscore)\s*\(\s*([A-Za-z_][A-Za-z0-9_]*)\s*\)`)
	barIndexRef    = regexp.MustCompile(`bars\[(-?[0-9]+)\]`)
	barFieldRef    = regexp.MustCompile(`bars\[(-?[0-9]+)\]\.([A-Za-z_][A-Za-z0-9_]*)`)
	quotedTextRef  = regexp.MustCompile(`"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'`)
	identifierRef  = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\b`)
)

// Evaluate computes one complete set of target weights. The definition and
// input arguments are intentionally interface-typed so the trigger can pass
// either this package's compact adapter or input.EvaluationInput while the
// compiler evolves independently. previous is omitted for stateless rules.
func Evaluate(definition any, rawInput any, previous ...map[string]domain.RuleState) (Evaluation, error) {
	definitionValue, err := asDefinition(definition)
	if err != nil {
		return Evaluation{}, err
	}
	rows, period, barIndex, barDuration, barEndAt, err := adaptInput(rawInput)
	if err != nil {
		return Evaluation{}, err
	}
	var oldStates map[string]domain.RuleState
	if len(previous) > 0 && previous[0] != nil {
		oldStates = previous[0]
	}
	result := Evaluation{RuleStates: map[string]domain.RuleState{}, Debug: DebugInfo{
		PoolSize: len(rows), PreCount: map[string]int{}, SelectedCount: map[string]int{},
		PostCount: map[string]int{}, Scores: map[string]map[string]string{},
	}}

	contributions := map[string]quant.Decimal{}
	absoluteTotal := quant.Zero()
	ruleNames := make([]string, 0, len(definitionValue.Rules))
	for name := range definitionValue.Rules {
		ruleNames = append(ruleNames, name)
	}
	sort.Strings(ruleNames)
	for _, name := range ruleNames {
		rule := definitionValue.Rules[name]
		selected, nextState, debug, err := evaluateRule(name, rule, rows, period, barIndex, barDuration, barEndAt, oldStates[name])
		if err != nil {
			return Evaluation{}, fmt.Errorf("rule %q: %w", name, err)
		}
		mergeDebug(&result.Debug, debug)
		nextState = NormalizeRuleState(nextState)
		if !nextState.Empty() {
			result.RuleStates[name] = nextState
		}
		for id, weight := range selected {
			contributions[id] = contributions[id].Add(weight)
			absoluteTotal = absoluteTotal.Add(abs(weight))
		}
	}
	if absoluteTotal.Cmp(quant.One()) > 0 {
		return Evaluation{}, fmt.Errorf("rule target weights exceed 1: %s", absoluteTotal.String())
	}
	ids := make([]string, 0, len(contributions))
	for id, weight := range contributions {
		if !weight.IsZero() {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		result.Targets = append(result.Targets, TargetWeight{InstrumentID: id, TargetWeight: contributions[id].String()})
	}
	result.Debug.Net = sumWeights(contributions).String()
	return result, nil
}

func asDefinition(value any) (Definition, error) {
	switch typed := value.(type) {
	case Definition:
		return typed, nil
	case *Definition:
		if typed == nil {
			return Definition{}, errors.New("strategy definition is required")
		}
		return *typed, nil
	case config.DSL:
		return definitionFromDSL(typed), nil
	case *config.DSL:
		if typed == nil {
			return Definition{}, errors.New("strategy definition is required")
		}
		return definitionFromDSL(*typed), nil
	default:
		return Definition{}, errors.New("selection evaluator requires a compiled selection definition")
	}
}

func definitionFromDSL(dsl config.DSL) Definition {
	definition := Definition{Rules: make(map[string]Rule, len(dsl.Rules))}
	for name, source := range dsl.Rules {
		rule := Rule{
			PoolSet:      source.Pool.Fixed != nil || source.Pool.UDF != nil,
			FilterBefore: source.FilterBefore,
			Score:        source.Score,
			Weight:       source.Weight,
			WeightEach:   source.WeightEach,
			Side:         source.Side,
			FilterAfter:  source.FilterAfter,
		}
		if source.Pool.Fixed != nil {
			rule.Pool = append([]string(nil), source.Pool.Fixed...)
		}
		if source.Select != nil {
			rule.Select = Select{Where: source.Select.Where}
			if source.Select.Top != nil {
				rule.Select.Top = *source.Select.Top
			}
			if source.Select.Tail != nil {
				rule.Select.Tail = *source.Select.Tail
			}
		}
		if source.Signals != nil {
			rule.Signals = &Signals{Entry: source.Signals.Entry, Exit: source.Signals.Exit}
		}
		if source.Holding != nil {
			rule.Holding = &Holding{Bars: source.Holding.Bars, Offsets: append([]int(nil), source.Holding.Offsets...)}
		}
		definition.Rules[name] = rule
	}
	return definition
}

func evaluateRule(name string, rule Rule, rows []row, period time.Time, barIndex int64, barDuration time.Duration, barEndAt func(int) time.Time, previous domain.RuleState) (map[string]quant.Decimal, domain.RuleState, DebugInfo, error) {
	if strings.EqualFold(rule.Side, "short") {
		for _, candidate := range filterPool(rows, rule.Pool, rule.PoolSet) {
			if strings.EqualFold(candidate.market, "spot") {
				return nil, domain.RuleState{}, DebugInfo{}, errors.New("short rule cannot select spot instruments")
			}
		}
	}
	if rule.Signals != nil && rule.Holding != nil {
		return nil, domain.RuleState{}, DebugInfo{}, errors.New("signals and holding cannot be combined")
	}
	if rule.Weight != "" && rule.WeightEach != "" {
		return nil, domain.RuleState{}, DebugInfo{}, errors.New("weight and weight_each are mutually exclusive")
	}
	if rule.Holding != nil && rule.WeightEach != "" {
		return nil, domain.RuleState{}, DebugInfo{}, errors.New("holding requires weight, not weight_each")
	}
	if rule.Weight == "" && rule.WeightEach == "" {
		return nil, domain.RuleState{}, DebugInfo{}, errors.New("rule weight or weight_each is required")
	}
	if rule.Signals != nil && (strings.TrimSpace(rule.Signals.Entry) == "" || strings.TrimSpace(rule.Signals.Exit) == "") {
		return nil, domain.RuleState{}, DebugInfo{}, errors.New("signals requires entry and exit")
	}
	candidates := filterPool(rows, rule.Pool, rule.PoolSet)
	// A Factor binding may intentionally cover only part of a rule's literal
	// pool. Rows without that scoped value are not candidates for this rule;
	// filtering them here avoids treating a binding scope as a global
	// completeness failure while preserving strict checks for fields that are
	// required by every admitted rule row.
	candidates = filterRowsByAvailableFields(candidates, rule)
	debug := DebugInfo{PreCount: map[string]int{}, SelectedCount: map[string]int{}, PostCount: map[string]int{}, Scores: map[string]map[string]string{}}
	if rule.FilterBefore != "" {
		program, err := compileBoolean(rule.FilterBefore, candidates, false)
		if err != nil {
			return nil, domain.RuleState{}, DebugInfo{}, fmt.Errorf("filter_before: %w", err)
		}
		filtered := make([]row, 0, len(candidates))
		for _, candidate := range candidates {
			ok, err := runBoolean(program, candidate, 0)
			if err != nil {
				return nil, domain.RuleState{}, DebugInfo{}, fmt.Errorf("filter_before on %s: %w", candidate.id, err)
			}
			if ok {
				filtered = append(filtered, candidate)
			}
		}
		candidates = filtered
	}
	debug.PreCount[name] = len(candidates)

	scored, scores, err := scoreRows(candidates, rule.Score)
	if err != nil {
		return nil, domain.RuleState{}, DebugInfo{}, err
	}
	debug.Scores[name] = scores
	selected, err := selectRows(scored, rule.Select, rule.Score != "")
	if err != nil {
		return nil, domain.RuleState{}, DebugInfo{}, err
	}
	debug.SelectedCount[name] = len(selected)

	var nextState domain.RuleState
	if rule.Signals != nil {
		selected, nextState, err = applySignals(selected, candidates, rows, rule, previous, period)
		if err != nil {
			return nil, domain.RuleState{}, DebugInfo{}, err
		}
	}
	if rule.Holding != nil {
		selected, nextState, err = applyHolding(selected, rule, previous, period, barIndex, barDuration, barEndAt, rule.FilterAfter)
		if err != nil {
			return nil, domain.RuleState{}, DebugInfo{}, err
		}
	}

	var weights map[string]quant.Decimal
	if rule.Holding != nil {
		weights, err = holdingWeights(nextState, rule.Weight, len(rule.Holding.Offsets))
	} else {
		weights, err = assignWeights(selected, rule)
	}
	if err != nil {
		return nil, domain.RuleState{}, DebugInfo{}, err
	}
	if rule.FilterAfter != "" && len(selected) > 0 && rule.Holding == nil {
		program, err := compileBoolean(rule.FilterAfter, rows, rule.Signals == nil)
		if err != nil {
			return nil, domain.RuleState{}, DebugInfo{}, fmt.Errorf("filter_after: %w", err)
		}
		for _, item := range selected {
			ok, err := runBoolean(program, item.row, item.score)
			if err != nil {
				return nil, domain.RuleState{}, DebugInfo{}, fmt.Errorf("filter_after on %s: %w", item.id, err)
			}
			if !ok {
				delete(weights, item.id)
			}
		}
		if rule.Signals != nil {
			nextState = removeSignalState(nextState, weights)
		}
		if rule.Holding != nil {
			nextState = removeHoldingWeights(nextState, weights)
		}
	}
	if strings.EqualFold(rule.Side, "short") {
		for id, weight := range weights {
			weights[id] = weight.Neg()
		}
	}
	debug.PostCount[name] = len(weights)
	debug.LongInstruments, debug.ShortInstruments = instrumentIDs(weights, strings.EqualFold(rule.Side, "short"))
	debug.Gross = sumAbsolute(weights).String()
	return weights, nextState, debug, nil
}

type expressionField struct {
	name string
	prev bool
}

func filterRowsByAvailableFields(rows []row, rule Rule) []row {
	fields := make([]expressionField, 0)
	for _, expression := range []string{rule.FilterBefore, rule.Score, rule.Select.Where, rule.FilterAfter} {
		fields = append(fields, expressionFields(expression)...)
	}
	if rule.Signals != nil {
		fields = append(fields, expressionFields(rule.Signals.Entry)...)
		fields = append(fields, expressionFields(rule.Signals.Exit)...)
	}
	if len(fields) == 0 {
		return rows
	}
	// Only scope fields that occur in at least one loaded row. A field absent
	// from every row remains a strict compiler/readiness error, not an implicit
	// empty candidate set.
	available := map[string]bool{}
	for _, field := range fields {
		for _, item := range rows {
			values := item.values
			if field.prev {
				values = item.previous
			}
			if _, ok := values[field.name]; ok {
				available[field.name+fmt.Sprint("\x00", field.prev)] = true
			}
		}
	}
	result := make([]row, 0, len(rows))
	for _, item := range rows {
		eligible := true
		for _, field := range fields {
			if !available[field.name+fmt.Sprint("\x00", field.prev)] {
				continue
			}
			values := item.values
			if field.prev {
				values = item.previous
			}
			if _, ok := values[field.name]; !ok {
				eligible = false
				break
			}
		}
		if eligible {
			result = append(result, item)
		}
	}
	return result
}

func expressionFields(expression string) []expressionField {
	if strings.TrimSpace(expression) == "" {
		return nil
	}
	clean := quotedTextRef.ReplaceAllString(expression, " ")
	fields := make([]expressionField, 0)
	seen := map[string]struct{}{}
	for _, match := range barFieldRef.FindAllStringSubmatch(clean, -1) {
		key := match[2] + "\x00" + match[1]
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		fields = append(fields, expressionField{name: match[2], prev: match[1] == "-1"})
	}
	for _, match := range identifierRef.FindAllStringIndex(clean, -1) {
		name := clean[match[0]:match[1]]
		if name == "bars" || name == "score" || name == "true" || name == "false" || name == "and" || name == "or" || name == "not" {
			continue
		}
		if match[0] > 0 && clean[match[0]-1] == '.' {
			continue
		}
		rest := strings.TrimSpace(clean[match[1]:])
		if strings.HasPrefix(rest, "(") {
			continue
		}
		key := name + "\x00false"
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		fields = append(fields, expressionField{name: name})
	}
	return fields
}

func scoreRows(rows []row, expression string) ([]scoredRow, map[string]string, error) {
	ordered := append([]row(nil), rows...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].id < ordered[j].id })
	if len(ordered) == 0 {
		return []scoredRow{}, map[string]string{}, nil
	}
	if strings.TrimSpace(expression) == "" {
		result := make([]scoredRow, len(ordered))
		for i, item := range ordered {
			result[i] = scoredRow{row: item}
		}
		return result, map[string]string{}, nil
	}
	normalized, err := normalizerValues(expression, ordered)
	if err != nil {
		return nil, nil, err
	}
	program, err := compileNumeric(expression, ordered, normalized)
	if err != nil {
		return nil, nil, fmt.Errorf("score: %w", err)
	}
	result := make([]scoredRow, 0, len(ordered))
	diagnostics := make(map[string]string, len(ordered))
	for _, item := range ordered {
		rowNormalized := make(map[string]float64, len(normalized))
		for factor, values := range normalized {
			rowNormalized[factor] = values[item.id]
		}
		value, err := runNumeric(program, item, rowNormalized, 0)
		if err != nil {
			return nil, nil, fmt.Errorf("score on %s: %w", item.id, err)
		}
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, nil, fmt.Errorf("score on %s is not finite", item.id)
		}
		result = append(result, scoredRow{row: item, score: value})
		diagnostics[item.id] = strconv.FormatFloat(value, 'f', -1, 64)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].score != result[j].score {
			return result[i].score > result[j].score
		}
		return result[i].id < result[j].id
	})
	return result, diagnostics, nil
}

func selectRows(rows []scoredRow, selectRule Select, hasScore bool) ([]scoredRow, error) {
	if selectRule.Where != "" && !hasScore {
		return nil, errors.New("select.where requires score")
	}
	if (selectRule.Top > 0 || selectRule.Tail > 0) && !hasScore {
		return nil, errors.New("select top/tail requires score")
	}
	if selectRule.Top > 0 && selectRule.Tail > 0 {
		return nil, errors.New("select top and tail are mutually exclusive")
	}
	selected := append([]scoredRow(nil), rows...)
	if selectRule.Where != "" {
		program, err := compileBoolean(selectRule.Where, selectedRows(selected), true)
		if err != nil {
			return nil, fmt.Errorf("select.where: %w", err)
		}
		filtered := selected[:0]
		for _, item := range selected {
			ok, err := runBoolean(program, item.row, item.score)
			if err != nil {
				return nil, fmt.Errorf("select.where on %s: %w", item.id, err)
			}
			if ok {
				filtered = append(filtered, item)
			}
		}
		selected = filtered
	}
	if selectRule.Top > 0 {
		if selectRule.Top < len(selected) {
			selected = selected[:selectRule.Top]
		}
	}
	if selectRule.Tail > 0 {
		if selectRule.Tail < len(selected) {
			selected = selected[len(selected)-selectRule.Tail:]
		}
	}
	return selected, nil
}

func assignWeights(rows []scoredRow, rule Rule) (map[string]quant.Decimal, error) {
	ids := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, item := range rows {
		if _, exists := seen[item.id]; !exists {
			seen[item.id] = struct{}{}
			ids = append(ids, item.id)
		}
	}
	sort.Strings(ids)
	weights := make(map[string]quant.Decimal, len(ids))
	if len(ids) == 0 {
		return weights, nil
	}
	if rule.Weight != "" {
		total, err := quant.Parse(strings.TrimSpace(rule.Weight))
		if err != nil || total.IsNegative() || total.IsZero() {
			return nil, errors.New("weight must be a positive decimal")
		}
		base := quant.DivideStable(quant.One(), ids)
		for _, id := range ids {
			weights[id] = base[id].Mul(total)
		}
		return weights, nil
	}
	each, err := quant.Parse(strings.TrimSpace(rule.WeightEach))
	if err != nil || each.IsNegative() || each.IsZero() {
		return nil, errors.New("weight_each must be a positive decimal")
	}
	for _, id := range ids {
		weights[id] = each
	}
	return weights, nil
}

func applySignals(selected []scoredRow, candidates, allRows []row, rule Rule, previous domain.RuleState, period time.Time) ([]scoredRow, domain.RuleState, error) {
	rowsByID := make(map[string]row, len(allRows))
	for _, item := range allRows {
		rowsByID[item.id] = item
	}
	exitProgram, err := compileBoolean(rule.Signals.Exit, allRows, false)
	if err != nil {
		return nil, domain.RuleState{}, fmt.Errorf("signals.exit: %w", err)
	}
	entryProgram, err := compileBoolean(rule.Signals.Entry, allRows, false)
	if err != nil {
		return nil, domain.RuleState{}, fmt.Errorf("signals.entry: %w", err)
	}
	state := make(map[string]domain.SignalState, len(previous.Signals))
	for _, held := range previous.Signals {
		item, ok := rowsByID[held.InstrumentID]
		if !ok {
			return nil, domain.RuleState{}, fmt.Errorf("signal holding %s is missing from input", held.InstrumentID)
		}
		exit, runErr := runBoolean(exitProgram, item, 0)
		if runErr != nil {
			return nil, domain.RuleState{}, fmt.Errorf("signals.exit on %s: %w", held.InstrumentID, runErr)
		}
		if !exit {
			state[held.InstrumentID] = held
		}
	}
	selectedByID := make(map[string]scoredRow, len(selected))
	for _, item := range selected {
		selectedByID[item.id] = item
	}
	for _, candidate := range candidates {
		if _, held := state[candidate.id]; held {
			continue
		}
		if _, wasHeld := previousSignal(previous, candidate.id); wasHeld {
			continue
		}
		if _, selected := selectedByID[candidate.id]; !selected {
			continue
		}
		entry, runErr := runBoolean(entryProgram, candidate, 0)
		if runErr != nil {
			return nil, domain.RuleState{}, fmt.Errorf("signals.entry on %s: %w", candidate.id, runErr)
		}
		if entry && !exitForCandidate(exitProgram, candidate) {
			entered := int64(0)
			if !period.IsZero() {
				entered = period.UTC().UnixMilli()
			}
			state[candidate.id] = domain.SignalState{InstrumentID: candidate.id, EnteredAt: entered}
		}
	}
	ids := make([]string, 0, len(state))
	for id := range state {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]scoredRow, 0, len(ids))
	for _, id := range ids {
		if item, ok := selectedByID[id]; ok {
			result = append(result, item)
			continue
		}
		item := rowsByID[id]
		result = append(result, scoredRow{row: item})
	}
	stateResult := domain.RuleState{Signals: make([]domain.SignalState, 0, len(ids))}
	for _, id := range ids {
		stateResult.Signals = append(stateResult.Signals, state[id])
	}
	return result, stateResult, nil
}

func exitForCandidate(program *vm.Program, candidate row) bool {
	value, err := runBoolean(program, candidate, 0)
	return err == nil && value
}

func previousSignal(state domain.RuleState, id string) (domain.SignalState, bool) {
	for _, item := range state.Signals {
		if item.InstrumentID == id {
			return item, true
		}
	}
	return domain.SignalState{}, false
}

func applyHolding(selected []scoredRow, rule Rule, previous domain.RuleState, period time.Time, barIndex int64, barDuration time.Duration, barEndAt func(int) time.Time, filterAfter string) ([]scoredRow, domain.RuleState, error) {
	if rule.Holding.Bars <= 0 || len(rule.Holding.Offsets) == 0 {
		return nil, domain.RuleState{}, errors.New("holding bars and offsets must be positive")
	}
	offsets := append([]int(nil), rule.Holding.Offsets...)
	sort.Ints(offsets)
	for i := 1; i < len(offsets); i++ {
		if offsets[i] == offsets[i-1] || offsets[i] < 0 || offsets[i] >= rule.Holding.Bars {
			return nil, domain.RuleState{}, errors.New("holding offsets must be unique and within bars")
		}
	}
	if offsets[0] < 0 || offsets[0] >= rule.Holding.Bars {
		return nil, domain.RuleState{}, errors.New("holding offset is outside bars")
	}
	active := make([]domain.HoldingBatchState, 0, len(previous.Batches)+1)
	for _, batch := range previous.Batches {
		if !expiredBatch(batch, period) {
			active = append(active, batch)
		}
	}
	hitOffset := -1
	if barIndex >= 0 {
		hitOffset = int(barIndex % int64(rule.Holding.Bars))
	}
	if containsInt(offsets, hitOffset) {
		newSelected := selected
		if strings.TrimSpace(filterAfter) != "" && len(newSelected) > 0 {
			program, err := compileBoolean(filterAfter, selectedRows(newSelected), true)
			if err != nil {
				return nil, domain.RuleState{}, fmt.Errorf("filter_after: %w", err)
			}
			filtered := make([]scoredRow, 0, len(newSelected))
			for _, item := range newSelected {
				ok, runErr := runBoolean(program, item.row, item.score)
				if runErr != nil {
					return nil, domain.RuleState{}, fmt.Errorf("filter_after on %s: %w", item.id, runErr)
				}
				if ok {
					filtered = append(filtered, item)
				}
			}
			newSelected = filtered
		}
		base, err := baseWeights(newSelected)
		if err != nil {
			return nil, domain.RuleState{}, err
		}
		start := int64(0)
		end := int64(0)
		if !period.IsZero() {
			start = period.UTC().UnixMilli()
			if barEndAt != nil {
				end = barEndAt(rule.Holding.Bars).UnixMilli()
			} else {
				if barDuration <= 0 {
					barDuration = time.Minute
				}
				end = period.UTC().Add(time.Duration(rule.Holding.Bars) * barDuration).UnixMilli()
			}
		}
		if len(base) > 0 {
			active = append(active, domain.HoldingBatchState{Offset: hitOffset, EstablishedAt: start, ExpiresAt: end, BaseWeights: base})
		}
	}
	state := domain.RuleState{Batches: active}
	weights := make(map[string]quant.Decimal)
	for _, batch := range active {
		for id, raw := range batch.BaseWeights {
			value, err := quant.Parse(raw)
			if err != nil {
				return nil, domain.RuleState{}, fmt.Errorf("batch %d base weight %s: %w", batch.Offset, id, err)
			}
			weights[id] = weights[id].Add(value)
		}
	}
	rows := make([]scoredRow, 0, len(weights))
	byID := make(map[string]scoredRow, len(selected))
	for _, item := range selected {
		byID[item.id] = item
	}
	for id := range weights {
		item, ok := byID[id]
		if !ok {
			item = scoredRow{row: row{id: id}}
		}
		rows = append(rows, item)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
	return rows, state, nil
}

func holdingWeights(state domain.RuleState, rawBudget string, offsetCount int) (map[string]quant.Decimal, error) {
	weights := make(map[string]quant.Decimal)
	if offsetCount <= 0 {
		return nil, errors.New("holding offsets are required")
	}
	budget, err := quant.Parse(strings.TrimSpace(rawBudget))
	if err != nil || budget.IsNegative() || budget.IsZero() {
		return nil, errors.New("holding weight must be a positive decimal")
	}
	perBatch := budget.Div(quant.Must(strconv.Itoa(offsetCount)))
	for _, batch := range state.Batches {
		for id, raw := range batch.BaseWeights {
			base, parseErr := quant.Parse(raw)
			if parseErr != nil {
				return nil, fmt.Errorf("batch %d base weight %s: %w", batch.Offset, id, parseErr)
			}
			weights[id] = weights[id].Add(base.Mul(perBatch))
		}
	}
	return weights, nil
}

func baseWeights(rows []scoredRow) (map[string]string, error) {
	ids := make([]string, 0, len(rows))
	for _, item := range rows {
		ids = append(ids, item.id)
	}
	sort.Strings(ids)
	base := quant.DivideStable(quant.One(), ids)
	result := make(map[string]string, len(base))
	for id, weight := range base {
		result[id] = weight.String()
	}
	return result, nil
}

func expiredBatch(batch domain.HoldingBatchState, period time.Time) bool {
	return batch.ExpiresAt > 0 && !period.IsZero() && period.UTC().UnixMilli() >= batch.ExpiresAt
}

func removeSignalState(state domain.RuleState, weights map[string]quant.Decimal) domain.RuleState {
	kept := state
	kept.Signals = kept.Signals[:0]
	for _, signal := range state.Signals {
		if _, ok := weights[signal.InstrumentID]; ok {
			kept.Signals = append(kept.Signals, signal)
		}
	}
	return kept
}

func removeHoldingWeights(state domain.RuleState, weights map[string]quant.Decimal) domain.RuleState {
	for i := range state.Batches {
		filtered := make(map[string]string)
		for id, raw := range state.Batches[i].BaseWeights {
			if _, ok := weights[id]; ok {
				filtered[id] = raw
			}
		}
		state.Batches[i].BaseWeights = filtered
	}
	return state
}

func compileBoolean(expression string, rows []row, allowScore bool) (*vm.Program, error) {
	if strings.TrimSpace(expression) == "" {
		return nil, errors.New("expression is empty")
	}
	if !allowScore && strings.Contains(expression, "score") {
		// This is a conservative lexical guard. The compiler still validates
		// the complete expression and unknown identifiers below.
		if regexp.MustCompile(`\bscore\b`).MatchString(expression) {
			return nil, errors.New("score is not available in this expression")
		}
	}
	prepared, err := prepareExpression(expression)
	if err != nil {
		return nil, err
	}
	env := expressionEnvironment(rows, nil)
	options := []expr.Option{expr.Env(env), expr.DisableAllBuiltins(), expr.AsBool()}
	return expr.Compile(prepared, options...)
}

func compileNumeric(expression string, rows []row, normalized map[string]map[string]float64) (*vm.Program, error) {
	prepared, err := prepareExpression(expression)
	if err != nil {
		return nil, err
	}
	env := expressionEnvironment(rows, normalized)
	env["score"] = float64(0)
	return expr.Compile(prepared, expr.Env(env), expr.DisableAllBuiltins(), expr.AsFloat64())
}

func prepareExpression(expression string) (string, error) {
	for _, match := range barIndexRef.FindAllStringSubmatch(expression, -1) {
		index, _ := strconv.Atoi(match[1])
		if index != 0 && index != -1 {
			return "", fmt.Errorf("bars[%d] is not supported; only bars[0] and bars[-1] are allowed", index)
		}
	}
	prepared := strings.ReplaceAll(expression, "bars[-1]", "bars[1]")
	return normalizerCall.ReplaceAllString(prepared, "__norm_$2"), nil
}

func expressionEnvironment(rows []row, normalized map[string]map[string]float64) map[string]any {
	env := map[string]any{
		"bars":          []map[string]float64{{}, {}},
		"score":         float64(0),
		"instrument_id": "",
	}
	fields := map[string]struct{}{}
	for _, item := range rows {
		for field := range item.values {
			fields[field] = struct{}{}
		}
		for field := range item.previous {
			fields[field] = struct{}{}
		}
	}
	for field := range fields {
		env[field] = float64(0)
		env["__norm_"+field] = float64(0)
	}
	for factor := range normalized {
		env["__norm_"+factor] = float64(0)
	}
	return env
}

func runBoolean(program *vm.Program, item row, score float64) (bool, error) {
	env := rowEnvironment(item, score)
	value, err := expr.Run(program, env)
	if err != nil {
		return false, err
	}
	result, ok := value.(bool)
	if !ok {
		return false, errors.New("expression did not return bool")
	}
	return result, nil
}

func runNumeric(program *vm.Program, item row, normalized map[string]float64, score float64) (float64, error) {
	env := rowEnvironment(item, score)
	for factor, value := range normalized {
		env["__norm_"+factor] = value
	}
	value, err := expr.Run(program, env)
	if err != nil {
		return 0, err
	}
	result, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("expression returned %T, want float64", value)
	}
	return result, nil
}

func rowEnvironment(item row, score float64) map[string]any {
	env := map[string]any{"score": score, "instrument_id": item.id, "bars": []map[string]float64{decimalMap(item.values), decimalMap(item.previous)}}
	for field, value := range item.values {
		if parsed, err := strconv.ParseFloat(value.String(), 64); err == nil {
			env[field] = parsed
		}
	}
	return env
}

func decimalMap(values map[string]quant.Decimal) map[string]float64 {
	result := make(map[string]float64, len(values))
	for key, value := range values {
		parsed, err := strconv.ParseFloat(value.String(), 64)
		if err == nil {
			result[key] = parsed
		}
	}
	return result
}

func normalizerValues(expression string, rows []row) (map[string]map[string]float64, error) {
	result := map[string]map[string]float64{}
	for _, match := range normalizerCall.FindAllStringSubmatch(expression, -1) {
		name, factor := match[1], match[2]
		if _, exists := result[factor]; exists {
			continue
		}
		for _, item := range rows {
			value, ok := item.values[factor]
			if !ok {
				return nil, fmt.Errorf("%s(%s): factor value is missing for %s", name, factor, item.id)
			}
			parsed, err := strconv.ParseFloat(value.String(), 64)
			if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
				return nil, fmt.Errorf("%s(%s): factor value for %s is not finite", name, factor, item.id)
			}
		}
		if name == "pct_rank" {
			result[factor] = pctRankForRows(rows, factor)
		} else {
			result[factor] = zScoreForRows(rows, factor)
		}
	}
	return result, nil
}

func pctRankForRows(rows []row, factor string) map[string]float64 {
	type pair struct {
		id    string
		value float64
	}
	values := make([]pair, 0, len(rows))
	for _, item := range rows {
		value, _ := strconv.ParseFloat(item.values[factor].String(), 64)
		values = append(values, pair{id: item.id, value: value})
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].value != values[j].value {
			return values[i].value < values[j].value
		}
		return values[i].id < values[j].id
	})
	result := make(map[string]float64, len(values))
	if len(values) == 1 {
		result[values[0].id] = 0.5
		return result
	}
	for i := 0; i < len(values); {
		j := i + 1
		for j < len(values) && values[j].value == values[i].value {
			j++
		}
		rank := (float64(i+1) + float64(j)) / 2
		for k := i; k < j; k++ {
			result[values[k].id] = (rank - 1) / float64(len(values)-1)
		}
		i = j
	}
	return result
}

func zScoreForRows(rows []row, factor string) map[string]float64 {
	result := make(map[string]float64, len(rows))
	if len(rows) == 0 {
		return result
	}
	values := make([]float64, 0, len(rows))
	for _, item := range rows {
		value, _ := strconv.ParseFloat(item.values[factor].String(), 64)
		values = append(values, value)
	}
	mean := 0.0
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	variance := 0.0
	for _, value := range values {
		delta := value - mean
		variance += delta * delta
	}
	variance /= float64(len(values))
	stddev := math.Sqrt(variance)
	for i, item := range rows {
		if stddev == 0 {
			result[item.id] = 0
		} else {
			result[item.id] = (values[i] - mean) / stddev
		}
	}
	return result
}

func filterPool(rows []row, pool []string, poolSet ...bool) []row {
	if len(pool) == 0 {
		if len(poolSet) > 0 && poolSet[0] {
			return []row{}
		}
		return append([]row(nil), rows...)
	}
	allowed := make(map[string]struct{}, len(pool))
	for _, id := range pool {
		allowed[strings.ToUpper(strings.TrimSpace(id))] = struct{}{}
	}
	result := make([]row, 0, len(pool))
	for _, item := range rows {
		if _, ok := allowed[strings.ToUpper(strings.TrimSpace(item.id))]; ok {
			result = append(result, item)
		}
	}
	return result
}

func selectedRows(rows []scoredRow) []row {
	result := make([]row, len(rows))
	for i, item := range rows {
		result[i] = item.row
	}
	return result
}

func mergeDebug(dst *DebugInfo, src DebugInfo) {
	for key, value := range src.PreCount {
		dst.PreCount[key] = value
	}
	for key, value := range src.SelectedCount {
		dst.SelectedCount[key] = value
	}
	for key, value := range src.PostCount {
		dst.PostCount[key] = value
	}
	for key, value := range src.Scores {
		dst.Scores[key] = value
	}
	dst.LongInstruments = append(dst.LongInstruments, src.LongInstruments...)
	dst.ShortInstruments = append(dst.ShortInstruments, src.ShortInstruments...)
	if src.Gross != "" {
		if dst.Gross == "" {
			dst.Gross = src.Gross
		} else {
			dst.Gross = quant.Must(dst.Gross).Add(quant.Must(src.Gross)).String()
		}
	}
}

func instrumentIDs(weights map[string]quant.Decimal, short bool) ([]string, []string) {
	ids := make([]string, 0, len(weights))
	for id, value := range weights {
		if !value.IsZero() {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if short {
		return nil, ids
	}
	return ids, nil
}

func sumWeights(weights map[string]quant.Decimal) quant.Decimal {
	total := quant.Zero()
	for _, value := range weights {
		total = total.Add(value)
	}
	return total
}
func sumAbsolute(weights map[string]quant.Decimal) quant.Decimal {
	total := quant.Zero()
	for _, value := range weights {
		total = total.Add(abs(value))
	}
	return total
}
func abs(value quant.Decimal) quant.Decimal {
	if value.IsNegative() {
		return value.Neg()
	}
	return value
}
func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func adaptInput(raw any) ([]row, time.Time, int64, time.Duration, func(int) time.Time, error) {
	switch typed := raw.(type) {
	case EvaluationInput:
		rows := make([]row, 0, len(typed.Rows))
		for _, item := range typed.Rows {
			rows = append(rows, row{id: item.InstrumentID, market: item.Market, values: item.Values, previous: item.PreviousValues})
		}
		return rows, typed.PeriodEnd.UTC(), typed.BarIndex, typed.BarDuration, typed.BarEndAt, nil
	case *EvaluationInput:
		if typed == nil {
			return nil, time.Time{}, 0, 0, nil, errors.New("evaluation input is required")
		}
		return adaptInput(*typed)
	case input.EvaluationInput:
		period, err := time.Parse(time.RFC3339Nano, typed.PeriodEnd)
		if err != nil && typed.PeriodEnd != "" {
			return nil, time.Time{}, 0, 0, nil, fmt.Errorf("period_end: %w", err)
		}
		duration := time.Minute
		if typed.DataFrequency != "" {
			if parsed, parseErr := report.ParseDatasetFrequency(typed.DataFrequency); parseErr == nil && parsed > 0 {
				duration = parsed
			}
		}
		index := int64(-1)
		if !period.IsZero() {
			index = period.UnixNano() / duration.Nanoseconds()
		}
		rows := make([]row, 0, len(typed.Items))
		for _, item := range typed.Items {
			rows = append(rows, row{id: item.InstrumentID, market: item.Market, values: item.Values, previous: item.PreviousValues})
		}
		if typed.BarIndex != 0 || typed.BarEndAt != nil {
			index = typed.BarIndex
		}
		return rows, period.UTC(), index, duration, typed.BarEndAt, nil
	default:
		return nil, time.Time{}, 0, 0, nil, errors.New("unsupported evaluation input")
	}
}
