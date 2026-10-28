package dsl

import (
	"fmt"
	"sort"
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
	// TrancheGroups 是 tranche 规则按排名签名分好的子策略组（见 tranche.go）；其他类型为空。
	TrancheGroups []TrancheGroup
}

// stageSource 是规则一个阶段的表达式文本。
type stageSource struct {
	stage  Stage
	source string
}

// stageSources 按固定顺序返回规则各阶段的表达式文本（可能为空）。
func (r Rule) stageSources() []stageSource {
	return []stageSource{
		{StageFilter, r.Filter},
		{StageScore, r.Score},
		{StageSelectWhere, r.Select.Where},
		{StageFilterAfter, r.FilterAfter},
		{StageEntry, r.Entry},
		{StageExit, r.Exit},
	}
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
	if r.Rule.Type == RuleTypeTranche {
		return r.Rule.TrancheColumns()
	}
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
	Strategy Strategy
	Rules    []CompiledRule
	// Params 是 factor_params 的已编译表达式（按成员键、参数名排序）。
	Params []CompiledParam
	// ParamRefs 是规则与参数表达式里 params(成员键, 参数名) 引用的全部参数（去重排序）；非空时求值需要读取 __params 列。
	ParamRefs       []ParamRef
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
	refs := make(map[ParamRef]struct{})
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
		if rule.Type == RuleTypeTranche {
			for _, column := range rule.TrancheColumns() {
				if _, ok := available[column]; !ok {
					return nil, fmt.Errorf("规则 %s 引用的列 %q 不存在于绑定的 View", rule.ID, column)
				}
			}
			compiled.TrancheGroups = rule.Tranche.TrancheGroups()
		}
		for _, column := range compiled.Columns() {
			allColumns[column] = struct{}{}
		}
		// 止损的价格列是可选列：一并读取，但不进规则的必需列（缺值不算缺数）；View 必须提供它们。
		for _, column := range rule.StopColumns() {
			if _, ok := available[column]; !ok {
				return nil, fmt.Errorf("规则 %s 的 stop 需要列 %q，绑定的 View 没有", rule.ID, column)
			}
			allColumns[column] = struct{}{}
		}
		for _, column := range compiled.PreviousColumns() {
			allPrevious[column] = struct{}{}
		}
		for _, expression := range compiled.Expressions() {
			for _, ref := range expression.AllParamRefs() {
				refs[ref] = struct{}{}
			}
		}
		program.Rules = append(program.Rules, compiled)
	}
	params, err := compileParams(strategy, available)
	if err != nil {
		return nil, err
	}
	for _, param := range params {
		for _, column := range param.Columns() {
			allColumns[column] = struct{}{}
		}
		for _, column := range param.PreviousColumns() {
			allPrevious[column] = struct{}{}
		}
		for _, expression := range []*Expression{param.Default, param.PerSubject} {
			for _, ref := range expression.AllParamRefs() {
				refs[ref] = struct{}{}
			}
		}
	}
	program.Params = params
	program.ParamRefs = sortedParamRefs(refs)
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
		for _, column := range rule.StopColumns() {
			set[column] = struct{}{}
		}
		if rule.Type == RuleTypeTranche {
			for _, column := range rule.TrancheColumns() {
				set[column] = struct{}{}
			}
			continue
		}
		for _, item := range rule.stageSources() {
			if item.source == "" {
				continue
			}
			expression, err := Analyze(item.source, item.stage)
			if err != nil {
				return nil, fmt.Errorf("规则 %s 的 %s 表达式无效：%w", rule.ID, item.stage, err)
			}
			for _, column := range expression.AllColumns() {
				set[column] = struct{}{}
			}
			for _, column := range expression.AllPreviousColumns() {
				set[column] = struct{}{}
			}
		}
	}
	if err := referencedParamExprColumns(strategy, set); err != nil {
		return nil, err
	}
	return sortedKeys(set), nil
}

// ReferencedParamRefs 在不知道任务的情况下返回 DSL 里 params(成员键, 参数名) 引用的全部参数（去重排序），
// 以及 factor_params 输出的参数；用于启用前对照任务的成员与定义的参数声明做检查。
func ReferencedParamRefs(strategy Strategy) ([]ParamRef, error) {
	refs := make(map[ParamRef]struct{})
	collect := func(source string, stage Stage, where string) error {
		if source == "" {
			return nil
		}
		expression, err := Analyze(source, stage)
		if err != nil {
			return fmt.Errorf("%s 无效：%w", where, err)
		}
		for _, ref := range expression.AllParamRefs() {
			refs[ref] = struct{}{}
		}
		return nil
	}
	for _, rule := range strategy.Rules {
		for _, item := range rule.stageSources() {
			if err := collect(item.source, item.stage, fmt.Sprintf("规则 %s 的 %s 表达式", rule.ID, item.stage)); err != nil {
				return nil, err
			}
		}
	}
	for _, output := range strategy.FactorParamOutputs() {
		where := "factor_params." + output.Member + "." + output.Name
		if err := collect(output.Expr.Default, StageScore, where+".default"); err != nil {
			return nil, err
		}
		if err := collect(output.Expr.PerSubject, StageScore, where+".per_subject"); err != nil {
			return nil, err
		}
	}
	return sortedParamRefs(refs), nil
}

func sortedParamRefs(set map[ParamRef]struct{}) []ParamRef {
	refs := make([]ParamRef, 0, len(set))
	for ref := range set {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Member != refs[j].Member {
			return refs[i].Member < refs[j].Member
		}
		return refs[i].Name < refs[j].Name
	})
	return refs
}
