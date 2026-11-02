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
	// 英文句子里 moox-cli 后面跟的普通单词。
	cliProseWords = []string{"and", "command", "starts"}
	// 除这些后缀外的文件不含用户要敲的命令。
	documentedSuffixes = []string{".md", ".go", ".sh", ".py", ".yaml", ".yml", ".vue", ".ts"}
)

// unknownCommand 沿命令树往下走，返回第一个不存在的子命令；走到叶子命令后面都是参数，不再检查。
func unknownCommand(root *cobra.Command, words []string) string {
	current := root
	for _, word := range words {
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
		quoted := map[string]bool{}
		if strings.HasSuffix(path, ".md") && strings.HasPrefix(path, filepath.Join(repository, "skills")) {
			for _, match := range quotedCommandPattern.FindAllStringSubmatch(string(raw), -1) {
				quoted[match[0]] = true
				matches = append(matches, match)
			}
		}
		for _, match := range matches {
			checked++
			if slices.ContainsFunc(quotedLogLines, func(line string) bool { return strings.HasPrefix(match[1], line) }) {
				continue
			}
			words := strings.Fields(match[1])
			// 代码注释和反引号里的第一个词可能是普通文字，只有 Markdown 里 moox-cli 后面的词要求是命令。
			prose := quoted[match[0]] || !strings.HasSuffix(path, ".md")
			if slices.Contains(cliProseWords, words[0]) || prose && findChild(rootCmd, words[0]) == nil {
				continue
			}
			if last := len(words) - 1; strings.HasSuffix(words[last], "-") {
				words = words[:last]
			}
			if word := unknownCommand(rootCmd, words); word != "" {
				relative, _ := filepath.Rel(repository, path)
				assert.Failf(t, "命令不存在", "%s: moox-cli %s（%s）", relative, match[1], word)
			}
		}
	}
	for _, dir := range []string{"skills", "docs", "modules", "scripts", "web/src"} {
		require.NoError(t, filepath.WalkDir(filepath.Join(repository, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if name := entry.Name(); name == "node_modules" || name == "计划" || name == "审查" || name == "test" || name == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			test := strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_test.sh") || strings.Contains(path, ".test.") || strings.Contains(path, ".spec.")
			if !test && slices.ContainsFunc(documentedSuffixes, func(suffix string) bool { return strings.HasSuffix(path, suffix) }) {
				scan(path)
			}
			return nil
		}))
	}
	scan(filepath.Join(repository, "README.md"))
	assert.Greater(t, checked, 50)
}
