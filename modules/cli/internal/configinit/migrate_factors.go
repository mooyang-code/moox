package configinit

import (
	"errors"
	"slices"
	"strings"
)

// DefaultFactorsReport 记录 --add-default-factors 补上的定义与成员（只有因子 ID 与数据集 ID，不含配置值）。
type DefaultFactorsReport struct {
	Definitions []string `json:"definitions,omitempty"`
	Members     []string `json:"members,omitempty"`
}

// addDefaultFactors 把内置样例（moox.toml.example）里有、moox.toml 里还没有的默认因子定义与成员补上：
// 定义按 factor_id 判断，成员按“源数据集 + 频率 + 成员别名（缺省为 factor_id）”判断；已有的条目一律不动。
// 新条目分别追加在最后一个同类条目之后，原样沿用样例里的写法（包括成员的 status）。
func addDefaultFactors(plain []byte) ([]byte, DefaultFactorsReport, error) {
	var report DefaultFactorsReport
	blocks := splitBlocks(strings.Split(string(plain), "\n"))
	if !slices.ContainsFunc(blocks, func(b block) bool { return b.header == "factors" && !b.array }) {
		return nil, report, errors.New("moox.toml 里没有 [factors]，先按样例补上 [factors] 与因子集")
	}
	defined := map[string]bool{}
	members := map[string]bool{}
	for _, b := range blocks {
		switch {
		case b.header == "factors.definitions" && b.array:
			defined[blockValue(b, "factor_id")] = true
		case b.header == "factors.members" && b.array:
			members[memberKey(b)] = true
		}
	}

	var newDefinitions, newMembers [][]string
	for _, b := range splitBlocks(strings.Split(exampleTemplate, "\n")) {
		switch {
		case b.header == "factors.definitions" && b.array:
			id := blockValue(b, "factor_id")
			if id == "" || defined[id] {
				continue
			}
			defined[id] = true
			newDefinitions = append(newDefinitions, entryLines(b))
			report.Definitions = append(report.Definitions, id)
		case b.header == "factors.members" && b.array:
			key := memberKey(b)
			if members[key] {
				continue
			}
			members[key] = true
			newMembers = append(newMembers, entryLines(b))
			report.Members = append(report.Members, key)
		}
	}
	if len(newDefinitions) == 0 && len(newMembers) == 0 {
		return plain, report, nil
	}
	blocks = insertAfterLast(blocks, "factors.definitions", newDefinitions)
	blocks = insertAfterLast(blocks, "factors.members", newMembers)
	var out []string
	for _, b := range blocks {
		out = append(out, b.lines...)
	}
	return []byte(strings.Join(out, "\n")), report, nil
}

// blockValue 读出块里某个键的值（去掉引号与行尾注释）；没有时为空。
func blockValue(b block, key string) string {
	for _, line := range b.lines[1:] {
		if k, v, ok := keyValue(line); ok && k == key {
			return quotedValue(v)
		}
	}
	return ""
}

// memberKey 是成员的身份：源数据集/频率/成员别名，别名缺省为 factor_id。
func memberKey(b block) string {
	alias := blockValue(b, "member_alias")
	if alias == "" {
		alias = blockValue(b, "factor_id")
	}
	return blockValue(b, "source_dataset_id") + "/" + blockValue(b, "freq") + "/" + alias
}

// entryLines 取样例条目的表头与键值行，去掉属于下一段的空行与注释。
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

// insertAfterLast 把条目插在最后一个同类数组条目之后，条目之间空一行；文件里没有同类条目时追加到末尾。
// 同类条目后面跟着的空行与注释属于下一段，挪到新条目之后，保持原来的间隔。
func insertAfterLast(blocks []block, header string, entries [][]string) []block {
	if len(entries) == 0 {
		return blocks
	}
	at := -1
	for i, b := range blocks {
		if b.header == header && b.array {
			at = i
		}
	}
	if at < 0 {
		at = len(blocks) - 1
	}
	prev := &blocks[at]
	core := entryLines(*prev)
	tail := slices.Clone(prev.lines[len(core):])
	prev.lines = append(core, "")
	added := make([]block, 0, len(entries))
	for i, lines := range entries {
		lines = slices.Clone(lines)
		if i < len(entries)-1 {
			lines = append(lines, "")
		} else {
			if len(tail) == 0 || strings.TrimSpace(tail[0]) != "" {
				lines = append(lines, "")
			}
			lines = append(lines, tail...)
		}
		added = append(added, block{header: header, array: true, lines: lines})
	}
	out := slices.Clone(blocks[:at+1])
	out = append(out, added...)
	return append(out, blocks[at+1:]...)
}
