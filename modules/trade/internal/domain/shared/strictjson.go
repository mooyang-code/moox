package shared

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// JSONKeys 返回结构体的 JSON 键名（取 json 标签），按字段声明顺序。
func JSONKeys(value any) []string {
	typ := reflect.TypeOf(value)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	keys := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			keys = append(keys, name)
		}
	}
	return keys
}

// DecodeStrictObject 把 JSON 对象解到结构体里，写入设置时用：
// encoding/json 的键名匹配不区分大小写，"Reserve_Ratio" 会被悄悄接受，这里逐个键精确比对，
// 未知键和大小写不规范的键都拒绝，值的类型不对也拒绝。空串当作空对象。
func DecodeStrictObject(raw string, target any) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return fmt.Errorf("设置不是合法的 JSON 对象：%w", err)
	}
	allowed := JSONKeys(target)
	for key := range fields {
		exact := false
		for _, name := range allowed {
			if key == name {
				exact = true
				break
			}
			if strings.EqualFold(key, name) {
				return fmt.Errorf("设置键 %q 的大小写不规范，应写成 %q", key, name)
			}
		}
		if !exact {
			return fmt.Errorf("设置里有未知的键 %q", key)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("设置的值类型不对：%w", err)
	}
	return nil
}
