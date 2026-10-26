// Package factorname 是因子成员键、结果列名、任务相关 ID 的唯一拼接、解析与校验实现。
//
// 命名规则（设计「因子任务与参数模型重构设计」5.1~5.7）：
//   - 因子 ID、输出名、参数名都是小写，名字内部用单下划线分词；
//   - 段与段之间用双下划线，所以列名不查因子定义就能解析；
//   - 成员键 = 因子 ID [ "__" 参数段 ]，参数段是 "var"（可变成员）或按定义声明顺序写出的参数值；
//   - 列名 = 成员键 [ "__" 输出名 ]，单输出因子没有输出段。
//
// 前端 web/src/utils/factor-name.ts 实现同样的规则，两边共用 testdata/vectors.json。
package factorname

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	// MaxFactorIDLen、MaxOutputLen、MaxParamNameLen、MaxMemberKeyLen、MaxColumnLen 是各部分的长度上限（字符数）。
	MaxFactorIDLen  = 24
	MaxOutputLen    = 16
	MaxParamNameLen = 16
	MaxMemberKeyLen = 44
	MaxColumnLen    = 64
	// MaxSuffixLen 是任务 ID 后缀的长度上限：dataset_factor_ 加后缀不超过 Storage 的 50 个字符。
	MaxSuffixLen = 35

	// Sep 是成员键、列名里段与段的分隔符。
	Sep = "__"
	// VarSegment 是可变成员的参数段。
	VarSegment = "var"
	// ParamsColumn 是每个因子结果数据集里记录各成员实际参数的 JSON 列。
	ParamsColumn = "__params"

	// KindFixed、KindVar 是成员类型。
	KindFixed = "fixed"
	KindVar   = "var"
	// ColumnKindOutput、ColumnKindParams 是结果列的 column_kind 属性值。
	ColumnKindOutput = "output"
	ColumnKindParams = "params"
)

var (
	namePattern  = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	valuePattern = regexp.MustCompile(`^n?[0-9]+(p[0-9]+)?$`)
)

func validateName(what, name string, max int) error {
	if name == "" {
		return fmt.Errorf("%s不能为空", what)
	}
	if len(name) > max {
		return fmt.Errorf("%s %q 超过 %d 个字符", what, name, max)
	}
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%s %q 不合法：只能是小写字母开头、由小写字母和数字组成，词与词之间用单个下划线", what, name)
	}
	return nil
}

// ValidateFactorID 校验因子 ID。
func ValidateFactorID(id string) error { return validateName("因子 ID", id, MaxFactorIDLen) }

// ValidateParamName 校验参数名。
func ValidateParamName(name string) error { return validateName("参数名", name, MaxParamNameLen) }

// ValidateOutputName 校验多输出因子的输出名：除了名字语法，还不能是 var、不能长得像参数段（否则列名无法解析）、
// 不能等于因子 ID（factorID 为空时跳过这一条）。
func ValidateOutputName(factorID, name string) error {
	if err := validateName("输出名", name, MaxOutputLen); err != nil {
		return err
	}
	if isParamSegment(name) {
		return fmt.Errorf("输出名 %q 与参数段的写法冲突", name)
	}
	if factorID != "" && name == factorID {
		return fmt.Errorf("多输出因子的输出名 %q 不能等于因子 ID", name)
	}
	return nil
}

// ValidateOutputs 校验一个因子的全部输出：单输出时输出名就是因子 ID；多输出时至少两项、互不相同、每项都合法。
func ValidateOutputs(factorID string, outputs []string) error {
	if err := ValidateFactorID(factorID); err != nil {
		return err
	}
	switch {
	case len(outputs) == 0:
		return fmt.Errorf("因子 %s 至少要有一个输出", factorID)
	case len(outputs) == 1:
		if outputs[0] != factorID {
			return fmt.Errorf("因子 %s 只有一个输出时不写输出名（输出名就是因子 ID），实际是 %q；有多个输出才需要输出名", factorID, outputs[0])
		}
		return nil
	}
	seen := make(map[string]bool, len(outputs))
	for _, name := range outputs {
		if err := ValidateOutputName(factorID, name); err != nil {
			return fmt.Errorf("因子 %s：%w", factorID, err)
		}
		if seen[name] {
			return fmt.Errorf("因子 %s 的输出名 %q 重复", factorID, name)
		}
		seen[name] = true
	}
	return nil
}

// isParamSegment 判断一段文本是否是参数段：var，或用单下划线连接的规范参数值。
func isParamSegment(s string) bool {
	if s == VarSegment {
		return true
	}
	if s == "" {
		return false
	}
	for _, part := range strings.Split(s, "_") {
		if !valuePattern.MatchString(part) {
			return false
		}
	}
	return true
}
