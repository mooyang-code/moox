package deployscan

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
)

//go:embed logs.py
var logsScript string

// LogSection 是一份日志文件的结尾。
type LogSection struct {
	Path string `json:"path"`
	Text string `json:"text"`
}

// 日志里可能带密钥的写法：Authorization 整行、字段值、Bearer 令牌、URL 里的账号密码和 key 参数、PEM 块。
// JSON 里被转义的引号也要算上；值没有闭合引号时遮到行尾。无引号的值遮到空白为止，同一行紧跟的字段会一起遮掉，宁可多遮。
const secretName = `(?:secret|password|passwd|token|api[_-]?key|private[_-]?key)[a-z0-9_-]*(?:\\?["'])?[ \t]*[:=][ \t]*`

var (
	pemBlock    = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]+-----.*?(?:-----END [A-Z ]+-----|\z)`)
	pemEnd      = regexp.MustCompile(`-----END [A-Z ]+-----`)
	authHeader  = regexp.MustCompile(`(?im)(authorization(?:\\?["'])?[ \t]*[:=][ \t]*).*$`)
	bearerToken = regexp.MustCompile(`(?i)\b(bearer|basic)[ \t]+[^\s"'\\,]+`)
	urlPassword = regexp.MustCompile(`(://[^\s:/@"']+:)[^\s/@"']+@`)
	queryKey    = regexp.MustCompile(`(?i)([?&](?:key|token|secret|sign|signature)=)[^\s&"'\\]+`)
	plainField  = regexp.MustCompile(`(?i)(` + secretName + `)(?:[^\s"'\\]|\\[^"\s])\S*`)
	quotedField = regexp.MustCompile(`(?im)(` + secretName + `)(?:\\"(?:\\\\\\"|[^\\\n]|\\[^"\n])*(?:\\"|$)|"(?:[^"\\\n]|\\.)*(?:"|\\?$)|'[^'\n]*(?:'|$))`)
)

// RedactLog 遮掉日志里的密钥。日志是给人和 Agent 排查用的，不应该带出密钥。
func RedactLog(text string) string {
	// 日志从中间截断时，开头可能是半个 PEM：第一个 END 排在第一个 BEGIN 之前。
	if end := pemEnd.FindStringIndex(text); end != nil {
		if begin := strings.Index(text, "-----BEGIN "); begin < 0 || end[0] < begin {
			text = "***" + text[end[1]:]
		}
	}
	text = pemBlock.ReplaceAllString(text, "***")
	text = authHeader.ReplaceAllString(text, "${1}***")
	text = bearerToken.ReplaceAllString(text, "${1} ***")
	text = urlPassword.ReplaceAllString(text, "${1}***@")
	text = queryKey.ReplaceAllString(text, "${1}***")
	text = plainField.ReplaceAllString(text, "${1}***")
	return quotedField.ReplaceAllString(text, "${1}***")
}

// ReadLogs 只读取一个组件在这台主机上最近的日志，包括失败回滚的发布目录里的日志。unit 是外部程序的 systemd 单元名。
func ReadLogs(ctx context.Context, dial Dial, host setupconfig.Host, roots []string, component, unit string, lines int) ([]LogSection, error) {
	transport, err := dial(ctx, host)
	if err != nil {
		return nil, err
	}
	defer transport.Close()
	request, err := json.Marshal(map[string]any{"roots": roots, "component": component, "unit": unit, "lines": lines})
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
