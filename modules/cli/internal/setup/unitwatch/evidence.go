package unitwatch

import (
	"io"
	"os"
	"regexp"
	"strings"
)

// crashPattern 是 Go 程序崩溃时常见的输出（参考 trpc monitor.sh 的 panic 检测）。
var crashPattern = regexp.MustCompile(`panic:|fatal error:|runtime error:|SIGSEGV|SIGABRT|\[PANIC\]`)

const (
	evidenceTailBytes = 256 << 10
	evidenceMaxLines  = 3
	evidenceMaxRunes  = 300
)

// crashEvidence 读取组件运行输出日志（run/logs/<组件>.log，运行助手把进程的标准输出与错误写在这里）的末尾，
// 返回最后几条崩溃相关的行。日志不存在或读不了时返回空：线索只用于告警正文，不影响拉起。
func crashEvidence(path string) []string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	offset := info.Size() - evidenceTailBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(file, evidenceTailBytes))
	if err != nil {
		return nil
	}
	lines := strings.Split(string(raw), "\n")
	if offset > 0 && len(lines) > 0 {
		lines = lines[1:] // 第一行可能被截断
	}
	var found []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || !crashPattern.MatchString(line) {
			continue
		}
		if runes := []rune(line); len(runes) > evidenceMaxRunes {
			line = string(runes[:evidenceMaxRunes]) + "…"
		}
		found = append(found, line)
	}
	if len(found) > evidenceMaxLines {
		found = found[len(found)-evidenceMaxLines:]
	}
	return found
}
