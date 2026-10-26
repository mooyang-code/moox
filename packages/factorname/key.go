package factorname

import (
	"fmt"
	"strings"
)

// Param 是因子定义声明的一个参数，顺序就是定义里的声明顺序。
type Param struct {
	Name    string
	Default float64
}

// FixedKey 生成固定成员的成员键：参数全为默认值（或因子没有参数）时就是因子 ID，否则是 因子ID__值1_值2…，
// 参数值按声明顺序全部写出。given 里没给的参数取默认值，给了定义没有的参数名会报错。
func FixedKey(factorID string, decl []Param, given map[string]float64) (string, error) {
	if err := ValidateFactorID(factorID); err != nil {
		return "", err
	}
	declared := make(map[string]bool, len(decl))
	for _, param := range decl {
		if err := ValidateParamName(param.Name); err != nil {
			return "", fmt.Errorf("因子 %s：%w", factorID, err)
		}
		declared[param.Name] = true
	}
	for name := range given {
		if !declared[name] {
			return "", fmt.Errorf("因子 %s 没有参数 %s", factorID, name)
		}
	}
	allDefault := true
	parts := make([]string, 0, len(decl))
	for _, param := range decl {
		value, ok := given[param.Name]
		if !ok {
			value = param.Default
		}
		if value != param.Default {
			allDefault = false
		}
		text, err := FormatValue(value)
		if err != nil {
			return "", fmt.Errorf("因子 %s 的参数 %s：%w", factorID, param.Name, err)
		}
		parts = append(parts, text)
	}
	if allDefault {
		return factorID, nil
	}
	key := factorID + Sep + strings.Join(parts, "_")
	if len(key) > MaxMemberKeyLen {
		return "", fmt.Errorf("按参数生成的成员键 %q 超过 %d 个字符", key, MaxMemberKeyLen)
	}
	return key, nil
}

// VarKey 生成可变成员的成员键：因子ID__var。
func VarKey(factorID string) (string, error) {
	if err := ValidateFactorID(factorID); err != nil {
		return "", err
	}
	return factorID + Sep + VarSegment, nil
}

// Column 生成结果列名：output 为空表示单输出因子，列名就是成员键；否则是 成员键__输出名。
func Column(memberKey, output string) (string, error) {
	if _, err := ParseMemberKey(memberKey); err != nil {
		return "", err
	}
	if output == "" {
		return memberKey, nil
	}
	if err := ValidateOutputName("", output); err != nil {
		return "", err
	}
	column := memberKey + Sep + output
	if len(column) > MaxColumnLen {
		return "", fmt.Errorf("列名 %q 超过 %d 个字符", column, MaxColumnLen)
	}
	return column, nil
}

// Columns 生成一个成员的全部结果列名，顺序与 outputs 一致。单输出因子（outputs 只有因子 ID 本身）得到一个列，列名就是成员键。
func Columns(factorID, memberKey string, outputs []string) ([]string, error) {
	if err := ValidateOutputs(factorID, outputs); err != nil {
		return nil, err
	}
	if len(outputs) == 1 {
		column, err := Column(memberKey, "")
		return []string{column}, err
	}
	columns := make([]string, len(outputs))
	for i, output := range outputs {
		column, err := Column(memberKey, output)
		if err != nil {
			return nil, err
		}
		columns[i] = column
	}
	return columns, nil
}

// Parsed 是从成员键或列名解析出来的、名字里本来就有的信息。
type Parsed struct {
	FactorID string
	Kind     string
	// HasParams 为 true 表示固定成员的名字里带参数段（Values 是按声明顺序的参数值）；
	// 为 false 的固定成员参数全为默认值，具体参数值要查列元数据或因子定义。
	HasParams bool
	Values    []float64
	// Output 为空表示单输出因子。
	Output string
}

// ParamMap 把参数值按定义的声明顺序对应到参数名。参数全为默认值的成员得到默认值；可变成员没有参数值，返回 nil。
func (p Parsed) ParamMap(decl []Param) (map[string]float64, error) {
	if p.Kind == KindVar {
		return nil, nil
	}
	out := make(map[string]float64, len(decl))
	if !p.HasParams {
		for _, param := range decl {
			out[param.Name] = param.Default
		}
		return out, nil
	}
	if len(p.Values) != len(decl) {
		return nil, fmt.Errorf("因子 %s 的成员键带 %d 个参数值，定义声明了 %d 个参数", p.FactorID, len(p.Values), len(decl))
	}
	for i, param := range decl {
		out[param.Name] = p.Values[i]
	}
	return out, nil
}

// ParseMemberKey 解析成员键（不含输出段）。
func ParseMemberKey(key string) (Parsed, error) { return parse(key, false) }

// ParseColumn 解析结果列名。__params 不是因子值列，调用方要先用 ParamsColumn 判断。
func ParseColumn(column string) (Parsed, error) { return parse(column, true) }

func parse(text string, allowOutput bool) (Parsed, error) {
	parts := strings.Split(text, Sep)
	var parsed Parsed
	if err := ValidateFactorID(parts[0]); err != nil {
		return parsed, err
	}
	parsed.FactorID = parts[0]
	parsed.Kind = KindFixed
	rest := parts[1:]
	if len(rest) > 2 || (len(rest) == 2 && !allowOutput) {
		return parsed, fmt.Errorf("%q 的段数太多", text)
	}
	key := parsed.FactorID
	if len(rest) >= 1 && isParamSegment(rest[0]) {
		if err := parsed.setParamSegment(rest[0]); err != nil {
			return Parsed{}, err
		}
		key += Sep + rest[0]
		rest = rest[1:]
	}
	if len(rest) == 1 {
		if !allowOutput {
			return Parsed{}, fmt.Errorf("成员键 %q 不能带输出段", text)
		}
		if err := ValidateOutputName(parsed.FactorID, rest[0]); err != nil {
			return Parsed{}, err
		}
		parsed.Output = rest[0]
	} else if len(rest) > 1 {
		return Parsed{}, fmt.Errorf("%q 的段数太多", text)
	}
	if len(key) > MaxMemberKeyLen {
		return Parsed{}, fmt.Errorf("成员键 %q 超过 %d 个字符", key, MaxMemberKeyLen)
	}
	if len(text) > MaxColumnLen {
		return Parsed{}, fmt.Errorf("列名 %q 超过 %d 个字符", text, MaxColumnLen)
	}
	return parsed, nil
}

func (p *Parsed) setParamSegment(segment string) error {
	if segment == VarSegment {
		p.Kind = KindVar
		return nil
	}
	for _, part := range strings.Split(segment, "_") {
		value, err := ParseValue(part)
		if err != nil {
			return err
		}
		p.Values = append(p.Values, value)
	}
	p.HasParams = true
	return nil
}
