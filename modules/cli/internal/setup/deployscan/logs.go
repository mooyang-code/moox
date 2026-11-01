package deployscan

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
)

//go:embed logs.py
var logsScript string

// LogSection 是一份日志文件的结尾。
type LogSection struct {
	Path string `json:"path"`
	Text string `json:"text"`
}

// secretField 匹配日志里形如 password=xxx、"token": "xxx" 的字段，值要遮掉。
var secretField = regexp.MustCompile(`(?i)((?:secret|password|passwd|token|api[_-]?key|authorization|private[_-]?key)[a-z_-]*["']?\s*[:=]\s*["']?)[^\s"',}]+`)

// RedactLog 遮掉日志里的密钥字段。日志是给人和 Agent 排查用的，不应该带出密钥。
func RedactLog(text string) string { return secretField.ReplaceAllString(text, "${1}***") }

// ReadLogs 只读取一个组件在这台主机上最近的日志，包括失败回滚的发布目录里的日志。unit 是外部程序的 systemd 单元名。
func ReadLogs(ctx context.Context, dial Dial, host setupconfig.Host, root, component, unit string, lines int) ([]LogSection, error) {
	transport, err := dial(ctx, host)
	if err != nil {
		return nil, err
	}
	defer transport.Close()
	request, err := json.Marshal(map[string]any{"deploy_root": root, "component": component, "unit": unit, "lines": lines})
	if err != nil {
		return nil, err
	}
	output, err := transport.Run(ctx, []string{"python3", "-c", logsScript, string(request)}, nil)
	if err != nil {
		return nil, fmt.Errorf("logs_unavailable: 读取 %s 上的日志失败", host.Name)
	}
	sections := []LogSection{}
	if json.Unmarshal([]byte(output.Stdout), &sections) != nil {
		return nil, errors.New("logs_unavailable: 目标机返回的日志格式不对")
	}
	for i := range sections {
		sections[i].Text = RedactLog(sections[i].Text)
	}
	return sections, nil
}
