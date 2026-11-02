package command

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	cliCommandPattern = regexp.MustCompile(`moox-cli((?: +[a-z][a-z0-9-]*)+)`)
	// skill 是照着执行的，常省略 moox-cli，只写反引号里的命令，比如 `setup verify`。
	quotedCommandPattern = regexp.MustCompile("`((?:[a-z][a-z0-9-]* +)+[a-z][a-z0-9-]*)")
	// skill 里引用的日志原文，开头碰巧和命令同名。
	quotedLogLines = []string{"monitor deployment sync updated", "storage view"}
)

// unknownCommand 沿命令树往下走，返回第一个不存在的子命令；走到叶子命令后面都是参数，不再检查。
// 第一个词不是一级命令时是英文注释里提到 moox-cli，不算命令。
func unknownCommand(root *cobra.Command, words []string) string {
	current := root
	for i, word := range words {
		if !current.HasSubCommands() {
			return ""
		}
		var next *cobra.Command
		for _, child := range current.Commands() {
			if child.Name() == word || child.HasAlias(word) {
				next = child
				break
			}
		}
		if next == nil {
			if i == 0 {
				return ""
			}
			return word
		}
		current = next
	}
	return ""
}

// skill、文档和报错提示是用户排障时照着敲的，里面的命令必须真实存在。
func TestDocumentedCommandsExist(t *testing.T) {
	mountRPCCommands(rootCmd)
	repository, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	checked := 0
	scan := func(path string) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		matches := cliCommandPattern.FindAllStringSubmatch(string(raw), -1)
		if strings.HasSuffix(path, ".md") && strings.HasPrefix(path, filepath.Join(repository, "skills")) {
			matches = append(matches, quotedCommandPattern.FindAllStringSubmatch(string(raw), -1)...)
		}
		for _, match := range matches {
			checked++
			if slices.ContainsFunc(quotedLogLines, func(line string) bool { return strings.HasPrefix(match[1], line) }) {
				continue
			}
			words := strings.Fields(match[1])
			if last := len(words) - 1; strings.HasSuffix(words[last], "-") {
				words = words[:last]
			}
			if word := unknownCommand(rootCmd, words); word != "" {
				relative, _ := filepath.Rel(repository, path)
				assert.Failf(t, "命令不存在", "%s: moox-cli %s（%s）", relative, match[1], word)
			}
		}
	}
	for _, dir := range []string{"skills", "docs", "modules"} {
		require.NoError(t, filepath.WalkDir(filepath.Join(repository, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if name := entry.Name(); name == "node_modules" || name == "计划" || name == "审查" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				scan(path)
			}
			return nil
		}))
	}
	scan(filepath.Join(repository, "README.md"))
	assert.Greater(t, checked, 50)
}
