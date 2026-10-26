package engine

import (
	"fmt"
	"math"
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// 参数输出（设计 8.3）：每期求值后算出下一期各可变成员的完整参数快照。
//
//   - default 与 per_subject 都是数值表达式，在这一期的一帧数据上按标的求值（可用 rank、zscore 与 bars[-1]）。
//   - 统一值取 default 在各标的上的中位数（不引用任何列的常数表达式就是它本身）；per_subject 与统一值不同的标的进入覆盖。
//   - 没有在 factor_params 里写出的参数取因子定义的默认值；整型参数四舍五入，所有参数限制在声明的范围内。

// ParamValue 是一个参数一期的取值：统一值加按标的覆盖。
type ParamValue struct {
	Default   float64
	Overrides map[string]float64
}

// ParamSet 是各可变成员的完整参数快照：成员键 → 参数名 → 取值。
type ParamSet map[string]map[string]ParamValue

// ParamOutcome 是一期参数输出的结果。Notes 记录求值失败或被限幅的说明（写进结果记录，便于排查）。
type ParamOutcome struct {
	Params ParamSet
	Notes  []string
}

// EvaluateParams 在 frame 上求出下一期的参数快照。decls 是各成员声明的可调参数（来自启用时固化的解析）；
// 程序没有参数输出时返回空结果。某个参数在所有标的上都求值失败时，整个成员不输出（不提交，因子沿用上一期），
// 避免用不完整的快照覆盖上一期的有效参数。
func EvaluateParams(program *dsl.Program, frame Frame, decls map[string][]dsl.ParamDecl) ParamOutcome {
	outcome := ParamOutcome{Params: ParamSet{}}
	if program == nil || len(program.Params) == 0 {
		return outcome
	}
	ids := make([]string, 0, len(frame.Rows))
	for _, id := range frame.Universe {
		if _, ok := frame.Rows[id]; ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	byMember := map[string][]dsl.CompiledParam{}
	for _, param := range program.Params {
		byMember[param.Member] = append(byMember[param.Member], param)
	}
	for _, member := range sortedMembers(byMember) {
		snapshot := map[string]ParamValue{}
		failed := false
		for _, decl := range decls[member] {
			snapshot[decl.Name] = ParamValue{Default: decl.Default}
		}
		for _, param := range byMember[member] {
			decl, known := findDecl(decls[member], param.Name)
			value, note, ok := evaluateParam(param, ids, frame.Rows, frame.FailedColumns)
			if !ok {
				outcome.Notes = append(outcome.Notes, fmt.Sprintf("成员 %s 的参数 %s 求值失败（%s），本期不提交该成员的参数", member, param.Name, note))
				failed = true
				break
			}
			if known {
				value, note = fitParam(decl, value)
				if note != "" {
					outcome.Notes = append(outcome.Notes, fmt.Sprintf("成员 %s 的参数 %s：%s", member, param.Name, note))
				}
			}
			snapshot[param.Name] = value
		}
		if !failed {
			outcome.Params[member] = snapshot
		}
	}
	return outcome
}

func sortedMembers(values map[string][]dsl.CompiledParam) []string {
	out := make([]string, 0, len(values))
	for member := range values {
		out = append(out, member)
	}
	sort.Strings(out)
	return out
}

func findDecl(decls []dsl.ParamDecl, name string) (dsl.ParamDecl, bool) {
	for _, decl := range decls {
		if decl.Name == name {
			return decl, true
		}
	}
	return dsl.ParamDecl{}, false
}

// evaluateParam 求出一个参数的统一值与覆盖。不引用任何列的表达式只求值一次；引用列的表达式在各标的上求值，
// 统一值取有效值的中位数。
func evaluateParam(param dsl.CompiledParam, ids []string, rows map[string]Row, failed map[string]map[string]string) (ParamValue, string, bool) {
	value := ParamValue{}
	constant := len(param.Default.AllColumns()) == 0 && len(param.Default.AllPreviousColumns()) == 0 && len(param.Default.Normalizers) == 0
	if constant {
		result := evaluateNumeric(param.Default, []string{""}, map[string]Row{"": {Values: map[string]float64{}}})
		number, ok := result.values[""]
		if !ok {
			return ParamValue{}, "default 不是有效数值", false
		}
		value.Default = number
	} else {
		result := evaluateNumeric(param.Default, usableIDs(param.Default, ids, rows, failed), rows)
		numbers := make([]float64, 0, len(result.values))
		for _, id := range ids {
			if number, ok := result.values[id]; ok {
				numbers = append(numbers, number)
			}
		}
		if len(numbers) == 0 {
			return ParamValue{}, "default 在所有标的上都没有有效值", false
		}
		value.Default = median(numbers)
	}
	if param.PerSubject != nil {
		usable := usableIDs(param.PerSubject, ids, rows, failed)
		result := evaluateNumeric(param.PerSubject, usable, rows)
		for _, id := range usable {
			number, ok := result.values[id]
			if !ok || number == value.Default {
				continue
			}
			if value.Overrides == nil {
				value.Overrides = map[string]float64{}
			}
			value.Overrides[id] = number
		}
	}
	return value, "", true
}

// usableIDs 去掉表达式引用的列（含 bars[-1] 的历史列）缺值或上游失败的标的：和规则求值一样，缺数的标的不参与，
// 否则缺失的历史值会被当成 0 算进中位数与截面函数，提交一个错误的参数。
func usableIDs(expression *dsl.Expression, ids []string, rows map[string]Row, failed map[string]map[string]string) []string {
	columns, previous := expression.AllColumns(), expression.AllPreviousColumns()
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if missingReason(id, rows[id], columns, previous, failed) == "" {
			out = append(out, id)
		}
	}
	return out
}

func median(values []float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}
	return (sorted[middle-1] + sorted[middle]) / 2
}

// fitParam 把取值整理成因子定义接受的形式：整型参数四舍五入，所有取值限制在 [min, max]。
func fitParam(decl dsl.ParamDecl, value ParamValue) (ParamValue, string) {
	clamped := false
	fit := func(number float64) float64 {
		if decl.Type == "int" {
			number = math.Round(number)
		}
		if number < decl.Min {
			clamped = true
			return decl.Min
		}
		if number > decl.Max {
			clamped = true
			return decl.Max
		}
		return number
	}
	out := ParamValue{Default: fit(value.Default)}
	for id, number := range value.Overrides {
		fitted := fit(number)
		if fitted == out.Default {
			continue
		}
		if out.Overrides == nil {
			out.Overrides = map[string]float64{}
		}
		out.Overrides[id] = fitted
	}
	if clamped {
		return out, fmt.Sprintf("取值超出范围 [%g, %g]，已限制在范围内", decl.Min, decl.Max)
	}
	return out, ""
}
