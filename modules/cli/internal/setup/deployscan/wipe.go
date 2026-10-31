package deployscan

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
)

//go:embed wipe.py
var wipeScript string

// wipeTimeout 覆盖停止服务、等待看护和删除大目录，SSH 本身不限制命令时长。
const wipeTimeout = 2 * time.Hour

// pendingFile 是 setup wipe 的未完成标记，放在操作员目录里，状态目录改名不影响它。
const pendingFile = "wipe-pending.json"

// 操作员目录里要留在原处的文件：没有钥匙串时 moox.toml 的密钥在 config.key，known_hosts 是已确认的指纹。
var operatorKeep = []string{"config.key", "known_hosts", pendingFile}

// HostPlan 是一台主机的清除范围。
type HostPlan struct {
	Host       string            `json:"host"`
	Address    string            `json:"address"`
	Units      map[string]string `json:"units"`
	DeployRoot string            `json:"deploy_root"`
	Entries    []string          `json:"entries"`
	Unknown    []string          `json:"unknown_entries,omitempty"`
	Dirs       []string          `json:"dirs,omitempty"`
	SizesKB    map[string]int64  `json:"sizes_kb,omitempty"`
}

// Plan 是清除预览。
type Plan struct {
	Hosts []HostPlan `json:"hosts"`
	// OtherHosts 是旧部署登记过、但这次配置没有的机器，不会被清理。
	OtherHosts []RegisteredHost `json:"other_hosts,omitempty"`
	// RegistryUnread 表示没能读取旧部署的主机登记。
	RegistryUnread bool `json:"registry_unread,omitempty"`
	// Trade 是实盘检查的结果：none、clear、live 或 unreadable。
	Trade string `json:"trade"`
	// Digest 绑定清除范围，执行时必须原样带回。
	Digest string `json:"digest"`
}

// rejectedRoots 是绝不能当作清除目标的系统目录。
var rejectedRoots = []string{"/data", "/home", "/root", "/etc", "/usr", "/var", "/opt", "/srv", "/mnt", "/media", "/tmp", "/bin", "/sbin", "/lib", "/lib64", "/boot", "/dev", "/proc", "/run", "/sys"}

// CheckDeleteTarget 是清除目录的保护：拒绝系统目录、家目录及其上级。fsutil.ValidateUnitRoots 不做这些检查。
func CheckDeleteTarget(target, home string) error {
	if !strings.HasPrefix(target, "/") || path.Clean(target) != target || target == "/" || strings.ContainsAny(target, "\x00\r\n") {
		return fmt.Errorf("delete_target_rejected: %s 不是干净的绝对路径", target)
	}
	if slices.Contains(rejectedRoots, target) {
		return fmt.Errorf("delete_target_rejected: %s 是系统目录", target)
	}
	if home == target || strings.HasPrefix(home, target+"/") {
		return fmt.Errorf("delete_target_rejected: %s 是用户家目录或它的上级目录", target)
	}
	if path.Dir(target) == "/home" {
		return fmt.Errorf("delete_target_rejected: %s 是用户家目录", target)
	}
	return nil
}

// knownEntries 是部署根下属于 MooX 的顶层条目。
func knownEntries(paths setupconfig.Paths) []string {
	known := []string{"host", "eventbus", "storage", "storage-view", "access", "egress-proxy", "trade", "collector-stream", "factor-engine", "archive-worker", "archive", "import", "bootstrap", "bootstrap-input", "identity", "run", "prod"}
	for _, root := range []string{paths.ControlRoot, paths.StorageRoot, paths.ArchiveRoot} {
		if relative, err := filepath.Rel(paths.DeployRoot, root); err == nil && !strings.HasPrefix(relative, "..") {
			known = append(known, strings.Split(relative, "/")[0])
		}
	}
	return known
}

// BuildPlan 根据检测结果生成清除范围；路径保护不通过时返回错误，不生成预览。
// pending 是上次没做完的清除：部署根可能已经没有 MooX 标记，服务文件删掉后外部目录也查不到了，要沿用当时的目录。
func BuildPlan(manifest setupconfig.Manifest, reports []HostReport, pending *PendingRecord) (Plan, error) {
	paths := manifest.Paths.Resolved()
	known := knownEntries(paths)
	plan := Plan{Trade: TradeNone, RegistryUnread: true}
	configured := map[string]bool{}
	for _, report := range reports {
		configured[report.Host] = true
	}
	for _, report := range reports {
		if err := CheckDeleteTarget(paths.DeployRoot, report.Home); err != nil {
			return plan, fmt.Errorf("host %s: %w", report.Host, err)
		}
		if len(report.Entries) != 0 && !report.Markers && pending == nil {
			return plan, fmt.Errorf("delete_target_rejected: %s 上的 %s 看不到 MooX 标记，不像部署目录", report.Host, paths.DeployRoot)
		}
		item := HostPlan{Host: report.Host, Address: report.Address, Units: report.Units, DeployRoot: paths.DeployRoot, Entries: report.Entries, SizesKB: report.SizesKB}
		for _, entry := range report.Entries {
			if !slices.Contains(known, entry) {
				item.Unknown = append(item.Unknown, entry)
			}
		}
		dirs := slices.Clone(report.ExternalDirs)
		for _, archive := range report.ArchiveDirs {
			if !archive.Empty {
				dirs = append(dirs, archive.Path)
			}
		}
		if pending != nil {
			dirs = append(dirs, pending.Dirs[report.Host]...)
		}
		for _, dir := range dirs {
			if dir == paths.DeployRoot || strings.HasPrefix(dir, paths.DeployRoot+"/") {
				continue
			}
			if err := CheckDeleteTarget(dir, report.Home); err != nil {
				return plan, fmt.Errorf("host %s: %w", report.Host, err)
			}
			item.Dirs = append(item.Dirs, dir)
		}
		slices.Sort(item.Dirs)
		item.Dirs = slices.Compact(item.Dirs)
		plan.Hosts = append(plan.Hosts, item)
		plan.Trade = worseTrade(plan.Trade, report.Trade)
		if report.RegisteredHosts != nil {
			plan.RegistryUnread = false
			for _, registered := range report.RegisteredHosts {
				if !configured[registered.ID] {
					plan.OtherHosts = append(plan.OtherHosts, registered)
				}
			}
		}
	}
	// 控制机上还没有管理数据库（空机、已清除）时，没有登记可读，不算读取失败。
	for _, report := range reports {
		if report.Host == manifest.ControlHost().Name && len(report.Units) == 0 {
			plan.RegistryUnread = false
		}
	}
	digest, err := digestOf(plan)
	if err != nil {
		return plan, err
	}
	plan.Digest = digest
	return plan, nil
}

func worseTrade(current, next string) string {
	rank := map[string]int{TradeNone: 0, TradeClear: 1, TradeUnreadable: 2, TradeLive: 3}
	if rank[next] > rank[current] {
		return next
	}
	return current
}

// digestOf 只绑定范围，不含目录占用：占用随时变化，不代表范围变了。
func digestOf(plan Plan) (string, error) {
	type scope struct {
		Host, Address string
		Units         map[string]string
		Root          string
		Entries, Dirs []string
	}
	scopes := make([]scope, 0, len(plan.Hosts))
	for _, host := range plan.Hosts {
		scopes = append(scopes, scope{host.Host, host.Address, host.Units, host.DeployRoot, host.Entries, host.Dirs})
	}
	raw, err := json.Marshal(struct {
		Scopes []scope
		Trade  string
	}{scopes, plan.Trade})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8]), nil
}

// Order 是清除顺序：先下游后上游，存储机最后，因为其他模块都往 Storage 写、EventBus 最后停。
func Order(manifest setupconfig.Manifest) []setupconfig.Host {
	control, storage := manifest.ControlHost().Name, manifest.StorageHost().Name
	rank := func(host setupconfig.Host) int {
		switch {
		case host.Name == storage:
			return 3
		case host.Name == control:
			return 2
		case slices.Contains(manifest.Deployments[host.Name], "factor-engine"), slices.Contains(manifest.Deployments[host.Name], "archive-worker"):
			return 0
		}
		return 1
	}
	hosts := slices.Clone(manifest.Hosts())
	slices.SortStableFunc(hosts, func(a, b setupconfig.Host) int { return rank(a) - rank(b) })
	return hosts
}

// HostResult 是一台主机的清除结果。
type HostResult struct {
	Host             string   `json:"host"`
	ProcessesOutside []string `json:"processes_outside,omitempty"`
}

// WipeHost 清除一台主机。目录外还有 MooX 进程时返回错误，不杀进程。
func WipeHost(ctx context.Context, dial Dial, host setupconfig.Host, root string, dirs []string) (HostResult, error) {
	result := HostResult{Host: host.Name}
	raw, err := json.Marshal(map[string]any{"deploy_root": root, "dirs": dirs})
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, wipeTimeout)
	defer cancel()
	transport, err := dial(ctx, host)
	if err != nil {
		return result, err
	}
	defer transport.Close()
	output, err := transport.Run(ctx, []string{"python3", "-c", wipeScript, string(raw)}, nil)
	if err != nil {
		return result, fmt.Errorf("wipe_interrupted: host %s 的清除中断，重新运行 setup wipe 会接着清", host.Name)
	}
	var reply struct {
		Status           string   `json:"status"`
		Reason           string   `json:"reason"`
		Path             string   `json:"path"`
		ProcessesOutside []string `json:"processes_outside"`
	}
	if err := decode(output.Stdout, &reply); err != nil {
		return result, fmt.Errorf("wipe_interrupted: host %s 返回了无效结果，重新运行 setup wipe 会接着清", host.Name)
	}
	if reply.Status != "done" {
		return result, fmt.Errorf("wipe_failed: host %s: %s %s", host.Name, reply.Reason, reply.Path)
	}
	result.ProcessesOutside = reply.ProcessesOutside
	if len(reply.ProcessesOutside) != 0 {
		return result, fmt.Errorf("wipe_outside_processes: host %s 的部署目录之外还有 MooX 进程：%v", host.Name, reply.ProcessesOutside)
	}
	return result, nil
}

// PendingRecord 是未完成标记的内容。
type PendingRecord struct {
	Digest string              `json:"digest"`
	Dirs   map[string][]string `json:"dirs"`
}

// PendingPath 返回未完成标记的路径。
func PendingPath(operatorDir string) string { return filepath.Join(operatorDir, pendingFile) }

// ReadPending 读取上次没做完的清除；没有标记时返回 nil。标记读不出来时也算没做完，只是没有目录记录。
func ReadPending(operatorDir string) *PendingRecord {
	raw, err := os.ReadFile(PendingPath(operatorDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	record := &PendingRecord{}
	if err == nil {
		_ = json.Unmarshal(raw, record)
	}
	return record
}

// MarkPending 在动手前写未完成标记。
func MarkPending(operatorDir string, plan Plan) error {
	record := PendingRecord{Digest: plan.Digest, Dirs: map[string][]string{}}
	for _, host := range plan.Hosts {
		record.Dirs[host.Host] = host.Dirs
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(operatorDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(PendingPath(operatorDir), append(raw, '\n'), 0o600)
}

// ClearPending 删除未完成标记。
func ClearPending(operatorDir string) error {
	err := os.Remove(PendingPath(operatorDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ArchiveLocal 归档这台电脑上的旧部署状态：操作员身份移到 .bak 子目录，状态目录改名。
// config.key、known_hosts 留在原处，否则 moox.toml 解不开、主机指纹要重新确认。
func ArchiveLocal(operatorDir, stateDir string, now time.Time) error {
	stamp := now.UTC().Format("20060102150405")
	entries, err := os.ReadDir(operatorDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	backup := filepath.Join(operatorDir, ".bak-"+stamp)
	for _, entry := range entries {
		if slices.Contains(operatorKeep, entry.Name()) || strings.HasPrefix(entry.Name(), ".bak-") {
			continue
		}
		if err := os.MkdirAll(backup, 0o700); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(operatorDir, entry.Name()), filepath.Join(backup, entry.Name())); err != nil {
			return err
		}
	}
	return ArchiveState(stateDir, now)
}

// ArchiveState 把状态目录改名为 .bak-<时间>，避免新发布复用旧软件。目录不存在时什么也不做。
func ArchiveState(stateDir string, now time.Time) error {
	if _, err := os.Lstat(stateDir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return os.Rename(stateDir, stateDir+".bak-"+now.UTC().Format("20060102150405"))
}
