package compiler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

func (c Compiler) Compile(ctx context.Context, manifest config.Manifest, spaceID string) (CompiledStrategy, error) {
	if c.Factors == nil || c.Storage == nil {
		return CompiledStrategy{}, errors.New("strategy compiler dependencies are required")
	}
	if strings.TrimSpace(spaceID) == "" {
		return CompiledStrategy{}, errors.New("strategy compiler space_id is required")
	}
	if err := config.Validate(&manifest); err != nil {
		return CompiledStrategy{}, err
	}
	source, err := c.Storage.GetView(ctx, manifest.Input.SourceViewID)
	if err != nil {
		return CompiledStrategy{}, fmt.Errorf("get source view %q: %w", manifest.Input.SourceViewID, err)
	}
	if !isActive(source.Status) {
		return CompiledStrategy{}, fmt.Errorf("source view %q is not active", source.ID)
	}
	if source.Frequency != "" && source.Frequency != manifest.Input.DataFrequency {
		return CompiledStrategy{}, fmt.Errorf("source view %q frequency %q does not match %q", source.ID, source.Frequency, manifest.Input.DataFrequency)
	}
	sourceFrequency := source.Frequency
	if sourceFrequency == "" {
		sourceFrequency = manifest.Input.DataFrequency
	}
	compiled := CompiledStrategy{
		APIVersion: manifest.APIVersion, Kind: manifest.Kind, SpaceID: spaceID,
		SourceView:     CompiledView{ID: source.ID, Status: source.Status, Frequency: sourceFrequency},
		InstrumentPool: manifest.InstrumentPool,
		Schedule:       CompiledSchedule{Every: manifest.Schedule.Every},
		Readiness:      manifest.Readiness.Policy, Long: manifest.Long, Short: manifest.Short,
	}
	normalizeSideWeights(&compiled)
	compiled.Factors = make([]CompiledFactor, 0, len(manifest.Input.Factors))
	viewIDs := make(map[string]struct{})
	for _, factorRef := range manifest.Input.Factors {
		factor, err := c.Factors.GetFactor(ctx, factorRef.FactorID)
		if err != nil {
			return CompiledStrategy{}, fmt.Errorf("get factor %q: %w", factorRef.FactorID, err)
		}
		if !isActive(factor.Status) {
			return CompiledStrategy{}, fmt.Errorf("factor %q is not enabled", factor.ID)
		}
		output, outputErr := selectOutput(factor, factorRef.Output)
		if outputErr != nil {
			return CompiledStrategy{}, outputErr
		}
		bindings, err := c.Factors.ListBindings(ctx, factor.ID)
		if err != nil {
			return CompiledStrategy{}, fmt.Errorf("list bindings for factor %q: %w", factor.ID, err)
		}
		binding, err := chooseBinding(bindings, factor.ID, spaceID, manifest.Input.SourceViewID, manifest.Input.DataFrequency)
		if err != nil {
			return CompiledStrategy{}, err
		}
		resultView, err := c.Storage.GetView(ctx, binding.ResultViewID)
		if err != nil {
			return CompiledStrategy{}, fmt.Errorf("get result view %q: %w", binding.ResultViewID, err)
		}
		if !isActive(resultView.Status) {
			return CompiledStrategy{}, fmt.Errorf("factor %q result view %q is not active", factor.ID, binding.ResultViewID)
		}
		columns, err := c.Storage.ListViewColumns(ctx, binding.ResultViewID)
		if err != nil {
			return CompiledStrategy{}, fmt.Errorf("list result view %q columns: %w", binding.ResultViewID, err)
		}
		column, ok := findFactorColumn(columns, factor.ID, output)
		if !ok {
			return CompiledStrategy{}, fmt.Errorf("factor %q output %q is missing from result view %q", factor.ID, output, binding.ResultViewID)
		}
		compiled.Factors = append(compiled.Factors, CompiledFactor{
			FactorID: factor.ID, SourceHash: factor.SourceHash,
			InputColumns: append([]string(nil), factor.InputColumns...), ParamsJSON: factor.ParamsJSON,
			LookbackPeriods: factor.LookbackPeriods, BindingID: binding.ID, ResultDatasetID: binding.ResultDatasetID,
			Frequency: binding.Frequency, ResultViewID: binding.ResultViewID, Output: output, ColumnName: column.Name,
			SubjectMode: binding.SubjectMode, SubjectsJSON: binding.SubjectsJSON,
		})
		viewIDs[binding.ResultViewID] = struct{}{}
	}
	if len(viewIDs) > 1 {
		return CompiledStrategy{}, fmt.Errorf("strategy must use one factor result view; found %d", len(viewIDs))
	}
	sort.Slice(compiled.Factors, func(i, j int) bool { return compiled.Factors[i].FactorID < compiled.Factors[j].FactorID })
	for viewID := range viewIDs {
		compiled.Dependencies.FactorResultViewIDs = append(compiled.Dependencies.FactorResultViewIDs, viewID)
	}
	sort.Strings(compiled.Dependencies.FactorResultViewIDs)
	raw, err := json.Marshal(compiled)
	if err != nil {
		return CompiledStrategy{}, fmt.Errorf("marshal compiled strategy: %w", err)
	}
	sum := sha256.Sum256(raw)
	compiled.CompiledJSON = raw
	compiled.Hash = hex.EncodeToString(sum[:])
	return compiled, nil
}

func selectOutput(factor FactorDescriptor, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		for _, output := range factor.Outputs {
			if strings.TrimSpace(output) == requested {
				return requested, nil
			}
		}
		return "", fmt.Errorf("factor %q output %q is not declared", factor.ID, requested)
	}
	if len(factor.Outputs) != 1 || strings.TrimSpace(factor.Outputs[0]) == "" {
		return "", fmt.Errorf("factor %q has multiple outputs; input.factors[].output is required", factor.ID)
	}
	return strings.TrimSpace(factor.Outputs[0]), nil
}

func normalizeSideWeights(compiled *CompiledStrategy) {
	values := make([]quant.Decimal, 0, 2)
	if compiled.Long != nil {
		if value, err := quant.Parse(compiled.Long.SideWeight); err == nil && !value.IsZero() {
			values = append(values, value)
		}
	}
	if compiled.Short != nil {
		if value, err := quant.Parse(compiled.Short.SideWeight); err == nil && !value.IsZero() {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		return
	}
	total := quant.Zero()
	for _, value := range values {
		total = total.Add(value)
	}
	index := 0
	if compiled.Long != nil && !sideZero(compiled.Long.SideWeight) {
		compiled.Long.SideWeight = values[index].Div(total).String()
		index++
	}
	if compiled.Short != nil && !sideZero(compiled.Short.SideWeight) {
		compiled.Short.SideWeight = values[index].Div(total).String()
	}
}

func sideZero(raw string) bool { value, err := quant.Parse(raw); return err != nil || value.IsZero() }

func Compile(ctx context.Context, manifest config.Manifest, spaceID string, deps Dependencies) (CompiledStrategy, error) {
	return (Compiler{Factors: deps, Storage: deps}).Compile(ctx, manifest, spaceID)
}

func chooseBinding(bindings []BindingDescriptor, factorID, spaceID, sourceViewID, frequency string) (BindingDescriptor, error) {
	matches := make([]BindingDescriptor, 0, len(bindings))
	for _, binding := range bindings {
		if binding.FactorID == factorID && binding.SpaceID == spaceID && binding.SourceViewID == sourceViewID && binding.Frequency == frequency && isActive(binding.Status) {
			matches = append(matches, binding)
		}
	}
	if len(matches) == 0 {
		return BindingDescriptor{}, fmt.Errorf("factor %q has no enabled binding for space/view/frequency", factorID)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	return matches[0], nil
}

func findFactorColumn(columns []ViewColumn, factorID, output string) (ViewColumn, bool) {
	for _, column := range columns {
		if column.Attributes["origin_factor_id"] == factorID && column.Attributes["factor_output"] == output {
			return column, true
		}
	}
	return ViewColumn{}, false
}

func isActive(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "active", "enabled":
		return status != ""
	default:
		return false
	}
}
