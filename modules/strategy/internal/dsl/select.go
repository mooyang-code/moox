package dsl

import (
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	"gopkg.in/yaml.v3"
)

// 比例换算个数的取整方式。
const (
	CountRoundFloor = "floor"
	CountRoundCeil  = "ceil"
	CountRoundRound = "round"
)

// MaxGroupQuotas 是 select.group.quotas 的个数上限。
const MaxGroupQuotas = 64

// SelectGroup 是按分组列限制入选个数：Max 是每组上限；Quotas 按组内最优名次给组排序后，第 i 组的名额是 Quotas[i]，其余组是 Rest。
// Max 与 Quotas 二选一。
type SelectGroup struct {
	By     string
	Max    int
	Quotas []int
	Rest   int
}

// SelectThen 是二次排序：在第一段选中的集合里按 Score 重新排序，取 Top 或 Bottom 个。
type SelectThen struct {
	Score  string
	Top    int
	Bottom int
}

// Count 返回二次排序要取的个数。
func (t SelectThen) Count() int {
	if t.Bottom > 0 {
		return t.Bottom
	}
	return t.Top
}

// HasRange 报告 select 是否写了名次区间。
func (s Select) HasRange() bool { return s.Range[0] > 0 }

// UsesBottom 报告第一段是否按低分优先排序（bottom）。range 按分数从高到低排名。
func (s Select) UsesBottom() bool { return s.Bottom > 0 }

// IsRatio 报告 top/bottom 是否写成 (0, 1) 的比例。
func (s Select) IsRatio() bool {
	value := s.Top
	if s.Bottom > 0 {
		value = s.Bottom
	}
	return value > 0 && value < 1
}

// FixedCount 返回不依赖样本数的选择个数：整数 top/bottom 是该数，range 是区间长度；写比例时返回 0 与 false。
func (s Select) FixedCount() (int, bool) {
	switch {
	case s.HasRange():
		return s.Range[1] - s.Range[0] + 1, true
	case s.IsRatio():
		return 0, false
	case s.Bottom > 0:
		return int(s.Bottom), true
	default:
		return int(s.Top), true
	}
}

// Count 返回第一段的选择个数。sample 是比例的分母：通过 filter 且分数有效的样本数。
// 比例按 count_round 取整后与 count_min 取大；整数与 range 不受样本数影响。
func (s Select) Count(sample int) int {
	if count, fixed := s.FixedCount(); fixed {
		return count
	}
	ratio := s.Top
	if s.Bottom > 0 {
		ratio = s.Bottom
	}
	// 比例按书写的十进制文本精确换算：0.1 × 30 必须是 3，浮点乘法会得到 3.0000000000000004 让 ceil 多取一个。
	exact, err := quant.ParseLiteral(strconv.FormatFloat(ratio, 'f', -1, 64))
	if err != nil {
		return s.CountMin
	}
	product := new(big.Rat).Mul(exact.Rat(), new(big.Rat).SetInt64(int64(sample)))
	count := roundRat(product, s.CountRound)
	return max(count, s.CountMin)
}

// roundRat 把非负有理数按取整方式换成整数：floor（默认，空串同此）向下、ceil 向上、round 四舍五入（0.5 进位）。
func roundRat(value *big.Rat, mode string) int {
	quotient, remainder := new(big.Int).QuoRem(value.Num(), value.Denom(), new(big.Int))
	count := int(quotient.Int64())
	if remainder.Sign() == 0 {
		return count
	}
	switch mode {
	case CountRoundCeil:
		return count + 1
	case CountRoundRound:
		// 余数 × 2 ≥ 分母 即小数部分 ≥ 0.5。
		if new(big.Int).Mul(remainder, big.NewInt(2)).Cmp(value.Denom()) >= 0 {
			return count + 1
		}
	}
	return count
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
			sel.Top, err = scalarFloat(f.value, fieldPath)
		case "bottom":
			sel.Bottom, err = scalarFloat(f.value, fieldPath)
		case "count_round":
			sel.CountRound, err = scalarRequiredText(f.value, fieldPath)
		case "count_min":
			sel.CountMin, err = scalarInt(f.value, fieldPath)
		case "range":
			var bounds []int
			if bounds, err = intList(f.value, fieldPath); err == nil {
				if len(bounds) != 2 {
					return Select{}, fmt.Errorf("%s 必须是两个整数 [起始名次, 结束名次]", fieldPath)
				}
				sel.Range = [2]int{bounds[0], bounds[1]}
				if bounds[0] <= 0 {
					return Select{}, fmt.Errorf("%s 的名次从 1 开始", fieldPath)
				}
			}
		case "group":
			sel.Group, err = decodeSelectGroup(f.value, fieldPath)
		case "then":
			sel.Then, err = decodeSelectThen(f.value, fieldPath)
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

func decodeSelectGroup(node *yaml.Node, path string) (*SelectGroup, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return nil, err
	}
	group := &SelectGroup{}
	hasMax, hasRest := false, false
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "by":
			group.By, err = scalarRequiredText(f.value, fieldPath)
		case "max":
			hasMax = true
			group.Max, err = scalarInt(f.value, fieldPath)
		case "quotas":
			group.Quotas, err = intList(f.value, fieldPath)
		case "rest":
			hasRest = true
			group.Rest, err = scalarInt(f.value, fieldPath)
		default:
			return nil, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return nil, err
		}
	}
	switch {
	case group.By == "":
		return nil, fmt.Errorf("%s 缺少 by（分组列）", path)
	case hasMax && group.Quotas != nil:
		return nil, fmt.Errorf("%s 的 max 与 quotas 只能写一个", path)
	case !hasMax && group.Quotas == nil:
		return nil, fmt.Errorf("%s 需要 max 或 quotas 之一", path)
	case hasMax && (group.Max <= 0 || group.Max > MaxCount):
		return nil, fmt.Errorf("%s.max 必须在 1 到 %d 之间", path, MaxCount)
	case hasRest && group.Quotas == nil:
		return nil, fmt.Errorf("%s.rest 只能与 quotas 一起写", path)
	case group.Quotas != nil && (len(group.Quotas) == 0 || len(group.Quotas) > MaxGroupQuotas):
		return nil, fmt.Errorf("%s.quotas 必须有 1 到 %d 项", path, MaxGroupQuotas)
	case group.Rest < 0 || group.Rest > MaxCount:
		return nil, fmt.Errorf("%s.rest 必须在 0 到 %d 之间", path, MaxCount)
	}
	if err := checkColumnName(group.By, path+".by"); err != nil {
		return nil, err
	}
	for i, quota := range group.Quotas {
		if quota < 0 || quota > MaxCount {
			return nil, fmt.Errorf("%s.quotas[%d] 必须在 0 到 %d 之间", path, i, MaxCount)
		}
	}
	return group, nil
}

func decodeSelectThen(node *yaml.Node, path string) (*SelectThen, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return nil, err
	}
	then := &SelectThen{}
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "score":
			then.Score, err = scalarRequiredText(f.value, fieldPath)
		case "top":
			then.Top, err = scalarInt(f.value, fieldPath)
		case "bottom":
			then.Bottom, err = scalarInt(f.value, fieldPath)
		default:
			return nil, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return nil, err
		}
	}
	switch {
	case then.Score == "":
		return nil, fmt.Errorf("%s 缺少 score", path)
	case (then.Top > 0) == (then.Bottom > 0):
		return nil, fmt.Errorf("%s 必须且只能设置 top 或 bottom 之一（正整数）", path)
	case then.Top < 0 || then.Bottom < 0 || then.Top > MaxCount || then.Bottom > MaxCount:
		return nil, fmt.Errorf("%s 的 top、bottom 必须在 1 到 %d 之间", path, MaxCount)
	}
	return then, nil
}

var columnNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidColumnName 报告 name 是普通列名（不是表达式，也不是以 __ 开头的内部列）。
func ValidColumnName(name string) bool {
	return columnNamePattern.MatchString(name) && !strings.HasPrefix(name, "__")
}

// checkColumnName 检查直接写列名的字段（例如 select.group.by）：只能是普通列名，不能是表达式或保留名称。
func checkColumnName(name, path string) error {
	if !columnNamePattern.MatchString(name) || strings.HasPrefix(name, "__") || name == identifierBars || name == identifierScore || name == identifierInstrument {
		return fmt.Errorf("%s 必须是 View 里的列名", path)
	}
	return nil
}

// validateSelectCount 校验 top、bottom、range、count_round、count_min。
func validateSelectCount(prefix string, sel *Select) error {
	for _, item := range []struct {
		name  string
		value float64
	}{{"top", sel.Top}, {"bottom", sel.Bottom}} {
		if item.value < 0 {
			return fmt.Errorf("%s 的 select.%s 不能为负数", prefix, item.name)
		}
		if item.value > MaxCount {
			return fmt.Errorf("%s 的 select.top、bottom、buffer 不能超过 %d", prefix, MaxCount)
		}
		if item.value >= 1 && item.value != math.Trunc(item.value) {
			return fmt.Errorf("%s 的 select.%s 必须是正整数或 0～1 之间的比例", prefix, item.name)
		}
	}
	if sel.Buffer < 0 {
		return fmt.Errorf("%s 的 select.top、bottom、buffer 不能为负数", prefix)
	}
	if sel.Buffer > MaxCount {
		return fmt.Errorf("%s 的 select.top、bottom、buffer 不能超过 %d", prefix, MaxCount)
	}
	if sel.HasRange() {
		if sel.Top > 0 || sel.Bottom > 0 {
			return fmt.Errorf("%s 的 select.range 与 top、bottom 只能写一个", prefix)
		}
		if sel.Range[1] < sel.Range[0] || sel.Range[1] > MaxCount {
			return fmt.Errorf("%s 的 select.range 必须是 [起始, 结束]，结束名次不小于起始且不超过 %d", prefix, MaxCount)
		}
		if sel.Buffer > 0 {
			return fmt.Errorf("%s 的 select.range 不能与 buffer 同时使用", prefix)
		}
	} else if (sel.Top > 0) == (sel.Bottom > 0) {
		return fmt.Errorf("%s 的 select 必须且只能设置 top 或 bottom 之一（或改用 range）", prefix)
	}
	if sel.CountRound != "" || sel.CountMin != 0 {
		if !sel.IsRatio() {
			return fmt.Errorf("%s 的 select.count_round、count_min 只能在 top/bottom 写成比例时使用", prefix)
		}
	}
	if sel.CountRound != "" && sel.CountRound != CountRoundFloor && sel.CountRound != CountRoundCeil && sel.CountRound != CountRoundRound {
		return fmt.Errorf("%s 的 select.count_round 只能是 floor、ceil 或 round", prefix)
	}
	if sel.CountMin < 0 || sel.CountMin > MaxCount {
		return fmt.Errorf("%s 的 select.count_min 必须在 0 到 %d 之间", prefix, MaxCount)
	}
	if sel.Then != nil && sel.Buffer > 0 {
		return fmt.Errorf("%s 的 select.then 不能与 buffer 同时使用", prefix)
	}
	return nil
}
