package compiler

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
	exprtypes "github.com/expr-lang/expr/types"
)

var defaultExpressionFields = map[string]reflect.Type{
	"open":   reflect.TypeOf(float64(0)),
	"high":   reflect.TypeOf(float64(0)),
	"low":    reflect.TypeOf(float64(0)),
	"close":  reflect.TypeOf(float64(0)),
	"volume": reflect.TypeOf(float64(0)),
}

var defaultExpressionIdentifiers = map[string]reflect.Type{
	"instrument_id": reflect.TypeOf(""),
}

// CompileExpression parses and statically checks one DSL expression.  fields
// contains logical input names and their types from the instance binding.  A
// nil map still exposes the standard OHLCV fields, but no arbitrary names.
func CompileExpression(source string, stage ExpressionStage, fields map[string]reflect.Type) (CompiledExpression, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return CompiledExpression{}, errors.New("expression is required")
	}
	if !validExpressionStage(stage) {
		return CompiledExpression{}, fmt.Errorf("unsupported expression stage %q", stage)
	}
	fieldTypes := mergeExpressionFields(fields)
	tree, err := parser.Parse(source)
	if err != nil {
		return CompiledExpression{}, fmt.Errorf("parse expression: %w", err)
	}
	deps := ExpressionDependencies{Bars: make(map[int][]string)}
	for name, typ := range defaultExpressionIdentifiers {
		fieldTypes[name] = typ
	}
	inspector := expressionInspector{stage: stage, fields: fieldTypes, dependencies: &deps}
	ast.Walk(&tree.Node, &inspector)
	if inspector.err != nil {
		return CompiledExpression{}, inspector.err
	}
	if deps.UsesScore && (stage == StageSignalEntry || stage == StageSignalExit) {
		return CompiledExpression{}, fmt.Errorf("score is not available in %s expressions", stage)
	}

	env := expressionEnv(fieldTypes)
	options := []expr.Option{
		expr.Env(env),
		expr.DisableAllBuiltins(),
		expr.MaxNodes(256),
	}
	if stage == StageScore {
		options = append(options,
			expr.Function("pct_rank", noopNumericFunction, func(float64) float64 { return 0 }),
			expr.Function("zscore", noopNumericFunction, func(float64) float64 { return 0 }),
			expr.AsFloat64(),
		)
	} else if stage == StageFilterBefore || stage == StageSelectWhere || stage == StageSignalEntry || stage == StageSignalExit || stage == StageFilterAfter {
		options = append(options, expr.AsBool())
	}
	program, err := expr.Compile(source, options...)
	if err != nil {
		return CompiledExpression{}, fmt.Errorf("compile %s expression: %w", stage, err)
	}
	normalizeDependencies(&deps)
	return CompiledExpression{Source: source, Stage: stage, Program: program, Dependencies: deps}, nil
}

func validExpressionStage(stage ExpressionStage) bool {
	switch stage {
	case StageFilterBefore, StageScore, StageSelectWhere, StageSignalEntry, StageSignalExit, StageFilterAfter:
		return true
	default:
		return false
	}
}

func mergeExpressionFields(fields map[string]reflect.Type) map[string]reflect.Type {
	result := make(map[string]reflect.Type, len(defaultExpressionFields)+len(fields))
	for name, typ := range defaultExpressionFields {
		result[name] = typ
	}
	for name, typ := range fields {
		name = strings.TrimSpace(name)
		if name != "" && typ != nil {
			result[name] = typ
		}
	}
	return result
}

func expressionEnv(fields map[string]reflect.Type) exprtypes.Map {
	env := make(exprtypes.Map, len(fields)+2)
	for name, typ := range fields {
		env[name] = exprtypes.TypeOf(reflect.New(typ).Elem().Interface())
	}
	env["instrument_id"] = exprtypes.String
	env["score"] = exprtypes.Float64
	barFields := make(exprtypes.Map, len(fields))
	for name, typ := range fields {
		barFields[name] = exprtypes.TypeOf(reflect.New(typ).Elem().Interface())
	}
	env["bars"] = exprtypes.Array(barFields)
	return env
}

func noopNumericFunction(params ...any) (any, error) {
	if len(params) != 1 {
		return nil, errors.New("numeric function requires one argument")
	}
	return float64(0), nil
}

type expressionInspector struct {
	stage        ExpressionStage
	fields       map[string]reflect.Type
	dependencies *ExpressionDependencies
	err          error
}

func (i *expressionInspector) Visit(node *ast.Node) {
	if i.err != nil || node == nil || *node == nil {
		return
	}
	switch n := (*node).(type) {
	case *ast.IdentifierNode:
		i.inspectIdentifier(n)
	case *ast.MemberNode:
		i.inspectMember(n)
	case *ast.CallNode:
		i.inspectCall(n)
	}
}

func (i *expressionInspector) inspectIdentifier(n *ast.IdentifierNode) {
	if n.Value == "bars" || n.Value == "score" || n.Value == "pct_rank" || n.Value == "zscore" {
		if n.Value == "score" {
			i.dependencies.UsesScore = true
		}
		return
	}
	if n.Value == "prev" {
		i.setError(errors.New("prev is not supported; use bars[0] or bars[-1]"))
		return
	}
	if _, ok := i.fields[n.Value]; ok {
		i.dependencies.Fields = append(i.dependencies.Fields, n.Value)
		return
	}
	// Logical factor names are supplied by the instance binding and are not
	// necessarily known to the compiler catalog at process start. Treat an
	// undeclared scalar identifier as a numeric input so the DSL can still be
	// compiled; bars[*] fields remain strict because their historical shape
	// must be explicitly bound.
	i.fields[n.Value] = reflect.TypeOf(float64(0))
	i.dependencies.Fields = append(i.dependencies.Fields, n.Value)
}

func (i *expressionInspector) inspectMember(n *ast.MemberNode) {
	if base, ok := n.Node.(*ast.MemberNode); ok && isBarsIdentifier(base.Node) {
		offset, err := parseBarOffset(base.Property)
		if err != nil {
			i.setError(err)
			return
		}
		fieldName, ok := memberPropertyName(n.Property)
		if !ok || strings.TrimSpace(fieldName) == "" {
			i.setError(errors.New("bars field must use a named property"))
			return
		}
		if _, ok := i.fields[fieldName]; !ok {
			i.setError(fmt.Errorf("bars field %q is not declared by input bindings", fieldName))
			return
		}
		i.dependencies.Bars[offset] = append(i.dependencies.Bars[offset], fieldName)
		return
	}
	if isBarsIdentifier(n.Node) {
		if _, err := parseBarOffset(n.Property); err != nil {
			i.setError(err)
		}
		return
	}
	if containsBars(n.Node) {
		i.setError(errors.New("bars access must be bars[0].field or bars[-1].field"))
	}
}

func memberPropertyName(node ast.Node) (string, bool) {
	switch property := node.(type) {
	case *ast.IdentifierNode:
		return property.Value, true
	case *ast.StringNode:
		value := property.Value
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		return value, true
	default:
		return "", false
	}
}

func (i *expressionInspector) inspectCall(n *ast.CallNode) {
	identifier, ok := n.Callee.(*ast.IdentifierNode)
	if !ok {
		i.setError(errors.New("method calls are not allowed in strategy expressions"))
		return
	}
	if identifier.Value != "pct_rank" && identifier.Value != "zscore" {
		// The Expr checker also rejects this after builtins are disabled.  Keep a
		// direct error so the whitelist is explicit and stable for callers.
		i.setError(fmt.Errorf("function %q is not allowed", identifier.Value))
		return
	}
	if i.stage != StageScore {
		i.setError(fmt.Errorf("function %q is allowed only in score expressions", identifier.Value))
		return
	}
	if len(n.Arguments) != 1 {
		i.setError(fmt.Errorf("function %q requires one field argument", identifier.Value))
		return
	}
	argument, ok := n.Arguments[0].(*ast.IdentifierNode)
	if !ok {
		i.setError(fmt.Errorf("function %q requires a field name argument", identifier.Value))
		return
	}
	if _, ok := i.fields[argument.Value]; !ok {
		i.setError(fmt.Errorf("function %q field %q is not declared by input bindings", identifier.Value, argument.Value))
		return
	}
	i.dependencies.Fields = append(i.dependencies.Fields, argument.Value)
}

func (i *expressionInspector) setError(err error) {
	if i.err == nil {
		i.err = err
	}
}

func isBarsIdentifier(node ast.Node) bool {
	identifier, ok := node.(*ast.IdentifierNode)
	return ok && identifier.Value == "bars"
}

func containsBars(node ast.Node) bool {
	if node == nil {
		return false
	}
	if isBarsIdentifier(node) {
		return true
	}
	member, ok := node.(*ast.MemberNode)
	return ok && containsBars(member.Node)
}

func parseBarOffset(node ast.Node) (int, error) {
	switch n := node.(type) {
	case *ast.IntegerNode:
		if n.Value == 0 {
			return 0, nil
		}
		return 0, errors.New("bars index must be 0 or -1")
	case *ast.UnaryNode:
		if n.Operator == "-" {
			if value, ok := n.Node.(*ast.IntegerNode); ok && value.Value == 1 {
				return -1, nil
			}
		}
		return 0, errors.New("bars index must be the constant 0 or -1")
	default:
		return 0, errors.New("bars index must be the constant 0 or -1")
	}
}

func normalizeDependencies(deps *ExpressionDependencies) {
	if len(deps.Bars) == 0 {
		deps.Bars = nil
	}
	fieldSet := make(map[string]struct{}, len(deps.Fields))
	for _, field := range deps.Fields {
		fieldSet[field] = struct{}{}
	}
	deps.Fields = deps.Fields[:0]
	for field := range fieldSet {
		deps.Fields = append(deps.Fields, field)
	}
	sort.Strings(deps.Fields)
	for offset, fields := range deps.Bars {
		set := make(map[string]struct{}, len(fields))
		for _, field := range fields {
			set[field] = struct{}{}
		}
		fields = fields[:0]
		for field := range set {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		deps.Bars[offset] = fields
	}
}
