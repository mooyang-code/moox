package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// MaxParamOverrides 是一个参数按标的覆盖的上限。
const MaxParamOverrides = 10000

// ParamValue 是一个参数在一期里的取值：默认值加按标的覆盖。不在覆盖里的标的用本期的默认值。
type ParamValue struct {
	Default   float64            `json:"default"`
	Overrides map[string]float64 `json:"overrides,omitempty"`
}

// ParamSnapshot 是一个成员一期的完整参数快照：参数名 → 取值。每期提交完整快照，不提交增量。
type ParamSnapshot map[string]ParamValue

// DefaultSnapshot 返回定义的默认参数快照（没有覆盖）。
func DefaultSnapshot(def FactorDef) ParamSnapshot {
	snapshot := ParamSnapshot{}
	if compiled := def.Compiled(); compiled != nil {
		for _, param := range compiled.Params {
			snapshot[param.Name] = ParamValue{Default: param.Default}
		}
	}
	return snapshot
}

// MemberSnapshot 返回成员的参数快照：以定义的默认值为底，成员带固定参数时用固定参数覆盖（没有标的覆盖）。
func MemberSnapshot(member MemberDef) ParamSnapshot {
	snapshot := DefaultSnapshot(member.Factor)
	for name, value := range member.Params {
		if _, ok := snapshot[name]; ok {
			snapshot[name] = ParamValue{Default: value}
		}
	}
	return snapshot
}

// Value 返回参数 name 在标的 subject 上的取值。
func (s ParamSnapshot) Value(name, subject string) float64 {
	value := s[name]
	if override, ok := value.Overrides[subject]; ok {
		return override
	}
	return value.Default
}

// Canonical 返回规范 JSON（键有序、没有覆盖时省略），用于摘要与存储。
func (s ParamSnapshot) Canonical() string {
	normalized := make(map[string]ParamValue, len(s))
	for name, value := range s {
		if len(value.Overrides) == 0 {
			value.Overrides = nil
		}
		normalized[name] = value
	}
	raw, _ := json.Marshal(normalized)
	return string(raw)
}

// Hash 返回快照的内容摘要。
func (s ParamSnapshot) Hash() string {
	sum := sha256.Sum256([]byte(s.Canonical()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ParseParamSnapshot 解析规范 JSON。
func ParseParamSnapshot(raw string) (ParamSnapshot, error) {
	snapshot := ParamSnapshot{}
	if strings.TrimSpace(raw) == "" {
		return snapshot, nil
	}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return nil, fmt.Errorf("参数快照不是合法的 JSON：%w", err)
	}
	return snapshot, nil
}

// ValidateSnapshot 检查一份快照能否用于成员 member：每个声明的参数都有且只有声明的参数；默认值与每个覆盖标的
// 合并后的完整取值都合法（范围、整数、算子联合约束）；覆盖的标的满足 inScope；流入截面算子的参数不允许覆盖。
func ValidateSnapshot(member MemberDef, snapshot ParamSnapshot, inScope func(subject string) bool) error {
	compiled := member.Factor.Compiled()
	if compiled == nil {
		return fmt.Errorf("成员 %s 的定义没有编译", member.Key)
	}
	declared := make(map[string]bool, len(compiled.Params))
	for _, param := range compiled.Params {
		declared[param.Name] = true
		if _, ok := snapshot[param.Name]; !ok {
			return fmt.Errorf("成员 %s 缺少参数 %s", member.Key, param.Name)
		}
	}
	for name := range snapshot {
		if !declared[name] {
			return fmt.Errorf("成员 %s 没有参数 %s", member.Key, name)
		}
	}
	defaults := make(map[string]float64, len(snapshot))
	subjects := map[string]struct{}{}
	for name, value := range snapshot {
		defaults[name] = value.Default
		if len(value.Overrides) > MaxParamOverrides {
			return fmt.Errorf("参数 %s 的按标的覆盖超过 %d 个", name, MaxParamOverrides)
		}
		for subject := range value.Overrides {
			if strings.TrimSpace(subject) == "" || subject != strings.TrimSpace(subject) {
				return fmt.Errorf("参数 %s 的覆盖里有空的或带首尾空白的标的", name)
			}
			if inScope != nil && !inScope(subject) {
				return fmt.Errorf("参数 %s 覆盖的标的 %s 不在任务的标的范围内", name, subject)
			}
			subjects[subject] = struct{}{}
		}
	}
	for _, name := range crossSectionParams(member) {
		if len(snapshot[name].Overrides) > 0 {
			return fmt.Errorf("参数 %s 流入截面算子，只能取统一值，不能按标的覆盖", name)
		}
	}
	if err := compiled.CheckParams(defaults); err != nil {
		return fmt.Errorf("成员 %s 的默认取值：%w", member.Key, err)
	}
	ordered := make([]string, 0, len(subjects))
	for subject := range subjects {
		ordered = append(ordered, subject)
	}
	sort.Strings(ordered)
	for _, subject := range ordered {
		values := make(map[string]float64, len(snapshot))
		for name := range snapshot {
			values[name] = snapshot.Value(name, subject)
		}
		if err := compiled.CheckParams(values); err != nil {
			return fmt.Errorf("成员 %s 在标的 %s 上的取值：%w", member.Key, subject, err)
		}
	}
	return nil
}

// crossSectionParams 返回流入截面算子参数位置的参数。当前的截面算子都没有常量参数，所以总是为空；
// 以后增加带参数的截面算子时在这里识别。
func crossSectionParams(MemberDef) []string { return nil }

// MergeSnapshots 按标的合并两次提交：默认值取本次提交的；按标的的覆盖里，本次带了的标的覆盖旧值，没带的标的保留旧值。
// 两份快照的参数集合应相同（提交前已校验）；旧快照里本次没有的参数被丢弃。
func MergeSnapshots(previous, submitted ParamSnapshot) ParamSnapshot {
	merged := make(ParamSnapshot, len(submitted))
	for name, value := range submitted {
		overrides := make(map[string]float64, len(value.Overrides)+len(previous[name].Overrides))
		for subject, old := range previous[name].Overrides {
			overrides[subject] = old
		}
		for subject, override := range value.Overrides {
			overrides[subject] = override
		}
		if len(overrides) == 0 {
			overrides = nil
		}
		merged[name] = ParamValue{Default: value.Default, Overrides: overrides}
	}
	return merged
}

// finite 报告取值是否为有限数。
func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// CheckSnapshotNumbers 拒绝快照里的非有限数（JSON 解析出来的快照不会有，接口转换来的需要检查）。
func CheckSnapshotNumbers(snapshot ParamSnapshot) error {
	for name, value := range snapshot {
		if !finite(value.Default) {
			return fmt.Errorf("参数 %s 的默认值必须是有限数", name)
		}
		for subject, override := range value.Overrides {
			if !finite(override) {
				return fmt.Errorf("参数 %s 在标的 %s 上的取值必须是有限数", name, subject)
			}
		}
	}
	return nil
}
