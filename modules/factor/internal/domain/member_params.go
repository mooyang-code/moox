package domain

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// MaxAliasLength 是成员别名的最大长度（与 ValidateAlias 一致）。
const MaxAliasLength = 32

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

// CheckMemberParams 检查成员保存的固定参数在定义上仍然合法（定义更新时用）；固定参数为空的成员不检查。
func CheckMemberParams(def FactorDef, params map[string]float64) error {
	if len(params) == 0 {
		return nil
	}
	_, err := ResolveMemberParams(def, params)
	return err
}

// FormatParamValue 把参数值写成别名里用的片段：十进制（不出现指数）、整数不带小数点、-0 按 0；
// 小数点换成 p、负号换成 n（0.5 → 0p5，-1 → n1）。
func FormatParamValue(value float64) string {
	if value == 0 || math.IsNaN(value) {
		value = 0
	}
	text := strconv.FormatFloat(value, 'f', -1, 64)
	text = strings.ReplaceAll(text, ".", "p")
	return strings.ReplaceAll(text, "-", "n")
}

// DeriveMemberAlias 按参数生成成员别名：全部参数等于定义的默认值（或定义没有参数）时沿用因子 ID，
// 否则是“因子ID_参数1_参数2…”，参数值按定义声明的顺序全部写出。
// 别名超过长度上限时返回错误，调用方提示手填。
func DeriveMemberAlias(def FactorDef, params map[string]float64) (string, error) {
	compiled := def.Compiled()
	if compiled == nil {
		return "", fmt.Errorf("因子 %s 的定义没有编译", def.FactorID)
	}
	allDefault := true
	parts := make([]string, 0, len(compiled.Params))
	for _, param := range compiled.Params {
		value, ok := params[param.Name]
		if !ok {
			value = param.Default
		}
		if value != param.Default {
			allDefault = false
		}
		parts = append(parts, FormatParamValue(value))
	}
	if allDefault {
		return def.FactorID, nil
	}
	alias := def.FactorID + "_" + strings.Join(parts, "_")
	if len(alias) > MaxAliasLength {
		return "", fmt.Errorf("按参数生成的别名 %q 超过 %d 个字符，请手动指定别名", alias, MaxAliasLength)
	}
	return alias, nil
}

// ParamsSummary 返回参数摘要（如 span=20, warmup=50000），按参数名排序；没有参数时为空。
func ParamsSummary(params map[string]float64) string {
	if len(params) == 0 {
		return ""
	}
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = name + "=" + strconv.FormatFloat(params[name], 'f', -1, 64)
	}
	return strings.Join(parts, ", ")
}
