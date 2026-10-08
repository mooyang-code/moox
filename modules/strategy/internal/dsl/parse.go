package dsl

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	"gopkg.in/yaml.v3"
)

// maxDSLBytes 是 DSL 文本的大小上限。
const maxDSLBytes = 64 << 10

// Parse 解析并校验一份 DSL。解析经由 yaml.Node 完成：拒绝重复键、未知字段和多文档，
// 让配置错误在保存时就暴露，而不是等到求值。
func Parse(raw []byte) (Strategy, error) {
	if len(raw) > maxDSLBytes {
		return Strategy{}, fmt.Errorf("策略 DSL 超过 %d KiB 上限", maxDSLBytes>>10)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return Strategy{}, errors.New("策略 DSL 为空")
		}
		return Strategy{}, fmt.Errorf("解析策略 DSL 失败：%w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Strategy{}, errors.New("策略 DSL 只能包含一个 YAML 文档")
		}
		return Strategy{}, fmt.Errorf("解析策略 DSL 失败：%w", err)
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return Strategy{}, errors.New("策略 DSL 必须是一个 YAML 对象")
	}
	strategy, err := decodeStrategy(document.Content[0])
	if err != nil {
		return Strategy{}, err
	}
	if err := Validate(&strategy); err != nil {
		return Strategy{}, err
	}
	return strategy, nil
}

type field struct {
	key   string
	value *yaml.Node
}

func mappingFields(node *yaml.Node, path string) ([]field, error) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s 必须是一个对象", path)
	}
	fields := make([]field, 0, len(node.Content)/2)
	seen := make(map[string]struct{}, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("%s 的字段名必须是字符串", path)
		}
		if _, exists := seen[key.Value]; exists {
			return nil, fmt.Errorf("%s 包含重复字段 %q", path, key.Value)
		}
		seen[key.Value] = struct{}{}
		fields = append(fields, field{key: key.Value, value: node.Content[i+1]})
	}
	return fields, nil
}

func scalarString(node *yaml.Node, path string) (string, error) {
	if node == nil || node.Kind != yaml.ScalarNode || (node.Tag != "!!str" && node.Tag != "!!int" && node.Tag != "!!float" && node.Tag != "!!bool") {
		return "", fmt.Errorf("%s 必须是字符串", path)
	}
	return strings.TrimSpace(node.Value), nil
}

func scalarRequiredText(node *yaml.Node, path string) (string, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", fmt.Errorf("%s 必须是字符串", path)
	}
	value := strings.TrimSpace(node.Value)
	if value == "" {
		return "", fmt.Errorf("%s 不能为空", path)
	}
	return value, nil
}

func scalarInt(node *yaml.Node, path string) (int, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
		return 0, fmt.Errorf("%s 必须是整数", path)
	}
	value, err := strconv.Atoi(strings.TrimSpace(node.Value))
	if err != nil {
		return 0, fmt.Errorf("%s 必须是整数", path)
	}
	return value, nil
}

func scalarDecimal(node *yaml.Node, path string) (quant.Decimal, error) {
	if node == nil || node.Kind != yaml.ScalarNode || (node.Tag != "!!int" && node.Tag != "!!float" && node.Tag != "!!str") {
		return quant.Decimal{}, fmt.Errorf("%s 必须是数字", path)
	}
	value, err := quant.ParseLiteral(node.Value)
	if err != nil {
		return quant.Decimal{}, fmt.Errorf("%s 必须是数字：%w", path, err)
	}
	return value, nil
}

func stringList(node *yaml.Node, path string) ([]string, error) {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s 必须是字符串列表", path)
	}
	// 标的与标签 ID 区分大小写：去重与匹配使用同一规则，拼写不存在的 ID 在启用时报错。
	values := make([]string, 0, len(node.Content))
	seen := make(map[string]struct{}, len(node.Content))
	for i, item := range node.Content {
		value, err := scalarRequiredText(item, fmt.Sprintf("%s[%d]", path, i))
		if err != nil {
			return nil, err
		}
		if _, exists := seen[value]; exists {
			return nil, fmt.Errorf("%s 包含重复项 %q", path, value)
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values, nil
}

func intList(node *yaml.Node, path string) ([]int, error) {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s 必须是整数列表", path)
	}
	values := make([]int, 0, len(node.Content))
	for i, item := range node.Content {
		value, err := scalarInt(item, fmt.Sprintf("%s[%d]", path, i))
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func decodeStrategy(root *yaml.Node) (Strategy, error) {
	fields, err := mappingFields(root, "策略 DSL")
	if err != nil {
		return Strategy{}, err
	}
	strategy := Strategy{Portfolio: defaultPortfolio()}
	rulesSeen := false
	for _, f := range fields {
		switch f.key {
		case "name":
			strategy.Name, err = scalarRequiredText(f.value, "name")
		case "bar":
			strategy.Bar, err = scalarRequiredText(f.value, "bar")
		case "universe":
			strategy.Universe, err = decodeUniverse(f.value)
		case "rules":
			rulesSeen = true
			strategy.Rules, err = decodeRules(f.value)
		case "portfolio":
			strategy.Portfolio, err = decodePortfolio(f.value)
		default:
			return Strategy{}, fmt.Errorf("策略 DSL 包含未知字段 %q", f.key)
		}
		if err != nil {
			return Strategy{}, err
		}
	}
	if !rulesSeen {
		return Strategy{}, errors.New("策略 DSL 缺少 rules")
	}
	return strategy, nil
}

func decodeUniverse(node *yaml.Node) (Universe, error) {
	fields, err := mappingFields(node, "universe")
	if err != nil {
		return Universe{}, err
	}
	var universe Universe
	for _, f := range fields {
		path := "universe." + f.key
		switch f.key {
		case "tags":
			universe.Tags, err = stringList(f.value, path)
		case "exclude_tags":
			universe.ExcludeTags, err = stringList(f.value, path)
		case "include":
			universe.Include, err = stringList(f.value, path)
		case "exclude":
			universe.Exclude, err = stringList(f.value, path)
		case "min_age_bars":
			universe.MinAgeBars, err = scalarInt(f.value, path)
		default:
			return Universe{}, fmt.Errorf("universe 包含未知字段 %q", f.key)
		}
		if err != nil {
			return Universe{}, err
		}
	}
	return universe, nil
}

func decodeRules(node *yaml.Node) ([]Rule, error) {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil, errors.New("rules 必须是规则列表，每条规则包含 id、type 等字段")
	}
	rules := make([]Rule, 0, len(node.Content))
	for i, item := range node.Content {
		rule, err := decodeRule(item, fmt.Sprintf("rules[%d]", i))
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func decodeRule(node *yaml.Node, path string) (Rule, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return Rule{}, err
	}
	rule := Rule{Side: SideLong, Weight: Weight{Method: WeightMethodEqual}}
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "id":
			rule.ID, err = scalarRequiredText(f.value, fieldPath)
			if err == nil {
				path = "rules." + rule.ID
			}
		case "name":
			rule.Name, err = scalarString(f.value, fieldPath)
		case "type":
			rule.Type, err = scalarRequiredText(f.value, fieldPath)
		case "pool":
			rule.Pool, err = decodePool(f.value, fieldPath)
		case "filter":
			rule.Filter, err = scalarRequiredText(f.value, fieldPath)
		case "score":
			rule.Score, err = scalarRequiredText(f.value, fieldPath)
		case "filter_after":
			rule.FilterAfter, err = scalarRequiredText(f.value, fieldPath)
		case "select":
			rule.HasSelect = true
			rule.Select, err = decodeSelect(f.value, fieldPath)
		case "weight":
			rule.Weight, err = decodeWeight(f.value, fieldPath)
		case "side":
			rule.Side, err = scalarRequiredText(f.value, fieldPath)
		case "entry":
			rule.Entry, err = scalarRequiredText(f.value, fieldPath)
		case "exit":
			rule.Exit, err = scalarRequiredText(f.value, fieldPath)
		case "holding":
			var holding Holding
			holding, err = decodeHolding(f.value, fieldPath)
			rule.Holding = &holding
		default:
			return Rule{}, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return Rule{}, err
		}
	}
	return rule, nil
}

func decodePool(node *yaml.Node, path string) (Pool, error) {
	if node == nil {
		return Pool{}, fmt.Errorf("%s 必须是标的列表或 {tags: [...]}", path)
	}
	switch node.Kind {
	case yaml.SequenceNode:
		fixed, err := stringList(node, path)
		if err != nil {
			return Pool{}, err
		}
		return Pool{Explicit: true, Fixed: fixed}, nil
	case yaml.MappingNode:
		fields, err := mappingFields(node, path)
		if err != nil {
			return Pool{}, err
		}
		pool := Pool{Explicit: true}
		for _, f := range fields {
			if f.key != "tags" {
				return Pool{}, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
			}
			pool.Tags, err = stringList(f.value, path+".tags")
			if err != nil {
				return Pool{}, err
			}
		}
		if len(pool.Tags) == 0 {
			return Pool{}, fmt.Errorf("%s.tags 不能为空", path)
		}
		return pool, nil
	default:
		return Pool{}, fmt.Errorf("%s 必须是标的列表或 {tags: [...]}", path)
	}
}

func decodeSelect(node *yaml.Node, path string) (Select, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return Select{}, err
	}
	var sel Select
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "top":
			sel.Top, err = scalarInt(f.value, fieldPath)
		case "bottom":
			sel.Bottom, err = scalarInt(f.value, fieldPath)
		case "buffer":
			sel.Buffer, err = scalarInt(f.value, fieldPath)
		case "where":
			sel.Where, err = scalarRequiredText(f.value, fieldPath)
		default:
			return Select{}, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return Select{}, err
		}
	}
	return sel, nil
}

func decodeWeight(node *yaml.Node, path string) (Weight, error) {
	if node == nil {
		return Weight{}, fmt.Errorf("%s 必须是数字或对象", path)
	}
	weight := Weight{Method: WeightMethodEqual}
	if node.Kind == yaml.ScalarNode {
		total, err := scalarDecimal(node, path)
		if err != nil {
			return Weight{}, err
		}
		weight.Total = total
		return weight, nil
	}
	fields, err := mappingFields(node, path)
	if err != nil {
		return Weight{}, err
	}
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "total":
			weight.Total, err = scalarDecimal(f.value, fieldPath)
		case "method":
			weight.Method, err = scalarRequiredText(f.value, fieldPath)
		case "cap":
			weight.Cap, err = scalarDecimal(f.value, fieldPath)
			weight.HasCap = true
		default:
			return Weight{}, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return Weight{}, err
		}
	}
	return weight, nil
}

func decodeHolding(node *yaml.Node, path string) (Holding, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return Holding{}, err
	}
	var holding Holding
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "bars":
			holding.Bars, err = scalarInt(f.value, fieldPath)
		case "offsets":
			holding.Offsets, err = intList(f.value, fieldPath)
		default:
			return Holding{}, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return Holding{}, err
		}
	}
	return holding, nil
}

// defaultPortfolio 是省略 portfolio 字段时的取值；显式写出的 0 不会被默认值覆盖。
func defaultPortfolio() Portfolio {
	return Portfolio{Leverage: quant.One(), MaxMissing: quant.Must(DefaultMaxMissing)}
}

func decodePortfolio(node *yaml.Node) (Portfolio, error) {
	fields, err := mappingFields(node, "portfolio")
	if err != nil {
		return Portfolio{}, err
	}
	portfolio := defaultPortfolio()
	for _, f := range fields {
		path := "portfolio." + f.key
		switch f.key {
		case "leverage":
			portfolio.Leverage, err = scalarDecimal(f.value, path)
		case "max_weight":
			portfolio.MaxWeight, err = scalarDecimal(f.value, path)
			portfolio.HasMaxWeight = true
		case "min_universe":
			portfolio.MinUniverse, err = scalarInt(f.value, path)
		case "max_missing":
			portfolio.MaxMissing, err = scalarDecimal(f.value, path)
		default:
			return Portfolio{}, fmt.Errorf("portfolio 包含未知字段 %q", f.key)
		}
		if err != nil {
			return Portfolio{}, err
		}
	}
	return portfolio, nil
}
