package command

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// commandIndexEntry 是命令清单中的一条叶子命令记录。
type commandIndexEntry struct {
	Path  string
	Short string
}

// renderSkillCommandIndex 从已挂载完整的根命令树生成 skills/moox/references/command-index.md 的完整内容。
// 命令说明以代码里的 Short 为唯一来源，消除手工维护清单与真实命令树的漂移。
func renderSkillCommandIndex(root *cobra.Command) string {
	var b strings.Builder
	b.WriteString("# moox-cli 命令清单\n\n")
	b.WriteString("> 本文件由 `moox-cli` 命令树自动生成，请勿手工修改。改动命令或说明后在 `modules/cli` 目录运行 `MOOX_UPDATE_COMMAND_INDEX=1 go test ./internal/command -run TestSkillCommandIndexMatchesCommandTree` 更新。\n\n")
	b.WriteString("`moox-cli` 的全部命令，按一级命令分组；每个命令的参数用 `./bin/moox-cli <命令> --help` 查看（帮助里写明对应的接口与默认值）。\n")
	b.WriteString("说明里写“默认只预览，--yes 才执行”的是破坏性命令：不带 `--yes` 只输出将要发出的请求，把预览给用户确认后再加 `--yes`。\n")
	b.WriteString("用法速查与场景见 [`modules.md`](modules.md)，管理台页面到命令的对照见 [`api-recipes.md`](api-recipes.md)。\n")

	topCommands := visibleChildren(root)
	prefix := root.Name() + " "
	for _, top := range topCommands {
		leaves := collectLeafEntries(top, prefix)
		fmt.Fprintf(&b, "\n## %s：%s\n\n", top.Name(), sanitizeCommandIndexCell(top.Short))
		b.WriteString("| 命令 | 说明 |\n")
		b.WriteString("| --- | --- |\n")
		for _, leaf := range leaves {
			fmt.Fprintf(&b, "| `%s` | %s |\n", leaf.Path, sanitizeCommandIndexCell(leaf.Short))
		}
	}
	return b.String()
}

func visibleChildren(cmd *cobra.Command) []*cobra.Command {
	var children []*cobra.Command
	for _, child := range cmd.Commands() {
		if child.Name() == "help" || child.Hidden {
			continue
		}
		children = append(children, child)
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
	return children
}

func collectLeafEntries(cmd *cobra.Command, rootPrefix string) []commandIndexEntry {
	var out []commandIndexEntry
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		children := visibleChildren(c)
		if len(children) == 0 {
			out = append(out, commandIndexEntry{
				Path:  strings.TrimPrefix(c.CommandPath(), rootPrefix),
				Short: c.Short,
			})
			return
		}
		for _, child := range children {
			walk(child)
		}
	}
	walk(cmd)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func sanitizeCommandIndexCell(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	return strings.ReplaceAll(text, "|", `\|`)
}
