package compiler

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/packages/report"
)

// ErrDependencyMismatch marks a permanent change to metadata frozen in an
// instance binding. RPC and transport errors remain retryable.
var ErrDependencyMismatch = errors.New("strategy dependency mismatch")

func dependencyMismatch(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrDependencyMismatch, fmt.Sprintf(format, args...))
}

// DependencyMismatchError preserves a permanent catalog error returned by a
// remote adapter while allowing callers to classify it separately.
func DependencyMismatchError(err error) error {
	if err == nil {
		return ErrDependencyMismatch
	}
	return fmt.Errorf("%w: %w", ErrDependencyMismatch, err)
}

// VerifyDependencies confirms each strategy factor is an enabled member of the
// FactorSet that owns the result View's dataset.
func (c Compiler) VerifyDependencies(ctx context.Context, compiled CompiledStrategy) error {
	if len(compiled.Factors) == 0 && strings.TrimSpace(compiled.SourceView.ID) == "" {
		return nil
	}
	if len(compiled.Factors) > 0 && c.Factors == nil {
		return dependencyMismatch("factor catalog is required")
	}
	if (len(compiled.Factors) > 0 || strings.TrimSpace(compiled.SourceView.ID) != "") && c.Storage == nil {
		return dependencyMismatch("storage catalog is required")
	}
	if sourceID := strings.TrimSpace(compiled.SourceView.ID); sourceID != "" {
		source, err := c.Storage.GetView(ctx, sourceID)
		if err != nil {
			return fmt.Errorf("verify source view %q: %w", sourceID, err)
		}
		compiledFrequency, compiledFrequencyErr := normalizeOptionalFrequency(compiled.SourceView.Frequency)
		sourceFrequency, sourceFrequencyErr := normalizeOptionalFrequency(source.Frequency)
		if compiledFrequencyErr != nil || sourceFrequencyErr != nil || !isActive(source.Status) || (compiledFrequency != "" && sourceFrequency != "" && sourceFrequency != compiledFrequency) {
			return dependencyMismatch("source view %q changed", sourceID)
		}
	}
	var sets []FactorSetDescriptor
	if len(compiled.Factors) > 0 {
		var err error
		sets, err = c.Factors.ListFactorSets(ctx)
		if err != nil {
			return fmt.Errorf("list factor sets: %w", err)
		}
	}
	views := make(map[string]ViewDescriptor)
	columnsByView := make(map[string][]ViewColumn)
	for _, factor := range compiled.Factors {
		view, ok := views[factor.ResultViewID]
		if !ok {
			var err error
			view, err = c.Storage.GetView(ctx, factor.ResultViewID)
			if err != nil {
				return fmt.Errorf("verify result view %q: %w", factor.ResultViewID, err)
			}
			views[factor.ResultViewID] = view
		}
		factorFrequency, factorFrequencyErr := normalizeOptionalFrequency(factor.Frequency)
		viewFrequency, viewFrequencyErr := normalizeOptionalFrequency(view.Frequency)
		if !isActive(view.Status) || strings.TrimSpace(view.DatasetID) == "" || factorFrequencyErr != nil || viewFrequencyErr != nil || (factorFrequency != "" && viewFrequency != "" && viewFrequency != factorFrequency) {
			return dependencyMismatch("result view %q changed", factor.ResultViewID)
		}
		set, found := findFactorSet(sets, factor.SetID, view.DatasetID)
		if !found || !isActive(set.Status) || set.ResultDatasetID != view.DatasetID ||
			(strings.TrimSpace(factor.ResultDatasetID) != "" && factor.ResultDatasetID != view.DatasetID) {
			return dependencyMismatch("factor set for result view %q changed", factor.ResultViewID)
		}
		factorDescriptors, err := c.Factors.ListFactors(ctx, set)
		if err != nil {
			return fmt.Errorf("list factors in set %q: %w", set.SetID, err)
		}
		descriptor, found := findFactor(factorDescriptors, factor.FactorID)
		if !found || !isActive(descriptor.Status) || descriptor.SetID != set.SetID || descriptor.ResultDatasetID != view.DatasetID || !containsOutput(descriptor.Outputs, factor.Output) ||
			(factor.SourceHash != "" && descriptor.SourceHash != factor.SourceHash) ||
			(len(factor.InputColumns) > 0 && !sameStringSet(descriptor.InputColumns, factor.InputColumns)) ||
			(strings.TrimSpace(factor.ParamsJSON) != "" && strings.TrimSpace(descriptor.ParamsJSON) != strings.TrimSpace(factor.ParamsJSON)) ||
			(factor.LookbackPeriods != 0 && descriptor.LookbackPeriods != factor.LookbackPeriods) {
			return dependencyMismatch("factor %q is not enabled in result View factor set %q", factor.FactorID, set.SetID)
		}
		columns, ok := columnsByView[factor.ResultViewID]
		if !ok {
			var err error
			columns, err = c.Storage.ListViewColumns(ctx, factor.ResultViewID)
			if err != nil {
				return fmt.Errorf("verify result view %q columns: %w", factor.ResultViewID, err)
			}
			columnsByView[factor.ResultViewID] = columns
		}
		column, ok := findFactorColumn(columns, factor.FactorID, factor.Output)
		if !ok || column.Name != factor.ColumnName {
			return dependencyMismatch("factor %q output column changed", factor.FactorID)
		}
	}
	return nil
}

func findFactorSet(sets []FactorSetDescriptor, wantedSetID, resultDatasetID string) (FactorSetDescriptor, bool) {
	wantedSetID = strings.TrimSpace(wantedSetID)
	for _, set := range sets {
		if wantedSetID != "" && set.SetID != wantedSetID {
			continue
		}
		if set.ResultDatasetID == resultDatasetID {
			return set, true
		}
	}
	return FactorSetDescriptor{}, false
}

func findFactor(factors []FactorDescriptor, wantedID string) (FactorDescriptor, bool) {
	for _, factor := range factors {
		if factor.FactorID == wantedID {
			return factor, true
		}
	}
	return FactorDescriptor{}, false
}

func normalizeOptionalFrequency(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return report.NormalizeDatasetFrequency(value)
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]struct{}, len(left))
	for _, value := range left {
		seen[strings.TrimSpace(value)] = struct{}{}
	}
	for _, value := range right {
		if _, ok := seen[strings.TrimSpace(value)]; !ok {
			return false
		}
	}
	return true
}

func containsOutput(outputs []string, wanted string) bool {
	wanted = strings.TrimSpace(wanted)
	if wanted == "" {
		return false
	}
	for _, output := range outputs {
		if strings.TrimSpace(output) == wanted {
			return true
		}
	}
	return false
}

func isActive(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active", "enabled":
		return true
	default:
		return false
	}
}

func findFactorColumn(columns []ViewColumn, factorID, output string) (ViewColumn, bool) {
	for _, column := range columns {
		if column.Attributes["origin_factor_id"] == factorID && column.Attributes["factor_output"] == output {
			return column, true
		}
	}
	return ViewColumn{}, false
}
