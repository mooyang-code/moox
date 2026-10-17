package factorexpr

import (
	"strconv"
	"strings"
)

// combination 是一组参数取值：列表参数取其中一个值，标量参数取唯一的值。
type combination map[string]Number

// expandedOutput 是展开后的一个输出：列名已代入参数，表达式在 combo 对应的参数取值下编译。
type expandedOutput struct {
	name    string
	binding Binding
	combo   int
}

// checkExpansionSize 在生成任何组合之前检查展开后的输出数（输出数 × 各列表参数的长度之积）不超过上限，
// 用除法比较避免溢出；否则少量参数就能让组合数达到千万级，在报错之前耗尽内存。
func checkExpansionSize(spec *Spec) error {
	total := len(spec.Outputs)
	for _, param := range spec.Params {
		if !param.List {
			continue
		}
		if total > MaxOutputs/len(param.Values) {
			return errorf("outputs", 0, "展开后的输出超过 %d 个（输出数 × 各列表参数的取值个数）", MaxOutputs)
		}
		total *= len(param.Values)
	}
	if total > MaxOutputs {
		return errorf("outputs", 0, "展开后的输出超过 %d 个（输出数 × 各列表参数的取值个数）", MaxOutputs)
	}
	return nil
}

// combinations 按参数声明顺序、列表内顺序生成笛卡尔积；没有列表参数时只有一组。调用前必须先通过 checkExpansionSize。
func combinations(spec *Spec) []combination {
	combos := []combination{{}}
	for _, param := range spec.Params {
		next := make([]combination, 0, len(combos)*len(param.Values))
		for _, combo := range combos {
			for _, value := range param.Values {
				extended := make(combination, len(combo)+1)
				for name, number := range combo {
					extended[name] = number
				}
				extended[param.Name] = value
				next = append(next, extended)
			}
		}
		combos = next
	}
	return combos
}

// expandOutputs 按输出的书写顺序、每个输出内按参数组合的顺序展开（设计 3.2）。
func expandOutputs(spec *Spec, combos []combination) ([]expandedOutput, error) {
	var listParams []string
	for _, param := range spec.Params {
		if param.List {
			listParams = append(listParams, param.Name)
		}
	}
	var outputs []expandedOutput
	seen := map[string]string{}
	for _, binding := range spec.Outputs {
		path := "outputs." + binding.Name
		segments, refs, err := parseTemplate(spec, path, binding)
		if err != nil {
			return nil, err
		}
		for _, name := range listParams {
			if !refs[name] {
				return nil, errorf(path, binding.Line, "列表参数 %s 必须出现在输出列名里（写成 {%s}），否则展开后会重名", name, name)
			}
		}
		for index, combo := range combos {
			name := renderTemplate(segments, combo)
			if !validColumnName(name) {
				return nil, errorf(path, binding.Line, "展开后的列名 %q 不合法：只能包含字母、数字和下划线，不能以数字开头，最长 %d 个字符", name, MaxIdentifierRunes)
			}
			if isReservedColumn(name) {
				return nil, errorf(path, binding.Line, "输出列名 %s 是系统列", name)
			}
			if previous, ok := seen[name]; ok {
				return nil, errorf(path, binding.Line, "展开后的列名 %s 与 %s 重复", name, previous)
			}
			seen[name] = path
			outputs = append(outputs, expandedOutput{name: name, binding: binding, combo: index})
			if len(outputs) > MaxOutputs {
				return nil, errorf(path, binding.Line, "展开后的输出超过 %d 个", MaxOutputs)
			}
		}
	}
	return outputs, nil
}

type templateSegment struct {
	text  string
	param string
}

// parseTemplate 解析输出列名模板：字面部分只能是字母、数字、下划线，{name} 引用整数参数。
func parseTemplate(spec *Spec, path string, binding Binding) ([]templateSegment, map[string]bool, error) {
	var segments []templateSegment
	refs := map[string]bool{}
	rest := binding.Name
	for rest != "" {
		open := strings.IndexByte(rest, '{')
		if close := strings.IndexByte(rest, '}'); close >= 0 && (open < 0 || close < open) {
			return nil, nil, errorf(path, binding.Line, "列名模板的花括号不匹配")
		}
		if open < 0 {
			segments = append(segments, templateSegment{text: rest})
			break
		}
		if open > 0 {
			segments = append(segments, templateSegment{text: rest[:open]})
		}
		end := strings.IndexByte(rest[open:], '}')
		if end < 0 {
			return nil, nil, errorf(path, binding.Line, "列名模板的花括号不匹配")
		}
		name := rest[open+1 : open+end]
		param, ok := spec.param(name)
		if !ok {
			return nil, nil, errorf(path, binding.Line, "列名模板引用了未定义的参数 %q", name)
		}
		for _, value := range param.Values {
			if !value.IsInt {
				return nil, nil, errorf(path, binding.Line, "列名模板里的参数 %s 必须是整数", name)
			}
		}
		refs[name] = true
		segments = append(segments, templateSegment{param: name})
		rest = rest[open+end+1:]
	}
	for _, segment := range segments {
		if segment.param == "" && strings.Trim(segment.text, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_") != "" {
			return nil, nil, errorf(path, binding.Line, "列名模板只能包含字母、数字、下划线和 {参数}")
		}
	}
	return segments, refs, nil
}

func renderTemplate(segments []templateSegment, combo combination) string {
	var b strings.Builder
	for _, segment := range segments {
		if segment.param == "" {
			b.WriteString(segment.text)
			continue
		}
		b.WriteString(strconv.FormatInt(int64(combo[segment.param].Value), 10))
	}
	return b.String()
}
