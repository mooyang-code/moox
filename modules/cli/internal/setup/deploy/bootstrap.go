package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// bootstrapSpec 与 moox-admin-cli bootstrap --spec 的格式一致。
type bootstrapSpec struct {
	Hosts []bootstrapHost `json:"hosts"`
}

type bootstrapHost struct {
	ID             string   `json:"id"`
	Address        string   `json:"address"`
	PrivateAddress string   `json:"private_address"`
	Region         string   `json:"region"`
	Components     []string `json:"components"`
}

// BootstrapSpec 是 moox.toml 部署表对应的初始化输入。
func (d *Deployer) BootstrapSpec() ([]byte, error) {
	spec := bootstrapSpec{}
	for _, host := range d.Manifest.HostList() {
		spec.Hosts = append(spec.Hosts, bootstrapHost{
			ID: host.ID, Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region,
			Components: append([]string{}, d.Manifest.Components(host.ID)...),
		})
	}
	return json.MarshalIndent(spec, "", "  ")
}

// BootstrapOptions 是首次初始化的选项。
type BootstrapOptions struct {
	SkipBuild bool
	// ControlOnly 只做前 4 步（初始化管理后台并部署 control），其他主机之后用 deploy-host 逐台部署。
	ControlOnly bool
	// MaintenanceLockHeld 表示操作员已在 control 上持有维护锁，安装器不再加锁；只能与 ControlOnly 一起使用，
	// 因为维护锁是按主机持有的。
	MaintenanceLockHeld bool
	// NewPlacements 在 control 就绪后创建部署记录的同步方式（经 SSH 隧道调用 Admin），用于部署其他主机。
	NewPlacements func(ctx context.Context) (PlacementSyncer, func(), error)
	// CLIKeyFile 是操作员机器上 moox-cli 签名密钥的位置。
	CLIKeyFile string
}

// Bootstrap 是空环境的首次部署（设计文档 3.12）：
//  1. 把 control 的发布装上，暂不启动；
//  2. 经 SSH 在 control 上离线执行 moox-admin-cli bootstrap：建表、生成 CA、按部署表写入全部主机和部署、生成全部
//     调用方密钥、为 control 签发主机网关证书；
//  3. 再次安装 control 的发布（复用二进制，带上签名密钥）并启动：管理后台、主机网关，然后是其余组件；
//  4. 把 moox-cli 的签名密钥取回操作员机器；
//  5. 依次部署其他主机，这时经 SSH 隧道调用 Admin 同步部署记录（ControlOnly 时跳过）。
//
// 可以重复执行：已有的表、CA 和密钥都会复用。
func (d *Deployer) Bootstrap(ctx context.Context, opts BootstrapOptions) ([]Result, error) {
	if opts.MaintenanceLockHeld && !opts.ControlOnly {
		return nil, fmt.Errorf("持有维护锁时只能初始化 control（维护锁按主机持有），其他主机请用 deploy-host 逐台部署")
	}
	control := d.Manifest.ControlHost()
	d.logf("初始化第 1 步：安装 control 的发布（暂不启动）")
	if _, err := d.Deploy(ctx, control.ID, Options{
		SkipBuild: opts.SkipBuild, NoStart: true, MaintenanceLockHeld: opts.MaintenanceLockHeld, withoutCredentials: true,
	}); err != nil {
		return nil, err
	}
	d.logf("初始化第 2 步：在 control 上离线初始化管理后台")
	if err := d.runAdminBootstrap(ctx); err != nil {
		return nil, err
	}
	d.logf("初始化第 3 步：启动 control")
	results := []Result{}
	result, err := d.Deploy(ctx, control.ID, Options{ReuseBinaries: true, FirstInstall: true, MaintenanceLockHeld: opts.MaintenanceLockHeld})
	if err != nil {
		return nil, err
	}
	results = append(results, result)
	d.logf("初始化第 4 步：取回 moox-cli 的签名密钥")
	if err := d.fetchCLIKey(ctx, opts.CLIKeyFile); err != nil {
		return results, err
	}
	others := d.Manifest.HostIDs()[1:]
	if len(others) == 0 || opts.ControlOnly {
		return results, nil
	}
	if opts.NewPlacements == nil {
		return results, fmt.Errorf("部署其他主机需要访问 Admin 的方式")
	}
	placements, closePlacements, err := opts.NewPlacements(ctx)
	if err != nil {
		return results, err
	}
	defer closePlacements()
	previous := d.Placements
	d.Placements = placements
	defer func() { d.Placements = previous }()
	for _, hostID := range others {
		d.logf("初始化第 5 步：部署主机 %s", hostID)
		result, err := d.Deploy(ctx, hostID, Options{SkipBuild: opts.SkipBuild})
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

func (d *Deployer) runAdminBootstrap(ctx context.Context) error {
	control := d.Manifest.ControlHost()
	spec, err := d.BootstrapSpec()
	if err != nil {
		return err
	}
	transport, err := d.Dial(ctx, control)
	if err != nil {
		return err
	}
	defer transport.Close()
	remoteSpec := "/tmp/moox-bootstrap-spec-" + d.releaseID() + "-" + randomToken() + ".json"
	if err := transport.Upload(ctx, bytes.NewReader(spec), int64(len(spec)), remoteSpec, 0o600); err != nil {
		return fmt.Errorf("上传部署表: %w", err)
	}
	result, err := transport.Run(ctx, []string{"bash", "-c", `set -eu
root="$1"
spec="$2"
trap 'rm -f "$spec"' EXIT
"$root/current/bin/moox-admin-cli" bootstrap --db-path "$root/data/admin/admin.db" \
  --encryption-key-file "$root/secrets/admin-encryption-key" --pki-dir "$root/secrets/pki" \
  --spec "$spec" --out-dir "$root"`, "moox-bootstrap", control.Root, remoteSpec}, nil)
	fmt.Fprint(d.out(), result.Stdout, result.Stderr)
	if err != nil {
		return fmt.Errorf("离线初始化管理后台失败: %w", err)
	}
	return nil
}

// fetchCLIKey 在 control 上确保 moox-cli 的签名密钥存在，并保存到操作员机器（0600）。
func (d *Deployer) fetchCLIKey(ctx context.Context, target string) error {
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("没有指定 moox-cli 签名密钥的保存位置")
	}
	if strings.HasPrefix(target, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		target = filepath.Join(home, target[2:])
	}
	control := d.Manifest.ControlHost()
	transport, err := d.Dial(ctx, control)
	if err != nil {
		return err
	}
	defer transport.Close()
	result, err := transport.Run(ctx, []string{"bash", "-c", `set -eu
root="$1"
out="$(mktemp)"
trap 'rm -f "$out"' EXIT
"$root/current/bin/moox-admin-cli" keys ensure --db-path "$root/data/admin/admin.db" \
  --encryption-key-file "$root/secrets/admin-encryption-key" --caller "$2" --out "$out" >/dev/null
cat "$out"`, "moox-cli-key", control.Root, servicecatalog.MooxCLICaller}, nil)
	if err != nil {
		return fmt.Errorf("导出 moox-cli 的签名密钥: %w: %s", err, strings.TrimSpace(result.Stderr))
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	temporary := target + ".next"
	if err := os.WriteFile(temporary, []byte(result.Stdout), 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	d.logf("moox-cli 的签名密钥已保存到 %s", target)
	return nil
}
