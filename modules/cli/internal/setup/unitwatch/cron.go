package unitwatch

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/packages/hostpolicy"
)

// CronMarker 标记 watch 的 crontab 行：安装时按它替换，卸载时按它删除。
const CronMarker = "# moox-watchdog"

// legacyCronMarker 是旧部署器每分钟执行各包 healthcheck.sh 的条目，原生部署不再使用，安装时一并删除。
const legacyCronMarker = "# moox-healthchecks"

// CronLine 是部署用户 crontab 里的 watch 条目：每分钟一次，flock 保证上一轮没跑完时不叠加，日志由 watch 自己写并轮转。
func CronLine(root, runtime string) string {
	return fmt.Sprintf("* * * * * PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin flock -n %s %s watch --root %s --log %s %s",
		filepath.Join(root, "run", "watchdog.lock"), runtime, root, filepath.Join(root, "run", "watchdog.log"), CronMarker)
}

// InstallOptions 是 install-watchdog 的参数。
type InstallOptions struct {
	Root string
	// Runtime 是 crontab 里调用的 moox-runtime，默认 <Root>/host/current/bin/moox-runtime（随 host 单元切换）。
	Runtime string
	// Seed 是部署时用 moox.toml 生成的策略种子；为空时不动策略缓存。
	Seed *hostpolicy.Policy
	// Remove 为 true 时删除 watch 条目（以及旧条目），不写种子。
	Remove bool

	// crontab 执行 crontab 命令（"-l" 读取、"-" 从标准输入写入）；cronActive 检查 cron 守护进程在运行。测试可替换。
	crontab    func(args []string, stdin []byte) ([]byte, error)
	cronActive func() error
}

// InstallResult 是 install-watchdog 的结果。
type InstallResult struct {
	CronLine      string `json:"cron_line,omitempty"`
	Removed       bool   `json:"removed,omitempty"`
	LegacyRemoved int    `json:"legacy_removed,omitempty"`
	// Seed 是策略种子的处理结果：written（写入）、unchanged（内容相同）、kept_monitor（已有从 Monitor 拉取的策略，不覆盖）。
	Seed string `json:"seed,omitempty"`
}

// Install 幂等地写入（或删除）部署用户 crontab 里的 watch 条目，并按需写入策略种子。
func Install(opts InstallOptions) (InstallResult, error) {
	var result InstallResult
	if opts.Root == "" || !filepath.IsAbs(opts.Root) {
		return result, errors.New("install-watchdog requires an absolute deployment root")
	}
	if opts.Runtime == "" {
		opts.Runtime = filepath.Join(opts.Root, "host", "current", "bin", "moox-runtime")
	}
	if opts.crontab == nil {
		opts.crontab = runCrontab
	}
	if opts.cronActive == nil {
		opts.cronActive = cronDaemonActive
	}
	if !opts.Remove {
		if err := opts.cronActive(); err != nil {
			return result, err
		}
	}
	current, err := opts.crontab([]string{"-l"}, nil)
	if err != nil {
		return result, err
	}
	var kept []string
	for _, line := range strings.Split(strings.TrimRight(string(current), "\n"), "\n") {
		switch {
		case line == "":
			continue
		case strings.Contains(line, CronMarker):
			result.Removed = opts.Remove
		case strings.Contains(line, legacyCronMarker):
			result.LegacyRemoved++
		default:
			kept = append(kept, line)
		}
	}
	line := CronLine(opts.Root, opts.Runtime)
	if !opts.Remove {
		kept = append(kept, line)
		result.CronLine = line
	}
	content := strings.Join(kept, "\n")
	if content != "" {
		content += "\n"
	}
	if _, err := opts.crontab([]string{"-"}, []byte(content)); err != nil {
		return result, err
	}
	written, err := opts.crontab([]string{"-l"}, nil)
	if err != nil {
		return result, err
	}
	count := strings.Count(string(written), CronMarker)
	if (opts.Remove && count != 0) || (!opts.Remove && (count != 1 || !strings.Contains(string(written), line))) {
		return result, errors.New("crontab verification failed after writing the watchdog entry")
	}
	if opts.Seed != nil && !opts.Remove {
		if result.Seed, err = writeSeed(opts.Root, *opts.Seed); err != nil {
			return result, err
		}
	}
	return result, nil
}

// writeSeed 写入策略种子：已有从 Monitor 拉取的策略时不覆盖（中心才是唯一来源），内容相同时不重写。
func writeSeed(root string, seed hostpolicy.Policy) (string, error) {
	path := hostpolicy.Path(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	existing, err := hostpolicy.Read(path)
	switch {
	case err == nil && existing.Source == hostpolicy.SourceMonitor:
		return "kept_monitor", nil
	case err == nil && existing.ContentHash == seed.Hash():
		return "unchanged", nil
	case err != nil && !os.IsNotExist(err):
		// 损坏或权限不对的旧文件：用种子覆盖，HostAgent 下一次拉取会再换成中心的值。
	}
	seed.Source = hostpolicy.SourceSeed
	if err == nil {
		seed.Version = existing.Version + 1
	}
	if err := hostpolicy.Write(path, seed); err != nil {
		return "", err
	}
	return "written", nil
}

func runCrontab(args []string, stdin []byte) ([]byte, error) {
	command := exec.Command("crontab", args...)
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if err != nil && len(args) == 1 && args[0] == "-l" && strings.Contains(stderr.String(), "no crontab") {
		return nil, nil // 用户还没有 crontab
	}
	if err != nil {
		return nil, fmt.Errorf("crontab %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// cronDaemonActive 确认 cron 守护进程在运行：没有 cron 时安装必须报错，不能静默跳过（否则守护根本不会执行）。
func cronDaemonActive() error {
	if _, err := exec.LookPath("crontab"); err != nil {
		return errors.New("crontab command is not installed; install cron before enabling the watchdog")
	}
	for _, name := range []string{"cron", "crond"} {
		if exec.Command("systemctl", "is-active", "--quiet", name).Run() == nil {
			return nil
		}
		if exec.Command("pgrep", "-x", name).Run() == nil {
			return nil
		}
	}
	return errors.New("cron daemon is not running; enable it (systemctl enable --now cron) before installing the watchdog")
}
