package storageio

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	"github.com/mooyang-code/moox/packages/report"
)

type ViewRow struct {
	InstrumentID string
	SubjectID    string
	SeriesTag    string
	DataTime     time.Time
	Values       map[string]string
	Attributes   map[string]string
}

type ViewReader interface {
	ReadPeriod(context.Context, string, string, time.Time) ([]ViewRow, error)
	ListSubjects(context.Context, string, string) ([]input.Subject, error)
	HistoryPeriods(context.Context, string, string, string, time.Time, time.Time) (map[string]int, error)
}

// ViewSnapshotReader is implemented by readers that can pin all dependent
// Views to the active index observed at the beginning of one evaluation. The
// legacy ViewReader methods remain the compatibility path for in-memory test
// readers and older embedders.
type ViewSnapshotReader interface {
	BeginViewSnapshot(context.Context, string, []string) (ViewReader, error)
}

// ViewSnapshotReaderWithIndexes additionally verifies the generation carried
// by a readiness event before pinning the dependent Views.
type ViewSnapshotReaderWithIndexes interface {
	BeginViewSnapshotAt(context.Context, string, []string, map[string]string) (ViewReader, error)
}

type Loader struct{ Reader ViewReader }

func (l Loader) Load(ctx context.Context, runner domain.StrategyRunner, compiled compiler.CompiledStrategy, period time.Time) (input.EvaluationInput, error) {
	return l.load(ctx, runner, compiled, period, nil)
}

// LoadAt reads one immutable cross-View snapshot after checking the expected
// active index IDs emitted by Storage's View-ready event.
func (l Loader) LoadAt(ctx context.Context, runner domain.StrategyRunner, compiled compiler.CompiledStrategy, period time.Time, expected map[string]string) (input.EvaluationInput, error) {
	return l.load(ctx, runner, compiled, period, expected)
}

// LoadPeriodAt evaluates a bar whose public timestamp is its end boundary
// while reading Storage rows by their start boundary.  Storage's time-series
// keys use BarStart; keeping this conversion at the adapter boundary avoids
// leaking the storage convention into the DSL/evaluator.
func (l Loader) LoadPeriodAt(ctx context.Context, runner domain.StrategyRunner, compiled compiler.CompiledStrategy, barEnd, storagePeriod time.Time, expected map[string]string) (input.EvaluationInput, error) {
	return l.loadWithPeriods(ctx, runner, compiled, barEnd, storagePeriod, expected)
}

func (l Loader) LoadPeriod(ctx context.Context, runner domain.StrategyRunner, compiled compiler.CompiledStrategy, barEnd, storagePeriod time.Time) (input.EvaluationInput, error) {
	return l.loadWithPeriods(ctx, runner, compiled, barEnd, storagePeriod, nil)
}

func (l Loader) load(ctx context.Context, runner domain.StrategyRunner, compiled compiler.CompiledStrategy, period time.Time, expected map[string]string) (input.EvaluationInput, error) {
	return l.loadWithPeriods(ctx, runner, compiled, period, period, expected)
}

func (l Loader) loadWithPeriods(ctx context.Context, runner domain.StrategyRunner, compiled compiler.CompiledStrategy, period, storagePeriod time.Time, expected map[string]string) (input.EvaluationInput, error) {
	if l.Reader == nil {
		return input.EvaluationInput{}, fmt.Errorf("strategy storage reader is required")
	}
	viewIDs := append([]string{compiled.SourceView.ID}, compiled.Dependencies.FactorResultViewIDs...)
	viewIDs = uniqueStrings(viewIDs)
	reader := l.Reader
	var pinned ViewReader
	var snapshotErr error
	if len(expected) > 0 {
		if snapshotReader, ok := l.Reader.(ViewSnapshotReaderWithIndexes); ok {
			pinned, snapshotErr = snapshotReader.BeginViewSnapshotAt(ctx, runner.SpaceID, viewIDs, expected)
		} else {
			return input.EvaluationInput{}, fmt.Errorf("%w: storage reader cannot verify View index provenance", input.ErrNotReady)
		}
	} else if snapshotReader, ok := l.Reader.(ViewSnapshotReader); ok {
		pinned, snapshotErr = snapshotReader.BeginViewSnapshot(ctx, runner.SpaceID, viewIDs)
	}
	if pinned != nil {
		reader = pinned
	}
	if snapshotErr != nil {
		return input.EvaluationInput{}, fmt.Errorf("%w: begin view snapshot: %w", input.ErrNotReady, snapshotErr)
	}
	subjects, err := reader.ListSubjects(ctx, runner.SpaceID, compiled.SourceView.ID)
	if err != nil {
		return input.EvaluationInput{}, fmt.Errorf("%w: list subjects: %v", input.ErrNotReady, err)
	}
	duration, err := report.ParseDatasetFrequency(compiled.SourceView.Frequency)
	if err != nil || duration <= 0 {
		return input.EvaluationInput{}, fmt.Errorf("invalid source frequency %q", compiled.SourceView.Frequency)
	}
	minHistory := compiled.InstrumentPool.MinHistoryPeriods
	if minHistory <= 0 {
		minHistory = 1
	}
	start := storagePeriod.Add(-time.Duration(minHistory-1) * duration)
	if minHistory > 1 {
		previous, previousErr := input.HistoryStart(compiled.Data.Calendar, compiled.SourceView.Frequency, storagePeriod, minHistory)
		if previousErr != nil {
			return input.EvaluationInput{}, fmt.Errorf("%w: history boundary: %v", input.ErrNotReady, previousErr)
		}
		start = previous
	}
	end := storagePeriod.Add(duration)
	// Storage time ranges are end-exclusive, while indexed_to is inclusive.
	// Asking for period+frequency would require a future bar before the View
	// can report complete, delaying the newest signal by one whole period.
	// period+1ns includes the requested bar without looking beyond it.
	end = storagePeriod.Add(time.Nanosecond)
	history, err := reader.HistoryPeriods(ctx, runner.SpaceID, compiled.SourceView.ID, compiled.SourceView.Frequency, start, end)
	if err != nil {
		return input.EvaluationInput{}, fmt.Errorf("%w: read history: %w", input.ErrNotReady, err)
	}
	pool := input.BuildPool(compiled.InstrumentPool, subjects, history)
	requiredFactors := requiredFactorsByInstrument(compiled, pool.Items)
	rowsByInstrument := make(map[string]input.InstrumentInput, len(pool.Items))
	sourceRowsPresent := make(map[string]bool, len(pool.Items))
	subjectToInstrument := make(map[string]string, len(pool.Items))
	for _, item := range pool.Items {
		rowsByInstrument[item.InstrumentID] = input.InstrumentInput{PoolItem: item, Values: map[string]quant.Decimal{}, PreviousValues: map[string]quant.Decimal{}}
		subjectToInstrument[item.SubjectID] = item.InstrumentID
	}
	for _, viewID := range viewIDs {
		rows, readErr := reader.ReadPeriod(ctx, runner.SpaceID, viewID, storagePeriod)
		if readErr != nil {
			return input.EvaluationInput{}, fmt.Errorf("%w: read view %s: %w", input.ErrNotReady, viewID, readErr)
		}
		for _, row := range rows {
			instrumentID := row.InstrumentID
			if row.SubjectID != "" {
				if mapped := subjectToInstrument[row.SubjectID]; mapped != "" {
					instrumentID = mapped
				}
			}
			item, ok := rowsByInstrument[instrumentID]
			if !ok || (row.SubjectID != "" && row.SubjectID != item.SubjectID) {
				continue
			}
			if item.SeriesTag != "" && row.SeriesTag != "" && item.SeriesTag != row.SeriesTag {
				continue
			}
			// Keep source OHLCV and any other numeric columns available to the
			// expression runtime. Previously only explicitly compiled Factor
			// columns were copied, which made bars[0].close/ma20 evaluate against
			// an empty value map after a process restart.
			for column, raw := range row.Values {
				parsed, parseErr := quant.Parse(raw)
				if parseErr == nil {
					item.Values[column] = parsed
				}
			}
			if viewID == compiled.SourceView.ID {
				sourceRowsPresent[instrumentID] = true
			}
			for _, factor := range compiled.Factors {
				if factor.ResultViewID != viewID {
					continue
				}
				value, exists := row.Values[factor.ColumnName]
				if !exists {
					continue
				}
				parsed, parseErr := quant.Parse(value)
				if parseErr != nil {
					return input.EvaluationInput{}, fmt.Errorf("view %s instrument %s factor %s: %w", viewID, row.InstrumentID, factor.FactorID, parseErr)
				}
				item.Values[factor.FactorID] = parsed
				sourceHash := strings.TrimSpace(row.Attributes["factor.source_hash."+factor.FactorID])
				if sourceHash == "" && singleFactorSourceHash(compiled) {
					sourceHash = strings.TrimSpace(row.Attributes["factor.source_hash"])
				}
				if factor.SourceHash != "" && sourceHash == "" {
					return input.EvaluationInput{}, fmt.Errorf("view %s instrument %s factor %s source hash is missing", viewID, row.InstrumentID, factor.FactorID)
				}
				if sourceHash != "" && sourceHash != factor.SourceHash {
					return input.EvaluationInput{}, fmt.Errorf("view %s instrument %s factor %s source hash mismatch: compiled=%s row=%s", viewID, row.InstrumentID, factor.FactorID, factor.SourceHash, sourceHash)
				}
			}
			rowsByInstrument[instrumentID] = item
		}
	}
	// bars[-1] is the immediately preceding closed bar. Fetch it only when a
	// compiled expression declares that dependency so normal ranking strategies
	// keep the existing single-period read path.
	if compiledUsesPreviousBar(compiled) {
		previousPeriod := storagePeriod.Add(-duration)
		if previous, previousErr := input.PreviousStorageStart(compiled.Data.Calendar, compiled.SourceView.Frequency, storagePeriod); previousErr == nil {
			previousPeriod = previous
		} else {
			return input.EvaluationInput{}, fmt.Errorf("%w: previous %s period: %v", input.ErrNotReady, compiled.SourceView.Frequency, previousErr)
		}
		for _, viewID := range viewIDs {
			rows, readErr := reader.ReadPeriod(ctx, runner.SpaceID, viewID, previousPeriod)
			if readErr != nil {
				return input.EvaluationInput{}, fmt.Errorf("%w: read previous view %s: %w", input.ErrNotReady, viewID, readErr)
			}
			for _, row := range rows {
				instrumentID := row.InstrumentID
				if row.SubjectID != "" {
					if mapped := subjectToInstrument[row.SubjectID]; mapped != "" {
						instrumentID = mapped
					}
				}
				item, ok := rowsByInstrument[instrumentID]
				if !ok || (row.SubjectID != "" && row.SubjectID != item.SubjectID) {
					continue
				}
				if item.SeriesTag != "" && row.SeriesTag != "" && item.SeriesTag != row.SeriesTag {
					continue
				}
				for column, raw := range row.Values {
					parsed, parseErr := quant.Parse(raw)
					if parseErr == nil {
						item.PreviousValues[column] = parsed
					}
				}
				rowsByInstrument[instrumentID] = item
			}
		}
	}
	barIndex := int64(-1)
	barEndAt := func(offset int) time.Time { return period.UTC().Add(time.Duration(offset) * duration) }
	if boundaries, boundaryErr := input.FromBarEnd(compiled.Data.Calendar, compiled.SourceView.Frequency, period); boundaryErr == nil {
		barIndex = boundaries.BarIndex
		barEndAt = func(offset int) time.Time {
			value, advanceErr := input.AdvanceBarEnd(compiled.Data.Calendar, compiled.SourceView.Frequency, period, offset)
			if advanceErr != nil {
				return period.UTC().Add(time.Duration(offset) * duration)
			}
			return value
		}
	}
	result := input.EvaluationInput{SpaceID: runner.SpaceID, StrategyID: runner.StrategyID, PeriodEnd: period.UTC().Format(time.RFC3339Nano), SourceViewID: compiled.SourceView.ID, DataFrequency: compiled.SourceView.Frequency, Ineligible: pool.Ineligible, BarIndex: barIndex, BarDuration: duration, BarEndAt: barEndAt}
	if err := (input.ReadinessChecker{}).CheckWithPresenceByInstrument(pool, rowsByInstrument, sourceRowsPresent, requiredFactors); err != nil {
		return input.EvaluationInput{}, err
	}
	for _, item := range pool.Items {
		value := rowsByInstrument[item.InstrumentID]
		result.Items = append(result.Items, value)
	}
	if missing := missingRequiredFields(compiled, rowsByInstrument, pool.Items); len(missing) > 0 {
		return input.EvaluationInput{}, &input.StrictIncompleteError{Pool: pool, Missing: missing}
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].InstrumentID < result.Items[j].InstrumentID })
	return result, nil
}

func compiledUsesPreviousBar(compiled compiler.CompiledStrategy) bool {
	for _, rule := range compiled.Rules {
		for _, expression := range []*compiler.CompiledExpression{rule.FilterBefore, rule.Score, rule.SelectWhere, rule.SignalEntry, rule.SignalExit, rule.FilterAfter} {
			if expression != nil {
				if _, ok := expression.Dependencies.Bars[-1]; ok {
					return true
				}
			}
		}
	}
	return false
}

func missingRequiredFields(compiled compiler.CompiledStrategy, rows map[string]input.InstrumentInput, pool []input.PoolItem) []string {
	missing := map[string]struct{}{}
	for _, item := range pool {
		row := rows[item.InstrumentID]
		for _, rule := range compiled.Rules {
			if !ruleAppliesToInstrument(compiled, rule.Definition.Pool, item.InstrumentID) {
				continue
			}
			currentFields, previousFields := ruleFields(rule)
			for field := range currentFields {
				if fieldRequiresScopedFactor(compiled, field, item) {
					continue
				}
				if _, ok := row.Values[field]; !ok {
					missing[item.InstrumentID+":current:"+field] = struct{}{}
				}
			}
			for field := range previousFields {
				if fieldRequiresScopedFactor(compiled, field, item) {
					continue
				}
				if _, ok := row.PreviousValues[field]; !ok {
					missing[item.InstrumentID+":previous:"+field] = struct{}{}
				}
			}
		}
	}
	result := make([]string, 0, len(missing))
	for key := range missing {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func singleFactorSourceHash(compiled compiler.CompiledStrategy) bool {
	seen := ""
	for _, factor := range compiled.Factors {
		hash := strings.TrimSpace(factor.SourceHash)
		if hash == "" {
			continue
		}
		if seen == "" {
			seen = hash
			continue
		}
		if seen != hash {
			return false
		}
	}
	return seen != ""
}

func requiredFactorsByInstrument(compiled compiler.CompiledStrategy, pool []input.PoolItem) map[string][]string {
	result := make(map[string][]string, len(pool))
	for _, item := range pool {
		seen := map[string]struct{}{}
		if len(compiled.Rules) == 0 {
			for _, factor := range compiled.Factors {
				if factorAppliesToItem(factor, item) {
					seen[factor.FactorID] = struct{}{}
					result[item.InstrumentID] = append(result[item.InstrumentID], factor.FactorID)
				}
			}
			sort.Strings(result[item.InstrumentID])
			continue
		}
		for _, rule := range compiled.Rules {
			if !ruleAppliesToInstrument(compiled, rule.Definition.Pool, item.InstrumentID) {
				continue
			}
			for _, factor := range compiled.Factors {
				if !ruleReferencesFactor(rule, factor) || !factorAppliesToItem(factor, item) {
					continue
				}
				if _, ok := seen[factor.FactorID]; ok {
					continue
				}
				seen[factor.FactorID] = struct{}{}
				result[item.InstrumentID] = append(result[item.InstrumentID], factor.FactorID)
			}
		}
		sort.Strings(result[item.InstrumentID])
	}
	return result
}

func factorAppliesToItem(factor compiler.CompiledFactor, item input.PoolItem) bool {
	if strings.EqualFold(strings.TrimSpace(factor.SubjectMode), "include") {
		var subjects []string
		if json.Unmarshal([]byte(factor.SubjectsJSON), &subjects) != nil {
			return false
		}
		for _, subject := range subjects {
			if strings.EqualFold(strings.TrimSpace(subject), strings.TrimSpace(item.SubjectID)) || strings.EqualFold(strings.TrimSpace(subject), strings.TrimSpace(item.InstrumentID)) {
				return true
			}
		}
		return false
	}
	return true
}

// fieldRequiresScopedFactor reports whether a field belongs exclusively to a
// factor binding that does not cover this instrument. Such a field is not a
// missing value for this row; the corresponding rule cannot use it here.
func fieldRequiresScopedFactor(compiled compiler.CompiledStrategy, field string, item input.PoolItem) bool {
	name := strings.ToLower(strings.TrimSpace(field))
	matched := false
	for _, factor := range compiled.Factors {
		aliases := []string{factor.FactorID, factor.Output, factor.ColumnName}
		for _, alias := range aliases {
			if strings.ToLower(strings.TrimSpace(alias)) != name {
				continue
			}
			matched = true
			if factorAppliesToItem(factor, item) {
				return false
			}
		}
	}
	return matched
}

func ruleFields(rule compiler.CompiledRule) (map[string]struct{}, map[string]struct{}) {
	current, previous := map[string]struct{}{}, map[string]struct{}{}
	for _, expression := range []*compiler.CompiledExpression{rule.FilterBefore, rule.Score, rule.SelectWhere, rule.SignalEntry, rule.SignalExit, rule.FilterAfter} {
		if expression == nil {
			continue
		}
		for _, field := range expression.Dependencies.Bars[-1] {
			previous[field] = struct{}{}
		}
		for _, field := range expression.Dependencies.Bars[0] {
			current[field] = struct{}{}
		}
		for _, field := range expression.Dependencies.Fields {
			if field != "instrument_id" {
				current[field] = struct{}{}
			}
		}
	}
	return current, previous
}

func ruleReferencesFactor(rule compiler.CompiledRule, factor compiler.CompiledFactor) bool {
	aliases := map[string]struct{}{}
	for _, name := range []string{factor.FactorID, factor.Output, factor.ColumnName} {
		if name != "" {
			aliases[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
		}
	}
	for _, expression := range []*compiler.CompiledExpression{rule.FilterBefore, rule.Score, rule.SelectWhere, rule.SignalEntry, rule.SignalExit, rule.FilterAfter} {
		if expression == nil {
			continue
		}
		for _, name := range expression.Dependencies.Fields {
			if _, ok := aliases[strings.ToLower(strings.TrimSpace(name))]; ok {
				return true
			}
		}
		for _, fields := range expression.Dependencies.Bars {
			for _, name := range fields {
				if _, ok := aliases[strings.ToLower(strings.TrimSpace(name))]; ok {
					return true
				}
			}
		}
	}
	return false
}

func ruleAppliesToInstrument(compiled compiler.CompiledStrategy, pool config.Pool, instrumentID string) bool {
	if pool.UDF != nil {
		return true
	}
	if pool.Fixed != nil {
		for _, value := range pool.Fixed {
			if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(instrumentID)) {
				return true
			}
		}
		return false
	}
	if compiled.InstrumentPool.IncludeSet || len(compiled.InstrumentPool.Include) > 0 {
		for _, value := range compiled.InstrumentPool.Include {
			if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(instrumentID)) {
				return true
			}
		}
		return false
	}
	return true
}
