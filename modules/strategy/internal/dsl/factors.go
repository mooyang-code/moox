package dsl

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// 因子声明与参数输出（因子动态调参执行计划 2.3、2.8）：
//
//	factors:
//	  fast: {factor: Bias}
//	  slow: {factor: Bias}
//	factor_params:
//	  fast:
//	    window:
//	      default: "20"
//	      per_subject: "10 + rank(vol) * 20"
//
// 声明了 factors 的策略在启用实例时获得私有因子任务（成员恰好是声明的因子，实例是唯一的参数写入方），并绑定它的结果 View；
// 规则按 <别名>_<输出名>（别名与定义 ID 相同时就是输出名）引用因子值，按 <别名>__<参数名> 读取这一期实际用的参数。
// factor_params 在每期求值后算出下一期的参数并提交；没有写出的成员不提交，因子沿用上一期（从未提交过则用默认值）。

var (
	factorAliasPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)
	factorIDPattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	paramNamePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// MaxFactors 是一个策略声明的因子成员上限。
const MaxFactors = 32

// ParamDecl 是因子定义声明的一个可调参数。
type ParamDecl struct {
	Name    string  `json:"name"`
	Type    string  `json:"type"` // int 或 float
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Default float64 `json:"default"`
}

// FactorDecl 是一个因子成员：别名与它引用的因子定义。
type FactorDecl struct {
	Alias    string
	FactorID string
}

// ParamExpr 是一个参数的输出表达式：Default 是这一期的统一值，PerSubject 可选，按标的求值，与统一值不同的标的进入覆盖。
// 两者都是数值表达式（与 score 相同，可用 rank、zscore 与 bars[-1]）；Default 按标的求值后取中位数，不引用列时就是常数。
type ParamExpr struct {
	Default    string
	PerSubject string
}

// ParamOutput 是一个成员的一个参数的输出。
type ParamOutput struct {
	Alias string
	Name  string
	Expr  ParamExpr
}

// FactorParamOutputs 按（别名、参数名）排序返回全部参数输出。
func (s Strategy) FactorParamOutputs() []ParamOutput {
	out := make([]ParamOutput, 0)
	for alias, params := range s.FactorParams {
		for name, expr := range params {
			out = append(out, ParamOutput{Alias: alias, Name: name, Expr: expr})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Alias != out[j].Alias {
			return out[i].Alias < out[j].Alias
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func decodeFactors(node *yaml.Node) ([]FactorDecl, error) {
	fields, err := mappingFields(node, "factors")
	if err != nil {
		return nil, err
	}
	out := make([]FactorDecl, 0, len(fields))
	for _, f := range fields {
		path := "factors." + f.key
		decl := FactorDecl{Alias: strings.TrimSpace(f.key)}
		members, err := mappingFields(f.value, path)
		if err != nil {
			return nil, fmt.Errorf("%s 必须写成 {factor: 因子定义 ID}", path)
		}
		for _, member := range members {
			switch member.key {
			case "factor":
				decl.FactorID, err = scalarRequiredText(member.value, path+".factor")
			default:
				return nil, fmt.Errorf("%s 包含未知字段 %q", path, member.key)
			}
			if err != nil {
				return nil, err
			}
		}
		out = append(out, decl)
	}
	return out, nil
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

// validateFactors 校验因子声明与参数输出：别名与定义 ID 的写法、别名不重复、参数输出只针对声明的成员、
// 表达式是合法的数值表达式。参数名是否属于该因子、取值范围由启用时对照因子定义检查。
func validateFactors(s *Strategy) error {
	if len(s.Factors) > MaxFactors {
		return fmt.Errorf("factors 最多声明 %d 个成员", MaxFactors)
	}
	declared := make(map[string]struct{}, len(s.Factors))
	for _, decl := range s.Factors {
		if !factorAliasPattern.MatchString(decl.Alias) || strings.Contains(decl.Alias, "__") {
			return fmt.Errorf("factors.%s：成员别名须字母开头，只含字母、数字、下划线，最长 32 个字符，不能含连续下划线", decl.Alias)
		}
		if !factorIDPattern.MatchString(decl.FactorID) {
			return fmt.Errorf("factors.%s.factor %q 不是合法的因子定义 ID", decl.Alias, decl.FactorID)
		}
		if _, exists := declared[decl.Alias]; exists {
			return fmt.Errorf("factors 重复声明成员 %s", decl.Alias)
		}
		declared[decl.Alias] = struct{}{}
	}
	for _, output := range s.FactorParamOutputs() {
		path := "factor_params." + output.Alias + "." + output.Name
		if _, ok := declared[output.Alias]; !ok {
			return fmt.Errorf("factor_params.%s：没有在 factors 中声明这个成员", output.Alias)
		}
		if !paramNamePattern.MatchString(output.Name) {
			return fmt.Errorf("%s：参数名须小写字母开头，只含小写字母、数字、下划线", path)
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
	Alias      string
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
		path := "factor_params." + output.Alias + "." + output.Name
		param := CompiledParam{Alias: output.Alias, Name: output.Name}
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

// referencedParamColumns 返回参数表达式引用的列（当期与上一根的并集）。
func referencedParamColumns(strategy Strategy, set map[string]struct{}) error {
	for _, output := range strategy.FactorParamOutputs() {
		for _, source := range []string{output.Expr.Default, output.Expr.PerSubject} {
			if source == "" {
				continue
			}
			expression, err := Analyze(source, StageScore)
			if err != nil {
				return fmt.Errorf("factor_params.%s.%s 无效：%w", output.Alias, output.Name, err)
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

// FactorIDs 返回声明引用的因子定义 ID（去重排序）。
func (s Strategy) FactorIDs() []string {
	set := map[string]struct{}{}
	for _, decl := range s.Factors {
		set[decl.FactorID] = struct{}{}
	}
	return sortedKeys(set)
}
