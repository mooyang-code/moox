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

// tranche（批次持仓）规则：每根 K 线按本期选币新开一批仓位，每批持有 h 期，多批叠加。
// 一条 tranche 规则由一个子策略模板和可选的参数表组成：参数表每行展开为一个子策略，模板里的 "{列名}" 取该行的值。

// RuleTypeTranche 是批次持仓的截面选币规则。
const RuleTypeTranche = "tranche"

// tranche 的取值上限。
const (
	MaxTrancheRows          = 5000  // 一条 tranche 规则的参数表最多行数
	MaxTrancheSubStrategies = 10000 // 一份 DSL 里全部 tranche 子策略的上限
	MaxTrancheFactors       = 16    // 每边因子个数上限
	MaxTrancheFilters       = 32    // 每个子策略的前置与后置过滤条件合计上限
	MaxTrancheHoldPeriod    = 2016  // hold_period 上限（5m 下一周）
	maxTrancheGroupItems    = 9     // 逻辑组合里的条件数：编号是单个数字
	trancheBound            = 1e100 // lt/gt 一类写成以 ±1e100 为边界的 between
)

// 过滤类型与比较方式。
const (
	TrancheFilterValue = "value"
	TrancheFilterRank  = "rank"
	TrancheFilterPct   = "pct"
)

var trancheOps = map[string]bool{"lt": true, "lte": true, "gt": true, "gte": true, "bt": true, "bte": true, "nbt": true, "nbte": true, "eq": true, "ne": true}

// TrancheFactor 是一个选币因子：列名、排序方向（descending 为真时因子值大者优先）与权重。
type TrancheFactor struct {
	Column     string  `json:"column"`
	Descending bool    `json:"descending"`
	Weight     float64 `json:"weight"`
}

// TrancheFilter 是一个过滤条件，或带逻辑组合的一组条件（Logic 非空时 Items 是组内条件）。
// 单个条件已规范化为 between(Lo, Hi, Inclusive)，Negate 为真时取反（nb* 与 ne）。
type TrancheFilter struct {
	Short         bool            `json:"short,omitempty"`
	Column        string          `json:"column,omitempty"`
	Type          string          `json:"type,omitempty"`
	Lo            float64         `json:"lo,omitempty"`
	Hi            float64         `json:"hi,omitempty"`
	Inclusive     bool            `json:"inclusive,omitempty"`
	Negate        bool            `json:"negate,omitempty"`
	RankAscending bool            `json:"rank_ascending,omitempty"`
	Logic         string          `json:"logic,omitempty"`
	Items         []TrancheFilter `json:"items,omitempty"`
	logic         *trancheLogic
}

// tranche 的预设与选项取值（设计 3.1）。
const (
	ScoreRankSum = "rank_sum" // 各因子截面名次加权求和
	ScoreRawSum  = "raw_sum"  // 因子原值加权求和

	MissingMedian = "median" // 因子空值补同期名次中位数
	MissingDrop   = "drop"   // 因子空值不参与本期排名

	HoldCountCandidateRows = "candidate_rows" // 该标的本期是候选才计数
	HoldCountBars          = "bars"           // 每根 K 线都计数

	SizingQuantity = "quantity" // 开仓时定数量，权重随价格变动
	SizingWeight   = "weight"   // 每批份额固定
)

// TrancheSub 是展开后的一个子策略。
type TrancheSub struct {
	Row          int             `json:"row"` // 参数表行号（从 1 数）；没有参数表时为 0
	HoldPeriod   int             `json:"hold_period"`
	HoldCount    string          `json:"hold_count"`
	ScoreMethod  string          `json:"score_method"`
	ScoreMissing string          `json:"score_missing"`
	SizingMethod string          `json:"sizing_method"`
	SizingScale  float64         `json:"sizing_scale"`
	LongFactors  []TrancheFactor `json:"long_factors"`
	ShortFactors []TrancheFactor `json:"short_factors"`
	LongWeight   float64         `json:"long_weight"`
	ShortWeight  float64         `json:"short_weight"`
	LongCoinNum  float64         `json:"long_coin_num"`
	ShortCoinNum float64         `json:"short_coin_num"`
	LongWhite    []string        `json:"long_white_list"`
	LongBlack    []string        `json:"long_black_list"`
	ShortWhite   []string        `json:"short_white_list"`
	ShortBlack   []string        `json:"short_black_list"`
	FilterBefore []TrancheFilter `json:"filter_before"`
	FilterAfter  []TrancheFilter `json:"filter_after"`
	StgWeight    float64         `json:"stg_weight"`
	Stop         *Stop           `json:"stop,omitempty"`
}

// TrancheSpec 是一条 tranche 规则的候选选项、子策略模板与参数表；Subs 是校验时展开的结果。
type TrancheSpec struct {
	// RequireVolume 为真时候选还要求 volume 非空且 > 0（volume = 0 记 idle）；为假时不读 volume。
	RequireVolume bool
	hasCandidates bool
	Template      *yaml.Node
	Columns       []string
	Rows          [][]*yaml.Node
	Subs          []TrancheSub
}

// TrancheGroup 是排名签名相同的一组子策略：它们每期的候选、排名与选币完全相同，只算一次。
// StateKey 同时覆盖签名与组内全部子策略：组成员或任何子策略字段变化时，上一期的开仓记录不再沿用。
type TrancheGroup struct {
	Key      string
	StateKey string
	Subs     []int // TrancheSpec.Subs 的下标
}

var (
	placeholderPattern = regexp.MustCompile(`\{([a-z][a-z0-9_]{0,31})\}`)
	paramColumnPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

func ensureTranche(rule *Rule) *TrancheSpec {
	if rule.Tranche == nil {
		rule.Tranche = &TrancheSpec{RequireVolume: true}
	}
	return rule.Tranche
}

func decodeTrancheCandidates(node *yaml.Node, path string, rule *Rule) error {
	fields, err := mappingFields(node, path)
	if err != nil {
		return err
	}
	spec := ensureTranche(rule)
	spec.hasCandidates = true
	for _, f := range fields {
		switch f.key {
		case "require_volume":
			spec.RequireVolume, err = scalarBool(f.value, path+"."+f.key)
		default:
			return fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func decodeTranche(node *yaml.Node, path string, rule *Rule) error {
	if node == nil || node.Kind != yaml.MappingNode {
		return fmt.Errorf("%s 必须是子策略模板对象", path)
	}
	ensureTranche(rule).Template = node
	return nil
}

func decodeTrancheParams(node *yaml.Node, path string, rule *Rule) error {
	fields, err := mappingFields(node, path)
	if err != nil {
		return err
	}
	spec := ensureTranche(rule)
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

// hasTrancheParams 报告 DSL 里是否有带参数表的 tranche 规则（只有这时才放宽 DSL 大小上限）。
func (s Strategy) hasTrancheParams() bool {
	for _, rule := range s.Rules {
		if rule.Type == RuleTypeTranche && rule.Tranche != nil && len(rule.Tranche.Rows) > 0 {
			return true
		}
	}
	return false
}

// HasTranche 报告 DSL 里是否有 tranche 规则。
func (s Strategy) HasTranche() bool {
	for _, rule := range s.Rules {
		if rule.Type == RuleTypeTranche {
			return true
		}
	}
	return false
}

// validateTrancheRule 校验 tranche 规则并展开子策略。
func validateTrancheRule(prefix string, rule *Rule) error {
	if rule.Pool.Explicit || rule.Filter != "" || rule.Score != "" || rule.HasSelect || rule.FilterAfter != "" || rule.Holding != nil || rule.Entry != "" || rule.Exit != "" {
		return fmt.Errorf("%s 是 tranche 规则，只能使用 id、name、type、weight、candidates、tranche、params", prefix)
	}
	if rule.Side != SideLong {
		return fmt.Errorf("%s 是 tranche 规则，多空方向写在 tranche 模板里（long_*、short_*），不能写 side", prefix)
	}
	if rule.Weight.HasCap || rule.Weight.Method != WeightMethodEqual {
		return fmt.Errorf("%s 是 tranche 规则，weight 只接受 total", prefix)
	}
	spec := rule.Tranche
	if spec == nil || spec.Template == nil {
		return fmt.Errorf("%s 缺少 tranche 子策略模板", prefix)
	}
	rows := spec.Rows
	if spec.Columns == nil {
		rows = [][]*yaml.Node{nil}
	}
	if spec.Columns != nil && len(rows) == 0 {
		return fmt.Errorf("%s 的 params.rows 不能为空", prefix)
	}
	if len(rows) > MaxTrancheRows {
		return fmt.Errorf("%s 的参数表有 %d 行，超过上限 %d", prefix, len(rows), MaxTrancheRows)
	}
	columnIndex := make(map[string]int, len(spec.Columns))
	for i, column := range spec.Columns {
		columnIndex[column] = i
	}
	used := make(map[string]bool)
	if err := collectPlaceholders(spec.Template, used); err != nil {
		return fmt.Errorf("%s 的 tranche 模板：%w", prefix, err)
	}
	for name := range used {
		if _, ok := columnIndex[name]; !ok {
			return fmt.Errorf("%s 的 tranche 模板引用了占位符 {%s}，参数表里没有这一列", prefix, name)
		}
	}
	subs := make([]TrancheSub, 0, len(rows))
	for i, row := range rows {
		where := prefix
		if spec.Columns != nil {
			where = fmt.Sprintf("%s 参数表第 %d 行", prefix, i+1)
			if len(row) != len(spec.Columns) {
				return fmt.Errorf("%s 有 %d 列，columns 声明了 %d 列", where, len(row), len(spec.Columns))
			}
		}
		node := substitute(spec.Template, columnIndex, row)
		sub, err := decodeTrancheSub(node, where)
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

// validateTrancheStrategy 做跨规则的校验：tranche 规则不与其他类型混用、子策略总数、不能用 factor_params。
func validateTrancheStrategy(s *Strategy) error {
	if !s.HasTranche() {
		return nil
	}
	total := 0
	for _, rule := range s.Rules {
		if rule.Type != RuleTypeTranche {
			return fmt.Errorf("含 tranche 规则的策略只能全部是 tranche 规则，规则 %s 是 %s", rule.ID, rule.Type)
		}
		total += len(rule.Tranche.Subs)
	}
	if total > MaxTrancheSubStrategies {
		return fmt.Errorf("tranche 子策略合计 %d 个，超过上限 %d", total, MaxTrancheSubStrategies)
	}
	if len(s.FactorParams) > 0 {
		return errors.New("含 tranche 规则的策略不支持 factor_params")
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

// StopPrice 是子策略止损的价格口径；没有止损时为空。它进入排名签名：同组共用的最有利价格必须按同一口径更新。
func (s TrancheSub) StopPrice() string {
	if s.Stop == nil {
		return ""
	}
	return s.Stop.Price
}

// trancheDefaults 返回子策略的默认值：只做多、因子空值不参与排名、持有期按 K 线根数、每批份额固定；hold_period 必填。
// 其他做法（按候选行计数、按数量持有、空值补中位数名次等）都要在 DSL 里显式写出。
func trancheDefaults() TrancheSub {
	return TrancheSub{HoldCount: HoldCountBars, ScoreMethod: ScoreRankSum, ScoreMissing: MissingDrop,
		SizingMethod: SizingWeight, SizingScale: 1, LongWeight: 1, LongCoinNum: 1, StgWeight: 1}
}

func decodeTrancheSub(node *yaml.Node, path string) (TrancheSub, error) {
	fields, err := mappingFields(node, path+" 的 tranche")
	if err != nil {
		return TrancheSub{}, err
	}
	sub := trancheDefaults()
	for _, f := range fields {
		fieldPath := path + " 的 tranche." + f.key
		switch f.key {
		case "hold_period":
			sub.HoldPeriod, err = scalarInt(f.value, fieldPath)
		case "hold_count":
			sub.HoldCount, err = scalarRequiredText(f.value, fieldPath)
		case "score":
			err = decodeTrancheScore(f.value, fieldPath, &sub)
		case "sizing":
			err = decodeTrancheSizing(f.value, fieldPath, &sub)
		case "long_factors":
			sub.LongFactors, err = decodeTrancheFactors(f.value, fieldPath)
		case "short_factors":
			sub.ShortFactors, err = decodeTrancheFactors(f.value, fieldPath)
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
			sub.FilterBefore, err = decodeTrancheFilters(f.value, fieldPath)
		case "filter_after":
			sub.FilterAfter, err = decodeTrancheFilters(f.value, fieldPath)
		case "stg_weight":
			sub.StgWeight, err = scalarFloat(f.value, fieldPath)
		case "stop":
			sub.Stop, err = decodeStop(f.value, fieldPath)
		default:
			return TrancheSub{}, fmt.Errorf("%s 的 tranche 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return TrancheSub{}, err
		}
	}
	if err := validateTrancheSub(path, &sub); err != nil {
		return TrancheSub{}, err
	}
	return sub, nil
}

func decodeTrancheScore(node *yaml.Node, path string, sub *TrancheSub) error {
	fields, err := mappingFields(node, path)
	if err != nil {
		return err
	}
	for _, f := range fields {
		switch f.key {
		case "method":
			sub.ScoreMethod, err = scalarRequiredText(f.value, path+"."+f.key)
		case "missing":
			sub.ScoreMissing, err = scalarRequiredText(f.value, path+"."+f.key)
		default:
			return fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func decodeTrancheSizing(node *yaml.Node, path string, sub *TrancheSub) error {
	fields, err := mappingFields(node, path)
	if err != nil {
		return err
	}
	for _, f := range fields {
		switch f.key {
		case "method":
			sub.SizingMethod, err = scalarRequiredText(f.value, path+"."+f.key)
		case "scale":
			sub.SizingScale, err = scalarFloat(f.value, path+"."+f.key)
		default:
			return fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func validateTrancheSub(path string, sub *TrancheSub) error {
	if sub.HoldPeriod < 1 || sub.HoldPeriod > MaxTrancheHoldPeriod {
		return fmt.Errorf("%s 的 hold_period 必须在 1 到 %d 之间（必填）", path, MaxTrancheHoldPeriod)
	}
	if sub.HoldCount != HoldCountCandidateRows && sub.HoldCount != HoldCountBars {
		return fmt.Errorf("%s 的 hold_count 只能是 %s 或 %s", path, HoldCountCandidateRows, HoldCountBars)
	}
	if sub.ScoreMethod != ScoreRankSum && sub.ScoreMethod != ScoreRawSum {
		return fmt.Errorf("%s 的 score.method 只能是 %s 或 %s", path, ScoreRankSum, ScoreRawSum)
	}
	if sub.ScoreMissing != MissingMedian && sub.ScoreMissing != MissingDrop {
		return fmt.Errorf("%s 的 score.missing 只能是 %s 或 %s", path, MissingMedian, MissingDrop)
	}
	if sub.SizingMethod != SizingQuantity && sub.SizingMethod != SizingWeight {
		return fmt.Errorf("%s 的 sizing.method 只能是 %s 或 %s", path, SizingQuantity, SizingWeight)
	}
	if sub.SizingScale <= 0 || sub.SizingScale > 10 {
		return fmt.Errorf("%s 的 sizing.scale 必须在 (0, 10] 之间", path)
	}
	if len(sub.LongFactors) > MaxTrancheFactors || len(sub.ShortFactors) > MaxTrancheFactors {
		return fmt.Errorf("%s 每边最多 %d 个因子", path, MaxTrancheFactors)
	}
	for _, side := range []struct {
		name            string
		weight, coinNum float64
		factors         []TrancheFactor
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
	count := countTrancheFilters(sub.FilterBefore) + countTrancheFilters(sub.FilterAfter)
	if count > MaxTrancheFilters {
		return fmt.Errorf("%s 的过滤条件合计 %d 个，超过上限 %d", path, count, MaxTrancheFilters)
	}
	return nil
}

func countTrancheFilters(filters []TrancheFilter) int {
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

func decodeTrancheFactors(node *yaml.Node, path string) ([]TrancheFactor, error) {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s 必须是因子列表", path)
	}
	factors := make([]TrancheFactor, 0, len(node.Content))
	for i, item := range node.Content {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		fields, err := mappingFields(item, itemPath)
		if err != nil {
			return nil, err
		}
		factor := TrancheFactor{Descending: true, Weight: 1}
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

func decodeTrancheFilters(node *yaml.Node, path string) ([]TrancheFilter, error) {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s 必须是过滤条件列表", path)
	}
	filters := make([]TrancheFilter, 0, len(node.Content))
	for i, item := range node.Content {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		filter, err := decodeTrancheFilter(item, itemPath, true)
		if err != nil {
			return nil, err
		}
		filters = append(filters, filter)
	}
	return filters, nil
}

func decodeTrancheFilter(node *yaml.Node, path string, allowGroup bool) (TrancheFilter, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return TrancheFilter{}, err
	}
	var direction, op, column, ftype string
	var valueNode *yaml.Node
	var rankAscending bool
	var filter TrancheFilter
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
				return TrancheFilter{}, fmt.Errorf("%s 不能嵌套逻辑组合", path)
			}
			filter.Logic, err = scalarRequiredText(f.value, fieldPath)
		case "items":
			if !allowGroup {
				return TrancheFilter{}, fmt.Errorf("%s 不能嵌套逻辑组合", path)
			}
			itemsNode = f.value
		default:
			return TrancheFilter{}, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return TrancheFilter{}, err
		}
	}
	if filter.Logic != "" || itemsNode != nil {
		if filter.Logic == "" || itemsNode == nil || direction != "" || column != "" || ftype != "" || op != "" || valueNode != nil {
			return TrancheFilter{}, fmt.Errorf("%s 是逻辑组合，只能写 logic 和 items", path)
		}
		if itemsNode.Kind != yaml.SequenceNode || len(itemsNode.Content) == 0 || len(itemsNode.Content) > maxTrancheGroupItems {
			return TrancheFilter{}, fmt.Errorf("%s.items 必须是 1～%d 个条件", path, maxTrancheGroupItems)
		}
		for i, item := range itemsNode.Content {
			child, err := decodeTrancheFilter(item, fmt.Sprintf("%s.items[%d]", path, i), false)
			if err != nil {
				return TrancheFilter{}, err
			}
			if i > 0 && child.Short != filter.Items[0].Short {
				return TrancheFilter{}, fmt.Errorf("%s 组内条件必须是同一个方向", path)
			}
			filter.Items = append(filter.Items, child)
		}
		filter.Short = filter.Items[0].Short
		logic, err := parseTrancheLogic(filter.Logic, len(filter.Items))
		if err != nil {
			return TrancheFilter{}, fmt.Errorf("%s.logic %q 无效：%w", path, filter.Logic, err)
		}
		filter.logic = logic
		return filter, nil
	}
	// direction 默认 long、type 默认 value；op 与 value 必填。
	if op == "" {
		return TrancheFilter{}, fmt.Errorf("%s 缺少 op", path)
	}
	switch direction {
	case "", "long":
	case "short":
		filter.Short = true
	default:
		return TrancheFilter{}, fmt.Errorf("%s.direction 必须是 long 或 short", path)
	}
	if column == "" {
		return TrancheFilter{}, fmt.Errorf("%s 缺少 factor（列名）", path)
	}
	if ftype == "" {
		ftype = TrancheFilterValue
	}
	if ftype != TrancheFilterValue && ftype != TrancheFilterRank && ftype != TrancheFilterPct {
		return TrancheFilter{}, fmt.Errorf("%s.type 只能是 value、rank 或 pct", path)
	}
	if !trancheOps[op] {
		return TrancheFilter{}, fmt.Errorf("%s.op 只能是 lt、lte、gt、gte、bt、bte、nbt、nbte、eq、ne", path)
	}
	filter.Column, filter.Type, filter.RankAscending = column, ftype, rankAscending
	if ftype == TrancheFilterPct {
		filter.RankAscending = true // pct 固定从小到大
	}
	if err := normalizeTrancheCompare(&filter, op, valueNode, path); err != nil {
		return TrancheFilter{}, err
	}
	return filter, nil
}

// normalizeTrancheCompare 把比较方式规范化为 between(Lo, Hi, Inclusive) 与是否取反：以 e 结尾的是闭区间，lt/gt 一类用 ±1e100 的有限边界，
// nb* 与 ne 取反（所以 NaN 在它们下为真）。
func normalizeTrancheCompare(filter *TrancheFilter, op string, valueNode *yaml.Node, path string) error {
	if valueNode == nil {
		return fmt.Errorf("%s 缺少 value", path)
	}
	checkValue := func(v float64, where string) error {
		switch filter.Type {
		case TrancheFilterRank:
			if v != float64(int64(v)) {
				return fmt.Errorf("%s 是 rank 过滤，value 必须是整数", where)
			}
		case TrancheFilterPct:
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
			filter.Lo, filter.Hi = -trancheBound, v
		case "gt", "gte":
			filter.Lo, filter.Hi = v, trancheBound
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

// trancheLogic 是逻辑组合的语法树：& 优先于 |（与 Python 位运算一致），括号改变优先级，叶子是组内条件编号（从 1 数）。
type trancheLogic struct {
	op          byte // '&'、'|' 或 0（叶子）
	index       int
	left, right *trancheLogic
}

// Eval 按组内条件的结果求值。
func (l *trancheLogic) Eval(values []bool) bool {
	switch l.op {
	case '&':
		return l.left.Eval(values) && l.right.Eval(values)
	case '|':
		return l.left.Eval(values) || l.right.Eval(values)
	}
	return values[l.index-1]
}

// LogicTree 返回逻辑组合的语法树（单个条件为 nil）。
func (f TrancheFilter) LogicTree() *trancheLogic { return f.logic }

func parseTrancheLogic(text string, items int) (*trancheLogic, error) {
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
	var parseOr, parseAnd, parseAtom func() (*trancheLogic, error)
	parseAtom = func() (*trancheLogic, error) {
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
			return &trancheLogic{index: index}, nil
		}
		return nil, fmt.Errorf("位置 %d 出现了意外的 %q", pos, string(c))
	}
	parseAnd = func() (*trancheLogic, error) {
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
			left = &trancheLogic{op: '&', left: left, right: right}
		}
		return left, nil
	}
	parseOr = func() (*trancheLogic, error) {
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
			left = &trancheLogic{op: '|', left: left, right: right}
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

// TrancheColumns 返回 tranche 规则引用的全部列（因子、过滤列，加上候选判定需要的 close，require_volume 时还有 volume）。
func (r Rule) TrancheColumns() []string {
	set := map[string]struct{}{"close": {}}
	if r.Tranche == nil {
		return sortedKeys(set)
	}
	if r.Tranche.RequireVolume {
		set["volume"] = struct{}{}
	}
	var addFilters func(filters []TrancheFilter)
	addFilters = func(filters []TrancheFilter) {
		for _, filter := range filters {
			if filter.Column != "" {
				set[filter.Column] = struct{}{}
			}
			addFilters(filter.Items)
		}
	}
	for _, sub := range r.Tranche.Subs {
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

// TrancheGroups 按排名签名把子策略分组：签名包括影响候选、排名与选币的全部字段（因子、打分方式与缺值处理、名单、前置过滤、个数，
// 以及哪一侧的权重大于 0——这决定这一侧要不要选币），不包括只影响配权与持有的字段（hold_period、权重大小、
// filter_after、stg_weight）。组按签名排序。数值在解析时已保证有限，序列化不会失败。
func (s *TrancheSpec) TrancheGroups() []TrancheGroup {
	type signature struct {
		ScoreMethod  string          `json:"c"`
		ScoreMissing string          `json:"m"`
		LongFactors  []TrancheFactor `json:"lf"`
		ShortFactors []TrancheFactor `json:"sf"`
		LongCoinNum  float64         `json:"lc"`
		ShortCoinNum float64         `json:"sc"`
		LongWhite    []string        `json:"lw"`
		LongBlack    []string        `json:"lb"`
		ShortWhite   []string        `json:"sw"`
		ShortBlack   []string        `json:"sb"`
		FilterBefore []TrancheFilter `json:"fb"`
		LongActive   bool            `json:"la"`
		ShortActive  bool            `json:"sa"`
		StopPrice    string          `json:"sp,omitempty"`
	}
	index := make(map[string]int)
	var groups []TrancheGroup
	for i, sub := range s.Subs {
		raw, _ := json.Marshal(signature{sub.ScoreMethod, sub.ScoreMissing, sub.LongFactors, sub.ShortFactors, sub.LongCoinNum, sub.ShortCoinNum,
			sub.LongWhite, sub.LongBlack, sub.ShortWhite, sub.ShortBlack, sub.FilterBefore, sub.LongWeight > 0, sub.ShortWeight > 0, sub.StopPrice()})
		key := Hash(raw)
		at, ok := index[key]
		if !ok {
			at = len(groups)
			index[key] = at
			groups = append(groups, TrancheGroup{Key: key})
		}
		groups[at].Subs = append(groups[at].Subs, i)
	}
	for i := range groups {
		members := make([]TrancheSub, len(groups[i].Subs))
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
