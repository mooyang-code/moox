package compiler

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/config"
)

// Compile builds the in-memory execution programs for one strategy DSL.  It
// never persists an artifact; callers persist the original dsl_yaml and call
// Compile again when an instance is enabled or the process restarts.
func (c Compiler) Compile(_ context.Context, dsl config.DSL, spaceID string) (CompiledStrategy, error) {
	if strings.TrimSpace(spaceID) == "" {
		return CompiledStrategy{}, errors.New("strategy compiler space_id is required")
	}
	if err := config.Validate(&dsl); err != nil {
		return CompiledStrategy{}, err
	}
	compiled := CompiledStrategy{
		Name:        dsl.Name,
		SpaceID:     spaceID,
		Data:        dsl.Data,
		Triggers:    dsl.Triggers,
		InputFields: cloneTypes(c.InputFields),
		Rules:       make([]CompiledRule, 0, len(dsl.Rules)),
		APIVersion:  config.APIVersion,
		Kind:        config.Kind,
		Readiness:   "strict",
	}
	if dsl.Triggers.Schedule != nil {
		compiled.Schedule = CompiledSchedule{Every: dsl.Data.Bar}
	}
	// Compatibility marker only; the new store persists DSL text, not this
	// field. It lets older embedders continue to construct a compiled value.
	compiled.CompiledJSON = []byte(`{"dsl":true}`)
	names := make([]string, 0, len(dsl.Rules))
	for name := range dsl.Rules {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		rule := dsl.Rules[name]
		compiledRule, err := compileRule(name, rule, c.InputFields)
		if err != nil {
			return CompiledStrategy{}, err
		}
		compiled.Rules = append(compiled.Rules, compiledRule)
		for _, expression := range []*CompiledExpression{compiledRule.FilterBefore, compiledRule.Score, compiledRule.SelectWhere, compiledRule.SignalEntry, compiledRule.SignalExit, compiledRule.FilterAfter} {
			if expression == nil {
				continue
			}
			if compiled.InputFields == nil {
				compiled.InputFields = make(map[string]reflect.Type)
			}
			for _, field := range expression.Dependencies.Fields {
				if _, exists := compiled.InputFields[field]; exists {
					continue
				}
				if field == "instrument_id" {
					compiled.InputFields[field] = reflect.TypeOf("")
				} else {
					compiled.InputFields[field] = reflect.TypeOf(float64(0))
				}
			}
			for _, fields := range expression.Dependencies.Bars {
				for _, field := range fields {
					if _, exists := compiled.InputFields[field]; !exists {
						compiled.InputFields[field] = reflect.TypeOf(float64(0))
					}
				}
			}
		}
	}
	return compiled, nil
}

func Compile(ctx context.Context, dsl config.DSL, spaceID string, deps Dependencies) (CompiledStrategy, error) {
	return (Compiler{Factors: deps, Storage: deps}).Compile(ctx, dsl, spaceID)
}

func compileRule(name string, rule config.Rule, fields map[string]reflect.Type) (CompiledRule, error) {
	result := CompiledRule{Name: name, Definition: rule}
	var err error
	if rule.FilterBefore != "" {
		result.FilterBefore, err = compileRuleExpression(name, rule.FilterBefore, StageFilterBefore, fields)
		if err != nil {
			return CompiledRule{}, err
		}
	}
	if rule.Score != "" {
		result.Score, err = compileRuleExpression(name, rule.Score, StageScore, fields)
		if err != nil {
			return CompiledRule{}, err
		}
	}
	if rule.Select != nil && rule.Select.Where != "" {
		result.SelectWhere, err = compileRuleExpression(name, rule.Select.Where, StageSelectWhere, fields)
		if err != nil {
			return CompiledRule{}, err
		}
	}
	if rule.Signals != nil {
		result.SignalEntry, err = compileRuleExpression(name, rule.Signals.Entry, StageSignalEntry, fields)
		if err != nil {
			return CompiledRule{}, err
		}
		result.SignalExit, err = compileRuleExpression(name, rule.Signals.Exit, StageSignalExit, fields)
		if err != nil {
			return CompiledRule{}, err
		}
	}
	if rule.FilterAfter != "" {
		result.FilterAfter, err = compileRuleExpression(name, rule.FilterAfter, StageFilterAfter, fields)
		if err != nil {
			return CompiledRule{}, err
		}
	}
	if rule.Signals != nil && ((result.FilterAfter != nil && result.FilterAfter.Dependencies.UsesScore) || (result.SignalEntry != nil && result.SignalEntry.Dependencies.UsesScore) || (result.SignalExit != nil && result.SignalExit.Dependencies.UsesScore)) {
		return CompiledRule{}, fmt.Errorf("strategy DSL rules.%s signals and filter_after cannot reference score", name)
	}
	return result, nil
}

func compileRuleExpression(ruleName string, source string, stage ExpressionStage, fields map[string]reflect.Type) (*CompiledExpression, error) {
	compiled, err := CompileExpression(source, stage, fields)
	if err != nil {
		return nil, fmt.Errorf("strategy DSL rules.%s.%s: %w", ruleName, stage, err)
	}
	return &compiled, nil
}

func cloneTypes(values map[string]reflect.Type) map[string]reflect.Type {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]reflect.Type, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
