package factorname

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// CanonicalValues 生成一个成员的参数（参数名 → 取值）的规范 JSON：键按字典序，数字用十进制 JSON 数字，没有空白。
// 列元数据的 factor_params 属性用它。
func CanonicalValues(values map[string]float64) (string, error) {
	var b strings.Builder
	if err := writeValues(&b, values); err != nil {
		return "", err
	}
	return b.String(), nil
}

// CanonicalParams 生成 __params 列的规范 JSON：成员键 → 参数名 → 取值，两层键都按字典序，没有空白。
// 同一期重算时它必须与因子值一样逐位相同（实时写入的提交 ID 依赖它）。
func CanonicalParams(members map[string]map[string]float64) (string, error) {
	keys := make([]string, 0, len(members))
	for key := range members {
		if _, err := ParseMemberKey(key); err != nil {
			return "", err
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"` + key + `":`)
		if err := writeValues(&b, members[key]); err != nil {
			return "", fmt.Errorf("成员 %s：%w", key, err)
		}
	}
	b.WriteByte('}')
	return b.String(), nil
}

// ParseParams 解析 __params 列的 JSON，并校验成员键和参数名。
func ParseParams(text string) (map[string]map[string]float64, error) {
	var members map[string]map[string]float64
	if err := json.Unmarshal([]byte(text), &members); err != nil {
		return nil, fmt.Errorf("__params 不是合法的 JSON：%w", err)
	}
	for key, values := range members {
		if _, err := ParseMemberKey(key); err != nil {
			return nil, err
		}
		for name := range values {
			if err := ValidateParamName(name); err != nil {
				return nil, err
			}
		}
	}
	return members, nil
}

func writeValues(b *strings.Builder, values map[string]float64) error {
	names := make([]string, 0, len(values))
	for name := range values {
		if err := ValidateParamName(name); err != nil {
			return err
		}
		names = append(names, name)
	}
	sort.Strings(names)
	b.WriteByte('{')
	for i, name := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		number, err := jsonNumber(values[name])
		if err != nil {
			return fmt.Errorf("参数 %s：%w", name, err)
		}
		b.WriteString(`"` + name + `":` + number)
	}
	b.WriteByte('}')
	return nil
}
