package factorname

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// FormatValue 把参数值写成成员键里的片段：十进制（不出现指数）、整数不带小数点、-0 按 0；
// 小数点换成 p、负号换成 n（0.5 → 0p5，-1 → n1）。NaN 与无穷大不是合法的参数值。
func FormatValue(value float64) (string, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "", fmt.Errorf("参数值 %v 不是有限数", value)
	}
	if value == 0 {
		value = 0
	}
	text := strconv.FormatFloat(value, 'f', -1, 64)
	text = strings.ReplaceAll(text, ".", "p")
	return strings.ReplaceAll(text, "-", "n"), nil
}

// ParseValue 解析 FormatValue 生成的片段，并重新格式化比对：01、1p0、n0 这类等价但不规范的写法一律拒绝。
func ParseValue(text string) (float64, error) {
	if !valuePattern.MatchString(text) {
		return 0, fmt.Errorf("参数值片段 %q 不合法", text)
	}
	raw := strings.ReplaceAll(strings.ReplaceAll(text, "p", "."), "n", "-")
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsInf(value, 0) {
		return 0, fmt.Errorf("参数值片段 %q 超出范围", text)
	}
	back, err := FormatValue(value)
	if err != nil || back != text {
		return 0, fmt.Errorf("参数值片段 %q 不是规范写法（应写成 %q）", text, back)
	}
	return value, nil
}

// jsonNumber 把数值写成 JSON 数字：十进制、-0 按 0。
func jsonNumber(value float64) (string, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "", fmt.Errorf("参数值 %v 不是有限数", value)
	}
	if value == 0 {
		value = 0
	}
	return strconv.FormatFloat(value, 'f', -1, 64), nil
}
