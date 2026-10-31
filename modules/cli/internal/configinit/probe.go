package configinit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

// SSHTarget 是一台要用 SSH 登录的服务器：腾讯云服务器或因子机（因子机可以是局域网地址）。
// 不含密码：登录一律用本机的 MooX 密钥，密码只在开通免密登录时用一次。
type SSHTarget struct {
	Address string `json:"address"`
	SSHPort int    `json:"ssh_port"`
	SSHUser string `json:"ssh_user"`
}

func (t SSHTarget) normalize(title string) (SSHTarget, error) {
	t.Address = strings.TrimSpace(t.Address)
	t.SSHUser = strings.TrimSpace(t.SSHUser)
	for _, field := range []struct{ name, value string }{
		{title + "的 IP 地址", t.Address}, {title + "的 SSH 用户名", t.SSHUser},
	} {
		if err := checkText(field.name, field.value, true); err != nil {
			return SSHTarget{}, err
		}
	}
	port, err := sshPort(title, t.SSHPort)
	if err != nil {
		return SSHTarget{}, err
	}
	t.SSHPort = port
	return t, nil
}

// 检测项的结果。
const (
	CheckOK   = "ok"
	CheckWarn = "warn"
	CheckFail = "fail"
)

// CheckItem 是一项检测结果。
type CheckItem struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// ProbeReport 是因子机的环境检测结果。
type ProbeReport struct {
	OK    bool        `json:"ok"`
	OS    string      `json:"os,omitempty"`
	Items []CheckItem `json:"items,omitempty"`
}

// Prober 负责录入页里的 SSH 操作；测试里替换。
type Prober interface {
	// Reach 只测网络：能不能连上这台机器的 SSH 端口并收到 SSH 服务的问候，不需要密码或密钥。
	// 成功时返回对方的 SSH 版本串。
	Reach(ctx context.Context, target SSHTarget) (string, error)
	// Login 用 MooX 密钥登录，报告免密登录是否可用、是否需要先确认主机指纹。
	Login(ctx context.Context, target SSHTarget) (LoginReport, error)
	// Reset 删除本机记录的这台主机的指纹（机器重装、指纹文件损坏时用），让页面重新核对指纹。
	Reset(ctx context.Context, target SSHTarget) error
	// Trust 在用户确认后记录主机指纹。
	Trust(ctx context.Context, target SSHTarget, fingerprint string) error
	// Authorize 用一次性密码登录并安装 MooX 公钥。
	Authorize(ctx context.Context, target SSHTarget, password string) error
	// Probe 用 MooX 密钥登录因子机，检测环境。
	Probe(ctx context.Context, target SSHTarget) (ProbeReport, error)
}

type systemProber struct{}

// facts 是检测脚本输出的 key=value。
type facts map[string]string

func (systemProber) Probe(ctx context.Context, target SSHTarget) (ProbeReport, error) {
	key, err := ensureSSHKey()
	if err != nil {
		return ProbeReport{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	client, err := setupssh.Dial(ctx, sshTarget(target), "", keyOnly(key))
	switch {
	case errors.Is(err, setupssh.ErrHostKeyUnknown), errors.Is(err, setupssh.ErrAuthFailed):
		return ProbeReport{}, fmt.Errorf("还不能免密登录 %s，请先在「免密登录」一步完成授权", target.Address)
	case err != nil:
		return ProbeReport{}, unreachable(target)
	}
	defer client.Close()
	output, err := runRemote(ctx, client)
	if err != nil {
		return ProbeReport{}, fmt.Errorf("已登录 %s，但读取系统信息失败", target.Address)
	}
	return evaluate(parseFacts(output)), nil
}

// posixProbe 收集系统信息。非交互 SSH 的 PATH 往往不含 Go、uv 等常见安装位置，先补上。
const posixProbe = `PATH="$PATH:/usr/local/go/bin:$HOME/go/bin:$HOME/.local/bin:$HOME/.cargo/bin:/usr/local/bin"
os=$(uname -s); echo "os=$os"; echo "arch=$(uname -m)"
echo "osname=$(. /etc/os-release 2>/dev/null; echo "$PRETTY_NAME")"; echo "cpu=$(nproc 2>/dev/null || getconf _NPROCESSORS_ONLN)"
echo "mem_kb=$(awk '/^MemTotal:/{print $2}' /proc/meminfo)"
echo "disk_kb=$(df -Pk "$HOME" | awk 'NR==2{print $4}')"
true`

func runRemote(ctx context.Context, client setupssh.Client) (string, error) {
	result, err := client.Run(ctx, []string{"sh", "-c", posixProbe}, nil)
	if err != nil || !strings.Contains(result.Stdout, "os=") {
		return "", errors.New("remote shell is not POSIX")
	}
	return result.Stdout, nil
}

func parseFacts(output string) facts {
	out := facts{}
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && key != "" {
			out[key] = strings.TrimSpace(value)
		}
	}
	return out
}

func (f facts) int(key string) int64 {
	value, err := strconv.ParseInt(f[key], 10, 64)
	if err != nil {
		return 0
	}
	return value
}

// 因子机的建议配置。
const (
	factorMinCPU      = 8
	factorMinMemoryGB = 16
	factorMinDiskGB   = 20
	factorLowDiskGB   = 5
)

// evaluate 按因子引擎的安装要求逐项判断。只有「装不上」的情况算失败，低于建议配置只提醒。
func evaluate(f facts) ProbeReport {
	report := ProbeReport{OK: true}
	add := func(name, status, detail string) {
		report.Items = append(report.Items, CheckItem{Name: name, Status: status, Detail: detail})
		if status == CheckFail {
			report.OK = false
		}
	}
	osName := f["osname"]
	if f["os"] != "Linux" {
		// 因子引擎只支持 Linux（systemd 用户服务）。
		add("系统", CheckFail, firstNonEmpty(osName, f["os"], "未知系统")+"。因子引擎只支持 Linux")
		return report
	}
	report.OS = "linux"
	add("系统", CheckOK, strings.TrimSpace(firstNonEmpty(osName, "Linux")+" · "+f["arch"]))

	if cpu := f.int("cpu"); cpu >= factorMinCPU {
		add("CPU", CheckOK, fmt.Sprintf("%d 核", cpu))
	} else if cpu > 0 {
		add("CPU", CheckWarn, fmt.Sprintf("%d 核，建议 %d 核以上，计算会比较慢", cpu, factorMinCPU))
	}

	memoryGB := float64(f.int("mem_kb")) / (1 << 20)
	if memoryGB >= factorMinMemoryGB-1 {
		add("内存", CheckOK, fmt.Sprintf("%.0f GB", memoryGB))
	} else if memoryGB > 0 {
		add("内存", CheckWarn, fmt.Sprintf("%.0f GB，建议 %d GB 以上", memoryGB, factorMinMemoryGB))
	}
	if memoryGB > 0 {
		// 进程内存预算 = 总内存 × 60%；窗口上限、单次计算上限由引擎自己的 tune 命令按公式写入 engine.yaml。
		add("内存预算", CheckOK, fmt.Sprintf("因子引擎进程预算约 %.0f GB（总内存的 60%%）；安装后执行 moox-factor-engine tune --write 写入窗口与计算上限的建议值", memoryGB*0.6))
	}

	diskGB := float64(f.int("disk_kb")) / (1 << 20)
	switch {
	case diskGB >= factorMinDiskGB:
		add("磁盘", CheckOK, fmt.Sprintf("可用 %.0f GB", diskGB))
	case diskGB >= factorLowDiskGB:
		add("磁盘", CheckWarn, fmt.Sprintf("可用 %.0f GB，建议留出 %d GB 以上", diskGB, factorMinDiskGB))
	case diskGB > 0:
		add("磁盘", CheckFail, fmt.Sprintf("可用只有 %.1f GB，至少需要 %d GB", diskGB, factorLowDiskGB))
	}
	return report
}
