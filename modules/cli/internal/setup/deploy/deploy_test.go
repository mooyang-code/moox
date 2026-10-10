package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/release"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testManifest = `[admin]
username = "admin"
password = "admin-password"
[tencent_cloud]
secret_id = "secret-id"
secret_key = "secret-key"
[eventbus]
port = 4222
tls_enabled = true
[notification]
channel_type = "wecom"
webhook_url = ""
[hosts.control]
address = "192.0.2.10"
ssh = { username = "ubuntu", password = "control-password" }
[hosts.storage]
address = "192.0.2.20"
private_address = "10.0.0.5"
region = "ap-nanjing"
ssh = { username = "ubuntu", password = "storage-password" }
[placements]
control = ["console-proxy", "web-host", "admin", "eventbus", "monitor"]
storage = ["storage-primary", "storage-node", "storage-view", "access"]
`

func testDeployManifest(t *testing.T) setupconfig.Manifest {
	t.Helper()
	manifest, err := setupconfig.Parse([]byte(testManifest))
	require.NoError(t, err)
	return manifest
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "..", ".."))
	require.NoError(t, err)
	return root
}

// fakeHost 记录一台主机上执行过的命令和上传的文件，并按命令返回固定结果。
type fakeHost struct {
	mu       sync.Mutex
	id       string
	commands [][]string
	uploads  []string
	cliKey   string
}

func (h *fakeHost) Check(context.Context) error { return nil }
func (h *fakeHost) ForwardLocal(context.Context, string) (net.Listener, error) {
	return nil, errors.New("不支持")
}
func (h *fakeHost) Download(context.Context, string, io.Writer) (int64, error) { return 0, nil }
func (h *fakeHost) Upload(_ context.Context, src io.Reader, _ int64, dst string, mode fs.FileMode) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if mode != 0o600 {
		return errors.New("上传的文件必须是 0600")
	}
	_, _ = io.Copy(io.Discard, src)
	h.uploads = append(h.uploads, dst)
	return nil
}
func (h *fakeHost) Run(_ context.Context, argv []string, _ io.Reader) (setupssh.Result, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.commands = append(h.commands, argv)
	switch {
	case argv[0] == "uname":
		return setupssh.Result{Stdout: "Linux aarch64\n"}, nil
	case len(argv) > 3 && argv[3] == "moox-credentials":
		return setupssh.Result{Stdout: tarGzBase64(map[string]string{"secrets/caller-admin.key": "{}", "certs/moox-ca.crt": "ca"})}, nil
	case len(argv) > 3 && argv[3] == "moox-cli-key":
		return setupssh.Result{Stdout: h.cliKey}, nil
	}
	return setupssh.Result{Stdout: "ok\n"}, nil
}
func (h *fakeHost) Close() error { return nil }

// scripts 返回以 bash -c 执行的命令的名称（$0）。
func (h *fakeHost) scripts() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, argv := range h.commands {
		if len(argv) > 3 && argv[0] == "bash" && argv[1] == "-c" {
			out = append(out, argv[3])
		}
	}
	return out
}

// installerArgs 返回最后一次安装器调用的参数。
func (h *fakeHost) installerArgs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.commands) - 1; i >= 0; i-- {
		argv := h.commands[i]
		if len(argv) > 3 && argv[3] == "moox-install" {
			return argv[6:]
		}
	}
	return nil
}

type fakeBuilder struct {
	dir      string
	requests []BuildRequest
}

func (b *fakeBuilder) Build(_ context.Context, request BuildRequest) (map[string]string, error) {
	b.requests = append(b.requests, request)
	out := map[string]string{}
	for _, component := range request.Components {
		binaries := component.Binaries
		if component.Caddy {
			binaries = []string{component.Binary}
		}
		for _, binary := range binaries {
			path := filepath.Join(b.dir, binary)
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
				return nil, err
			}
			out[binary] = path
		}
	}
	return out, nil
}

type fakePlacements struct{ synced []string }

func (p *fakePlacements) SyncHostPlacements(_ context.Context, host setupconfig.Host, components []string) error {
	p.synced = append(p.synced, host.ID+":"+strings.Join(components, ","))
	return nil
}

type testDeployer struct {
	*Deployer
	hosts   map[string]*fakeHost
	builder *fakeBuilder
}

func newTestDeployer(t *testing.T) testDeployer {
	t.Helper()
	hosts := map[string]*fakeHost{"control": {id: "control"}, "storage": {id: "storage"}}
	builder := &fakeBuilder{dir: t.TempDir()}
	deployer := &Deployer{
		Manifest: testDeployManifest(t), RepositoryRoot: repositoryRoot(t), Version: "abc123",
		Dial: func(_ context.Context, host setupconfig.Host) (setupssh.Client, error) {
			return hosts[host.ID], nil
		},
		Builder: builder,
		Out:     io.Discard,
		Now:     func() time.Time { return time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC) },
	}
	return testDeployer{Deployer: deployer, hosts: hosts, builder: builder}
}

func tarGzBase64(files map[string]string) string {
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gz)
	for name, content := range files {
		_ = writer.WriteHeader(&tar.Header{Name: "./" + name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg})
		_, _ = writer.Write([]byte(content))
	}
	_ = writer.Close()
	_ = gz.Close()
	return base64.StdEncoding.EncodeToString(buffer.Bytes())
}

func TestCredentialRequestForSelectedComponents(t *testing.T) {
	d := newTestDeployer(t)
	plan, err := d.Plan("storage")
	require.NoError(t, err)
	request := credentialRequestFor(plan, plan.Components)
	assert.True(t, request.HostCertificate, "部署主机网关时重新签发证书")
	assert.True(t, request.Principals, "部署外部接入时导出外部调用方密钥集")
	assert.Contains(t, request.SharedSecrets, "health-auth.env", "其他主机从 control 复制共享密钥")
	assert.Contains(t, request.EventBusFiles, "ca.pem")
	files := map[string]bool{}
	for _, key := range request.CallerKeys {
		assert.False(t, files[key.File], "调用方密钥去重")
		files[key.File] = true
	}
	assert.True(t, files["caller-host-gateway.key"])

	view, ok := plan.Component("storage-view")
	require.True(t, ok)
	only := credentialRequestFor(plan, []release.Component{view})
	assert.False(t, only.HostCertificate, "只部署业务组件时不换发主机证书")
	assert.False(t, only.Principals)

	controlPlan, err := d.Plan("control")
	require.NoError(t, err)
	control := credentialRequestFor(controlPlan, controlPlan.Components)
	assert.Empty(t, control.SharedSecrets, "control 上的共享密钥就在本机")
	assert.Empty(t, control.EventBusFiles)
}

func TestCredentialScriptQuotesHostAndIssuesForEveryAddress(t *testing.T) {
	host := setupconfig.Host{ID: "storage", Address: "192.0.2.20", PrivateAddress: "10.0.0.5"}
	script := credentialScript(host, credentialRequest{
		CallerKeys:      []release.CallerKey{{Identity: "storage-view", File: "caller-storage-view.key"}},
		HostCertificate: true, Principals: true,
		SharedSecrets: []string{"health-auth.env"}, EventBusFiles: []string{"ca.pem"},
	})
	assert.Contains(t, script, `--caller 'storage-view' --out "$out/secrets/caller-storage-view.key"`)
	assert.Contains(t, script, `pki issue --pki-dir "$pki" --host 'storage' --address '192.0.2.20' --address '10.0.0.5'`)
	assert.Contains(t, script, "keys export-principals")
	assert.Contains(t, script, `copy "$root/secrets/health-auth.env"`)
	assert.Contains(t, script, `copy "$root/secrets/eventbus/ca.pem"`)
	assert.Equal(t, `'it'\''s'`, shellQuote("it's"))
}

func TestUntarFilesAcceptsOnlySecretsAndCerts(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(tarGzBase64(map[string]string{"secrets/a.key": "a", "certs/moox-ca.crt": "ca"}))
	require.NoError(t, err)
	files, err := untarFiles(raw)
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Equal(t, "certs/moox-ca.crt", files[0].Path)
	assert.Equal(t, fs.FileMode(0o600), files[1].Mode)

	for _, name := range []string{"../etc/passwd", "bin/moox-admin", "secrets/../../x"} {
		raw, err := base64.StdEncoding.DecodeString(tarGzBase64(map[string]string{name: "x"}))
		require.NoError(t, err)
		_, err = untarFiles(raw)
		require.ErrorContains(t, err, "路径无效", name)
	}
}

func TestGeneratedSecretsFollowComponents(t *testing.T) {
	d := newTestDeployer(t)
	control, err := d.Plan("control")
	require.NoError(t, err)
	assert.Equal(t, []string{"health-auth.env", "storage-internal-auth.env", "admin-jwt.env", "admin-encryption-key"}, generatedSecrets(control))
	storage, err := d.Plan("storage")
	require.NoError(t, err)
	assert.Equal(t, []string{"storage-node-auth.env"}, generatedSecrets(storage))
}

func TestSelectComponentsRejectsComponentsNotOnHost(t *testing.T) {
	d := newTestDeployer(t)
	plan, err := d.Plan("storage")
	require.NoError(t, err)
	selected, err := selectComponents(plan, []string{"access", "storage-view"})
	require.NoError(t, err)
	assert.Equal(t, []string{"storage-view", "access"}, []string{selected[0].ID, selected[1].ID}, "按启动顺序")
	_, err = selectComponents(plan, []string{"admin"})
	require.ErrorContains(t, err, "主机 storage 上没有部署组件 admin")
}

func TestDeployHostBuildsSyncsUploadsAndInstalls(t *testing.T) {
	d := newTestDeployer(t)
	placements := &fakePlacements{}
	d.Placements = placements
	result, err := d.Deploy(context.Background(), "storage", Options{Components: []string{"storage-view"}, MaintenanceLockHeld: true})
	require.NoError(t, err)
	assert.Equal(t, "20261009T080000Z-abc123", result.Release)
	assert.Equal(t, []string{"storage-view"}, result.Components)
	require.Len(t, d.builder.requests, 1)
	assert.Equal(t, "arm64", d.builder.requests[0].GOARCH, "按主机的 CPU 架构构建")
	assert.Equal(t, []string{"storage:storage-primary,storage-node,storage-view,access"}, placements.synced, "同步主机的完整组件列表")
	assert.Equal(t, []string{"moox-credentials"}, d.hosts["control"].scripts(), "密钥与证书从 control 取")
	assert.Equal(t, []string{"/tmp/moox-release-20261009T080000Z-abc123.tar.gz"}, d.hosts["storage"].uploads)
	assert.Equal(t, []string{
		"--root", "/data/moox/storage", "--archive", "/tmp/moox-release-20261009T080000Z-abc123.tar.gz",
		"--release", "20261009T080000Z-abc123", "--components", "storage-view",
		"--generate-secrets", "storage-node-auth.env", "--maintenance-lock-held",
	}, d.hosts["storage"].installerArgs())
}

func TestDeployReuseBinariesSkipsBuildAndPlatform(t *testing.T) {
	d := newTestDeployer(t)
	_, err := d.Deploy(context.Background(), "control", Options{ReuseBinaries: true, NoStart: true})
	require.NoError(t, err)
	assert.Empty(t, d.builder.requests)
	for _, argv := range d.hosts["control"].commands {
		assert.NotEqual(t, "uname", argv[0])
	}
	args := d.hosts["control"].installerArgs()
	assert.Contains(t, args, "--no-start")
	assert.NotContains(t, args, "--components")
	assert.Contains(t, d.hosts["control"].scripts(), "moox-credentials", "目标是 control 时复用同一条连接取密钥")
}

func TestBootstrapRunsFiveStepsInOrder(t *testing.T) {
	d := newTestDeployer(t)
	d.hosts["control"].cliKey = `{"caller":"moox-cli"}`
	keyFile := filepath.Join(t.TempDir(), "keys", "caller-moox-cli.key")
	placements := &fakePlacements{}
	opened := 0
	results, err := d.Bootstrap(context.Background(), BootstrapOptions{
		CLIKeyFile: keyFile,
		NewPlacements: func(context.Context) (PlacementSyncer, func(), error) {
			opened++
			return placements, func() {}, nil
		},
	})
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, []string{"control", "storage"}, []string{results[0].Host, results[1].Host})
	assert.Equal(t, []string{"moox-install", "moox-bootstrap", "moox-credentials", "moox-install", "moox-cli-key", "moox-credentials"},
		d.hosts["control"].scripts(), "安装（不启动）→ 离线初始化 → 取密钥并启动 → 取回 CLI 密钥 → 为 storage 导出密钥")
	assert.Len(t, d.builder.requests, 2, "control 第二次安装复用二进制，storage 单独构建")
	raw, err := os.ReadFile(keyFile)
	require.NoError(t, err)
	assert.Equal(t, `{"caller":"moox-cli"}`, string(raw))
	info, err := os.Stat(keyFile)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o600), info.Mode().Perm())
	assert.Equal(t, 1, opened)
	assert.Equal(t, []string{"storage:storage-primary,storage-node,storage-view,access"}, placements.synced, "control 由离线初始化写入，不经 Admin 同步")
	assert.Nil(t, d.Placements, "初始化结束后恢复原来的同步方式")
}

func TestBootstrapControlOnlyKeepsMaintenanceLock(t *testing.T) {
	d := newTestDeployer(t)
	d.hosts["control"].cliKey = `{"caller":"moox-cli"}`
	opened := 0
	results, err := d.Bootstrap(context.Background(), BootstrapOptions{
		ControlOnly: true, MaintenanceLockHeld: true,
		CLIKeyFile: filepath.Join(t.TempDir(), "caller-moox-cli.key"),
		NewPlacements: func(context.Context) (PlacementSyncer, func(), error) {
			opened++
			return &fakePlacements{}, func() {}, nil
		},
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "control", results[0].Host)
	assert.Zero(t, opened, "只初始化 control 时不经 Admin 同步其他主机")
	assert.Empty(t, d.hosts["storage"].commands, "不部署其他主机")
	installs := 0
	for _, argv := range d.hosts["control"].commands {
		if len(argv) > 3 && argv[3] == "moox-install" {
			installs++
			assert.Contains(t, argv, "--maintenance-lock-held", "两次安装都不能再加锁")
		}
	}
	assert.Equal(t, 2, installs)

	_, err = d.Bootstrap(context.Background(), BootstrapOptions{MaintenanceLockHeld: true})
	require.ErrorContains(t, err, "只能初始化 control")
}

func TestBootstrapSpecListsEveryHost(t *testing.T) {
	d := newTestDeployer(t)
	raw, err := d.BootstrapSpec()
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"id": "storage"`)
	assert.Contains(t, string(raw), `"private_address": "10.0.0.5"`)
	assert.Contains(t, string(raw), `"access"`)
	assert.NotContains(t, string(raw), "password")
}

func TestRunScriptPassesMaintenanceLockAndRejectsUnknownScripts(t *testing.T) {
	d := newTestDeployer(t)
	_, err := d.RunScript(context.Background(), "control", "pause", true, "collector")
	require.NoError(t, err)
	assert.Equal(t, []string{"env", "MOOX_MAINTENANCE_LOCK_HELD=1", "/data/moox/control/pause.sh", "collector"}, d.hosts["control"].commands[0])
	_, err = d.RunScript(context.Background(), "control", "status", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"/data/moox/control/status.sh"}, d.hosts["control"].commands[1])
	_, err = d.RunScript(context.Background(), "control", "install", false)
	require.ErrorContains(t, err, "不支持的运行脚本")
	_, err = d.Rollback(context.Background(), "storage")
	require.NoError(t, err)
	assert.Equal(t, []string{"moox-rollback"}, d.hosts["storage"].scripts())
}
