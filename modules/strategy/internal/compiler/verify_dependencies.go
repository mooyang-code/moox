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

// VerifyDependencies rechecks bindings frozen by the instance.  A strategy
// DSL itself contains logical field names, so the concrete Factor/View
// binding is supplied by the instance and represented in CompiledStrategy.Factors.
func (c Compiler) VerifyDependencies(ctx context.Context, compiled CompiledStrategy) error {
	if len(compiled.Factors) == 0 && strings.TrimSpace(compiled.SourceView.ID) == "" {
		return nil
	}
	if c.Factors == nil || c.Storage == nil {
		return dependencyMismatch("strategy compiler dependencies are required")
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
	for _, factor := range compiled.Factors {
		descriptor, err := c.Factors.GetFactor(ctx, factor.FactorID)
		if err != nil {
			return fmt.Errorf("verify factor %q: %w", factor.FactorID, err)
		}
		if !isActive(descriptor.Status) || !containsOutput(descriptor.Outputs, factor.Output) ||
			descriptor.SourceHash != factor.SourceHash ||
			!sameStringSet(descriptor.InputColumns, factor.InputColumns) ||
			strings.TrimSpace(descriptor.ParamsJSON) != strings.TrimSpace(factor.ParamsJSON) ||
			descriptor.LookbackPeriods != factor.LookbackPeriods {
			return dependencyMismatch("factor %q status or output changed", factor.FactorID)
		}
		bindings, err := c.Factors.ListBindings(ctx, factor.FactorID)
		if err != nil {
			return fmt.Errorf("verify factor %q bindings: %w", factor.FactorID, err)
		}
		found := false
		for _, binding := range bindings {
			if binding.ID != factor.BindingID {
				continue
			}
			bindingFrequency, bindingFrequencyErr := normalizeOptionalFrequency(binding.Frequency)
			factorFrequency, factorFrequencyErr := normalizeOptionalFrequency(factor.Frequency)
			if !isActive(binding.Status) || binding.FactorID != factor.FactorID || binding.SpaceID != compiled.SpaceID || binding.SourceViewID != compiled.SourceView.ID || bindingFrequencyErr != nil || factorFrequencyErr != nil || bindingFrequency != factorFrequency ||
				binding.ResultDatasetID != factor.ResultDatasetID || binding.ResultViewID != factor.ResultViewID ||
				binding.SubjectMode != factor.SubjectMode || binding.SubjectsJSON != factor.SubjectsJSON {
				return dependencyMismatch("binding %q changed", factor.BindingID)
			}
			found = true
			break
		}
		if !found {
			return dependencyMismatch("binding %q no longer exists", factor.BindingID)
		}
		view, err := c.Storage.GetView(ctx, factor.ResultViewID)
		if err != nil {
			return fmt.Errorf("verify result view %q: %w", factor.ResultViewID, err)
		}
		factorFrequency, factorFrequencyErr := normalizeOptionalFrequency(factor.Frequency)
		viewFrequency, viewFrequencyErr := normalizeOptionalFrequency(view.Frequency)
		if !isActive(view.Status) || factorFrequencyErr != nil || viewFrequencyErr != nil || (factorFrequency != "" && viewFrequency != "" && viewFrequency != factorFrequency) {
			return dependencyMismatch("result view %q changed", factor.ResultViewID)
		}
		columns, err := c.Storage.ListViewColumns(ctx, factor.ResultViewID)
		if err != nil {
			return fmt.Errorf("verify result view %q columns: %w", factor.ResultViewID, err)
		}
		column, ok := findFactorColumn(columns, factor.FactorID, factor.Output)
		if !ok || column.Name != factor.ColumnName {
			return dependencyMismatch("factor %q output column changed", factor.FactorID)
		}
	}
	return nil
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
