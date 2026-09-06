package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/packages/report"
)

// InstanceBindings is the concrete source/factor selection stored on one
// strategy instance. The shared DSL remains independent of these IDs.
type InstanceBindings struct {
	SourceViewID  string
	Frequency     string
	Factors       []CompiledFactor
	FactorViewIDs []string
}

func ParseInstanceBindings(raw []byte) (InstanceBindings, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return InstanceBindings{}, nil
	}
	if !json.Valid(raw) {
		return InstanceBindings{}, fmt.Errorf("strategy instance input_bindings_json must be valid JSON")
	}
	var value struct {
		SourceViewID string   `json:"source_view_id"`
		ViewID       string   `json:"view_id"`
		Frequency    string   `json:"frequency"`
		FactorViews  []string `json:"factor_view_ids"`
		Factors      []struct {
			FactorID        string   `json:"factor_id"`
			SourceHash      string   `json:"source_hash"`
			InputColumns    []string `json:"input_columns"`
			ParamsJSON      string   `json:"params_json"`
			LookbackPeriods int      `json:"lookback_periods"`
			BindingID       string   `json:"binding_id"`
			Frequency       string   `json:"frequency"`
			ResultDatasetID string   `json:"result_dataset_id"`
			ResultViewID    string   `json:"result_view_id"`
			Output          string   `json:"output"`
			ColumnName      string   `json:"column_name"`
			SubjectMode     string   `json:"subject_mode"`
			SubjectsJSON    string   `json:"subjects_json"`
		} `json:"factors"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return InstanceBindings{}, fmt.Errorf("strategy instance input_bindings_json must be an object: %w", err)
	}
	if strings.TrimSpace(value.SourceViewID) == "" {
		value.SourceViewID = value.ViewID
	}
	result := InstanceBindings{SourceViewID: strings.TrimSpace(value.SourceViewID), Frequency: strings.TrimSpace(value.Frequency)}
	for _, viewID := range value.FactorViews {
		if viewID = strings.TrimSpace(viewID); viewID != "" {
			result.FactorViewIDs = append(result.FactorViewIDs, viewID)
		}
	}
	for _, factor := range value.Factors {
		result.Factors = append(result.Factors, CompiledFactor{
			FactorID: factor.FactorID, SourceHash: factor.SourceHash, InputColumns: append([]string(nil), factor.InputColumns...), ParamsJSON: factor.ParamsJSON,
			LookbackPeriods: factor.LookbackPeriods, BindingID: factor.BindingID, Frequency: factor.Frequency,
			ResultDatasetID: factor.ResultDatasetID, ResultViewID: factor.ResultViewID, Output: factor.Output,
			ColumnName: factor.ColumnName, SubjectMode: factor.SubjectMode, SubjectsJSON: factor.SubjectsJSON,
		})
	}
	return result, nil
}

// CompileWithBindings compiles expressions with the fields exposed by the
// instance's concrete factor bindings and attaches those bindings to the
// artifact so VerifyDependencies can be run before enabling the instance.
func (c Compiler) CompileWithBindings(ctx context.Context, dsl config.DSL, spaceID string, raw []byte) (CompiledStrategy, error) {
	binding, err := ParseInstanceBindings(raw)
	if err != nil {
		return CompiledStrategy{}, err
	}
	if isSourceReadyEvent(dsl) && (len(binding.Factors) > 0 || len(binding.FactorViewIDs) > 0) {
		return CompiledStrategy{}, fmt.Errorf("strategy trigger source.ready cannot be used with factor bindings; use factor.ready")
	}
	if dsl.Triggers.Schedule != nil && dsl.Triggers.Event == nil && (len(binding.Factors) > 0 || len(binding.FactorViewIDs) > 0) {
		return CompiledStrategy{}, fmt.Errorf("scheduled strategies with factor bindings require a factor.ready event trigger")
	}
	if err := validateFactorAliases(binding.Factors); err != nil {
		return CompiledStrategy{}, err
	}
	if resultViews := uniqueResultViews(binding.Factors); len(resultViews) > 1 {
		return CompiledStrategy{}, fmt.Errorf("strategy factor bindings must share one result_view_id, got %s", strings.Join(resultViews, ", "))
	} else if len(resultViews) == 1 && strings.TrimSpace(binding.SourceViewID) != "" && resultViews[0] == strings.TrimSpace(binding.SourceViewID) {
		return CompiledStrategy{}, fmt.Errorf("strategy source_view_id must differ from factor result_view_id %q", resultViews[0])
	}
	fields := cloneTypes(c.InputFields)
	if fields == nil {
		fields = map[string]reflect.Type{}
	}
	for _, factor := range binding.Factors {
		for _, name := range []string{factor.FactorID, factor.Output, factor.ColumnName} {
			if name = strings.TrimSpace(name); name != "" {
				fields[name] = reflect.TypeOf(float64(0))
			}
		}
		for _, name := range factor.InputColumns {
			if name = strings.TrimSpace(name); name != "" {
				fields[name] = reflect.TypeOf(float64(0))
			}
		}
	}
	clone := c
	clone.InputFields = fields
	compiled, err := clone.Compile(ctx, dsl, spaceID)
	if err != nil {
		return CompiledStrategy{}, err
	}
	if err := rejectUndeclaredFields(compiled, fields); err != nil {
		return CompiledStrategy{}, err
	}
	if binding.SourceViewID != "" {
		compiled.SourceView.ID = binding.SourceViewID
	}
	if binding.Frequency != "" {
		frequency, frequencyErr := report.NormalizeDatasetFrequency(binding.Frequency)
		if frequencyErr != nil {
			return CompiledStrategy{}, fmt.Errorf("strategy instance binding frequency: %w", frequencyErr)
		}
		if frequency != dsl.Data.Bar {
			return CompiledStrategy{}, fmt.Errorf("strategy instance binding frequency %s does not match DSL data.bar %s", frequency, dsl.Data.Bar)
		}
		compiled.SourceView.Frequency = frequency
	}
	if compiled.SourceView.Status == "" {
		compiled.SourceView.Status = "active"
	}
	compiled.Factors = append([]CompiledFactor(nil), binding.Factors...)
	for i := range compiled.Factors {
		frequency := dsl.Data.Bar
		if strings.TrimSpace(compiled.Factors[i].Frequency) != "" {
			normalized, frequencyErr := report.NormalizeDatasetFrequency(compiled.Factors[i].Frequency)
			if frequencyErr != nil {
				return CompiledStrategy{}, fmt.Errorf("strategy factor %s binding frequency: %w", compiled.Factors[i].FactorID, frequencyErr)
			}
			frequency = normalized
		}
		if frequency != dsl.Data.Bar {
			return CompiledStrategy{}, fmt.Errorf("strategy factor %s binding frequency %s does not match DSL data.bar %s", compiled.Factors[i].FactorID, frequency, dsl.Data.Bar)
		}
		compiled.Factors[i].Frequency = frequency
	}
	for _, viewID := range binding.FactorViewIDs {
		if !containsString(compiled.Dependencies.FactorResultViewIDs, viewID) {
			compiled.Dependencies.FactorResultViewIDs = append(compiled.Dependencies.FactorResultViewIDs, viewID)
		}
	}
	for _, factor := range compiled.Factors {
		if factor.ResultViewID != "" && !containsString(compiled.Dependencies.FactorResultViewIDs, factor.ResultViewID) {
			compiled.Dependencies.FactorResultViewIDs = append(compiled.Dependencies.FactorResultViewIDs, factor.ResultViewID)
		}
	}
	if compiled.SourceView.Frequency == "" {
		compiled.SourceView.Frequency = compiled.Data.Bar
	}
	return compiled, nil
}

func isSourceReadyEvent(dsl config.DSL) bool {
	if dsl.Triggers.Event == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(dsl.Triggers.Event.Name)) {
	case "viewsourceperiodready", "source.ready", "ready", "event.storage.view.source_period.ready":
		return true
	default:
		return false
	}
}

// validateFactorAliases protects the expression namespace from ambiguous
// bindings. Aliases belonging to one factor may intentionally be synonyms;
// aliases belonging to different factors must remain unique and must not
// shadow the built-in OHLCV/instrument/score fields.
func validateFactorAliases(factors []CompiledFactor) error {
	owners := make(map[string]int)
	sourceFields := make(map[string]struct{})
	for _, factor := range factors {
		for _, raw := range factor.InputColumns {
			if name := strings.TrimSpace(raw); name != "" {
				sourceFields[name] = struct{}{}
			}
		}
	}
	reserved := make(map[string]struct{}, len(defaultExpressionFields)+len(defaultExpressionIdentifiers)+1)
	for name := range defaultExpressionFields {
		reserved[name] = struct{}{}
	}
	for name := range defaultExpressionIdentifiers {
		reserved[name] = struct{}{}
	}
	reserved["score"] = struct{}{}
	for index, factor := range factors {
		for _, raw := range []string{factor.FactorID, factor.Output, factor.ColumnName} {
			name := strings.TrimSpace(raw)
			if name == "" {
				continue
			}
			if _, exists := reserved[name]; exists {
				return fmt.Errorf("strategy factor alias %q conflicts with a built-in field", name)
			}
			if _, exists := sourceFields[name]; exists {
				return fmt.Errorf("strategy factor alias %q conflicts with a source input column", name)
			}
			if owner, exists := owners[name]; exists && owner != index {
				return fmt.Errorf("strategy factor alias %q is used by multiple factors", name)
			}
			owners[name] = index
		}
	}
	return nil
}

func uniqueResultViews(factors []CompiledFactor) []string {
	seen := make(map[string]struct{})
	for _, factor := range factors {
		if value := strings.TrimSpace(factor.ResultViewID); value != "" {
			seen[value] = struct{}{}
		}
	}
	values := make([]string, 0, len(seen))
	for value := range seen {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func rejectUndeclaredFields(compiled CompiledStrategy, declared map[string]reflect.Type) error {
	allowed := make(map[string]struct{}, len(declared)+len(defaultExpressionFields)+len(defaultExpressionIdentifiers))
	for name := range declared {
		allowed[name] = struct{}{}
	}
	for name := range defaultExpressionFields {
		allowed[name] = struct{}{}
	}
	for name := range defaultExpressionIdentifiers {
		allowed[name] = struct{}{}
	}
	for _, rule := range compiled.Rules {
		expressions := []*CompiledExpression{rule.FilterBefore, rule.Score, rule.SelectWhere, rule.SignalEntry, rule.SignalExit, rule.FilterAfter}
		for _, expression := range expressions {
			if expression == nil {
				continue
			}
			for _, field := range expression.Dependencies.Fields {
				if _, ok := allowed[field]; !ok {
					return fmt.Errorf("strategy expression field %q is not declared by instance bindings", field)
				}
			}
		}
	}
	return nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
