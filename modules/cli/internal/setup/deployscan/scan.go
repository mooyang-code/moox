// Package deployscan 检测目标机器上的 MooX 部署情况，决定发布走哪条路线，并在用户同意后彻底清除旧部署。
//
// 检测全程只读：不建目录、不生成密钥，也不把任何密钥带回操作机。
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

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

//go:embed inspect.py
var inspectScript string

// 目标机上 Trade 的状态。
const (
	TradeNone       = "none"       // 没有 Trade 单元
	TradeClear      = "clear"      // 没有实盘账户
	TradeLive       = "live"       // 有实盘账户
	TradeUnreadable = "unreadable" // 文件存在但读不出
)

// ArchiveDir 是一个归档目录及其是否为空。
type ArchiveDir struct {
	Path  string `json:"path"`
	Empty bool   `json:"empty"`
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
	ExternalDirs     []string          `json:"external_dirs"`
	ArchiveDirs      []ArchiveDir      `json:"archive_dirs"`
	ProcessesInside  int               `json:"processes_inside"`
	ProcessesOutside []string          `json:"processes_outside"`
	PortsOccupied    []int             `json:"ports_occupied"`
	RuntimeSHA256    string            `json:"runtime_sha256,omitempty"`
	Units            map[string]string `json:"units"`
	Trade            string            `json:"trade"`
	RegisteredHosts  []RegisteredHost  `json:"registered_hosts"`
	SizesKB          map[string]int64  `json:"sizes_kb,omitempty"`
}

// Empty 报告这台主机是否干净：部署根、看护、外部服务和归档都没有残留。半清的机器不算空机。
func (r HostReport) Empty() bool {
	if len(r.Entries) != 0 || r.Cron || len(r.Services) != 0 {
		return false
	}
	for _, archive := range r.ArchiveDirs {
		if !archive.Empty {
			return false
		}
	}
	return true
}

// Options 是一次检测的参数。
type Options struct {
	// Sizes 为 true 时统计目录占用，只在清除预览里需要。
	Sizes bool
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
	reports := make([]HostReport, 0, len(hosts))
	for _, host := range hosts {
		request := map[string]any{"deploy_root": paths.DeployRoot, "archive_root": paths.ArchiveRoot, "ports": hostPorts(manifest, catalog, host.Name), "sizes": options.Sizes}
		if host.Name == control {
			request["admin_db"] = path.Join(paths.ControlRoot, "current/admin/data/admin.db")
		}
		report, err := inspect(ctx, dial, host, request)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func inspect(ctx context.Context, dial Dial, host setupconfig.Host, request map[string]any) (HostReport, error) {
	report := HostReport{Host: host.Name, Address: host.Address}
	raw, err := json.Marshal(request)
	if err != nil {
		return report, err
	}
	transport, err := dial(ctx, host)
	if err != nil {
		return report, err
	}
	defer transport.Close()
	output, err := transport.Run(ctx, []string{"python3", "-c", inspectScript, string(raw)}, nil)
	if err != nil {
		return report, fmt.Errorf("host %s: deployment inspection failed; private output omitted", host.Name)
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
