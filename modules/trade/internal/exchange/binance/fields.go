package binance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// fields 按原样大小写保存一个 JSON 对象的键。
// 币安私有流用单字母键且大小写不同的是两个字段（c/C、i/I、o/O 等），
// encoding/json 解到结构体时找不到同名键会忽略大小写去匹配，所以私有流一律按精确键取值。
type fields map[string]json.RawMessage

func exactFields(raw []byte) (fields, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	if values == nil {
		return nil, fmt.Errorf("event is not an object")
	}
	return fields(values), nil
}

// value 返回键对应的原始值；JSON null 按缺键处理。
func (f fields) value(key string) (json.RawMessage, bool) {
	raw, ok := f[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	return raw, true
}

func (f fields) required(key string) error {
	if _, ok := f.value(key); !ok {
		return fmt.Errorf("field %q is required", key)
	}
	return nil
}

// str 取字符串；数字也按原文返回（订单号、成交号有时是数字）。
func (f fields) str(key string) (string, error) {
	raw, ok := f.value(key)
	if !ok {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String(), nil
	}
	return "", fmt.Errorf("field %q: want string, got %s", key, truncateRaw(raw))
}

// int64 取整数，数字和字符串形式的数字都接受。
func (f fields) int64(key string) (int64, error) {
	text, err := f.str(key)
	if err != nil || text == "" {
		return 0, err
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("field %q: want integer, got %q", key, text)
	}
	return value, nil
}

func (f fields) bool(key string) (bool, error) {
	raw, ok := f.value(key)
	if !ok {
		return false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("field %q: want bool, got %s", key, truncateRaw(raw))
	}
	return value, nil
}

func (f fields) object(key string) (fields, error) {
	raw, ok := f.value(key)
	if !ok {
		return nil, fmt.Errorf("field %q is required", key)
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("field %q: want object, got %s", key, truncateRaw(raw))
	}
	return exactFields(raw)
}

func (f fields) objects(key string) ([]fields, error) {
	raw, ok := f.value(key)
	if !ok {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("field %q: want array, got %s", key, truncateRaw(raw))
	}
	out := make([]fields, 0, len(items))
	for index, item := range items {
		value, err := exactFields(item)
		if err != nil {
			return nil, fmt.Errorf("field %q[%d]: %w", key, index, err)
		}
		out = append(out, value)
	}
	return out, nil
}

func truncateRaw(raw []byte) string {
	if len(raw) > 64 {
		return string(raw[:64]) + "..."
	}
	return string(raw)
}
