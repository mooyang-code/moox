// Package deployscan 检测目标机器上的 MooX 部署情况，决定发布走哪条路线，并在用户同意后彻底清除旧部署。
//
// 检测不改动部署：不建目录、不生成密钥，也不把任何密钥带回操作机（只读打开 SQLite 时可能留下 -shm、-wal 文件）。
package deployscan

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

//go:embed inspect.py
var inspectScript string

// inspectTimeout 限制一台主机的检测时长：要遍历部署目录，慢盘上可能很久，但不能一直挂住。
const inspectTimeout = 10 * time.Minute

// 目标机上 Trade 的状态。
const (
	TradeNone       = "none"       // 没有 Trade 单元
	TradeClear      = "clear"      // 没有实盘账户
	TradeLive       = "live"       // 有实盘账户
	TradeUnreadable = "unreadable" // 文件存在但读不出
)

// DirReport 是部署根之外的一个候选目录（外部程序目录或归档目录）。
// Marked 表示能看到 MooX 自己的文件；没有标记的目录不算 MooX 的，不计入残留，也绝不清除。
type DirReport struct {
	Path    string `json:"path"`
	Empty   bool   `json:"empty"`
	Marked  bool   `json:"marked"`
	Symlink bool   `json:"symlink"`
	Foreign bool   `json:"foreign"`
}

// RegisteredHost 是旧部署 t_hosts 里登记的一台主机。
type RegisteredHost struct {
	ID      string `json:"id"`
	Address string `json:"address"`
}

// HostReport 是一台主机的检测结果。
type HostReport struct {
	Host             string            `json:"host"`
	Address          string            `json:"address"`
	Home             string            `json:"home"`
	RootExists       bool              `json:"root_exists"`
	Entries          []string          `json:"entries"`
	OwnerOK          bool              `json:"owner_ok"`
	ModeOK           bool              `json:"mode_ok"`
	ParentWritable   bool              `json:"parent_writable"`
	ForeignFiles     bool              `json:"foreign_files"`
	Markers          bool              `json:"markers"`
	Cron             bool              `json:"cron"`
	Services         []string          `json:"services"`
	ExternalDirs     []DirReport       `json:"external_dirs"`
	ArchiveDirs      []DirReport       `json:"archive_dirs"`
	ProcessesInside  int               `json:"processes_inside"`
	ProcessesOutside []string          `json:"processes_outside"`
	PortsOccupied    []int             `json:"ports_occupied"`
	RuntimeSHA256    string            `json:"runtime_sha256,omitempty"`
	Units            map[string]string `json:"units"`
	Trade            string            `json:"trade"`
	RegisteredHosts  []RegisteredHost  `json:"registered_hosts"`
	SizesKB          map[string]int64  `json:"sizes_kb,omitempty"`
	// HelperRunning 表示目标机上还有部署助手在运行。
	HelperRunning bool `json:"helper_running"`
	// StagingKB 是已下载的软件包占用的空间，它们不算遗留。
	StagingKB int64 `json:"staging_kb"`
	// DiskFreeKB 是部署目录所在磁盘的可用空间，-1 表示没查到。
	DiskFreeKB int64 `json:"disk_free_kb"`
	// ReclaimableKB 是下一次部署开始时会自动清掉的空间（失败尝试的残留），只在 Options.Reclaim 时统计。
	ReclaimableKB int64 `json:"reclaimable_kb,omitempty"`
	// LastAttempt 是最近一次 bootstrap 的阶段，只有管理台机有。
	LastAttempt string `json:"last_attempt,omitempty"`
}

// Empty 报告这台主机是否干净：部署根、看护、外部服务和归档都没有残留。半清的机器不算空机。
func (r HostReport) Empty() bool {
	if len(r.Entries) != 0 || r.Cron || len(r.Services) != 0 || r.ProcessesInside != 0 {
		return false
	}
	for _, dir := range append(slices.Clone(r.ArchiveDirs), r.ExternalDirs...) {
		if dir.Marked && !dir.Empty {
			return false
		}
	}
	return true
}

// Options 是一次检测的参数。
type Options struct {
	// Sizes 为 true 时统计目录占用，只在清除预览里需要。
	Sizes bool
	// Reclaim 为 true 时统计可自动回收的空间。
	Reclaim bool
	// Host 不为空时只检测这一台主机。
	Host string
}

// Dial 连接一台主机。
type Dial func(context.Context, setupconfig.Host) (setupssh.Client, error)

// Scan 只读检测全部主机。
func Scan(ctx context.Context, manifest setupconfig.Manifest, dial Dial, options Options) ([]HostReport, error) {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	paths := manifest.Paths.Resolved()
	control := manifest.ControlHost().Name
	hosts := manifest.Hosts()
	selected := slices.DeleteFunc(slices.Clone(hosts), func(host setupconfig.Host) bool { return options.Host != "" && host.Name != options.Host })
	// 并行检测，结果保持主机顺序：清除范围的摘要依赖这个顺序。
	reports := make([]HostReport, len(selected))
	errs := make([]error, len(selected))
	var workers sync.WaitGroup
	for i, host := range selected {
		request := map[string]any{"deploy_root": paths.DeployRoot, "ports": hostPorts(manifest, catalog, host.Name), "sizes": options.Sizes, "reclaim": options.Reclaim}
		// 归档目录只在跑 archive-worker 的主机上属于 MooX，别的主机上同名目录是用户自己的。
		if slices.Contains(manifest.Deployments[host.Name], "archive-worker") {
			request["archive_root"] = paths.ArchiveRoot
		}
		if host.Name == control {
			request["admin_db"] = path.Join(paths.ControlRoot, "current/admin/data/admin.db")
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			reports[i], errs[i] = inspect(ctx, dial, host, request)
		}()
	}
	workers.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return reports, nil
}

func inspect(ctx context.Context, dial Dial, host setupconfig.Host, request map[string]any) (HostReport, error) {
	report := HostReport{Host: host.Name, Address: host.Address}
	raw, err := json.Marshal(request)
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
	output, err := transport.Run(ctx, []string{"python3", "-c", inspectScript, string(raw)}, nil)
	if err != nil {
		return report, fmt.Errorf("deployment_inspection_failed: host %s 检测失败或超时，详细输出已隐藏", host.Name)
	}
	if err := decode(output.Stdout, &report); err != nil {
		return report, fmt.Errorf("host %s: deployment inspection returned invalid metadata", host.Name)
	}
	report.Host, report.Address = host.Name, host.Address
	return report, nil
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

// hostPorts 是这台主机上的组件要用的端口。
func hostPorts(manifest setupconfig.Manifest, catalog servicecatalog.Catalog, host string) []int {
	ids := manifest.Deployments[host]
	ports := []int{}
	for _, component := range catalog.Components {
		if component.Scope == "host" || slices.Contains(ids, component.ID) {
			ports = append(ports, component.AllPorts()...)
		}
	}
	if slices.Contains(ids, "eventbus") {
		ports = append(ports, manifest.EventBus.Port)
	}
	slices.Sort(ports)
	return slices.Compact(ports)
}
