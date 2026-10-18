package configinit

import (
	"fmt"
	"regexp"
	"strings"
)

// 按行修改 TOML：只动目标键所在的那一行，注释、空行和其它键原样保留。
// 只处理本包写出的简单形式（[段] 下的 key = value 与内联表），不是通用 TOML 编辑器；
// 改完的结果一律再经 setupconfig.Parse 严格校验。

// sectionRange 返回 [section] 的标题行下标与段尾（下一个段标题或文件末尾）；没有该段时 start = -1。
func sectionRange(lines []string, section string) (start, end int) {
	header := "[" + section + "]"
	start, end = -1, len(lines)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "[") {
			continue
		}
		if start >= 0 {
			return start, i
		}
		if trimmed == header {
			start = i
		}
	}
	return start, end
}

// keyPattern 匹配 key = …（key 可以是裸键或带引号的键），捕获值与行尾注释。
func keyPattern(key string) *regexp.Regexp {
	quoted := regexp.QuoteMeta(key)
	return regexp.MustCompile(`^(\s*)(` + quoted + `|"` + quoted + `")(\s*=\s*)(.*)$`)
}

// findKey 返回段内 key 所在行；没有时返回 -1。
func findKey(lines []string, section, key string) int {
	start, end := sectionRange(lines, section)
	if start < 0 {
		return -1
	}
	pattern := keyPattern(key)
	for i := start + 1; i < end; i++ {
		if pattern.MatchString(lines[i]) {
			return i
		}
	}
	return -1
}

// putKey 设置段内 key 的值（value 是已经渲染好的 TOML 字面量）。
// 已有的行原地替换：值不是字符串时保留行尾注释；没有这个键时插到段内最后一个键之后；没有这个段时在文件末尾新建。
func putKey(lines []string, section, key, value string) []string {
	out := append([]string(nil), lines...)
	name := key
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`).MatchString(key) {
		name = tomlString(key)
	}
	if i := findKey(out, section, key); i >= 0 {
		match := keyPattern(key).FindStringSubmatch(out[i])
		comment := ""
		if rest := strings.TrimSpace(match[4]); !strings.HasPrefix(rest, `"`) && !strings.HasPrefix(rest, "'") && !strings.HasPrefix(rest, "{") {
			if at := strings.Index(rest, "#"); at >= 0 {
				comment = "   " + strings.TrimSpace(rest[at:])
			}
		}
		out[i] = match[1] + name + " = " + value + comment
		return out
	}
	start, end := sectionRange(out, section)
	if start < 0 {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		return append(out, "["+section+"]", name+" = "+value)
	}
	insert := start + 1
	for i := start + 1; i < end; i++ {
		trimmed := strings.TrimSpace(out[i])
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			insert = i + 1
		}
	}
	out = append(out[:insert], append([]string{name + " = " + value}, out[insert:]...)...)
	return out
}

// dropKey 删除段内的 key 行（不存在时不动）。
func dropKey(lines []string, section, key string) []string {
	i := findKey(lines, section, key)
	if i < 0 {
		return lines
	}
	return append(append([]string(nil), lines[:i]...), lines[i+1:]...)
}

// setKey 把 [section] 段内已有的 key 行替换为 key = value。示例里找不到时报错，避免示例改版后悄悄漏替换。
func setKey(lines []string, section, key, value string) ([]string, error) {
	if findKey(lines, section, key) < 0 {
		return nil, fmt.Errorf("示例配置里没有 [%s] 的 %s，请同步模板", section, key)
	}
	return putKey(lines, section, key, value), nil
}
