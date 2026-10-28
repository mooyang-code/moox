// Package template 实现策略模板（设计第 6 节）：命名的 DSL 骨架 + 变量定义 + 可选的参数表样例行。
// 渲染只在 YAML 节点上替换完整的 "{{变量}}" 标量并填入参数表行，生成的 DSL 交给现有的 DSL 校验；引擎不感知模板。
package template

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"gopkg.in/yaml.v3"
)

// 变量类型。
const (
	TypeInt    = "int"
	TypeNumber = "number"
	TypeString = "string"
	TypeBool   = "bool"
)

// NameVariable 是内置变量：生成的策略名，渲染时必须给出。
const NameVariable = "name"

const (
	maxTemplateBytes = 1 << 20
	maxVariables     = 64
)

var (
	idPattern          = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	variablePattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	placeholderPattern = regexp.MustCompile(`^\{\{([a-z][a-z0-9_]{0,31})\}\}$`)
)

// Variable 是一个模板变量的声明。
type Variable struct {
	Name    string   `yaml:"name"`
	Type    string   `yaml:"type"`
	Min     *float64 `yaml:"min,omitempty"`
	Max     *float64 `yaml:"max,omitempty"`
	Default *string  `yaml:"default,omitempty"`
	Doc     string   `yaml:"doc,omitempty"`
}

// Template 是解析并校验后的模板。
type Template struct {
	ID          string     `yaml:"template_id"`
	Name        string     `yaml:"name"`
	Description string     `yaml:"description,omitempty"`
	Variables   []Variable `yaml:"variables,omitempty"`
	SampleRow   []string   `yaml:"sample_row,omitempty"`
	DSL         string     `yaml:"dsl"`

	skeleton *yaml.Node
	// columns 是骨架里 tranche 参数表的列（params.columns）；hasParams 表示骨架带参数表，渲染时在副本的 params.rows 里填行。
	columns   []string
	hasParams bool
}

// Columns 返回参数表的列；模板没有参数表时为 nil。
func (t *Template) Columns() []string { return append([]string(nil), t.columns...) }

// Parse 解析模板 YAML，校验字段、变量声明与骨架，并用默认值与样例行试渲染一次。
func Parse(raw []byte) (*Template, error) {
	if len(raw) == 0 {
		return nil, errors.New("模板为空")
	}
	if len(raw) > maxTemplateBytes {
		return nil, fmt.Errorf("模板超过 %d KiB", maxTemplateBytes>>10)
	}
	var t Template
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&t); err != nil {
		return nil, fmt.Errorf("模板 YAML 无效：%w", err)
	}
	if !idPattern.MatchString(t.ID) {
		return nil, errors.New("template_id 必须是小写字母开头的字母、数字、下划线，最长 64 个字符")
	}
	if strings.TrimSpace(t.Name) == "" {
		return nil, errors.New("模板缺少 name")
	}
	if len(t.Variables) > maxVariables {
		return nil, fmt.Errorf("模板最多 %d 个变量", maxVariables)
	}
	seen := map[string]bool{NameVariable: true}
	for i, variable := range t.Variables {
		if !variablePattern.MatchString(variable.Name) {
			return nil, fmt.Errorf("variables[%d].name 必须是小写字母开头的字母、数字、下划线", i)
		}
		if seen[variable.Name] {
			return nil, fmt.Errorf("变量 %s 重复声明（name 是内置变量）", variable.Name)
		}
		seen[variable.Name] = true
		switch variable.Type {
		case TypeInt, TypeNumber, TypeString, TypeBool:
		default:
			return nil, fmt.Errorf("变量 %s 的 type 只能是 int、number、string 或 bool", variable.Name)
		}
		if (variable.Min != nil || variable.Max != nil) && variable.Type != TypeInt && variable.Type != TypeNumber {
			return nil, fmt.Errorf("变量 %s 不是数值类型，不能写 min、max", variable.Name)
		}
		if variable.Default != nil {
			if _, err := variable.convert(*variable.Default); err != nil {
				return nil, fmt.Errorf("变量 %s 的 default：%w", variable.Name, err)
			}
		}
	}
	var skeleton yaml.Node
	if err := yaml.Unmarshal([]byte(t.DSL), &skeleton); err != nil || skeleton.Kind != yaml.DocumentNode || len(skeleton.Content) == 0 {
		return nil, errors.New("模板的 dsl 不是有效的 YAML 对象")
	}
	t.skeleton = skeleton.Content[0]
	if err := t.checkPlaceholders(t.skeleton, seen); err != nil {
		return nil, err
	}
	if err := t.findParams(); err != nil {
		return nil, err
	}
	if t.hasParams && len(t.SampleRow) == 0 {
		return nil, errors.New("骨架含参数表，模板需要一行合法的 sample_row 用于保存时试渲染")
	}
	if !t.hasParams && len(t.SampleRow) > 0 {
		return nil, errors.New("骨架没有参数表，不能写 sample_row")
	}
	// 试渲染：策略名用模板 ID，变量用默认值（没有默认值的变量用类型下限的合法值无法确定，所以要求都有默认值）。
	vars := map[string]string{NameVariable: t.ID}
	for _, variable := range t.Variables {
		if variable.Default == nil {
			return nil, fmt.Errorf("变量 %s 需要 default（保存时用默认值试渲染）", variable.Name)
		}
		vars[variable.Name] = *variable.Default
	}
	var rows [][]string
	if t.hasParams {
		rows = [][]string{t.SampleRow}
	}
	if _, err := t.render(vars, rows); err != nil {
		return nil, fmt.Errorf("用默认值与 sample_row 试渲染失败：%w", err)
	}
	return &t, nil
}

// checkPlaceholders 检查骨架：键里不能有占位符；值里的 "{{" 必须是整个标量就是一个已声明变量的占位符。
func (t *Template) checkPlaceholders(node *yaml.Node, declared map[string]bool) error {
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			if strings.Contains(node.Content[i].Value, "{{") {
				return fmt.Errorf("键 %q 不能使用模板占位符", node.Content[i].Value)
			}
			if err := t.checkPlaceholders(node.Content[i+1], declared); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if err := t.checkPlaceholders(child, declared); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		if !strings.Contains(node.Value, "{{") && !strings.Contains(node.Value, "}}") {
			return nil
		}
		match := placeholderPattern.FindStringSubmatch(node.Value)
		if match == nil {
			return fmt.Errorf("%q：模板占位符必须独占整个值，写成 \"{{变量}}\"", node.Value)
		}
		if !declared[match[1]] {
			return fmt.Errorf("占位符 {{%s}} 没有声明", match[1])
		}
	case yaml.AliasNode:
		return errors.New("模板的 dsl 不能使用 YAML 别名")
	}
	return nil
}

// findParams 找到骨架里的参数表：最多一条规则带 params，它的 columns 必须是字面量列表、rows 必须为空列表。
func (t *Template) findParams() error {
	rules := mappingValue(t.skeleton, "rules")
	if rules == nil || rules.Kind != yaml.SequenceNode {
		return nil
	}
	for _, rule := range rules.Content {
		params := mappingValue(rule, "params")
		if params == nil {
			continue
		}
		if t.hasParams {
			return errors.New("模板最多一条规则带参数表")
		}
		columns := mappingValue(params, "columns")
		rows := mappingValue(params, "rows")
		if columns == nil || columns.Kind != yaml.SequenceNode || rows == nil || rows.Kind != yaml.SequenceNode {
			return errors.New("骨架的 params 需要 columns 列表与空的 rows 列表")
		}
		if len(rows.Content) > 0 {
			return errors.New("骨架的 params.rows 必须为空，行由建策略时的参数表填入")
		}
		for _, column := range columns.Content {
			t.columns = append(t.columns, column.Value)
		}
		t.hasParams = true
	}
	return nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// convert 按声明类型校验一个变量值，返回写入 YAML 的标量节点。
func (v Variable) convert(value string) (*yaml.Node, error) {
	value = strings.TrimSpace(value)
	switch v.Type {
	case TypeInt:
		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q 不是整数", value)
		}
		if err := v.inRange(float64(number)); err != nil {
			return nil, err
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(number, 10)}, nil
	case TypeNumber:
		number, err := strconv.ParseFloat(value, 64)
		if err != nil || number != number || number > 1e300 || number < -1e300 {
			return nil, fmt.Errorf("%q 不是有限的数字", value)
		}
		if err := v.inRange(number); err != nil {
			return nil, err
		}
		return numberNode(number), nil
	case TypeBool:
		flag, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("%q 不是 true 或 false", value)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(flag)}, nil
	default:
		return stringNode(value)
	}
}

// stringNode 返回字符串标量。值里不能有花括号：否则会被 tranche 参数表的 {列名} 展开二次解释（设计 6.1）。
func stringNode(value string) (*yaml.Node, error) {
	if strings.ContainsAny(value, "{}") {
		return nil, fmt.Errorf("%q：字符串值不能包含花括号", value)
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: yaml.DoubleQuotedStyle}, nil
}

// numberNode 写出数字标量：整数值用 !!int，否则用 !!float，标签与字面量一致，生成的 DSL 里不出现显式标签。
func numberNode(number float64) *yaml.Node {
	text := strconv.FormatFloat(number, 'g', -1, 64)
	if !strings.ContainsAny(text, ".eEn") {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: text}
	}
	if !strings.ContainsAny(text, ".n") {
		text = strconv.FormatFloat(number, 'f', -1, 64) // 1e-05 这类写法在 YAML 1.2 里不是浮点字面量
		if !strings.Contains(text, ".") {
			text += ".0"
		}
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: text}
}

func (v Variable) inRange(number float64) error {
	if v.Min != nil && number < *v.Min {
		return fmt.Errorf("%v 小于下限 %v", number, *v.Min)
	}
	if v.Max != nil && number > *v.Max {
		return fmt.Errorf("%v 大于上限 %v", number, *v.Max)
	}
	return nil
}

// cellNode 把参数表的一个单元格写成标量：整数、数字、布尔按字面量类型，其余是字符串（不能含花括号）。
func cellNode(value string) (*yaml.Node, error) {
	value = strings.TrimSpace(value)
	if _, err := strconv.ParseInt(value, 10, 64); err == nil {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: value}, nil
	}
	if number, err := strconv.ParseFloat(value, 64); err == nil && number == number && number <= 1e300 && number >= -1e300 {
		return numberNode(number), nil
	}
	if value == "true" || value == "false" {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: value}, nil
	}
	return stringNode(value)
}

// Render 用变量与参数表生成策略 DSL 并做完整校验。骨架含参数表时 rows 必须非空，不含时不接受 rows。
func (t *Template) Render(vars map[string]string, rows [][]string) (string, error) {
	if t.hasParams && len(rows) == 0 {
		return "", errors.New("模板带参数表，参数表不能为空")
	}
	if !t.hasParams && len(rows) > 0 {
		return "", errors.New("模板没有参数表，不接受参数表")
	}
	return t.render(vars, rows)
}

func (t *Template) render(vars map[string]string, rows [][]string) (string, error) {
	declared := map[string]Variable{NameVariable: {Name: NameVariable, Type: TypeString}}
	for _, variable := range t.Variables {
		declared[variable.Name] = variable
	}
	values := make(map[string]*yaml.Node, len(declared))
	for name := range vars {
		if _, ok := declared[name]; !ok {
			return "", fmt.Errorf("模板没有变量 %s", name)
		}
	}
	for name, variable := range declared {
		value, ok := vars[name]
		if !ok {
			if variable.Default == nil {
				return "", fmt.Errorf("缺少变量 %s", name)
			}
			value = *variable.Default
		}
		node, err := variable.convert(value)
		if err != nil {
			return "", fmt.Errorf("变量 %s：%w", name, err)
		}
		if name == NameVariable && strings.TrimSpace(node.Value) == "" {
			return "", errors.New("策略名 name 不能为空")
		}
		values[name] = node
	}
	root := copyNode(t.skeleton, values)
	if t.hasParams {
		rules := mappingValue(root, "rules")
		for _, rule := range rules.Content {
			params := mappingValue(rule, "params")
			if params == nil {
				continue
			}
			target := mappingValue(params, "rows")
			target.Style = 0
			for i, row := range rows {
				if len(row) != len(t.columns) {
					return "", fmt.Errorf("参数表第 %d 行有 %d 列，模板的参数表有 %d 列（%s）", i+1, len(row), len(t.columns), strings.Join(t.columns, ", "))
				}
				line := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
				for k, cell := range row {
					node, err := cellNode(cell)
					if err != nil {
						return "", fmt.Errorf("参数表第 %d 行第 %d 列：%w", i+1, k+1, err)
					}
					line.Content = append(line.Content, node)
				}
				target.Content = append(target.Content, line)
			}
		}
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(root); err != nil {
		return "", err
	}
	if err := encoder.Close(); err != nil {
		return "", err
	}
	text := out.String()
	if _, err := dsl.Parse([]byte(text)); err != nil {
		return "", fmt.Errorf("生成的 DSL 无效：%w", err)
	}
	return text, nil
}

// copyNode 复制骨架，把值为 "{{变量}}" 的标量换成变量节点（检查已保证占位符只出现在整个标量上）。
func copyNode(node *yaml.Node, values map[string]*yaml.Node) *yaml.Node {
	copied := *node
	if node.Kind == yaml.ScalarNode {
		if match := placeholderPattern.FindStringSubmatch(node.Value); match != nil {
			replacement := *values[match[1]]
			replacement.Line, replacement.Column = node.Line, node.Column
			return &replacement
		}
		return &copied
	}
	copied.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		copied.Content[i] = copyNode(child, values)
	}
	return &copied
}

// ParseRows 解析参数表 CSV：第一行是表头，必须与模板的参数表列一致。
func (t *Template) ParseRows(text string) ([][]string, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	reader := csv.NewReader(strings.NewReader(text))
	reader.TrimLeadingSpace = true
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("参数表 CSV 无效：%w", err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	header := records[0]
	if strings.Join(header, ",") != strings.Join(t.columns, ",") {
		return nil, fmt.Errorf("参数表表头 %s 与模板的列 %s 不一致", strings.Join(header, ","), strings.Join(t.columns, ","))
	}
	return records[1:], nil
}
