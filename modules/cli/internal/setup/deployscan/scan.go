// Package deployscan 按新的部署布局检测各主机（部署根、version.json、moox-* 用户单元、部署根里的进程），
// 判定是“没装过”还是“装过”，并在用户确认后清空部署（setup wipe）。
//
// 检测只读：不建目录、不改文件，也不把任何密钥带回操作机（只读 version.json 这份公开的部署记录）。
// 目标机上的检测与清空是内嵌的 python3 脚本（只用标准库），参数以 JSON 经 SSH 的 argv 传入，不经 shell 拼接。
package deployscan

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
)

//go:embed inspect.py
var inspectScript string

//go:embed wipe.py
var wipeScript string

// inspectTimeout 限制一台主机的检测时长：统计归档目录大小要遍历文件，慢盘上可能很久，但不能一直挂住。
const inspectTimeout = 10 * time.Minute

// 路线：任一主机有 version.json 就是“装过”，否则“没装过”。
const (
	RouteFresh     = "fresh"
	RouteInstalled = "installed"
)

// Notice 是清空预览里固定的提醒：秘密文件只是初始值，管理台里后来改的、新加的密钥只在线上有。
const Notice = "管理台里改过的密钥只在线上有，清空前先在管理台导出"

// HostReport 是一台主机的检测结果。
type HostReport struct {
	Host    string `json:"host"`
	Address string `json:"address"`
	Root    string `json:"root"`
	// RootExists 表示部署根是一个目录。
	RootExists bool `json:"root_exists"`
	// Entries 是部署根下第一层条目（排序）。
	Entries []string `json:"entries"`
	// Installed 表示部署根下有 version.json，这台主机用新路径装过。
	Installed bool `json:"installed"`
	// Version 是 version.json 的原文（不超过 64 KiB）；没有或不是合法 JSON 时为空。
	Version json.RawMessage `json:"version,omitempty"`
	// Units 是 ~/.config/systemd/user 下的 moox-*.service（排序）。
	Units []string `json:"units"`
	// Active 是其中 systemctl --user is-active 为 active 的单元。
	Active []string `json:"active"`
	// Disabled 是其中 systemctl --user is-enabled 为 disabled 的单元：被用户停用的组件。
	Disabled []string `json:"disabled"`
	// Processes 是可执行文件在部署根里的进程数。
	Processes int `json:"processes"`
	// ArchiveKB、SeaweedFSKB 是 <部署根>/archive 与 <部署根>/data/seaweedfs 的占用；-1 表示不存在或没有统计（Options.Sizes 为假）。
	ArchiveKB   int64 `json:"archive_kb"`
	SeaweedFSKB int64 `json:"seaweedfs_kb"`
	// DiskFreeKB 是部署根所在盘的可用空间（部署根不存在时取最近的上级目录），-1 表示查不到。
	DiskFreeKB int64 `json:"disk_free_kb"`
	// Linger 表示部署用户开了 linger（loginctl show-user <用户> -p Linger 为 yes）；没开时用户登出后服务会被停掉。
	Linger bool `json:"linger"`
	// Journald 表示 /var/log/journal 存在（journald 持久化）。
	Journald bool `json:"journald"`
}

// Options 是一次检测的参数。
type Options struct {
	// Host 非空时只检测这一台主机。
	Host string
	// Sizes 为真时统计归档目录与 SeaweedFS 数据的大小，只在清空预览里需要。
	Sizes bool
}

// Dial 连接一台主机。
type Dial func(context.Context, topology.Host) (setupssh.Client, error)

// inspectResult 是 inspect.py 的输出：version.json 以文本带回，由这里判断是不是合法 JSON，保留原文字节。
type inspectResult struct {
	Root        string   `json:"root"`
	RootExists  bool     `json:"root_exists"`
	Entries     []string `json:"entries"`
	Installed   bool     `json:"installed"`
	VersionText string   `json:"version_text"`
	Units       []string `json:"units"`
	Active      []string `json:"active"`
	Disabled    []string `json:"disabled"`
	Processes   int      `json:"processes"`
	ArchiveKB   int64    `json:"archive_kb"`
	SeaweedFSKB int64    `json:"seaweedfs_kb"`
	DiskFreeKB  int64    `json:"disk_free_kb"`
	Linger      bool     `json:"linger"`
	Journald    bool     `json:"journald"`
}

// Scan 只读检测全部主机（并行），结果按 cfg.Hosts() 的顺序。
func Scan(ctx context.Context, cfg *topology.Config, dial Dial, options Options) ([]HostReport, error) {
	if cfg == nil || dial == nil {
		return nil, errors.New("deployment inspection requires the topology and an SSH dialer")
	}
	hosts := slices.DeleteFunc(cfg.Hosts(), func(host topology.Host) bool { return options.Host != "" && host.Name != options.Host })
	if options.Host != "" && len(hosts) == 0 {
		return nil, fmt.Errorf("moox.toml 里没有主机 %s", options.Host)
	}
	reports := make([]HostReport, len(hosts))
	errs := make([]error, len(hosts))
	var workers sync.WaitGroup
	for i, host := range hosts {
		workers.Add(1)
		go func() {
			defer workers.Done()
			reports[i], errs[i] = inspect(ctx, dial, host, cfg.Paths.DeployRoot, options.Sizes)
		}()
	}
	workers.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return reports, nil
}

func inspect(ctx context.Context, dial Dial, host topology.Host, root string, sizes bool) (HostReport, error) {
	report := HostReport{Host: host.Name, Address: host.Address, Root: root}
	request, err := json.Marshal(map[string]any{"root": root, "sizes": sizes})
	if err != nil {
		return report, err
	}
	ctx, cancel := context.WithTimeout(ctx, inspectTimeout)
	defer cancel()
	transport, err := dial(ctx, host)
	if err != nil {
		return report, err
	}
	defer transport.Close()
	output, err := transport.Run(ctx, []string{"python3", "-c", inspectScript, string(request)}, nil)
	if err != nil {
		return report, fmt.Errorf("deployment_inspection_failed: 主机 %s 检测失败或超时（要有 python3）：%s", host.Name, lastLine(output.Stderr))
	}
	var result inspectResult
	if err := decode(output.Stdout, &result); err != nil || result.Root != root {
		return report, fmt.Errorf("deployment_inspection_failed: 主机 %s 的检测结果无法解析", host.Name)
	}
	report.RootExists, report.Entries, report.Installed = result.RootExists, nonNil(result.Entries), result.Installed
	if text := []byte(result.VersionText); result.Installed && json.Valid(text) {
		report.Version = json.RawMessage(text)
	}
	report.Units, report.Active, report.Disabled, report.Processes = nonNil(result.Units), nonNil(result.Active), nonNil(result.Disabled), result.Processes
	report.ArchiveKB, report.SeaweedFSKB, report.DiskFreeKB = result.ArchiveKB, result.SeaweedFSKB, result.DiskFreeKB
	report.Linger, report.Journald = result.Linger, result.Journald
	return report, nil
}

// Route 判定路线：任一主机装过（有 version.json）就是 installed，否则 fresh。
func Route(reports []HostReport) string {
	if slices.ContainsFunc(reports, func(report HostReport) bool { return report.Installed }) {
		return RouteInstalled
	}
	return RouteFresh
}

// PlanHost 是清空范围里的一台主机。
type PlanHost struct {
	Host        string   `json:"host"`
	Root        string   `json:"root"`
	Units       []string `json:"units"`
	Processes   int      `json:"processes"`
	Entries     []string `json:"entries"`
	ArchiveKB   int64    `json:"archive_kb"`
	SeaweedFSKB int64    `json:"seaweedfs_kb"`
}

// Plan 是清空预览：Digest 是清空范围的摘要，执行时要和预览一致（--expect）。
type Plan struct {
	Digest string     `json:"digest"`
	Hosts  []PlanHost `json:"hosts"`
	Notice string     `json:"notice"`
}

// BuildPlan 只列有东西要清的主机（部署根里有条目、有 moox-* 单元或部署根里还有进程）。摘要覆盖每台的根、单元、条目与进程数，
// 不含大小与磁盘空间：它们随时间变化，不代表范围变了。
func BuildPlan(reports []HostReport) Plan {
	plan := Plan{Hosts: []PlanHost{}, Notice: Notice}
	for _, report := range reports {
		if len(report.Entries) == 0 && len(report.Units) == 0 && report.Processes == 0 {
			continue
		}
		plan.Hosts = append(plan.Hosts, PlanHost{
			Host: report.Host, Root: report.Root, Units: nonNil(report.Units), Processes: report.Processes,
			Entries: nonNil(report.Entries), ArchiveKB: report.ArchiveKB, SeaweedFSKB: report.SeaweedFSKB,
		})
	}
	type scope struct {
		Host      string   `json:"host"`
		Root      string   `json:"root"`
		Units     []string `json:"units"`
		Entries   []string `json:"entries"`
		Processes int      `json:"processes"`
	}
	scopes := make([]scope, 0, len(plan.Hosts))
	for _, host := range plan.Hosts {
		scopes = append(scopes, scope{Host: host.Host, Root: host.Root, Units: host.Units, Entries: host.Entries, Processes: host.Processes})
	}
	raw, _ := json.Marshal(scopes)
	sum := sha256.Sum256(raw)
	plan.Digest = "sha256:" + hex.EncodeToString(sum[:])
	return plan
}

// HostResult 是一台主机清空的结果：停用删除的单元、杀掉的进程数、删掉的部署根条目。
// SystemdError 不为空表示 systemctl 有操作没成功（例如没有用户实例）：进程照样杀、文件照样清。
type HostResult struct {
	Host         string   `json:"host"`
	Units        []string `json:"units"`
	Killed       int      `json:"killed"`
	Removed      []string `json:"removed"`
	SystemdError string   `json:"systemd_error,omitempty"`
}

// Wipe 清空一台主机：停用并删除全部 moox-* 用户单元，杀掉可执行文件在部署根里的进程，清空部署根下的内容（保留根目录）。
func Wipe(ctx context.Context, dial Dial, host topology.Host, root string) (HostResult, error) {
	result := HostResult{Host: host.Name}
	if dial == nil {
		return result, errors.New("deployment wipe requires an SSH dialer")
	}
	request, err := json.Marshal(map[string]string{"root": root})
	if err != nil {
		return result, err
	}
	transport, err := dial(ctx, host)
	if err != nil {
		return result, err
	}
	defer transport.Close()
	output, err := transport.Run(ctx, []string{"python3", "-c", wipeScript, string(request)}, nil)
	if err != nil {
		return result, fmt.Errorf("deployment_wipe_failed: 主机 %s 清空失败：%s", host.Name, lastLine(output.Stderr))
	}
	if err := decode(output.Stdout, &result); err != nil {
		return HostResult{Host: host.Name}, fmt.Errorf("deployment_wipe_failed: 主机 %s 的清空结果无法解析", host.Name)
	}
	result.Host, result.Units, result.Removed = host.Name, nonNil(result.Units), nonNil(result.Removed)
	return result, nil
}

func decode(raw string, out any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if !errors.Is(decoder.Decode(new(any)), io.EOF) {
		return errors.New("trailing data")
	}
	return nil
}

// lastLine 取脚本标准错误的最后一行作为原因（脚本自己的报错不含秘密），截到 200 字节。
func lastLine(stderr string) string {
	lines := bytes.Split(bytes.TrimSpace([]byte(stderr)), []byte("\n"))
	line := strings.TrimSpace(string(lines[len(lines)-1]))
	if len(line) > 200 {
		line = line[:200]
	}
	if line == "" {
		return "没有输出"
	}
	return line
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
