package compiler

import (
	"context"
	"reflect"
	"regexp"

	"github.com/mooyang-code/moox/modules/strategy/internal/config"
)

var normalizerFieldRef = regexp.MustCompile(`\b(?:pct_rank|zscore)\s*\(\s*([A-Za-z_][A-Za-z0-9_]*)`)

// ValidateDSL performs syntax and stage checks without requiring an instance
// binding.  Factor names used through bars[0]/bars[-1] are collected from the
// document first; their concrete type and provenance are still verified by
// CompileWithBindings when an instance is enabled.
func (c Compiler) ValidateDSL(_ context.Context, dsl config.DSL) error {
	if err := config.Validate(&dsl); err != nil {
		return err
	}
	fields := map[string]reflect.Type{}
	for _, rule := range dsl.Rules {
		for _, expression := range ruleExpressions(rule) {
			for _, match := range barFieldRef.FindAllStringSubmatch(expression, -1) {
				fields[match[2]] = reflect.TypeOf(float64(0))
			}
			for _, match := range normalizerFieldRef.FindAllStringSubmatch(expression, -1) {
				fields[match[1]] = reflect.TypeOf(float64(0))
			}
		}
	}
	for name, rule := range dsl.Rules {
		if _, err := compileRule(name, rule, fields); err != nil {
			return err
		}
	}
	return nil
}

func ruleExpressions(rule config.Rule) []string {
	result := []string{rule.FilterBefore, rule.Score}
	if rule.Select != nil {
		result = append(result, rule.Select.Where)
	}
	if rule.Signals != nil {
		result = append(result, rule.Signals.Entry, rule.Signals.Exit)
	}
	result = append(result, rule.FilterAfter)
	return result
}
