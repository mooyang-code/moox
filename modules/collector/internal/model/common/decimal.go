package common

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// Decimal 高精度数值类型（简化实现）
type Decimal struct {
	value string
}

// NewDecimal 创建新的Decimal
func NewDecimal(value string) Decimal {
	return Decimal{value: value}
}

// NewDecimalFromFloat 从浮点数创建Decimal
func NewDecimalFromFloat(value float64) Decimal {
	return Decimal{value: fmt.Sprintf("%.8f", value)}
}

// String 返回字符串表示
func (d Decimal) String() string {
	return d.value
}

// Float64 转换为float64
func (d Decimal) Float64() (float64, error) {
	return strconv.ParseFloat(d.value, 64)
}

// MarshalJSON JSON序列化
func (d Decimal) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.value)
}

// UnmarshalJSON JSON反序列化
func (d *Decimal) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		// 尝试从数字解析
		var num float64
		if err := json.Unmarshal(data, &num); err != nil {
			return err
		}
		value = fmt.Sprintf("%.8f", num)
	}
	d.value = value
	return nil
}

// Zero 零值
func Zero() Decimal {
	return Decimal{value: "0"}
}