package domain

import (
	"fmt"

	"github.com/mooyang-code/moox/packages/factorname"
)

// ResolveMemberParams 把加成员时给出的参数补全成完整的固定参数：没给的参数取定义的默认值；
// 参数名必须是定义声明的，类型、范围与算子联合约束都要通过。定义没有参数时返回 nil。
func ResolveMemberParams(def FactorDef, given map[string]float64) (map[string]float64, error) {
	compiled := def.Compiled()
	if compiled == nil {
		return nil, fmt.Errorf("因子 %s 的定义没有编译", def.FactorID)
	}
	if len(compiled.Params) == 0 {
		if len(given) > 0 {
			return nil, fmt.Errorf("因子 %s 没有参数", def.FactorID)
		}
		return nil, nil
	}
	for name := range given {
		if _, ok := compiled.Param(name); !ok {
			return nil, fmt.Errorf("因子 %s 没有参数 %s", def.FactorID, name)
		}
	}
	full := make(map[string]float64, len(compiled.Params))
	for _, param := range compiled.Params {
		value, ok := given[param.Name]
		if !ok {
			value = param.Default
		}
		full[param.Name] = value
	}
	if err := compiled.CheckParams(full); err != nil {
		return nil, fmt.Errorf("因子 %s 的参数：%w", def.FactorID, err)
	}
	return full, nil
}

// FactorParams 把定义声明的参数转成公共命名包用的声明（顺序就是定义里的声明顺序）。
func FactorParams(def FactorDef) []factorname.Param {
	compiled := def.Compiled()
	if compiled == nil {
		return nil
	}
	decl := make([]factorname.Param, len(compiled.Params))
	for i, param := range compiled.Params {
		decl[i] = factorname.Param{Name: param.Name, Default: param.Default}
	}
	return decl
}

// DeriveMemberKey 推导成员键：可变成员是 因子ID__var；固定成员参数全为默认值（或因子没有参数）时是因子 ID，
// 否则带参数段。params 是加成员时给出的参数（没给的取默认值）。
func DeriveMemberKey(def FactorDef, kind string, params map[string]float64) (string, error) {
	switch kind {
	case MemberKindVar:
		return factorname.VarKey(def.FactorID)
	case MemberKindFixed:
		return factorname.FixedKey(def.FactorID, FactorParams(def), params)
	default:
		return "", fmt.Errorf("成员类型 %q 不合法：只能是 %s 或 %s", kind, MemberKindFixed, MemberKindVar)
	}
}

// CheckMemberParams 检查成员保存的参数在（更新后的）定义上仍然成立：保存的必须恰好是定义声明的全部参数，
// 且类型、范围与联合约束都通过。定义新增或删去了参数时，成员的参数就不完整或多余了——它的取值会随定义默认值漂移，
// 所以要求先移除再重加。
func CheckMemberParams(def FactorDef, params map[string]float64) error {
	compiled := def.Compiled()
	if compiled == nil {
		return fmt.Errorf("因子 %s 的定义没有编译", def.FactorID)
	}
	declared := make(map[string]bool, len(compiled.Params))
	for _, param := range compiled.Params {
		declared[param.Name] = true
		if _, ok := params[param.Name]; !ok {
			return fmt.Errorf("定义新增了参数 %s，成员保存的参数不完整；请先移除成员，再带完整参数重新添加", param.Name)
		}
	}
	for name := range params {
		if !declared[name] {
			return fmt.Errorf("定义不再声明参数 %s，成员保存的参数已失效；请先移除成员，再重新添加", name)
		}
	}
	if len(params) == 0 {
		return nil
	}
	_, err := ResolveMemberParams(def, params)
	return err
}
