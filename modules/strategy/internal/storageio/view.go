package storageio

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
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

func (l Loader) load(ctx context.Context, runner domain.StrategyRunner, compiled compiler.CompiledStrategy, period time.Time, expected map[string]string) (input.EvaluationInput, error) {
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
	start := period.Add(-time.Duration(minHistory-1) * duration)
	end := period.Add(duration)
	// Storage time ranges are end-exclusive, while indexed_to is inclusive.
	// Asking for period+frequency would require a future bar before the View
	// can report complete, delaying the newest signal by one whole period.
	// period+1ns includes the requested bar without looking beyond it.
	end = period.Add(time.Nanosecond)
	history, err := reader.HistoryPeriods(ctx, runner.SpaceID, compiled.SourceView.ID, compiled.SourceView.Frequency, start, end)
	if err != nil {
		return input.EvaluationInput{}, fmt.Errorf("%w: read history: %w", input.ErrNotReady, err)
	}
	pool := input.BuildPool(compiled.InstrumentPool, subjects, history)
	requiredFactors := factorIDs(compiled.Factors)
	rowsByInstrument := make(map[string]input.InstrumentInput, len(pool.Items))
	sourceRowsPresent := make(map[string]bool, len(pool.Items))
	subjectToInstrument := make(map[string]string, len(pool.Items))
	for _, item := range pool.Items {
		rowsByInstrument[item.InstrumentID] = input.InstrumentInput{PoolItem: item, Values: map[string]quant.Decimal{}}
		subjectToInstrument[item.SubjectID] = item.InstrumentID
	}
	for _, viewID := range viewIDs {
		rows, readErr := reader.ReadPeriod(ctx, runner.SpaceID, viewID, period)
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
	result := input.EvaluationInput{SpaceID: runner.SpaceID, StrategyID: runner.StrategyID, PeriodEnd: period.UTC().Format(time.RFC3339Nano), SourceViewID: compiled.SourceView.ID, DataFrequency: compiled.SourceView.Frequency, Ineligible: pool.Ineligible}
	if err := (input.ReadinessChecker{}).CheckWithPresence(pool, rowsByInstrument, sourceRowsPresent, requiredFactors); err != nil {
		return input.EvaluationInput{}, err
	}
	for _, item := range pool.Items {
		value := rowsByInstrument[item.InstrumentID]
		result.Items = append(result.Items, value)
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].InstrumentID < result.Items[j].InstrumentID })
	return result, nil
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

func factorIDs(factors []compiler.CompiledFactor) []string {
	ids := make([]string, 0, len(factors))
	for _, factor := range factors {
		ids = append(ids, factor.FactorID)
	}
	return ids
}
