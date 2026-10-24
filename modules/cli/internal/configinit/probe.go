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

// FactorServer 是因子机的填写内容：用 SSH 登录的一台 Linux 机器（可以是局域网地址）。
// 因子机只能出站，登记成只能出站的普通部署主机，经 control 网关的反向连接中转。
type FactorServer struct {
	Address     string `json:"address"`
	SSHPort     int    `json:"ssh_port"`
	SSHUser     string `json:"ssh_user"`
	SSHPassword string `json:"ssh_password"`
}

func (f FactorServer) normalize() (FactorServer, error) {
	f.Address = strings.TrimSpace(f.Address)
	f.SSHUser = strings.TrimSpace(f.SSHUser)
	for _, field := range []struct{ name, value string }{
		{"因子机的 IP 地址", f.Address}, {"因子机的 SSH 用户名", f.SSHUser}, {"因子机的 SSH 密码", f.SSHPassword},
	} {
		if err := checkText(field.name, field.value, true); err != nil {
			return FactorServer{}, err
		}
	}
	if f.SSHPort == 0 {
		f.SSHPort = 22
	}
	if f.SSHPort < 1 || f.SSHPort > 65535 {
		return FactorServer{}, fmt.Errorf("因子机的 SSH 端口 %d 不合法", f.SSHPort)
	}
	return f, nil
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

// ProbeReport 是因子机的环境检测结果。HostKey 不为空时表示首次连接，需要用户确认指纹后再检测。
type ProbeReport struct {
	OK      bool        `json:"ok"`
	OS      string      `json:"os,omitempty"`
	Items   []CheckItem `json:"items,omitempty"`
	HostKey string      `json:"host_key,omitempty"`
}

// Prober 检测因子机的环境；测试里替换。
type Prober interface {
	Probe(ctx context.Context, target FactorServer) (ProbeReport, error)
	Trust(ctx context.Context, target FactorServer, fingerprint string) error
}

type systemProber struct{}

// facts 是检测脚本输出的 key=value。
type facts map[string]string

func (systemProber) Probe(ctx context.Context, target FactorServer) (ProbeReport, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	client, err := setupssh.Dial(ctx, sshTarget(target), target.SSHPassword, setupssh.Options{Timeout: 15 * time.Second})
	switch {
	case errors.Is(err, setupssh.ErrHostKeyUnknown):
		_, fingerprint, _ := strings.Cut(err.Error(), ": ")
		return ProbeReport{HostKey: strings.TrimSpace(fingerprint)}, nil
	case errors.Is(err, setupssh.ErrAuthFailed):
		return ProbeReport{}, fmt.Errorf("SSH 登录 %s 失败，请检查用户名和密码", target.Address)
	case err != nil:
		return ProbeReport{}, fmt.Errorf("连不上 %s:%d，请确认机器已开机、SSH 服务已打开，并且这台电脑能访问它", target.Address, target.SSHPort)
	}
	defer client.Close()
	output, err := runRemote(ctx, client)
	if err != nil {
		return ProbeReport{}, fmt.Errorf("已登录 %s，但读取系统信息失败", target.Address)
	}
	return evaluate(parseFacts(output)), nil
}

func (systemProber) Trust(ctx context.Context, target FactorServer, fingerprint string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := setupssh.TrustHost(ctx, sshTarget(target), strings.TrimSpace(fingerprint), setupssh.Options{Timeout: 15 * time.Second}); err != nil {
		return fmt.Errorf("没能记录 %s 的主机指纹：机器的指纹可能已经变化，请重新检测", target.Address)
	}
	return nil
}

func sshTarget(target FactorServer) setupssh.Target {
	return setupssh.Target{Name: "factor", Address: target.Address, Port: target.SSHPort, Username: target.SSHUser}
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
