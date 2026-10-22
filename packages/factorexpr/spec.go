package factorexpr

import (
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// 限制见设计 3.3。
const (
	MaxSpecBytes       = 64 << 10
	MaxExpressionRunes = 4096
	MaxExpressionNodes = 512
	// MaxFactorNodes 是一个因子展开参数、内联 let 之后全部输出的表达式树节点总数上限（去重之前）。
	MaxFactorNodes = 4096
	MaxParams      = 16
	MaxOutputs     = 64
	// MaxWindow 与 MaxLookback 是包内的绝对上限（回看数量 N 的上限，设计 8.1）。实际的上限由数据源的回看数量 N 决定，
	// 因子引擎在准备数据源时按 Lookback ≤ N 校验；单个函数的窗口参数不超过 N 已被这条隐含（窗口 n 的回看就是 n）。
	MaxWindow   = 100000
	MaxLookback = 100000
	// MaxIdentifierRunes 是输入列名与输出列名的长度上限；它与 MaxFactorNodes 一起给规范文本的长度定了上界。
	MaxIdentifierRunes = 64
	// maxIntegerParam 保证整数参数能被 float64 精确表示。
	maxIntegerParam = 1 << 53
)

// ReservedColumns 是系统列，不能作为输入或输出。
var ReservedColumns = []string{"subject_id", "freq", "data_time", "series_tag"}

var (
	namePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	columnPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// validColumnName 检查输入列名与输出列名：字母或下划线开头，只含字母、数字、下划线，不超过 MaxIdentifierRunes。
func validColumnName(name string) bool {
	return len(name) <= MaxIdentifierRunes && columnPattern.MatchString(name)
}

func isReservedColumn(name string) bool {
	for _, reserved := range ReservedColumns {
		if name == reserved {
			return true
		}
	}
	return false
}

// Spec 是解析后的定义文本，保持文档中的书写顺序。
type Spec struct {
	Params  []Param
	Lets    []Binding
	Outputs []Binding
}

// Param 是一个命名常量；List 为真时按列表展开（设计 3.2）。
type Param struct {
	Name   string
	Values []Number
	List   bool
	Line   int
}

// Number 是参数或字面量的数值。
type Number struct {
	Value float64
	IsInt bool
}

// Binding 是 let 或 outputs 中的一项：名称（输出为列名模板）与表达式原文。
type Binding struct {
	Name   string
	Source string
	// Line 是表达式值在定义文本中的起始行。
	Line int
}

func (s *Spec) param(name string) (Param, bool) {
	for _, param := range s.Params {
		if param.Name == name {
			return param, true
		}
	}
	return Param{}, false
}

// ParseSpec 解析定义文本：只接受一个 YAML 映射文档，键只能是 params、let、outputs。
func ParseSpec(text string) (*Spec, error) {
	if len(text) > MaxSpecBytes {
		return nil, errorf("", 0, "定义超过 %d 字节", MaxSpecBytes)
	}
	if strings.TrimSpace(text) == "" {
		return nil, errorf("", 0, "定义为空")
	}
	decoder := yaml.NewDecoder(strings.NewReader(text))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, yamlError(err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, yamlError(err)
		}
		return nil, errorf("", extra.Line, "定义只能包含一个 YAML 文档")
	}
	if len(document.Content) != 1 {
		return nil, errorf("", 0, "定义为空")
	}
	root := document.Content[0]
	if err := checkPlainYAML(root); err != nil {
		return nil, err
	}
	if root.Kind != yaml.MappingNode {
		return nil, errorf("", root.Line, "定义必须是 YAML 映射，包含 params、let、outputs")
	}
	spec := &Spec{}
	seen := map[string]bool{}
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if seen[key.Value] {
			return nil, errorf(key.Value, key.Line, "重复的键")
		}
		seen[key.Value] = true
		var err error
		switch key.Value {
		case "params":
			spec.Params, err = parseParams(value)
		case "let":
			spec.Lets, err = parseBindings("let", value)
		case "outputs":
			spec.Outputs, err = parseBindings("outputs", value)
		default:
			err = errorf(key.Value, key.Line, "未知的键，只允许 params、let、outputs")
		}
		if err != nil {
			return nil, err
		}
	}
	if len(spec.Outputs) == 0 {
		return nil, errorf("outputs", root.Line, "至少需要一个输出")
	}
	if err := checkNames(spec); err != nil {
		return nil, err
	}
	return spec, nil
}

// checkPlainYAML 拒绝锚点、别名与显式标签：定义应当一眼看清，不允许引用展开或类型改写。
func checkPlainYAML(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode {
		return errorf("", node.Line, "不允许使用 YAML 别名（*）")
	}
	if node.Anchor != "" {
		return errorf("", node.Line, "不允许使用 YAML 锚点（&）")
	}
	if node.Style&yaml.TaggedStyle != 0 {
		return errorf("", node.Line, "不允许使用 YAML 标签（!）")
	}
	for _, child := range node.Content {
		if err := checkPlainYAML(child); err != nil {
			return err
		}
	}
	return nil
}

func isNull(node *yaml.Node) bool {
	return node.Kind == yaml.ScalarNode && node.ShortTag() == "!!null"
}

func parseParams(node *yaml.Node) ([]Param, error) {
	if isNull(node) {
		return nil, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, errorf("params", node.Line, "必须是映射：参数名 → 数值或数值列表")
	}
	if len(node.Content)/2 > MaxParams {
		return nil, errorf("params", node.Line, "参数不能超过 %d 个", MaxParams)
	}
	var params []Param
	seen := map[string]bool{}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		path := "params." + key.Value
		if seen[key.Value] {
			return nil, errorf(path, key.Line, "重复的参数")
		}
		seen[key.Value] = true
		param := Param{Name: key.Value, Line: key.Line}
		switch value.Kind {
		case yaml.ScalarNode:
			number, err := parseNumber(path, value)
			if err != nil {
				return nil, err
			}
			param.Values = []Number{number}
		case yaml.SequenceNode:
			if len(value.Content) == 0 {
				return nil, errorf(path, value.Line, "列表不能为空")
			}
			if len(value.Content) > MaxOutputs {
				return nil, errorf(path, value.Line, "列表不能超过 %d 个值", MaxOutputs)
			}
			param.List = true
			for _, item := range value.Content {
				if item.Kind != yaml.ScalarNode {
					return nil, errorf(path, item.Line, "列表里只能是数值")
				}
				number, err := parseNumber(path, item)
				if err != nil {
					return nil, err
				}
				param.Values = append(param.Values, number)
			}
		default:
			return nil, errorf(path, value.Line, "必须是数值或数值列表")
		}
		params = append(params, param)
	}
	return params, nil
}

func parseNumber(path string, node *yaml.Node) (Number, error) {
	switch node.ShortTag() {
	case "!!int":
		var value int64
		if err := node.Decode(&value); err != nil || value > maxIntegerParam || value < -maxIntegerParam {
			return Number{}, errorf(path, node.Line, "整数 %q 超出范围", node.Value)
		}
		return Number{Value: float64(value), IsInt: true}, nil
	case "!!float":
		var value float64
		if err := node.Decode(&value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return Number{}, errorf(path, node.Line, "数值 %q 必须是有限数", node.Value)
		}
		return Number{Value: value}, nil
	}
	return Number{}, errorf(path, node.Line, "必须是数值，%q 不是", node.Value)
}

func parseBindings(section string, node *yaml.Node) ([]Binding, error) {
	if isNull(node) {
		return nil, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, errorf(section, node.Line, "必须是映射：名称 → 表达式")
	}
	var bindings []Binding
	seen := map[string]bool{}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		path := section + "." + key.Value
		if key.Kind != yaml.ScalarNode || key.Value == "" {
			return nil, errorf(section, key.Line, "名称必须是非空字符串")
		}
		if seen[key.Value] {
			return nil, errorf(path, key.Line, "重复的名称")
		}
		seen[key.Value] = true
		if value.Kind != yaml.ScalarNode || isNull(value) || strings.TrimSpace(value.Value) == "" {
			return nil, errorf(path, value.Line, "表达式不能为空")
		}
		if utf8.RuneCountInString(value.Value) > MaxExpressionRunes {
			return nil, errorf(path, value.Line, "表达式超过 %d 个字符", MaxExpressionRunes)
		}
		bindings = append(bindings, Binding{Name: key.Value, Source: value.Value, Line: value.Line})
	}
	return bindings, nil
}

// checkNames 检查 params 与 let 的名称：格式、互不重名、不与函数名和系统列冲突。
func checkNames(spec *Spec) error {
	used := map[string]string{}
	check := func(path, name string, line int) error {
		if !namePattern.MatchString(name) {
			return errorf(path, line, "名称必须是小写字母开头、只含小写字母、数字和下划线，最长 32 个字符")
		}
		if isFunctionName(name) {
			return errorf(path, line, "名称 %s 与函数重名", name)
		}
		if isReservedColumn(name) {
			return errorf(path, line, "名称 %s 是系统列", name)
		}
		if name == "true" || name == "false" || name == "nil" {
			return errorf(path, line, "名称 %s 是保留字", name)
		}
		if previous, ok := used[name]; ok {
			return errorf(path, line, "名称 %s 已在 %s 中使用", name, previous)
		}
		used[name] = path
		return nil
	}
	for _, param := range spec.Params {
		if err := check("params."+param.Name, param.Name, param.Line); err != nil {
			return err
		}
	}
	for _, binding := range spec.Lets {
		if err := check("let."+binding.Name, binding.Name, binding.Line); err != nil {
			return err
		}
	}
	return nil
}

var yamlLinePattern = regexp.MustCompile(`line (\d+)`)

var yamlHints = []struct{ raw, text string }{
	{"mapping values are not allowed", "这里不能出现键值对，请检查缩进与冒号"},
	{"block sequence entries are not allowed", "这里不能出现列表项，请检查缩进"},
	{"did not find expected key", "缺少预期的键，请检查缩进"},
	{"did not find expected '-' indicator", "列表项的缩进不一致"},
	{"could not find expected ':'", "缺少冒号"},
	{"did not find expected node content", "缺少预期的值"},
	{"found character that cannot start any token", "出现了不能作为开头的字符（例如用 Tab 缩进）"},
	{"found unexpected end of stream", "文本意外结束（引号或括号没有闭合）"},
	{"found unknown escape character", "字符串里有无法识别的转义字符"},
	{"unknown anchor", "引用了未定义的锚点"},
}

func yamlError(err error) error {
	raw := err.Error()
	line := 0
	if match := yamlLinePattern.FindStringSubmatch(raw); match != nil {
		fmt.Sscanf(match[1], "%d", &line)
	}
	message := "定义不是合法的 YAML"
	for _, hint := range yamlHints {
		if strings.Contains(raw, hint.raw) {
			message += "：" + hint.text
			break
		}
	}
	return &Error{DocLine: line, Message: message, cause: err}
}
