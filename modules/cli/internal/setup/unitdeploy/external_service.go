package unitdeploy

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

// ExternalProgramUnit 返回外部程序组件对应的 systemd 用户服务名；不是外部程序时 ok 为 false。
func ExternalProgramUnit(component string) (unit string, ok bool) {
	program, ok := externalPrograms[component]
	return program.unit, ok
}

// StopExternalService 停止外部程序并取消开机自启，核对已停止后才返回成功；服务本来就没安装也算停止。
func StopExternalService(ctx context.Context, target setupssh.Client, component string) error {
	unit, ok := ExternalProgramUnit(component)
	if !ok {
		return fmt.Errorf("%s 不是外部程序", component)
	}
	return runExternal(ctx, target, "stop and disable "+component, `systemctl --user stop "$1" >/dev/null 2>&1 || true
systemctl --user disable "$1" >/dev/null 2>&1 || true
[ "$(systemctl --user is-active "$1" 2>/dev/null || true)" != active ] && [ "$(systemctl --user is-enabled "$1" 2>/dev/null || true)" != enabled ]`, unit)
}

// StartExternalService 启用并启动外部程序，再用安装目录里的健康凭据执行本机健康检查，等到就绪才返回。
// installDeploymentRoot 是部署根（安装目录为 <部署根>/<组件>）。
func StartExternalService(ctx context.Context, target setupssh.Client, component, installDeploymentRoot string) error {
	program, ok := externalPrograms[component]
	if !ok {
		return fmt.Errorf("%s 不是外部程序", component)
	}
	installDir := path.Join(installDeploymentRoot, component)
	if err := runExternal(ctx, target, "enable and start "+component, `set -eu
systemctl --user daemon-reload
systemctl --user enable "$1"
systemctl --user restart "$1"`, program.unit); err != nil {
		return err
	}
	for attempt := 0; attempt < 45; attempt++ {
		output, err := target.Run(ctx, []string{"sh", "-c", `set -eu
set -a
. "$1/secrets/health-auth.env"
set +a
exec env -i PATH=/usr/bin:/bin MOOX_HEALTH_AUTH_ACCESS_KEY="$MOOX_HEALTH_AUTH_ACCESS_KEY" MOOX_HEALTH_AUTH_SECRET_KEY="$MOOX_HEALTH_AUTH_SECRET_KEY" "$1/bin/moox-$2" health`, "moox-external-health", installDir, component}, nil)
		if err == nil && output.ExitCode == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return errors.New(strings.TrimSpace(component + " 已启动但没有在限定时间内就绪"))
}
