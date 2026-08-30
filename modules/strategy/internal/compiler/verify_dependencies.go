package compiler

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrDependencyMismatch marks a permanent change to the metadata frozen in a
// compiled strategy. Callers should acknowledge these deliveries after
// recording the failure; errors from dependency RPCs remain retryable.
var ErrDependencyMismatch = errors.New("strategy dependency mismatch")

func dependencyMismatch(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrDependencyMismatch, fmt.Sprintf(format, args...))
}

// DependencyMismatchError preserves a permanent catalog error returned by a
// remote adapter while allowing the trigger to distinguish it from transport
// or service-unavailable failures, which remain retryable.
func DependencyMismatchError(err error) error {
	if err == nil {
		return ErrDependencyMismatch
	}
	return fmt.Errorf("%w: %w", ErrDependencyMismatch, err)
}

// VerifyDependencies rechecks only the identifiers and schema frozen in a
// compiled artifact. It deliberately never chooses a replacement Binding.
func (c Compiler) VerifyDependencies(ctx context.Context, compiled CompiledStrategy) error {
	if c.Factors == nil || c.Storage == nil {
		return dependencyMismatch("strategy compiler dependencies are required")
	}
	if strings.TrimSpace(compiled.SpaceID) == "" || strings.TrimSpace(compiled.SourceView.ID) == "" {
		return dependencyMismatch("compiled strategy identity is incomplete")
	}
	source, err := c.Storage.GetView(ctx, compiled.SourceView.ID)
	if err != nil {
		return fmt.Errorf("verify source view %q: %w", compiled.SourceView.ID, err)
	}
	if !isActive(source.Status) {
		return dependencyMismatch("source view %q is not active", source.ID)
	}
	if source.Frequency != "" && compiled.SourceView.Frequency != "" && source.Frequency != compiled.SourceView.Frequency {
		return dependencyMismatch("source view %q frequency changed", source.ID)
	}
	for _, factor := range compiled.Factors {
		descriptor, err := c.Factors.GetFactor(ctx, factor.FactorID)
		if err != nil {
			return fmt.Errorf("verify factor %q: %w", factor.FactorID, err)
		}
		if !isActive(descriptor.Status) || !containsOutput(descriptor.Outputs, factor.Output) ||
			(factor.SourceHash != "" && descriptor.SourceHash != factor.SourceHash) ||
			!sameStringSet(descriptor.InputColumns, factor.InputColumns) ||
			(strings.TrimSpace(factor.ParamsJSON) != "" && strings.TrimSpace(descriptor.ParamsJSON) != strings.TrimSpace(factor.ParamsJSON)) ||
			(factor.LookbackPeriods > 0 && descriptor.LookbackPeriods != factor.LookbackPeriods) {
			return dependencyMismatch("factor %q status or output changed", factor.FactorID)
		}
		bindings, err := c.Factors.ListBindings(ctx, factor.FactorID)
		if err != nil {
			return fmt.Errorf("verify factor %q bindings: %w", factor.FactorID, err)
		}
		found := false
		for _, binding := range bindings {
			if binding.ID == factor.BindingID {
				if !isActive(binding.Status) || binding.FactorID != factor.FactorID || binding.SpaceID != compiled.SpaceID || binding.SourceViewID != compiled.SourceView.ID || binding.Frequency != factor.Frequency || binding.ResultDatasetID != factor.ResultDatasetID || binding.ResultViewID != factor.ResultViewID || binding.SubjectMode != factor.SubjectMode || binding.SubjectsJSON != factor.SubjectsJSON {
					return dependencyMismatch("binding %q changed", factor.BindingID)
				}
				found = true
				break
			}
		}
		if !found {
			return dependencyMismatch("binding %q no longer exists", factor.BindingID)
		}
		view, err := c.Storage.GetView(ctx, factor.ResultViewID)
		if err != nil {
			return fmt.Errorf("verify result view %q: %w", factor.ResultViewID, err)
		}
		if !isActive(view.Status) {
			return dependencyMismatch("result view %q is not active", factor.ResultViewID)
		}
		if factor.Frequency != "" && view.Frequency != "" && view.Frequency != factor.Frequency {
			return dependencyMismatch("result view %q frequency changed", factor.ResultViewID)
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
