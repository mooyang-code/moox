package dsl

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// f1 规则：语义与 alphalib F1 框架的 F1StrategyConfig / F1MultiStrategy 一致（docs/计划/F1策略迁移/F1策略迁移设计.md 第 1、7 节）。
// 一条 f1 规则由一个子策略模板和可选的参数表组成：参数表每行展开为一个子策略，模板里的 "{列名}" 取该行的值。

// RuleTypeF1 是语义与 F1 框架一致的截面选币规则。
const RuleTypeF1 = "f1"

// f1 的取值上限。
const (
	MaxF1Rows          = 5000  // 一条 f1 规则的参数表最多行数
	MaxF1SubStrategies = 10000 // 一份 DSL 里全部 f1 子策略的上限
	MaxF1Factors       = 16    // 每边因子个数上限
	MaxF1Filters       = 32    // 每个子策略的前置与后置过滤条件合计上限
	MaxF1HoldPeriod    = 2016  // hold_period 上限（5m 下一周）
	maxF1GroupItems    = 9     // 逻辑组合里的条件数：F1 用单个数字编号
	f1Bound            = 1e100 // F1 把 lt/gt 一类写成以 ±1e100 为边界的 between
)

// 过滤类型与比较方式（F1 的 filter_type 与 compare_operator）。
const (
	F1FilterValue = "value"
	F1FilterRank  = "rank"
	F1FilterPct   = "pct"
)

var f1Ops = map[string]bool{"lt": true, "lte": true, "gt": true, "gte": true, "bt": true, "bte": true, "nbt": true, "nbte": true, "eq": true, "ne": true}

// F1Factor 是一个选币因子：列名、排序方向（descending 为真时因子值大者优先）与权重。
type F1Factor struct {
	Column     string  `json:"column"`
	Descending bool    `json:"descending"`
	Weight     float64 `json:"weight"`
}

// F1Filter 是一个过滤条件，或带逻辑组合的一组条件（Logic 非空时 Items 是组内条件）。
// 单个条件已按 F1 规范化：between(Lo, Hi, Inclusive)，Negate 为真时取反（F1 的 nb* 与 ne）。
type F1Filter struct {
	Short         bool       `json:"short,omitempty"`
	Column        string     `json:"column,omitempty"`
	Type          string     `json:"type,omitempty"`
	Lo            float64    `json:"lo,omitempty"`
	Hi            float64    `json:"hi,omitempty"`
	Inclusive     bool       `json:"inclusive,omitempty"`
	Negate        bool       `json:"negate,omitempty"`
	RankAscending bool       `json:"rank_ascending,omitempty"`
	Logic         string     `json:"logic,omitempty"`
	Items         []F1Filter `json:"items,omitempty"`
	logic         *f1Logic
}

// F1Sub 是展开后的一个子策略（对应一个 F1StrategyConfig）。
type F1Sub struct {
	Row          int        `json:"row"` // 参数表行号（从 1 数）；没有参数表时为 0
	HoldPeriod   int        `json:"hold_period"`
	CalcType     string     `json:"calc_factor_type"`
	Seal999      bool       `json:"seal999"`
	LongFactors  []F1Factor `json:"long_factors"`
	ShortFactors []F1Factor `json:"short_factors"`
	LongWeight   float64    `json:"long_weight"`
	ShortWeight  float64    `json:"short_weight"`
	LongCoinNum  float64    `json:"long_coin_num"`
	ShortCoinNum float64    `json:"short_coin_num"`
	LongWhite    []string   `json:"long_white_list"`
	LongBlack    []string   `json:"long_black_list"`
	ShortWhite   []string   `json:"short_white_list"`
	ShortBlack   []string   `json:"short_black_list"`
	FilterBefore []F1Filter `json:"filter_before"`
	FilterAfter  []F1Filter `json:"filter_after"`
	StgWeight    float64    `json:"stg_weight"`
}

// F1Spec 是一条 f1 规则的子策略模板与参数表；Subs 是校验时展开的结果。
type F1Spec struct {
	Template *yaml.Node
	Columns  []string
	Rows     [][]*yaml.Node
	Subs     []F1Sub
}

// F1Group 是排名签名相同的一组子策略：它们每期的候选、排名与选币完全相同，只算一次。
// StateKey 同时覆盖签名与组内全部子策略：组成员或任何子策略字段变化时，上一期的开仓记录不再沿用。
type F1Group struct {
	Key      string
	StateKey string
	Subs     []int // F1Spec.Subs 的下标
}

var (
	placeholderPattern = regexp.MustCompile(`\{([a-z][a-z0-9_]{0,31})\}`)
	paramColumnPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

func decodeF1(node *yaml.Node, path string, rule *Rule) error {
	if node == nil || node.Kind != yaml.MappingNode {
		return fmt.Errorf("%s 必须是子策略模板对象", path)
	}
	if rule.F1 == nil {
		rule.F1 = &F1Spec{}
	}
	rule.F1.Template = node
	return nil
}

func decodeF1Params(node *yaml.Node, path string, rule *Rule) error {
	fields, err := mappingFields(node, path)
	if err != nil {
		return err
	}
	if rule.F1 == nil {
		rule.F1 = &F1Spec{}
	}
	spec := rule.F1
	seenColumns, seenRows := false, false
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "columns":
			seenColumns = true
			spec.Columns, err = stringList(f.value, fieldPath)
			if err == nil {
				for _, column := range spec.Columns {
					if !paramColumnPattern.MatchString(column) {
						return fmt.Errorf("%s 的列名 %q 只能用小写字母、数字和下划线，并以字母开头", fieldPath, column)
					}
				}
			}
		case "rows":
			seenRows = true
			if f.value == nil || f.value.Kind != yaml.SequenceNode {
				return fmt.Errorf("%s 必须是列表，每行是一个数组", fieldPath)
			}
			spec.Rows = make([][]*yaml.Node, 0, len(f.value.Content))
			for i, item := range f.value.Content {
				if item.Kind != yaml.SequenceNode {
					return fmt.Errorf("%s 第 %d 行必须是数组", fieldPath, i+1)
				}
				for j, cell := range item.Content {
					if cell.Kind != yaml.ScalarNode || (cell.Tag != "!!int" && cell.Tag != "!!float" && cell.Tag != "!!str") {
						return fmt.Errorf("%s 第 %d 行第 %d 列必须是数字或字符串", fieldPath, i+1, j+1)
					}
				}
				spec.Rows = append(spec.Rows, item.Content)
			}
		default:
			return fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return err
		}
	}
	if !seenColumns || !seenRows {
		return fmt.Errorf("%s 需要同时给出 columns 和 rows", path)
	}
	return nil
}

// hasF1Params 报告 DSL 里是否有带参数表的 f1 规则（只有这时才放宽 DSL 大小上限）。
func (s Strategy) hasF1Params() bool {
	for _, rule := range s.Rules {
		if rule.Type == RuleTypeF1 && rule.F1 != nil && len(rule.F1.Rows) > 0 {
			return true
		}
	}
	return false
}

// HasF1 报告 DSL 里是否有 f1 规则。
func (s Strategy) HasF1() bool {
	for _, rule := range s.Rules {
		if rule.Type == RuleTypeF1 {
			return true
		}
	}
	return false
}

// validateF1Rule 校验 f1 规则并展开子策略。
func validateF1Rule(prefix string, rule *Rule) error {
	if rule.Pool.Explicit || rule.Filter != "" || rule.Score != "" || rule.HasSelect || rule.FilterAfter != "" || rule.Holding != nil || rule.Entry != "" || rule.Exit != "" {
		return fmt.Errorf("%s 是 f1 规则，只能使用 id、name、type、weight、f1、params", prefix)
	}
	if rule.Side != SideLong {
		return fmt.Errorf("%s 是 f1 规则，多空方向写在 f1 模板里（long_*、short_*），不能写 side", prefix)
	}
	if rule.Weight.HasCap || rule.Weight.Method != WeightMethodEqual {
		return fmt.Errorf("%s 是 f1 规则，weight 只接受 total", prefix)
	}
	spec := rule.F1
	if spec == nil || spec.Template == nil {
		return fmt.Errorf("%s 缺少 f1 子策略模板", prefix)
	}
	rows := spec.Rows
	if spec.Columns == nil {
		rows = [][]*yaml.Node{nil}
	}
	if spec.Columns != nil && len(rows) == 0 {
		return fmt.Errorf("%s 的 params.rows 不能为空", prefix)
	}
	if len(rows) > MaxF1Rows {
		return fmt.Errorf("%s 的参数表有 %d 行，超过上限 %d", prefix, len(rows), MaxF1Rows)
	}
	columnIndex := make(map[string]int, len(spec.Columns))
	for i, column := range spec.Columns {
		columnIndex[column] = i
	}
	used := make(map[string]bool)
	if err := collectPlaceholders(spec.Template, used); err != nil {
		return fmt.Errorf("%s 的 f1 模板：%w", prefix, err)
	}
	for name := range used {
		if _, ok := columnIndex[name]; !ok {
			return fmt.Errorf("%s 的 f1 模板引用了占位符 {%s}，参数表里没有这一列", prefix, name)
		}
	}
	subs := make([]F1Sub, 0, len(rows))
	for i, row := range rows {
		where := prefix
		if spec.Columns != nil {
			where = fmt.Sprintf("%s 参数表第 %d 行", prefix, i+1)
			if len(row) != len(spec.Columns) {
				return fmt.Errorf("%s 有 %d 列，columns 声明了 %d 列", where, len(row), len(spec.Columns))
			}
		}
		node := substitute(spec.Template, columnIndex, row)
		sub, err := decodeF1Sub(node, where)
		if err != nil {
			return err
		}
		if spec.Columns != nil {
			sub.Row = i + 1
		}
		subs = append(subs, sub)
	}
	spec.Subs = subs
	return nil
}

// validateF1Strategy 做跨规则的校验：f1 规则不与其他类型混用、seal999 一致、子策略总数、不能用 factor_params。
func validateF1Strategy(s *Strategy) error {
	if !s.HasF1() {
		return nil
	}
	total := 0
	seal := -1
	for _, rule := range s.Rules {
		if rule.Type != RuleTypeF1 {
			return fmt.Errorf("含 f1 规则的策略只能全部是 f1 规则，规则 %s 是 %s", rule.ID, rule.Type)
		}
		total += len(rule.F1.Subs)
		for _, sub := range rule.F1.Subs {
			value := 0
			if sub.Seal999 {
				value = 1
			}
			if seal >= 0 && seal != value {
				return errors.New("同一份策略里所有 f1 子策略的 seal999 必须相同（年龄判定是策略级的）")
			}
			seal = value
		}
	}
	if total > MaxF1SubStrategies {
		return fmt.Errorf("f1 子策略合计 %d 个，超过上限 %d", total, MaxF1SubStrategies)
	}
	if seal == 1 && s.Universe.MinAgeBars < 999 {
		return errors.New("f1 子策略开启了 seal999，需要 universe.min_age_bars ≥ 999（近似 F1 的去掉每个标的前 999 根）")
	}
	if len(s.FactorParams) > 0 {
		return errors.New("含 f1 规则的策略不支持 factor_params")
	}
	return nil
}

func collectPlaceholders(node *yaml.Node, used map[string]bool) error {
	if node.Kind == yaml.ScalarNode {
		for _, match := range placeholderPattern.FindAllStringSubmatch(node.Value, -1) {
			used[match[1]] = true
		}
		if strings.Contains(placeholderPattern.ReplaceAllString(node.Value, ""), "{") {
			return fmt.Errorf("%q 里的占位符写法不对，应为 {小写列名}", node.Value)
		}
		return nil
	}
	for _, child := range node.Content {
		if err := collectPlaceholders(child, used); err != nil {
			return err
		}
	}
	return nil
}

// substitute 复制模板并替换占位符：整个标量就是一个占位符时，换成参数表里的原样字面量（保留数字类型）；
// 出现在字符串里时按原样文本拼接。
func substitute(node *yaml.Node, columns map[string]int, row []*yaml.Node) *yaml.Node {
	copied := *node
	if node.Kind == yaml.ScalarNode {
		if row == nil || !strings.Contains(node.Value, "{") {
			return &copied
		}
		if match := placeholderPattern.FindStringSubmatch(node.Value); match != nil && match[0] == node.Value {
			cell := row[columns[match[1]]]
			copied.Value, copied.Tag, copied.Style = cell.Value, cell.Tag, 0
			return &copied
		}
		copied.Value = placeholderPattern.ReplaceAllStringFunc(node.Value, func(token string) string {
			return row[columns[token[1:len(token)-1]]].Value
		})
		copied.Tag = "!!str"
		return &copied
	}
	copied.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		copied.Content[i] = substitute(child, columns, row)
	}
	return &copied
}

func scalarFloat(node *yaml.Node, path string) (float64, error) {
	if node == nil || node.Kind != yaml.ScalarNode || (node.Tag != "!!int" && node.Tag != "!!float") {
		return 0, fmt.Errorf("%s 必须是数字", path)
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(node.Value), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("%s 必须是有限的数字", path)
	}
	return value, nil
}

func scalarBool(node *yaml.Node, path string) (bool, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!bool" {
		return false, fmt.Errorf("%s 必须是 true 或 false", path)
	}
	return strconv.ParseBool(strings.TrimSpace(node.Value))
}

func decodeF1Sub(node *yaml.Node, path string) (F1Sub, error) {
	fields, err := mappingFields(node, path+" 的 f1")
	if err != nil {
		return F1Sub{}, err
	}
	// 默认值与 F1StrategyConfig 相同（hold_period=12、enable_seal999=True、两边权重与个数都为 1）。
	sub := F1Sub{HoldPeriod: 12, CalcType: "cross", Seal999: true, LongWeight: 1, ShortWeight: 1, LongCoinNum: 1, ShortCoinNum: 1, StgWeight: 1}
	for _, f := range fields {
		fieldPath := path + " 的 f1." + f.key
		switch f.key {
		case "hold_period":
			sub.HoldPeriod, err = scalarInt(f.value, fieldPath)
		case "calc_factor_type":
			sub.CalcType, err = scalarRequiredText(f.value, fieldPath)
		case "seal999":
			sub.Seal999, err = scalarBool(f.value, fieldPath)
		case "long_factors":
			sub.LongFactors, err = decodeF1Factors(f.value, fieldPath)
		case "short_factors":
			sub.ShortFactors, err = decodeF1Factors(f.value, fieldPath)
		case "long_weight":
			sub.LongWeight, err = scalarFloat(f.value, fieldPath)
		case "short_weight":
			sub.ShortWeight, err = scalarFloat(f.value, fieldPath)
		case "long_coin_num":
			sub.LongCoinNum, err = scalarFloat(f.value, fieldPath)
		case "short_coin_num":
			sub.ShortCoinNum, err = scalarFloat(f.value, fieldPath)
		case "long_white_list":
			sub.LongWhite, err = stringList(f.value, fieldPath)
		case "long_black_list":
			sub.LongBlack, err = stringList(f.value, fieldPath)
		case "short_white_list":
			sub.ShortWhite, err = stringList(f.value, fieldPath)
		case "short_black_list":
			sub.ShortBlack, err = stringList(f.value, fieldPath)
		case "filter_before":
			sub.FilterBefore, err = decodeF1Filters(f.value, fieldPath)
		case "filter_after":
			sub.FilterAfter, err = decodeF1Filters(f.value, fieldPath)
		case "stg_weight":
			sub.StgWeight, err = scalarFloat(f.value, fieldPath)
		default:
			return F1Sub{}, fmt.Errorf("%s 的 f1 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return F1Sub{}, err
		}
	}
	if err := validateF1Sub(path, &sub); err != nil {
		return F1Sub{}, err
	}
	return sub, nil
}

func validateF1Sub(path string, sub *F1Sub) error {
	if sub.HoldPeriod < 1 || sub.HoldPeriod > MaxF1HoldPeriod {
		return fmt.Errorf("%s 的 hold_period 必须在 1 到 %d 之间", path, MaxF1HoldPeriod)
	}
	if sub.CalcType != "cross" && sub.CalcType != "vertical" {
		return fmt.Errorf("%s 的 calc_factor_type 只能是 cross 或 vertical", path)
	}
	if len(sub.LongFactors) > MaxF1Factors || len(sub.ShortFactors) > MaxF1Factors {
		return fmt.Errorf("%s 每边最多 %d 个因子", path, MaxF1Factors)
	}
	for _, side := range []struct {
		name            string
		weight, coinNum float64
		factors         []F1Factor
	}{{"long", sub.LongWeight, sub.LongCoinNum, sub.LongFactors}, {"short", sub.ShortWeight, sub.ShortCoinNum, sub.ShortFactors}} {
		if side.weight < 0 || side.weight > 20 {
			return fmt.Errorf("%s 的 %s_weight 必须在 0 到 20 之间", path, side.name)
		}
		if side.coinNum < 0 || side.coinNum > MaxCount || (side.coinNum >= 1 && side.coinNum != float64(int(side.coinNum))) {
			return fmt.Errorf("%s 的 %s_coin_num 必须是 0～1 之间的比例或不超过 %d 的整数", path, side.name, MaxCount)
		}
		if side.weight > 0 && side.coinNum > 0 && len(side.factors) == 0 {
			return fmt.Errorf("%s 的 %s_factors 不能为空（%s_weight 与 %s_coin_num 都大于 0）", path, side.name, side.name, side.name)
		}
	}
	if sub.StgWeight <= 0 {
		return fmt.Errorf("%s 的 stg_weight 必须大于 0", path)
	}
	count := countF1Filters(sub.FilterBefore) + countF1Filters(sub.FilterAfter)
	if count > MaxF1Filters {
		return fmt.Errorf("%s 的过滤条件合计 %d 个，超过上限 %d", path, count, MaxF1Filters)
	}
	return nil
}

func countF1Filters(filters []F1Filter) int {
	count := 0
	for _, filter := range filters {
		if filter.Logic != "" {
			count += len(filter.Items)
		} else {
			count++
		}
	}
	return count
}

func decodeF1Factors(node *yaml.Node, path string) ([]F1Factor, error) {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s 必须是因子列表", path)
	}
	factors := make([]F1Factor, 0, len(node.Content))
	for i, item := range node.Content {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		fields, err := mappingFields(item, itemPath)
		if err != nil {
			return nil, err
		}
		factor := F1Factor{Descending: true, Weight: 1}
		for _, f := range fields {
			fieldPath := itemPath + "." + f.key
			switch f.key {
			case "factor":
				factor.Column, err = scalarRequiredText(f.value, fieldPath)
			case "descending":
				factor.Descending, err = scalarBool(f.value, fieldPath)
			case "weight":
				factor.Weight, err = scalarFloat(f.value, fieldPath)
			default:
				return nil, fmt.Errorf("%s 包含未知字段 %q", itemPath, f.key)
			}
			if err != nil {
				return nil, err
			}
		}
		if factor.Column == "" {
			return nil, fmt.Errorf("%s 缺少 factor（列名）", itemPath)
		}
		factors = append(factors, factor)
	}
	return factors, nil
}

func decodeF1Filters(node *yaml.Node, path string) ([]F1Filter, error) {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s 必须是过滤条件列表", path)
	}
	filters := make([]F1Filter, 0, len(node.Content))
	for i, item := range node.Content {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		filter, err := decodeF1Filter(item, itemPath, true)
		if err != nil {
			return nil, err
		}
		filters = append(filters, filter)
	}
	return filters, nil
}

func decodeF1Filter(node *yaml.Node, path string, allowGroup bool) (F1Filter, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return F1Filter{}, err
	}
	var direction, op, column, ftype string
	var valueNode *yaml.Node
	var rankAscending bool
	var filter F1Filter
	var itemsNode *yaml.Node
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "direction":
			direction, err = scalarRequiredText(f.value, fieldPath)
		case "factor":
			column, err = scalarRequiredText(f.value, fieldPath)
		case "type":
			ftype, err = scalarRequiredText(f.value, fieldPath)
		case "op":
			op, err = scalarRequiredText(f.value, fieldPath)
		case "value":
			valueNode = f.value
		case "rank_ascending":
			rankAscending, err = scalarBool(f.value, fieldPath)
		case "logic":
			if !allowGroup {
				return F1Filter{}, fmt.Errorf("%s 不能嵌套逻辑组合", path)
			}
			filter.Logic, err = scalarRequiredText(f.value, fieldPath)
		case "items":
			if !allowGroup {
				return F1Filter{}, fmt.Errorf("%s 不能嵌套逻辑组合", path)
			}
			itemsNode = f.value
		default:
			return F1Filter{}, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return F1Filter{}, err
		}
	}
	if filter.Logic != "" || itemsNode != nil {
		if filter.Logic == "" || itemsNode == nil || direction != "" || column != "" || ftype != "" || op != "" || valueNode != nil {
			return F1Filter{}, fmt.Errorf("%s 是逻辑组合，只能写 logic 和 items", path)
		}
		if itemsNode.Kind != yaml.SequenceNode || len(itemsNode.Content) == 0 || len(itemsNode.Content) > maxF1GroupItems {
			return F1Filter{}, fmt.Errorf("%s.items 必须是 1～%d 个条件", path, maxF1GroupItems)
		}
		for i, item := range itemsNode.Content {
			child, err := decodeF1Filter(item, fmt.Sprintf("%s.items[%d]", path, i), false)
			if err != nil {
				return F1Filter{}, err
			}
			if i > 0 && child.Short != filter.Items[0].Short {
				return F1Filter{}, fmt.Errorf("%s 组内条件必须是同一个方向", path)
			}
			filter.Items = append(filter.Items, child)
		}
		filter.Short = filter.Items[0].Short
		logic, err := parseF1Logic(filter.Logic, len(filter.Items))
		if err != nil {
			return F1Filter{}, fmt.Errorf("%s.logic %q 无效：%w", path, filter.Logic, err)
		}
		filter.logic = logic
		return filter, nil
	}
	// 默认值与 F1FilterParams 相同（direction=df1、filter_type=value、compare_operator=lt、filter_value=0.2）。
	if op == "" {
		op = "lt"
	}
	if valueNode == nil {
		valueNode = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: "0.2"}
	}
	switch direction {
	case "", "long", "df1":
	case "short", "df2":
		filter.Short = true
	default:
		return F1Filter{}, fmt.Errorf("%s.direction 必须是 long 或 short（也接受 df1、df2）", path)
	}
	if column == "" {
		return F1Filter{}, fmt.Errorf("%s 缺少 factor（列名）", path)
	}
	if ftype == "" {
		ftype = F1FilterValue
	}
	if ftype != F1FilterValue && ftype != F1FilterRank && ftype != F1FilterPct {
		return F1Filter{}, fmt.Errorf("%s.type 只能是 value、rank 或 pct", path)
	}
	if !f1Ops[op] {
		return F1Filter{}, fmt.Errorf("%s.op 只能是 lt、lte、gt、gte、bt、bte、nbt、nbte、eq、ne", path)
	}
	filter.Column, filter.Type, filter.RankAscending = column, ftype, rankAscending
	if ftype == F1FilterPct {
		filter.RankAscending = true // F1：pct 固定从小到大
	}
	if err := normalizeF1Compare(&filter, op, valueNode, path); err != nil {
		return F1Filter{}, err
	}
	return filter, nil
}

// normalizeF1Compare 按 f1_utils.filter_generate 把比较方式规范化为 between(Lo, Hi, Inclusive) 与是否取反。
func normalizeF1Compare(filter *F1Filter, op string, valueNode *yaml.Node, path string) error {
	if valueNode == nil {
		return fmt.Errorf("%s 缺少 value", path)
	}
	checkValue := func(v float64, where string) error {
		switch filter.Type {
		case F1FilterRank:
			if v != float64(int64(v)) {
				return fmt.Errorf("%s 是 rank 过滤，value 必须是整数", where)
			}
		case F1FilterPct:
			if v < 0 || v > 1 {
				return fmt.Errorf("%s 是 pct 过滤，value 必须在 0 到 1 之间", where)
			}
		}
		return nil
	}
	switch op {
	case "lt", "lte", "gt", "gte", "eq", "ne":
		v, err := scalarFloat(valueNode, path+".value")
		if err != nil {
			return fmt.Errorf("%s 的比较方式 %s 需要一个数字：%w", path, op, err)
		}
		if err := checkValue(v, path+".value"); err != nil {
			return err
		}
		switch op {
		case "lt", "lte":
			filter.Lo, filter.Hi = -f1Bound, v
		case "gt", "gte":
			filter.Lo, filter.Hi = v, f1Bound
		case "eq":
			op, filter.Lo, filter.Hi = "bte", v, v
		case "ne":
			op, filter.Lo, filter.Hi = "nbt", v, v
		}
	default:
		if valueNode.Kind != yaml.SequenceNode || len(valueNode.Content) != 2 {
			return fmt.Errorf("%s 的比较方式 %s 需要 [下限, 上限]", path, op)
		}
		lo, err := scalarFloat(valueNode.Content[0], path+".value[0]")
		if err != nil {
			return err
		}
		hi, err := scalarFloat(valueNode.Content[1], path+".value[1]")
		if err != nil {
			return err
		}
		if lo > hi {
			return fmt.Errorf("%s.value 的下限不能大于上限", path)
		}
		if err := checkValue(lo, path+".value[0]"); err != nil {
			return err
		}
		if err := checkValue(hi, path+".value[1]"); err != nil {
			return err
		}
		filter.Lo, filter.Hi = lo, hi
	}
	filter.Inclusive = strings.HasSuffix(op, "e")
	if strings.HasPrefix(op, "nb") {
		filter.Inclusive = !filter.Inclusive
		filter.Negate = true
	}
	return nil
}

// f1Logic 是逻辑组合的语法树：& 优先于 |（与 Python 位运算一致），括号改变优先级，叶子是组内条件编号（从 1 数）。
type f1Logic struct {
	op          byte // '&'、'|' 或 0（叶子）
	index       int
	left, right *f1Logic
}

// Eval 按组内条件的结果求值。
func (l *f1Logic) Eval(values []bool) bool {
	switch l.op {
	case '&':
		return l.left.Eval(values) && l.right.Eval(values)
	case '|':
		return l.left.Eval(values) || l.right.Eval(values)
	}
	return values[l.index-1]
}

// LogicTree 返回逻辑组合的语法树（单个条件为 nil）。
func (f F1Filter) LogicTree() *f1Logic { return f.logic }

func parseF1Logic(text string, items int) (*f1Logic, error) {
	tokens := make([]byte, 0, len(text))
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == ' ':
		case c >= '1' && c <= '9', c == '&', c == '|', c == '(', c == ')':
			tokens = append(tokens, c)
		default:
			return nil, fmt.Errorf("只能使用 1～9 的编号与 & | ( )")
		}
	}
	pos := 0
	var parseOr, parseAnd, parseAtom func() (*f1Logic, error)
	parseAtom = func() (*f1Logic, error) {
		if pos >= len(tokens) {
			return nil, errors.New("表达式不完整")
		}
		c := tokens[pos]
		pos++
		if c == '(' {
			node, err := parseOr()
			if err != nil {
				return nil, err
			}
			if pos >= len(tokens) || tokens[pos] != ')' {
				return nil, errors.New("括号不配对")
			}
			pos++
			return node, nil
		}
		if c >= '1' && c <= '9' {
			index := int(c - '0')
			if index > items {
				return nil, fmt.Errorf("编号 %d 超过组内条件数 %d", index, items)
			}
			return &f1Logic{index: index}, nil
		}
		return nil, fmt.Errorf("位置 %d 出现了意外的 %q", pos, string(c))
	}
	parseAnd = func() (*f1Logic, error) {
		left, err := parseAtom()
		if err != nil {
			return nil, err
		}
		for pos < len(tokens) && tokens[pos] == '&' {
			pos++
			right, err := parseAtom()
			if err != nil {
				return nil, err
			}
			left = &f1Logic{op: '&', left: left, right: right}
		}
		return left, nil
	}
	parseOr = func() (*f1Logic, error) {
		left, err := parseAnd()
		if err != nil {
			return nil, err
		}
		for pos < len(tokens) && tokens[pos] == '|' {
			pos++
			right, err := parseAnd()
			if err != nil {
				return nil, err
			}
			left = &f1Logic{op: '|', left: left, right: right}
		}
		return left, nil
	}
	tree, err := parseOr()
	if err != nil {
		return nil, err
	}
	if pos != len(tokens) {
		return nil, errors.New("表达式后面有多余的内容")
	}
	return tree, nil
}

// F1Columns 返回 f1 规则引用的全部列（因子、过滤列，加上候选判定需要的 close 与 volume）。
func (r Rule) F1Columns() []string {
	set := map[string]struct{}{"close": {}, "volume": {}}
	if r.F1 == nil {
		return sortedKeys(set)
	}
	var addFilters func(filters []F1Filter)
	addFilters = func(filters []F1Filter) {
		for _, filter := range filters {
			if filter.Column != "" {
				set[filter.Column] = struct{}{}
			}
			addFilters(filter.Items)
		}
	}
	for _, sub := range r.F1.Subs {
		for _, factor := range sub.LongFactors {
			set[factor.Column] = struct{}{}
		}
		for _, factor := range sub.ShortFactors {
			set[factor.Column] = struct{}{}
		}
		addFilters(sub.FilterBefore)
		addFilters(sub.FilterAfter)
	}
	return sortedKeys(set)
}

// F1Groups 按排名签名把子策略分组：签名包括影响候选、排名与选币的全部字段（因子、计算方式、名单、前置过滤、个数，
// 以及哪一侧的权重大于 0——这决定这一侧要不要选币），不包括只影响配权与持有的字段（hold_period、权重大小、
// filter_after、stg_weight）。组按签名排序。数值在解析时已保证有限，序列化不会失败。
func (s *F1Spec) F1Groups() []F1Group {
	type signature struct {
		CalcType     string     `json:"c"`
		LongFactors  []F1Factor `json:"lf"`
		ShortFactors []F1Factor `json:"sf"`
		LongCoinNum  float64    `json:"lc"`
		ShortCoinNum float64    `json:"sc"`
		LongWhite    []string   `json:"lw"`
		LongBlack    []string   `json:"lb"`
		ShortWhite   []string   `json:"sw"`
		ShortBlack   []string   `json:"sb"`
		FilterBefore []F1Filter `json:"fb"`
		LongActive   bool       `json:"la"`
		ShortActive  bool       `json:"sa"`
	}
	index := make(map[string]int)
	var groups []F1Group
	for i, sub := range s.Subs {
		raw, _ := json.Marshal(signature{sub.CalcType, sub.LongFactors, sub.ShortFactors, sub.LongCoinNum, sub.ShortCoinNum,
			sub.LongWhite, sub.LongBlack, sub.ShortWhite, sub.ShortBlack, sub.FilterBefore, sub.LongWeight > 0, sub.ShortWeight > 0})
		key := Hash(raw)
		at, ok := index[key]
		if !ok {
			at = len(groups)
			index[key] = at
			groups = append(groups, F1Group{Key: key})
		}
		groups[at].Subs = append(groups[at].Subs, i)
	}
	for i := range groups {
		members := make([]F1Sub, len(groups[i].Subs))
		for k, index := range groups[i].Subs {
			members[k] = s.Subs[index]
			members[k].Row = 0 // 行号不影响状态
		}
		raw, _ := json.Marshal(members)
		groups[i].StateKey = Hash(append([]byte(groups[i].Key), raw...))
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Key < groups[j].Key })
	return groups
}
