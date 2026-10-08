package dsl

import (
	"fmt"
)

// CompiledRule 是一条规则及其各阶段已编译的表达式。
type CompiledRule struct {
	Rule        Rule
	Filter      *Expression
	Score       *Expression
	SelectWhere *Expression
	FilterAfter *Expression
	Entry       *Expression
	Exit        *Expression
}

// Expressions 按固定顺序返回非空的阶段表达式。
func (r *CompiledRule) Expressions() []*Expression {
	out := make([]*Expression, 0, 6)
	for _, expression := range []*Expression{r.Filter, r.Score, r.SelectWhere, r.FilterAfter, r.Entry, r.Exit} {
		if expression != nil {
			out = append(out, expression)
		}
	}
	return out
}

// Columns 返回该规则引用的全部当期列。
func (r *CompiledRule) Columns() []string {
	set := make(map[string]struct{})
	for _, expression := range r.Expressions() {
		for _, column := range expression.AllColumns() {
			set[column] = struct{}{}
		}
	}
	return sortedKeys(set)
}

// PreviousColumns 返回该规则通过 bars[-1] 引用的上一根列。
func (r *CompiledRule) PreviousColumns() []string {
	set := make(map[string]struct{})
	for _, expression := range r.Expressions() {
		for _, column := range expression.AllPreviousColumns() {
			set[column] = struct{}{}
		}
	}
	return sortedKeys(set)
}

// Program 是启用实例时编译出的可执行策略；它只存在于内存，重启后由 DSL 文本与 View 列重新编译。
type Program struct {
	Strategy        Strategy
	Rules           []CompiledRule
	Columns         []string
	PreviousColumns []string
	UsesPreviousBar bool
}

// Compile 用绑定 View 的列编译全部表达式。引用不存在的列、类型不匹配都会在这里报错。
func Compile(strategy Strategy, columns []string) (*Program, error) {
	if err := Validate(&strategy); err != nil {
		return nil, err
	}
	available := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		available[column] = struct{}{}
	}
	program := &Program{Strategy: strategy, Rules: make([]CompiledRule, 0, len(strategy.Rules))}
	allColumns := make(map[string]struct{})
	allPrevious := make(map[string]struct{})
	for _, rule := range strategy.Rules {
		compiled := CompiledRule{Rule: rule}
		var err error
		stages := []struct {
			stage  Stage
			source string
			target **Expression
		}{
			{StageFilter, rule.Filter, &compiled.Filter},
			{StageScore, rule.Score, &compiled.Score},
			{StageSelectWhere, rule.Select.Where, &compiled.SelectWhere},
			{StageFilterAfter, rule.FilterAfter, &compiled.FilterAfter},
			{StageEntry, rule.Entry, &compiled.Entry},
			{StageExit, rule.Exit, &compiled.Exit},
		}
		for _, item := range stages {
			if item.source == "" {
				continue
			}
			*item.target, err = CompileExpression(item.source, item.stage, available)
			if err != nil {
				return nil, fmt.Errorf("规则 %s 的 %s 表达式无效：%w", rule.ID, item.stage, err)
			}
		}
		for _, column := range compiled.Columns() {
			allColumns[column] = struct{}{}
		}
		for _, column := range compiled.PreviousColumns() {
			allPrevious[column] = struct{}{}
		}
		program.Rules = append(program.Rules, compiled)
	}
	program.Columns = sortedKeys(allColumns)
	program.PreviousColumns = sortedKeys(allPrevious)
	program.UsesPreviousBar = len(program.PreviousColumns) > 0
	return program, nil
}

// ReferencedColumns 在不知道 View 列的情况下返回 DSL 引用的全部列名（当期与上一根的并集）。
// 用于启用前的列解析与提示。
func ReferencedColumns(strategy Strategy) ([]string, error) {
	set := make(map[string]struct{})
	for _, rule := range strategy.Rules {
		for stage, source := range map[Stage]string{StageFilter: rule.Filter, StageScore: rule.Score, StageSelectWhere: rule.Select.Where, StageFilterAfter: rule.FilterAfter, StageEntry: rule.Entry, StageExit: rule.Exit} {
			if source == "" {
				continue
			}
			expression, err := Analyze(source, stage)
			if err != nil {
				return nil, fmt.Errorf("规则 %s 的 %s 表达式无效：%w", rule.ID, stage, err)
			}
			for _, column := range expression.AllColumns() {
				set[column] = struct{}{}
			}
			for _, column := range expression.AllPreviousColumns() {
				set[column] = struct{}{}
			}
		}
	}
	return sortedKeys(set), nil
}
