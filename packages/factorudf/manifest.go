package factorudf

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
)

// ABIVersion 是当前唯一的 ABI 版本；宿主只接受它。ABI 升级时全部 UDF 用新 SDK 重编、重新发布。
const ABIVersion = 1

// CustomSection 是写进 wasm 的自定义段名，内容是 Manifest 的 JSON（只有影响计算的语义字段）。
const CustomSection = "moox.udf"

const (
	// 元数据各项的上限，防止畸形或恶意元数据撑大宿主的内存与计算量。
	maxInputs   = 8
	maxParams   = 16
	maxIdentLen = 32
	// maxIntParam 是整数参数的绝对值上限：整数参数以 f64 精确传递，远小于 2^53。
	maxIntParam = 1 << 40
)

var (
	nameRE  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)
	identRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// NaN 处理方式。
const (
	NaNStrict  = "strict"
	NaNRelaxed = "relaxed"
)

// 参数类型。
const (
	ParamInt   = "int"
	ParamFloat = "float"
)

// Param 是一个标量参数的声明。取值范围在编译 DSL 时检查。
type Param struct {
	Name string  `json:"name"`
	Type string  `json:"type"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
}

// Manifest 是 UDF 影响计算的语义元数据，随 wasm 字节进入版本哈希（写在自定义段里）。
type Manifest struct {
	ABI    int      `json:"abi"`
	SDK    string   `json:"sdk"`
	Name   string   `json:"name"`
	Kind   string   `json:"kind"`
	Inputs []string `json:"inputs"`
	Params []Param  `json:"params"`
	// Lookback 是窗口长度 L 的公式：整数常量，或 a*<整数参数>+b（a、b 为非负整数常量）。
	Lookback string `json:"lookback"`
	NaN      string `json:"nan"`
	MinValid int    `json:"min_valid"`
}

// ParseManifest 解析并校验自定义段的内容。
func ParseManifest(raw []byte) (*Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytesReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("元数据不是合法的 JSON：%w", err)
	}
	if dec.More() {
		return nil, errors.New("元数据后面有多余内容")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate 检查元数据合法：名称、输入、参数范围、回看公式（含端点检查）、空值模式。
func (m *Manifest) Validate() error {
	if m.ABI != ABIVersion {
		return fmt.Errorf("ABI 版本 %d 不被支持（当前 %d）", m.ABI, ABIVersion)
	}
	if !nameRE.MatchString(m.Name) {
		return fmt.Errorf("名称 %q 不合法：要匹配 %s", m.Name, nameRE)
	}
	if m.Kind != "series" {
		return fmt.Errorf("kind %q 不被支持：第一期只有 series", m.Kind)
	}
	if n := len(m.Inputs); n < 1 || n > maxInputs {
		return fmt.Errorf("序列输入个数 %d 超出范围 1..%d", n, maxInputs)
	}
	seen := map[string]bool{}
	for _, name := range m.Inputs {
		if !identRE.MatchString(name) {
			return fmt.Errorf("输入名 %q 不合法：要匹配 %s", name, identRE)
		}
		if seen[name] {
			return fmt.Errorf("输入名 %q 重复", name)
		}
		seen[name] = true
	}
	if len(m.Params) > maxParams {
		return fmt.Errorf("参数个数 %d 超过上限 %d", len(m.Params), maxParams)
	}
	for _, p := range m.Params {
		if !identRE.MatchString(p.Name) {
			return fmt.Errorf("参数名 %q 不合法：要匹配 %s", p.Name, identRE)
		}
		if seen[p.Name] {
			return fmt.Errorf("参数名 %q 与输入名或其他参数重复", p.Name)
		}
		seen[p.Name] = true
		if err := validateParam(p); err != nil {
			return err
		}
	}
	switch m.NaN {
	case NaNStrict, NaNRelaxed:
	default:
		return fmt.Errorf("nan 取值 %q 不合法：strict 或 relaxed", m.NaN)
	}
	if m.MinValid < 1 {
		return fmt.Errorf("min_valid 要 ≥ 1，得到 %d", m.MinValid)
	}
	if m.NaN == NaNStrict && m.MinValid != 1 {
		return errors.New("严格模式不使用 min_valid（要保持 1）")
	}
	lb, err := ParseLookback(m.Lookback, m.Params)
	if err != nil {
		return err
	}
	if _, _, err := lb.Range(); err != nil {
		return err
	}
	return nil
}

func validateParam(p Param) error {
	if p.Type != ParamInt && p.Type != ParamFloat {
		return fmt.Errorf("参数 %s 的类型 %q 不合法：int 或 float", p.Name, p.Type)
	}
	if math.IsNaN(p.Min) || math.IsNaN(p.Max) || math.IsInf(p.Min, 0) || math.IsInf(p.Max, 0) {
		return fmt.Errorf("参数 %s 的范围要是有限数", p.Name)
	}
	if p.Min > p.Max {
		return fmt.Errorf("参数 %s 的范围 [%v, %v] 不合法：下限大于上限", p.Name, p.Min, p.Max)
	}
	if p.Type == ParamInt {
		if p.Min != math.Trunc(p.Min) || p.Max != math.Trunc(p.Max) {
			return fmt.Errorf("整数参数 %s 的范围要是整数", p.Name)
		}
		if math.Abs(p.Min) > maxIntParam || math.Abs(p.Max) > maxIntParam {
			return fmt.Errorf("整数参数 %s 的范围超出 ±%d", p.Name, int64(maxIntParam))
		}
	}
	return nil
}

// ParamIndex 返回参数在声明顺序里的位置。
func (m *Manifest) ParamIndex(name string) int {
	for i, p := range m.Params {
		if p.Name == name {
			return i
		}
	}
	return -1
}

// Equal 报告两份语义元数据是否完全一致（包内 manifest.json 必须与自定义段一致）。
func (m *Manifest) Equal(o *Manifest) bool {
	a, _ := json.Marshal(m)
	b, _ := json.Marshal(o)
	return string(a) == string(b)
}
