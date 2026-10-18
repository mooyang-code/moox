// Package servicehost 让运维机上的外部程序（因子引擎、归档 worker）在 Windows 上作为系统服务运行：
// `service install|uninstall|start|stop` 子命令注册与控制服务，服务启动时收到停止请求走与 SIGTERM 相同的优雅退出。
// Linux（systemd 用户服务）与 macOS（launchd）不使用它：Run 直接运行，service 子命令报错。
package servicehost

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Spec 描述一个服务。
type Spec struct {
	// Name 是服务名，例如 moox-factor-engine。
	Name        string
	DisplayName string
	Description string
	// Args 是服务启动时传给程序的参数（例如 serve -config <绝对路径>）。
	Args []string
}

// RestartDelay 是服务异常退出后由服务管理器重启前的等待时间。
const RestartDelay = 10 * time.Second

// StopTimeout 是收到停止请求后等待优雅退出的上限，超过后报告已停止，由系统结束进程。
const StopTimeout = 60 * time.Second

// ErrUnsupported 表示当前系统不使用 service 子命令。
var ErrUnsupported = errors.New("service subcommands are only available on Windows; use the systemd (Linux) or launchd (macOS) install scripts")

// Command 执行 service 子命令：install、uninstall、start、stop。
func Command(spec Spec, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: service install|uninstall|start|stop")
	}
	if spec.Name == "" {
		return errors.New("service name is required")
	}
	switch args[0] {
	case "install":
		return install(spec)
	case "uninstall":
		return uninstall(spec.Name)
	case "start":
		return start(spec.Name)
	case "stop":
		return stop(spec.Name)
	default:
		return fmt.Errorf("unknown service command %q (install, uninstall, start, stop)", args[0])
	}
}

// Run 运行 serve：由 Windows 服务管理器启动时以服务方式运行（停止请求时取消 ctx 并等待 serve 返回），否则直接运行。
func Run(name string, serve func(ctx context.Context) error) error {
	return run(name, serve)
}
