package configinit

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// 按行修改 TOML：只动目标键所在的那一行，注释、空行和其它键原样保留。
// 只处理本包写出的简单形式（[段] 下的 key = value 与内联表），不是通用 TOML 编辑器；
// 改完的结果一律再经 topology.Parse（或 ParseSecrets）严格校验。

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

// dropSection 删除整个 [section] 段：标题行、段内的键与注释，连同紧挨在标题上方的注释行（段的说明）。
// 紧挨在下一段标题上方的注释属于下一段，保留；删除处不留连续的空行。没有该段时不动。不删子表（[section.x]）。
func dropSection(lines []string, section string) []string {
	start, end := sectionRange(lines, section)
	if start < 0 {
		return lines
	}
	isComment := func(i int) bool { return strings.HasPrefix(strings.TrimSpace(lines[i]), "#") }
	if end < len(lines) {
		for end-1 > start && isComment(end-1) {
			end--
		}
	} else if lines[end-1] == "" && end-1 > start {
		end-- // 保留文件末尾的换行
	}
	for start > 0 && isComment(start-1) {
		start--
	}
	out := append(append([]string(nil), lines[:start]...), lines[end:]...)
	for start > 0 && start < len(out) && strings.TrimSpace(out[start-1]) == "" && strings.TrimSpace(out[start]) == "" {
		out = append(out[:start], out[start+1:]...)
	}
	return out
}

var (
	headerPattern = regexp.MustCompile(`^\s*(\[\[?)\s*([^\[\]]+?)\s*(\]\]?)\s*(#.*)?$`)
	keyPattern2   = regexp.MustCompile(`^\s*([A-Za-z0-9_-]+|"[^"]*")\s*=\s*(.*)$`)
)

// tableHeader 识别 [name] 或 [[name]]。
func tableHeader(line string) (name string, array bool, ok bool) {
	m := headerPattern.FindStringSubmatch(line)
	if m == nil || len(m[1]) != len(m[3]) {
		return "", false, false
	}
	return m[2], m[1] == "[[", true
}

func keyValue(line string) (key, value string, ok bool) {
	m := keyPattern2.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	return strings.Trim(m[1], `"`), m[2], true
}

// valueEnd 返回从第 i 行开始的键值占用到的最后一行（多行数组与多行字符串）。
func valueEnd(lines []string, i int) int {
	_, value, ok := keyValue(lines[i])
	if !ok {
		return i
	}
	value = strings.TrimSpace(value)
	for _, quote := range []string{`"""`, `'''`} {
		if strings.HasPrefix(value, quote) && strings.Count(value, quote) < 2 {
			for j := i + 1; j < len(lines); j++ {
				if strings.Contains(lines[j], quote) {
					return j
				}
			}
			return len(lines) - 1
		}
	}
	if strings.HasPrefix(value, "[") {
		depth := strings.Count(value, "[") - strings.Count(value, "]")
		for j := i + 1; depth > 0 && j < len(lines); j++ {
			depth += strings.Count(lines[j], "[") - strings.Count(lines[j], "]")
			if depth <= 0 {
				return j
			}
		}
	}
	return i
}

type block struct {
	header string // 表名；文件开头没有表头的部分为空
	array  bool
	lines  []string
}

func splitBlocks(lines []string) []block {
	blocks := []block{{}}
	for _, line := range lines {
		if name, array, ok := tableHeader(line); ok {
			blocks = append(blocks, block{header: name, array: array})
		}
		blocks[len(blocks)-1].lines = append(blocks[len(blocks)-1].lines, line)
	}
	return blocks
}

// writeExclusive 以 0600 新建文件，已存在时拒绝覆盖。
func writeExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("无法创建备份 %s：%w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("写备份 %s 失败：%w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("写备份 %s 失败：%w", path, err)
	}
	return file.Close()
}
