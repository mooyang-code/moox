package configinit

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// setStorageRetention 设置 [storage_retention.defaults] 或 [storage_retention.spaces.<空间>] 里某个频率的保留期（--set-retention）。
// target 写成 defaults.<频率> 或 <空间>.<频率>；对应的表不存在时报错而不是新建：
// 只写一个频率的表会让其余频率失去内置默认值。
func setStorageRetention(plain []byte, target, value string) ([]byte, error) {
	scope, freq, ok := strings.Cut(strings.TrimSpace(target), ".")
	value = strings.TrimSpace(value)
	if !ok || scope == "" || freq == "" || value == "" {
		return nil, errors.New("--set-retention 需要写成 defaults.<频率>=<时长> 或 <空间>.<频率>=<时长>，例如 defaults.1m=35d")
	}
	header := "storage_retention.defaults"
	if scope != "defaults" {
		header = "storage_retention.spaces." + scope
	}
	blocks := splitBlocks(strings.Split(string(plain), "\n"))
	i := slices.IndexFunc(blocks, func(b block) bool { return b.header == header && !b.array })
	if i < 0 {
		return nil, fmt.Errorf("moox.toml 里没有 [%s]：这一项正在使用内置默认值，不需要改写", header)
	}
	line := strconv.Quote(freq) + " = " + strconv.Quote(value)
	b := &blocks[i]
	replaced := false
	for j := 1; j < len(b.lines); j++ {
		if k, _, ok := keyValue(b.lines[j]); ok && k == freq {
			b.lines[j] = line + retentionComment(b.lines[j])
			replaced = true
			break
		}
	}
	if !replaced {
		at := len(entryLines(*b))
		b.lines = slices.Insert(b.lines, at, line)
	}
	var out []string
	for _, blk := range blocks {
		out = append(out, blk.lines...)
	}
	return []byte(strings.Join(out, "\n")), nil
}

// retentionComment 保留原行末尾的注释（值本身不含 #）。
func retentionComment(line string) string {
	if i := strings.Index(line, "#"); i >= 0 {
		return "  " + strings.TrimSpace(line[i:])
	}
	return ""
}

// entryLines 取一个条目的表头与键值行，去掉属于下一段的空行与注释。
func entryLines(b block) []string {
	lines := slices.Clone(b.lines)
	for len(lines) > 1 {
		last := strings.TrimSpace(lines[len(lines)-1])
		if last != "" && !strings.HasPrefix(last, "#") {
			break
		}
		lines = lines[:len(lines)-1]
	}
	return lines
}
