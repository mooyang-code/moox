package unitdeploy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitinstall"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
)

// 外部程序（因子引擎、归档 worker）不由 moox-runtime 管理，而是 systemd 用户服务：
// 软件包随版本制品交付，凭据由 CLI 准备并经 SSH 上传，再运行包里的安装脚本。

type externalProgram struct {
	caller   string // 外部调用方身份（Access 签名）
	keyFile  string // 签名密钥在 secrets 目录里的文件名
	script   string // 安装脚本在软件包里的位置
	unit     string // systemd 用户服务名
	eventBus string // 需要的 EventBus 角色；空表示不用 EventBus
}

var externalPrograms = map[string]externalProgram{
	"factor-engine": {
		caller: "factor-engine", keyFile: "access-factor-engine.key", unit: "moox-factor-engine.service", eventBus: "factor-eventbus",
		script: "factor-engine/scripts/deploy/deploy-factor-engine.sh",
	},
	"archive-worker": {
		caller: "archive-worker", keyFile: "access-archive-worker.key", unit: "moox-archive-worker.service",
		script: "archive-worker/scripts/deploy/deploy-archive-worker.sh",
	},
}

func isExternalProfile(profile string) bool {
	_, ok := externalPrograms[profile]
	return ok
}

// externalInstall 是安装外部程序需要的上下文：前半段（状态、软件包、EventBus 凭据导出）与其他单元共用。
type externalInstall struct {
	options      UnitOptions
	manifest     setupconfig.Manifest
	host         setupconfig.Host
	control      setupconfig.Host
	identity     runtimeIdentity
	operation    unitOperation
	localClients string
	target       setupssh.Client
	source       setupssh.Client
	// disabled 为 true 表示主机或该部署在 Admin 里已停用：只安装文件，不启动也不开机自启。
	disabled bool
}

func deployExternal(ctx context.Context, in externalInstall) (UnitResult, error) {
	var result UnitResult
	program := externalPrograms[in.options.Profile]
	profile := in.options.Profile
	deployRoot := in.manifest.Paths.Resolved().DeployRoot
	installDir := path.Join(deployRoot, profile)
	stage := path.Join(deployRoot, "bootstrap-input/external", in.operation.Source.ExportID)

	access, err := externalAccess(in.manifest)
	if err != nil {
		return result, err
	}
	credentials, err := ExportExternalCaller(ctx, in.source, in.manifest.Paths.Resolved().ControlRoot, deployRoot, program.caller)
	if err != nil {
		return result, err
	}
	if len(in.identity.StoragePrimarySecret) < 32 {
		return result, errors.New("external program deployment requires the original Storage primary secret")
	}
	secrets := map[string][]byte{
		program.keyFile:               []byte(credentials.Secret + "\n"),
		"storage-primary-auth.secret": []byte(in.identity.StoragePrimarySecret + "\n"),
		"health-auth.env":             []byte(healthEnvironment(in.identity)),
	}
	if program.eventBus != "" {
		for _, name := range []string{program.eventBus + ".yaml", "ca.pem"} {
			raw, err := os.ReadFile(filepath.Join(in.localClients, name))
			if err != nil {
				return result, errors.New("EventBus client credential for the external program is missing")
			}
			secrets[name] = raw
		}
	}
	defer func() {
		for _, raw := range secrets {
			clear(raw)
		}
	}()

	if err := prepareTarget(ctx, in.target, deployRoot); err != nil {
		return result, err
	}
	// 无论成败都清掉暂存目录里的凭据与解包内容。
	defer func() {
		_, _ = in.target.Run(ctx, []string{"sh", "-c", `rm -rf -- "$1"`, "moox-external-cleanup", stage}, nil)
	}()
	archive := path.Join(stage, profile+".tar.gz")
	if err := uploadFile(ctx, in.target, deployRoot, in.operation.Package.Archive, archive, strings.TrimPrefix(in.operation.Package.SHA256, "sha256:"), 0o600); err != nil {
		return result, err
	}
	extracted := path.Join(stage, "software")
	if err := runExternal(ctx, in.target, "unpack the verified software package", `set -eu
cd "$1"
echo "$2  $3" | sha256sum -c - >/dev/null
rm -rf "$4"
mkdir -m 0700 "$4"
tar -xzf "$3" -C "$4" --no-same-owner --no-same-permissions`, stage, strings.TrimPrefix(in.operation.Package.SHA256, "sha256:"), archive, extracted); err != nil {
		return result, err
	}
	for name, raw := range secrets {
		if err := uploadBytes(ctx, in.target, deployRoot, raw, path.Join(stage, "secrets", name), 0o600); err != nil {
			return result, err
		}
	}
	// 凭据放到安装目录自己的 secrets 目录（脚本要求它们已经在那里）；健康凭据由脚本经 --health-env 安装。
	installSecrets := path.Join(installDir, "secrets")
	moves := []string{}
	for name := range secrets {
		if name != "health-auth.env" {
			moves = append(moves, name)
		}
	}
	slices.Sort(moves)
	if err := runExternal(ctx, in.target, "place the external program credentials", `set -eu
umask 077
stage="$1"; secrets="$2"; shift 2
mkdir -p "$secrets"
chmod 0700 "$secrets"
for name in "$@"; do
  install -m 0600 "$stage/secrets/$name" "$secrets/$name"
done`, append([]string{stage, installSecrets}, moves...)...); err != nil {
		return result, err
	}

	arguments := []string{
		"bash", path.Join(extracted, program.script),
		"--dir", installDir,
		"--binary", path.Join(extracted, "bin/moox-"+profile),
		"--health-env", path.Join(stage, "secrets/health-auth.env"),
		"--access-address", access.address, "--access-id", access.id, "--access-key-id", credentials.KeyID,
	}
	switch profile {
	case "factor-engine":
		arguments = append(arguments, "--engine-id", "factor-engine@"+in.host.Name, "--eventbus-url", in.manifest.EventBusURLFor(in.host.Name))
	case "archive-worker":
		arguments = append(arguments, "--worker-id", "archive-worker@"+in.host.Name, "--root-dir", in.manifest.Paths.Resolved().ArchiveRoot)
	}
	if in.disabled {
		arguments = append(arguments, "--no-start")
	}
	output, err := in.target.Run(ctx, arguments, nil)
	if err != nil || output.ExitCode != 0 {
		return result, errors.New("external program installation failed; retry the same deployment; private output omitted" + helperDiagnostic(err, output))
	}
	if err := verifyExternal(ctx, in.target, program.unit, in.disabled); err != nil {
		return result, err
	}
	state, stage2 := "running", profile+"-ready"
	if in.disabled {
		state, stage2 = "paused", profile+"-paused"
	}
	result.Stage = stage2
	result.Deployment = unitinstall.Deployment{
		Version: 1, HostID: in.host.Name, Directory: installDir, Phase: "complete",
		Components: []unitruntime.Status{{ID: profile, State: state, Ready: !in.disabled}},
	}
	return result, nil
}

// verifyExternal 核对 systemd 用户服务的实际状态和 linger：启用时必须 enabled 且 active，停用时必须 disabled 且 inactive。
func verifyExternal(ctx context.Context, target setupssh.Client, unit string, disabled bool) error {
	if disabled {
		// 停用状态下安装脚本不会启动服务；这里确保它也不会开机自启、不在运行。
		if err := runExternal(ctx, target, "keep the disabled external program stopped", externalStopScript, unit); err != nil {
			return errors.New("disabled external program is still enabled or running")
		}
		return nil
	}
	if err := runExternal(ctx, target, "verify the external program service", `[ "$(systemctl --user is-enabled "$1" 2>/dev/null)" = enabled ] && [ "$(systemctl --user is-active "$1" 2>/dev/null)" = active ]`, unit); err != nil {
		return errors.New("external program service is not enabled and active")
	}
	if err := runExternal(ctx, target, "verify user lingering", `[ "$(loginctl show-user "$(id -un)" -p Linger --value 2>/dev/null)" = yes ]`); err != nil {
		return errors.New("user lingering is off, the service would stop at logout: run loginctl enable-linger <user> on the target and deploy again")
	}
	return nil
}

func runExternal(ctx context.Context, target setupssh.Client, what, script string, args ...string) error {
	output, err := target.Run(ctx, append([]string{"sh", "-c", script, "moox-external"}, args...), nil)
	if err != nil || output.ExitCode != 0 {
		return fmt.Errorf("could not %s on the target", what)
	}
	return nil
}

type externalAccessTarget struct{ address, id string }

// externalAccess 取外部程序使用的 Access：Access 可以部署在多台主机上，优先用与 Storage primary 同一台的那个，
// 没有时取主机 ID 排序最前的一台。外部程序只经 Access 访问系统，不经主机网关路由。
func externalAccess(manifest setupconfig.Manifest) (externalAccessTarget, error) {
	var hosts []string
	for id, components := range manifest.Deployments {
		if slices.Contains(components, "access") {
			hosts = append(hosts, id)
		}
	}
	if len(hosts) == 0 {
		return externalAccessTarget{}, errors.New("external programs require the access component to be deployed on a host")
	}
	slices.Sort(hosts)
	chosen := hosts[0]
	if storage := manifest.ComponentHost("storage-primary"); storage != "" && slices.Contains(hosts, storage) {
		chosen = storage
	}
	host := manifest.HostByID(chosen)
	return externalAccessTarget{address: net.JoinHostPort(host.Address, "11004"), id: "access@" + host.Name}, nil
}

func healthEnvironment(identity runtimeIdentity) string {
	return "MOOX_HEALTH_AUTH_VERSION=moox-health-v1\n" +
		"MOOX_HEALTH_AUTH_ACCESS_KEY=" + identity.HealthAccessKey + "\n" +
		"MOOX_HEALTH_AUTH_SECRET_KEY=" + identity.HealthSecret + "\n"
}
