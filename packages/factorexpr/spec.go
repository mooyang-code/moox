package factorexpr

import (
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/mooyang-code/moox/packages/factorname"
	"gopkg.in/yaml.v3"
)

// 限制见设计 3.3。
const (
	MaxSpecBytes       = 64 << 10
	MaxExpressionRunes = 4096
	MaxExpressionNodes = 512
	// MaxFactorNodes 是一个因子内联 let 之后全部输出的表达式树节点总数上限（去重之前）。
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
	Params []Param
	Lets   []Binding
	// Outputs 是输出：单输出因子（写 output）只有一项，名称为空，输出名就是因子 ID；多输出因子（写 outputs）至少两项。
	Outputs []Binding
}

// SingleOutput 报告定义是否是单输出写法（output）。
func (s *Spec) SingleOutput() bool { return len(s.Outputs) == 1 && s.Outputs[0].Name == "" }

// ParamType 是参数的取值类型。
type ParamType string

const (
	ParamInt   ParamType = "int"
	ParamFloat ParamType = "float"
)

// Param 是一个可调参数的声明（因子动态调参执行计划 2.2）：类型、闭区间 [Min, Max] 与默认值。
// 每期的取值由参数快照提供，不在定义里；从未提交过快照时取 Default。
type Param struct {
	Name    string
	Type    ParamType
	Min     float64
	Max     float64
	Default float64
	Line    int
}

// IsInt 报告参数是否为整数类型。
func (p Param) IsInt() bool { return p.Type == ParamInt }

// Check 检查一个取值是否合法：有限、在范围内，整数参数必须是整数。
func (p Param) Check(value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("参数 %s 的取值必须是有限数", p.Name)
	}
	if p.IsInt() && value != math.Trunc(value) {
		return fmt.Errorf("参数 %s 是整数参数，取值 %s 不是整数", p.Name, formatFloat(value))
	}
	if value < p.Min || value > p.Max {
		return fmt.Errorf("参数 %s 的取值 %s 超出范围 [%s, %s]", p.Name, formatFloat(value), formatFloat(p.Min), formatFloat(p.Max))
	}
	return nil
}

// Number 是字面量的数值。
type Number struct {
	Value float64
	IsInt bool
}

// Binding 是 let 或 outputs 中的一项：名称（输出为静态列名）与表达式原文。
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

// ParseSpec 解析定义文本：只接受一个 YAML 映射文档，键只能是 params、let，以及 output（单输出）或 outputs（多输出）二选一。
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
		return nil, errorf("", root.Line, "定义必须是 YAML 映射，包含 params、let，以及 output 或 outputs")
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
		case "output":
			if seen["outputs"] {
				return nil, errorf(key.Value, key.Line, "output 与 outputs 只能写一个")
			}
			spec.Outputs, err = parseSingleOutput(value)
		case "outputs":
			if seen["output"] {
				return nil, errorf(key.Value, key.Line, "output 与 outputs 只能写一个")
			}
			spec.Outputs, err = parseBindings("outputs", value)
		default:
			err = errorf(key.Value, key.Line, "未知的键，只允许 params、let、output、outputs")
		}
		if err != nil {
			return nil, err
		}
	}
	if len(spec.Outputs) == 0 {
		return nil, errorf("outputs", root.Line, "至少需要一个输出：只有一个输出写 output，多个输出写 outputs")
	}
	if seen["outputs"] && len(spec.Outputs) < 2 {
		return nil, errorf("outputs", root.Line, "outputs 至少要有两项；只有一个输出请写 output")
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
		return nil, errorf("params", node.Line, "必须是映射：参数名 → {type, min, max, default}")
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
		param, err := parseParam(path, key, value)
		if err != nil {
			return nil, err
		}
		params = append(params, param)
	}
	return params, nil
}

// parseParam 解析一个参数声明：{type: int|float, min, max, default}，四项都必填。
func parseParam(path string, key, node *yaml.Node) (Param, error) {
	if node.Kind != yaml.MappingNode {
		return Param{}, errorf(path, node.Line, "参数声明必须是映射 {type, min, max, default}")
	}
	param := Param{Name: key.Value, Line: key.Line}
	numbers := map[string]Number{}
	for i := 0; i < len(node.Content); i += 2 {
		field, value := node.Content[i], node.Content[i+1]
		fieldPath := path + "." + field.Value
		if _, duplicate := numbers[field.Value]; duplicate || (field.Value == "type" && param.Type != "") {
			return Param{}, errorf(fieldPath, field.Line, "重复的键")
		}
		switch field.Value {
		case "type":
			switch ParamType(value.Value) {
			case ParamInt, ParamFloat:
				param.Type = ParamType(value.Value)
			default:
				return Param{}, errorf(fieldPath, value.Line, "type 只能是 int 或 float")
			}
		case "min", "max", "default":
			if value.Kind != yaml.ScalarNode {
				return Param{}, errorf(fieldPath, value.Line, "必须是数值")
			}
			number, err := parseNumber(fieldPath, value)
			if err != nil {
				return Param{}, err
			}
			numbers[field.Value] = number
		default:
			return Param{}, errorf(fieldPath, field.Line, "未知的键，只允许 type、min、max、default")
		}
	}
	if param.Type == "" {
		return Param{}, errorf(path, node.Line, "缺少 type")
	}
	for _, name := range []string{"min", "max", "default"} {
		number, ok := numbers[name]
		if !ok {
			return Param{}, errorf(path, node.Line, "缺少 %s", name)
		}
		if param.IsInt() && !number.IsInt {
			return Param{}, errorf(path+"."+name, node.Line, "整数参数的 %s 必须是整数", name)
		}
	}
	param.Min, param.Max, param.Default = numbers["min"].Value, numbers["max"].Value, numbers["default"].Value
	if param.Min > param.Max {
		return Param{}, errorf(path, node.Line, "min 不能大于 max")
	}
	if param.Default < param.Min || param.Default > param.Max {
		return Param{}, errorf(path+".default", node.Line, "默认值必须在 [min, max] 内")
	}
	return param, nil
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

// parseSingleOutput 解析单输出写法：output 的值就是表达式，没有名字（输出名就是因子 ID）。
func parseSingleOutput(node *yaml.Node) ([]Binding, error) {
	if node.Kind != yaml.ScalarNode || isNull(node) || strings.TrimSpace(node.Value) == "" {
		return nil, errorf("output", node.Line, "表达式不能为空")
	}
	if utf8.RuneCountInString(node.Value) > MaxExpressionRunes {
		return nil, errorf("output", node.Line, "表达式超过 %d 个字符", MaxExpressionRunes)
	}
	return []Binding{{Source: node.Value, Line: node.Line}}, nil
}

// checkNames 检查 params 与 let 的名称：格式、互不重名、不与函数名和系统列冲突。参数名按因子命名规则校验（会写进 __params）。
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
		if err := factorname.ValidateParamName(param.Name); err != nil {
			return errorf("params."+param.Name, param.Line, "%v", err)
		}
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
