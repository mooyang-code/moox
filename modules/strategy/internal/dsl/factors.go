package dsl

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/packages/factorname"
	"gopkg.in/yaml.v3"
)

// ParamDecl 是因子定义声明的一个可调参数。
type ParamDecl struct {
	Name    string  `json:"name"`
	Type    string  `json:"type"` // int 或 float
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Default float64 `json:"default"`
}

// ParamExpr 是一个参数的输出表达式：Default 是这一期的统一值，PerSubject 可选，按标的求值，与统一值不同的标的进入覆盖。
// 两者都是数值表达式（与 score 相同，可用 rank、zscore 与 bars[-1]）；Default 按标的求值后取中位数，不引用列时就是常数。
type ParamExpr struct {
	Default    string
	PerSubject string
}

// ParamOutput 是一个成员的一个参数的输出。
type ParamOutput struct {
	Member string
	Name   string
	Expr   ParamExpr
}

// FactorParamOutputs 按（成员键、参数名）排序返回全部参数输出。
func (s Strategy) FactorParamOutputs() []ParamOutput {
	out := make([]ParamOutput, 0)
	for member, params := range s.FactorParams {
		for name, expr := range params {
			out = append(out, ParamOutput{Member: member, Name: name, Expr: expr})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Member != out[j].Member {
			return out[i].Member < out[j].Member
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func decodeFactorParams(node *yaml.Node) (map[string]map[string]ParamExpr, error) {
	fields, err := mappingFields(node, "factor_params")
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]ParamExpr, len(fields))
	for _, f := range fields {
		path := "factor_params." + f.key
		params, err := mappingFields(f.value, path)
		if err != nil {
			return nil, err
		}
		values := make(map[string]ParamExpr, len(params))
		for _, param := range params {
			paramPath := path + "." + param.key
			var expr ParamExpr
			if param.value != nil && param.value.Kind == yaml.ScalarNode {
				// 简写：参数直接写一个表达式，等同于 {default: 表达式}。
				expr.Default, err = scalarString(param.value, paramPath)
			} else {
				expr, err = decodeParamExpr(param.value, paramPath)
			}
			if err != nil {
				return nil, err
			}
			values[strings.TrimSpace(param.key)] = expr
		}
		out[strings.TrimSpace(f.key)] = values
	}
	return out, nil
}

func decodeParamExpr(node *yaml.Node, path string) (ParamExpr, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return ParamExpr{}, fmt.Errorf("%s 必须是表达式或 {default, per_subject}", path)
	}
	var expr ParamExpr
	for _, f := range fields {
		switch f.key {
		case "default":
			expr.Default, err = scalarString(f.value, path+".default")
		case "per_subject":
			expr.PerSubject, err = scalarString(f.value, path+".per_subject")
		default:
			return ParamExpr{}, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return ParamExpr{}, err
		}
	}
	return expr, nil
}

// validateFactorParams 校验参数输出：成员键必须是可变成员的合法成员键，参数名合法，表达式是合法的数值表达式。
// 成员是否在关联的任务里、参数名是否属于该因子、取值范围由启用时对照因子定义检查。
func validateFactorParams(s *Strategy) error {
	for _, output := range s.FactorParamOutputs() {
		path := "factor_params." + output.Member + "." + output.Name
		parsed, err := factorname.ParseMemberKey(output.Member)
		if err != nil {
			return fmt.Errorf("factor_params.%s：成员键不合法：%w", output.Member, err)
		}
		if parsed.Kind != factorname.KindVar {
			return fmt.Errorf("factor_params.%s：只有可变成员（形如 因子ID__var）才接受策略提交的参数", output.Member)
		}
		if err := factorname.ValidateParamName(output.Name); err != nil {
			return fmt.Errorf("%s：%w", path, err)
		}
		if strings.TrimSpace(output.Expr.Default) == "" {
			return fmt.Errorf("%s 缺少 default", path)
		}
		if _, err := Analyze(output.Expr.Default, StageScore); err != nil {
			return fmt.Errorf("%s.default 无效：%w", path, err)
		}
		if output.Expr.PerSubject != "" {
			if _, err := Analyze(output.Expr.PerSubject, StageScore); err != nil {
				return fmt.Errorf("%s.per_subject 无效：%w", path, err)
			}
		}
	}
	return nil
}

// CompiledParam 是一个参数输出的已编译表达式。
type CompiledParam struct {
	Member     string
	Name       string
	Default    *Expression
	PerSubject *Expression
}

// Columns 返回参数表达式引用的全部当期列。
func (p CompiledParam) Columns() []string {
	set := map[string]struct{}{}
	for _, expression := range []*Expression{p.Default, p.PerSubject} {
		if expression == nil {
			continue
		}
		for _, column := range expression.AllColumns() {
			set[column] = struct{}{}
		}
	}
	return sortedKeys(set)
}

// PreviousColumns 返回参数表达式经 bars[-1] 引用的列。
func (p CompiledParam) PreviousColumns() []string {
	set := map[string]struct{}{}
	for _, expression := range []*Expression{p.Default, p.PerSubject} {
		if expression == nil {
			continue
		}
		for _, column := range expression.AllPreviousColumns() {
			set[column] = struct{}{}
		}
	}
	return sortedKeys(set)
}

func compileParams(strategy Strategy, available map[string]struct{}) ([]CompiledParam, error) {
	outputs := strategy.FactorParamOutputs()
	compiled := make([]CompiledParam, 0, len(outputs))
	for _, output := range outputs {
		path := "factor_params." + output.Member + "." + output.Name
		param := CompiledParam{Member: output.Member, Name: output.Name}
		var err error
		if param.Default, err = CompileExpression(output.Expr.Default, StageScore, available); err != nil {
			return nil, fmt.Errorf("%s.default 无效：%w", path, err)
		}
		if output.Expr.PerSubject != "" {
			if param.PerSubject, err = CompileExpression(output.Expr.PerSubject, StageScore, available); err != nil {
				return nil, fmt.Errorf("%s.per_subject 无效：%w", path, err)
			}
		}
		compiled = append(compiled, param)
	}
	return compiled, nil
}

// referencedParamExprColumns 返回参数表达式引用的列（当期与上一根的并集）。
func referencedParamExprColumns(strategy Strategy, set map[string]struct{}) error {
	for _, output := range strategy.FactorParamOutputs() {
		for _, source := range []string{output.Expr.Default, output.Expr.PerSubject} {
			if source == "" {
				continue
			}
			expression, err := Analyze(source, StageScore)
			if err != nil {
				return fmt.Errorf("factor_params.%s.%s 无效：%w", output.Member, output.Name, err)
			}
			for _, column := range expression.AllColumns() {
				set[column] = struct{}{}
			}
			for _, column := range expression.AllPreviousColumns() {
				set[column] = struct{}{}
			}
		}
	}
	return nil
}
