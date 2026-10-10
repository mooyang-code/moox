// Package deploy 把渲染好的发布装到主机上：构建二进制，从 control 取签名密钥与证书，同步部署记录，上传发布包并运行
// 安装器。主机上的目录布局与运行脚本见 release 包和仓库的 deploy/runtime。
package deploy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/release"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// PlacementSyncer 按部署表同步一台主机在 Admin 中的部署记录（SysDeploy.SyncHostPlacements）。
type PlacementSyncer interface {
	SyncHostPlacements(ctx context.Context, host setupconfig.Host, components []string) error
}

// Deployer 部署主机。
type Deployer struct {
	Manifest       setupconfig.Manifest
	RepositoryRoot string
	// Version 是发布版本（一般是 git 提交），写入组件的 MOOX_VERSION。
	Version string
	Dial    func(ctx context.Context, host setupconfig.Host) (setupssh.Client, error)
	Builder Builder
	// Placements 为空时不同步部署记录（首次初始化由离线 bootstrap 写入）。
	Placements PlacementSyncer
	Out        io.Writer
	// Now 用于生成发布 ID，测试时替换。
	Now func() time.Time
}

// Options 是一次部署的选项。
type Options struct {
	// Components 为空时部署主机上的全部组件（主机组件加部署表中的业务组件）。
	Components []string
	// SkipBuild 复用仓库 bin/ 中已有的二进制。
	SkipBuild bool
	// ReuseBinaries 时发布包不带二进制，全部复用当前发布（只更新配置、密钥和证书）。
	ReuseBinaries bool
	NoStart       bool
	// FirstInstall 是空环境的首次启动：依赖 setup init 元数据的组件（Collector）起不来不算安装失败。
	FirstInstall bool
	// MaintenanceLockHeld 表示调用方已持有主机的维护锁，安装器不再加锁。
	MaintenanceLockHeld bool

	// withoutCredentials 只在首次初始化的第一步使用：control 上还没有 moox-admin-cli，不取密钥。
	withoutCredentials bool
}

// Result 是一次部署的结果。
type Result struct {
	Host       string   `json:"host"`
	Release    string   `json:"release"`
	Components []string `json:"components"`
	Output     string   `json:"output"`
}

func (d *Deployer) out() io.Writer {
	if d.Out == nil {
		return os.Stderr
	}
	return d.Out
}

func (d *Deployer) logf(format string, args ...any) {
	fmt.Fprintf(d.out(), "[moox-cli] "+format+"\n", args...)
}

func (d *Deployer) releaseID() string {
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	id := now().UTC().Format("20060102T150405Z")
	if version := strings.TrimSpace(d.Version); version != "" {
		id += "-" + version
	}
	return id
}

// Plan 渲染一台主机的发布。
func (d *Deployer) Plan(hostID string) (release.Plan, error) {
	return release.Render(d.Manifest, hostID, release.Options{RepositoryRoot: d.RepositoryRoot, Version: d.Version})
}

// selectComponents 返回要部署的组件；names 为空时是全部组件。
func selectComponents(plan release.Plan, names []string) ([]release.Component, error) {
	if len(names) == 0 {
		return plan.Components, nil
	}
	wanted := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if _, ok := plan.Component(name); !ok {
			return nil, fmt.Errorf("主机 %s 上没有部署组件 %s（见 moox.toml 的 [placements]）", plan.HostID, name)
		}
		wanted[name] = true
	}
	var out []release.Component
	for _, component := range plan.Components {
		if wanted[component.ID] {
			out = append(out, component)
		}
	}
	return out, nil
}

// generatedSecrets 是安装器在主机上生成（已有时保留）的本机密钥。
func generatedSecrets(plan release.Plan) []string {
	var out []string
	if _, ok := plan.Component("admin"); ok {
		out = append(out, "health-auth.env", "storage-internal-auth.env", "admin-jwt.env", "admin-encryption-key")
	}
	for _, component := range plan.Components {
		if strings.HasPrefix(component.ID, "storage-") {
			out = append(out, "storage-node-auth.env")
			break
		}
	}
	return out
}

// detectPlatform 读取主机的操作系统与 CPU 架构。
func detectPlatform(ctx context.Context, transport setupssh.Client) (string, string, error) {
	result, err := transport.Run(ctx, []string{"uname", "-s", "-m"}, nil)
	if err != nil {
		return "", "", fmt.Errorf("读取主机平台: %w", err)
	}
	fields := strings.Fields(result.Stdout)
	if len(fields) != 2 || fields[0] != "Linux" {
		return "", "", fmt.Errorf("只支持部署到 Linux 主机，当前是 %q", strings.TrimSpace(result.Stdout))
	}
	switch fields[1] {
	case "x86_64", "amd64":
		return "linux", "amd64", nil
	case "aarch64", "arm64":
		return "linux", "arm64", nil
	default:
		return "", "", fmt.Errorf("不支持的 CPU 架构 %s", fields[1])
	}
}

// Deploy 部署一台主机：渲染发布、构建、取密钥、同步部署记录、上传并安装。
func (d *Deployer) Deploy(ctx context.Context, hostID string, opts Options) (Result, error) {
	host, ok := d.Manifest.Host(hostID)
	if !ok {
		return Result{}, fmt.Errorf("moox.toml 中没有主机 %s", hostID)
	}
	plan, err := d.Plan(hostID)
	if err != nil {
		return Result{}, err
	}
	selected, err := selectComponents(plan, opts.Components)
	if err != nil {
		return Result{}, err
	}
	ids := make([]string, 0, len(selected))
	for _, component := range selected {
		ids = append(ids, component.ID)
	}
	d.logf("部署主机 %s：%s", hostID, strings.Join(ids, "、"))
	transport, err := d.Dial(ctx, host)
	if err != nil {
		return Result{}, err
	}
	defer transport.Close()

	binaries := map[string]string{}
	if !opts.ReuseBinaries {
		goos, goarch, err := detectPlatform(ctx, transport)
		if err != nil {
			return Result{}, err
		}
		d.logf("构建 %s/%s 二进制", goos, goarch)
		if binaries, err = d.Builder.Build(ctx, BuildRequest{Components: selected, GOOS: goos, GOARCH: goarch, SkipBuild: opts.SkipBuild}); err != nil {
			return Result{}, err
		}
	}

	var incoming []release.File
	if !opts.withoutCredentials {
		if incoming, err = d.credentials(ctx, transport, host, plan, selected); err != nil {
			return Result{}, err
		}
	}
	for _, component := range plan.Components {
		if containsString(component.SecretEnv, "notification.env") {
			incoming = append(incoming, notificationFile(d.Manifest))
			break
		}
	}

	if d.Placements != nil {
		d.logf("同步主机 %s 的部署记录", hostID)
		if err := d.Placements.SyncHostPlacements(ctx, host, d.Manifest.Components(hostID)); err != nil {
			return Result{}, fmt.Errorf("同步主机 %s 的部署记录: %w", hostID, err)
		}
	}

	releaseID := d.releaseID()
	archive, err := os.CreateTemp("", "moox-release-*.tar.gz")
	if err != nil {
		return Result{}, err
	}
	defer os.Remove(archive.Name())
	if err := release.WriteArchive(archive, release.ArchiveInput{
		Plan: plan, ReleaseID: releaseID, RuntimeDir: filepath.Join(d.RepositoryRoot, "deploy", "runtime"),
		Binaries: binaries, Incoming: incoming,
	}); err != nil {
		_ = archive.Close()
		return Result{}, fmt.Errorf("打包发布: %w", err)
	}
	info, err := archive.Stat()
	if err != nil {
		_ = archive.Close()
		return Result{}, err
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		_ = archive.Close()
		return Result{}, err
	}
	remoteArchive := "/tmp/moox-release-" + releaseID + "-" + randomToken() + ".tar.gz"
	d.logf("上传发布 %s（%.1f MiB）", releaseID, float64(info.Size())/(1<<20))
	if err := transport.Upload(ctx, archive, info.Size(), remoteArchive, 0o600); err != nil {
		_ = archive.Close()
		return Result{}, fmt.Errorf("上传发布包: %w", err)
	}
	_ = archive.Close()

	args := []string{"--root", host.Root, "--archive", remoteArchive, "--release", releaseID}
	if len(opts.Components) > 0 {
		args = append(args, "--components", strings.Join(ids, ","))
	}
	if secrets := generatedSecrets(plan); len(secrets) > 0 {
		args = append(args, "--generate-secrets", strings.Join(secrets, ","))
	}
	if opts.NoStart {
		args = append(args, "--no-start")
	}
	if opts.FirstInstall {
		args = append(args, "--first-install")
	}
	if opts.MaintenanceLockHeld {
		args = append(args, "--maintenance-lock-held")
	}
	d.logf("在主机 %s 上安装", hostID)
	output, err := runInstaller(ctx, transport, remoteArchive, releaseID, args)
	fmt.Fprint(d.out(), output)
	if err != nil {
		return Result{}, err
	}
	result := Result{Host: hostID, Release: releaseID, Components: ids, Output: output}
	if !opts.NoStart && d.Manifest.HasComponent(hostID, "console-proxy") && RequiresLocalCATrust(host) {
		if err := saveConsoleProxyCA(ctx, transport, host); err != nil {
			return result, err
		}
	}
	return result, nil
}

// credentials 取这次部署需要的密钥与证书：目标是 control 时复用同一条 SSH 连接。
func (d *Deployer) credentials(ctx context.Context, transport setupssh.Client, host setupconfig.Host, plan release.Plan, selected []release.Component) ([]release.File, error) {
	request := credentialRequestFor(plan, selected)
	control := transport
	if host.ID != servicecatalog.ControlHostID {
		var err error
		if control, err = d.Dial(ctx, d.Manifest.ControlHost()); err != nil {
			return nil, err
		}
		defer control.Close()
	}
	d.logf("从 control 取主机 %s 的签名密钥与证书", host.ID)
	return fetchCredentials(ctx, control, d.Manifest.ControlHost().Root, host, request)
}

const installerCommand = `set -eu
archive="$1"
script="$2"
shift 2
tar -xzOf "$archive" install.sh >"$script"
status=0
bash "$script" "$@" || status=$?
rm -f "$archive" "$script"
exit "$status"`

// randomToken 给远端临时文件名加上随机后缀：发布编号是时间加提交哈希，可以预测；主机上有其他本地用户时，
// 可预测的 /tmp 路径可以被预先放置符号链接或抢先写入。
func randomToken() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw)
}

func runInstaller(ctx context.Context, transport setupssh.Client, archive, releaseID string, args []string) (string, error) {
	argv := append([]string{"bash", "-c", installerCommand, "moox-install", archive, "/tmp/moox-install-" + releaseID + "-" + randomToken() + ".sh"}, args...)
	result, err := transport.Run(ctx, argv, nil)
	output := result.Stdout
	if result.Stderr != "" {
		output += result.Stderr
	}
	if err != nil {
		return output, fmt.Errorf("安装失败: %w", err)
	}
	return output, nil
}

// saveConsoleProxyCA 把控制台代理（Caddy 内置 CA）的根证书保存到操作员机器。
func saveConsoleProxyCA(ctx context.Context, transport setupssh.Client, host setupconfig.Host) error {
	path := host.Root + "/data/console-proxy/caddy/pki/authorities/local/root.crt"
	result, err := transport.Run(ctx, []string{"cat", "--", path}, nil)
	if err != nil {
		return fmt.Errorf("读取控制台代理的根证书 %s: %w", path, err)
	}
	return SaveCA(host.Address, []byte(result.Stdout))
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// Rollback 把主机切回上一个发布并重启全部组件。lockHeld 表示操作员已在主机上持有维护锁（停机窗口里回滚），
// 安装器不再加锁。
func (d *Deployer) Rollback(ctx context.Context, hostID string, lockHeld bool) (string, error) {
	host, ok := d.Manifest.Host(hostID)
	if !ok {
		return "", fmt.Errorf("moox.toml 中没有主机 %s", hostID)
	}
	transport, err := d.Dial(ctx, host)
	if err != nil {
		return "", err
	}
	defer transport.Close()
	held := "0"
	if lockHeld {
		held = "1"
	}
	result, err := transport.Run(ctx, []string{"bash", "-c", `set -eu
root="$1"
[ -L "$root/current" ] || { echo "主机上还没有发布" >&2; exit 1; }
if [ "$2" = 1 ]; then
  exec bash "$root/current/install.sh" --root "$root" --rollback --maintenance-lock-held
fi
exec bash "$root/current/install.sh" --root "$root" --rollback`, "moox-rollback", host.Root, held}, nil)
	output := result.Stdout + result.Stderr
	if err != nil {
		return output, fmt.Errorf("回滚失败: %w", err)
	}
	return output, nil
}

// RunScript 在主机上执行部署根目录下的运行脚本（start、stop、restart、status、pause、resume）。lockHeld 表示操作员
// 已在主机上持有维护锁，pause、resume 不再加锁。
func (d *Deployer) RunScript(ctx context.Context, hostID, script string, lockHeld bool, args ...string) (string, error) {
	switch script {
	case "start", "stop", "restart", "status", "pause", "resume":
	default:
		return "", fmt.Errorf("不支持的运行脚本 %s", script)
	}
	host, ok := d.Manifest.Host(hostID)
	if !ok {
		return "", fmt.Errorf("moox.toml 中没有主机 %s", hostID)
	}
	transport, err := d.Dial(ctx, host)
	if err != nil {
		return "", err
	}
	defer transport.Close()
	argv := []string{host.Root + "/" + script + ".sh"}
	if lockHeld {
		argv = append([]string{"env", "MOOX_MAINTENANCE_LOCK_HELD=1"}, argv...)
	}
	argv = append(argv, args...)
	result, err := transport.Run(ctx, argv, nil)
	output := result.Stdout + result.Stderr
	if err != nil {
		return output, fmt.Errorf("在主机 %s 上执行 %s.sh 失败: %w", hostID, script, err)
	}
	return output, nil
}
