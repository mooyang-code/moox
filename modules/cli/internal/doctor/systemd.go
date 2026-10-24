package doctor

import (
	"context"
	"fmt"
	"strings"

	core "github.com/mooyang-code/moox/packages/doctor"
)

// SystemdState 是 systemd 用户服务的实际状态：外部程序要同时满足开机自启、正在运行和 linger 才算托管正常
// （linger 关闭时用户登出后服务会被停掉）。
type SystemdState struct {
	Enabled bool
	Active  bool
	Linger  bool
}

func (s SystemdState) OK() bool { return s.Enabled && s.Active && s.Linger }

// SystemdChecker 读取节点上 systemd 用户服务的状态；节点是本机时在本机执行，否则经 SSH。
type SystemdChecker func(ctx context.Context, nodeID, unit string) (SystemdState, error)

// CommandRunner 在节点上执行一条命令并返回标准输出；非零退出码也返回 stdout，不当作错误。
type CommandRunner func(ctx context.Context, nodeID string, argv []string) (stdout string, err error)

const systemdProbe = `echo "enabled=$(systemctl --user is-enabled "$1" 2>/dev/null || true)"
echo "active=$(systemctl --user is-active "$1" 2>/dev/null || true)"
echo "linger=$(loginctl show-user "$(id -un)" -p Linger --value 2>/dev/null || true)"`

// NewSystemdChecker 用给定的命令执行器实现 SystemdChecker。
func NewSystemdChecker(run CommandRunner) SystemdChecker {
	return func(ctx context.Context, nodeID, unit string) (SystemdState, error) {
		if run == nil {
			return SystemdState{}, fmt.Errorf("systemd checker has no command runner")
		}
		out, err := run(ctx, nodeID, []string{"sh", "-c", systemdProbe, "moox-doctor-systemd", unit})
		if err != nil {
			return SystemdState{}, err
		}
		values := map[string]string{}
		for _, line := range strings.Split(out, "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
			if ok {
				values[key] = strings.TrimSpace(value)
			}
		}
		if _, ok := values["enabled"]; !ok {
			return SystemdState{}, fmt.Errorf("unexpected systemd probe output")
		}
		return SystemdState{Enabled: values["enabled"] == "enabled", Active: values["active"] == "active", Linger: values["linger"] == "yes"}, nil
	}
}

// supervisorResult 把 systemd 状态转成检查结果：三项都满足才通过，否则失败并给出每一项的实际情况。
func supervisorResult(id, unit string, state SystemdState, err error) core.CheckResult {
	if err != nil {
		return checkResult(id, core.StatusUnknown, "systemd user service state could not be read", err, "restart_service_manually")
	}
	if state.OK() {
		return checkResult(id, core.StatusPass, "systemd user service is enabled, active and lingering", nil)
	}
	summary := fmt.Sprintf("systemd user service %s is not healthy: enabled=%t active=%t linger=%t", unit, state.Enabled, state.Active, state.Linger)
	return checkResult(id, core.StatusFail, summary, nil, "restart_service_manually")
}

// systemdUnit 返回组件的 systemd 用户服务名；不是 systemd_user 托管时 ok 为 false。
func systemdUnit(manifest core.Manifest, componentID string) (string, bool) {
	for _, component := range manifest.Components {
		if component.ComponentID == componentID && component.Supervisor == "systemd_user" {
			return component.Unit, true
		}
	}
	return "", false
}
