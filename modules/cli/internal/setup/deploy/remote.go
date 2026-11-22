package deploy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

// 组件的三个操作：restart 重启；stop 停掉并不再开机自启（systemctl --user disable --now，之后的部署保持停用）；
// start 恢复（enable --now）。没有暂停标记文件，状态就是 systemd 的状态。
const (
	OperationRestart = "restart"
	OperationStop    = "stop"
	OperationStart   = "start"
)

// Operate 在目标机上对一个组件的用户服务执行一个操作，返回操作后的状态（systemctl --user is-active 的输出）。
func Operate(ctx context.Context, target setupssh.Client, component, operation string) (string, error) {
	unit := UnitName(component)
	var argv []string
	switch operation {
	case OperationRestart:
		argv = []string{"systemctl", "--user", "restart", unit}
	case OperationStop:
		argv = []string{"systemctl", "--user", "disable", "--now", unit}
	case OperationStart:
		argv = []string{"systemctl", "--user", "enable", "--now", unit}
	default:
		return "", fmt.Errorf("未知的操作 %q", operation)
	}
	result, runErr := target.Run(ctx, argv, nil)
	// is-active 在没运行时退出码非 0，输出仍是状态。
	output, _ := target.Run(ctx, []string{"systemctl", "--user", "is-active", unit}, nil)
	state := strings.TrimSpace(output.Stdout)
	// 停一个没装单元的组件时 disable 会报错，但它确实没在运行：按结果算，不按命令的退出码算。
	if operation == OperationStop && state != "" && state != "active" {
		return state, nil
	}
	if runErr != nil {
		return "", fmt.Errorf("component_operation_failed: %s %s 失败：%s", operation, unit, lastLine(result.Stderr))
	}
	if state == "" {
		return "", errors.New("component_operation_failed: 查不到 " + unit + " 的状态")
	}
	return state, nil
}

// LogSection 是一段日志：来源（文件路径或 journald）与内容。
type LogSection struct {
	Component string `json:"component"`
	Source    string `json:"source"`
	Text      string `json:"text"`
}

// logScript 先读 tRPC 的文件日志（logs/<组件>/ 下最新的一份），没有再读 journald 里这个用户服务的输出。
// 第一行输出来源，其余是日志。grep 过滤是固定字符串匹配。
const logScript = `set -u
dir="$1"; unit="$2"; lines="$3"; pattern="$4"
file="$(ls -t "$dir"/*.log 2>/dev/null | head -n 1)"
if [ -n "$file" ] && [ -s "$file" ]; then
  echo "$file"
  if [ -n "$pattern" ]; then grep -F -- "$pattern" "$file" | tail -n "$lines"; else tail -n "$lines" "$file"; fi
else
  echo "journald:$unit"
  if [ -n "$pattern" ]; then journalctl --user -u "$unit" --no-pager -o short-iso 2>/dev/null | grep -F -- "$pattern" | tail -n "$lines"
  else journalctl --user -u "$unit" --no-pager -o short-iso -n "$lines" 2>/dev/null; fi
fi
exit 0`

// journalScript 只读 journald（标准输出与标准错误：崩溃信息、启动失败的原因）。
const journalScript = `set -u
unit="$1"; lines="$2"
echo "journald:$unit"
journalctl --user -u "$unit" --no-pager -o short-iso -n "$lines" 2>/dev/null
exit 0`

// Logs 读一组组件的日志：先文件日志，没有再读 journald；journal 为真时只读 journald。
func Logs(ctx context.Context, target setupssh.Client, layout Layout, components []string, pattern string, lines int, journal bool) ([]LogSection, error) {
	if lines <= 0 || lines > 5000 {
		return nil, errors.New("--lines 要在 1 到 5000 之间")
	}
	sections := []LogSection{}
	for _, id := range components {
		argv := []string{"sh", "-c", logScript, "moox-logs", layout.Logs(id), UnitName(id), strconv.Itoa(lines), pattern}
		if journal {
			argv = []string{"sh", "-c", journalScript, "moox-logs", UnitName(id), strconv.Itoa(lines)}
		}
		output, err := target.Run(ctx, argv, nil)
		if err != nil {
			return nil, fmt.Errorf("logs_failed: 读 %s 的日志失败：%s", id, lastLine(output.Stderr))
		}
		source, text, _ := strings.Cut(output.Stdout, "\n")
		sections = append(sections, LogSection{Component: id, Source: strings.TrimSpace(source), Text: text})
	}
	return sections, nil
}

// HostStatus 是一台主机的部署状态。
type HostStatus struct {
	Host       string            `json:"host"`
	Installed  bool              `json:"installed"`
	Version    string            `json:"version,omitempty"`
	Commit     string            `json:"commit,omitempty"`
	DeployedAt string            `json:"deployed_at,omitempty"`
	Components []ComponentStatus `json:"components"`
}

// ComponentStatus 是一个组件的状态：State 是 active（在运行）、inactive（单元在但没运行）或 missing（没有单元文件）。
// Installed 表示 version.json 里记着它；Extra 表示它已不属于这台主机（moox.toml 里去掉了），下一次整台部署会停掉并删除它。
type ComponentStatus struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Installed bool   `json:"installed"`
	Extra     bool   `json:"extra,omitempty"`
}

// Deployed 报告组件在这台主机上是否部署过：version.json 里有记录，或者还有它的单元文件。
func (c ComponentStatus) Deployed() bool { return c.Installed || c.State != "missing" }

// Status 由一次只读检测的结果（version.json、moox-* 单元与其中在运行的）整理出一台主机上各组件的状态。
// expected 是 moox.toml 里这台主机该有的组件；多出来的单元和 version.json 里多出来的记录也列出（Extra）。
func Status(host string, expected []string, version *Version, units, active []string) HostStatus {
	status := HostStatus{Host: host, Installed: version != nil, Components: []ComponentStatus{}}
	if version != nil {
		status.Version, status.Commit, status.DeployedAt = version.Version, version.Commit, version.DeployedAt
	}
	ids := slices.Clone(expected)
	for _, unit := range units {
		id := strings.TrimSuffix(strings.TrimPrefix(path.Base(unit), "moox-"), ".service")
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if version != nil {
		for _, id := range slices.Sorted(maps.Keys(version.Components)) {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	for _, id := range ids {
		component := ComponentStatus{ID: id, State: "missing", Installed: version.known(id), Extra: !slices.Contains(expected, id)}
		if slices.Contains(units, UnitName(id)) {
			component.State = "inactive"
		}
		if slices.Contains(active, UnitName(id)) {
			component.State = "active"
		}
		status.Components = append(status.Components, component)
	}
	return status
}
