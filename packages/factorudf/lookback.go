package factorudf

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// MaxLookback 是单个 UDF 窗口长度 L 的上限（与因子回看的整体上限保持同一量级）；超过它的公式在发布时被拒绝。
// 它同时限制每次调用的拷贝量。
const MaxLookback = 1 << 20

// Lookback 是窗口长度公式：L = A*参数 + B；Param 为空表示常量 B。
// 只允许这两种形式，保证窗口长度随参数单调不减：最小、最大值都在参数范围的端点取得，回看上界只需枚举端点。
type Lookback struct {
	A, B  int64
	Param string
	p     *Param
}

// ParseLookback 解析回看公式并绑定到参数声明。语法：
//
//	b | name | a*name | name+b | a*name+b      （a、b 是非负整数常量，name 是整数参数）
func ParseLookback(expr string, params []Param) (Lookback, error) {
	s := strings.ReplaceAll(strings.TrimSpace(expr), " ", "")
	if s == "" {
		return Lookback{}, errors.New("回看公式不能为空")
	}
	lb := Lookback{A: 1}
	rest := s
	if i := strings.LastIndex(rest, "+"); i >= 0 {
		b, err := parseNonNegInt(rest[i+1:])
		if err != nil {
			return Lookback{}, fmt.Errorf("回看公式 %q 不合法：加数 %q 要是非负整数常量", expr, rest[i+1:])
		}
		lb.B = b
		rest = rest[:i]
		if rest == "" {
			return Lookback{}, fmt.Errorf("回看公式 %q 不合法", expr)
		}
	}
	if c, err := parseNonNegInt(rest); err == nil {
		// 纯常量：b 或 b+b'（后者等价于常量，仍按两项相加处理）。
		if lb.B > math.MaxInt64-c {
			return Lookback{}, fmt.Errorf("回看公式 %q 溢出", expr)
		}
		return Lookback{B: lb.B + c}, nil
	}
	name := rest
	if i := strings.Index(rest, "*"); i >= 0 {
		a, err := parseNonNegInt(rest[:i])
		if err != nil {
			return Lookback{}, fmt.Errorf("回看公式 %q 不合法：系数 %q 要是非负整数常量", expr, rest[:i])
		}
		lb.A = a
		name = rest[i+1:]
	}
	if !identRE.MatchString(name) {
		return Lookback{}, fmt.Errorf("回看公式 %q 不合法：只允许常量或 a*整数参数+b", expr)
	}
	for i := range params {
		if params[i].Name == name {
			if params[i].Type != ParamInt {
				return Lookback{}, fmt.Errorf("回看公式引用的参数 %s 要是整数参数", name)
			}
			lb.Param, lb.p = name, &params[i]
			return lb, nil
		}
	}
	return Lookback{}, fmt.Errorf("回看公式引用了未声明的参数 %q", name)
}

func parseNonNegInt(s string) (int64, error) {
	if s == "" || s[0] == '+' || s[0] == '-' {
		return 0, errors.New("不是非负整数")
	}
	return strconv.ParseInt(s, 10, 64)
}

// At 求某个参数取值下的窗口长度 L；溢出或超过 MaxLookback 返回错误。
func (lb Lookback) At(param int64) (int64, error) {
	if lb.Param == "" {
		return checkLookback(lb.B)
	}
	if lb.A != 0 && param > (math.MaxInt64-lb.B)/lb.A {
		return 0, errors.New("回看计算溢出")
	}
	return checkLookback(lb.A*param + lb.B)
}

func checkLookback(l int64) (int64, error) {
	if l > MaxLookback {
		return 0, fmt.Errorf("窗口长度 %d 超过上限 %d", l, int64(MaxLookback))
	}
	return l, nil
}

// Range 返回参数取下限、上限时的窗口长度，并检查 L ≥ 1 且不超过上限、不溢出。
func (lb Lookback) Range() (lo, hi int64, err error) {
	if lb.Param == "" {
		l, err := lb.At(0)
		if err != nil {
			return 0, 0, err
		}
		if l < 1 {
			return 0, 0, errors.New("窗口长度要 ≥ 1")
		}
		return l, l, nil
	}
	if lo, err = lb.At(int64(lb.p.Min)); err != nil {
		return 0, 0, fmt.Errorf("参数取下限 %v 时：%w", lb.p.Min, err)
	}
	if hi, err = lb.At(int64(lb.p.Max)); err != nil {
		return 0, 0, fmt.Errorf("参数取上限 %v 时：%w", lb.p.Max, err)
	}
	if lo < 1 {
		return 0, 0, fmt.Errorf("参数取下限 %v 时窗口长度 %d < 1", lb.p.Min, lo)
	}
	return lo, hi, nil
}

// LookbackFor 按参数值（声明顺序）求窗口长度。
func (m *Manifest) LookbackFor(values []float64) (int64, error) {
	lb, err := ParseLookback(m.Lookback, m.Params)
	if err != nil {
		return 0, err
	}
	if lb.Param == "" {
		return lb.At(0)
	}
	i := m.ParamIndex(lb.Param)
	if i < 0 || i >= len(values) {
		return 0, errors.New("参数值个数与声明不符")
	}
	l, err := lb.At(int64(values[i]))
	if err != nil {
		return 0, err
	}
	if l < 1 {
		return 0, fmt.Errorf("窗口长度 %d < 1", l)
	}
	return l, nil
}
