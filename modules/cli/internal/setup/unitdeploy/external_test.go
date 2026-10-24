package unitdeploy

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	storageSecret = "storage-primary-secret-0123456789abcdef"
	callerKey     = "caller-signing-key-0123456789abcdef0123456789"
)

// scriptedSSH 是目标机的假 SSH：记录执行的命令与上传的文件，按需让某类命令失败。
type scriptedSSH struct {
	setupssh.Client
	runs     [][]string
	uploads  map[string][]byte
	failWhen func(argv []string) bool
}

func (s *scriptedSSH) Run(_ context.Context, argv []string, _ io.Reader) (setupssh.Result, error) {
	s.runs = append(s.runs, argv)
	if s.failWhen != nil && s.failWhen(argv) {
		return setupssh.Result{ExitCode: 1, Stderr: callerKey + storageSecret}, nil
	}
	switch {
	case argv[0] == "python3" && argv[3] == "prepare":
		return setupssh.Result{Stdout: "{}"}, nil
	case argv[0] == "python3" && argv[3] == "inspect":
		_, present := s.uploads[argv[5]]
		if present {
			return setupssh.Result{Stdout: `{"present": true}`}, nil
		}
		return setupssh.Result{Stdout: `{"present": false}`}, nil
	}
	return setupssh.Result{}, nil
}

func (s *scriptedSSH) Upload(_ context.Context, src io.Reader, _ int64, dst string, _ fs.FileMode) error {
	raw, err := io.ReadAll(src)
	if s.uploads == nil {
		s.uploads = map[string][]byte{}
	}
	s.uploads[dst] = raw
	return err
}

func (s *scriptedSSH) ran(substring string) bool {
	for _, argv := range s.runs {
		if strings.Contains(strings.Join(argv, " "), substring) {
			return true
		}
	}
	return false
}

// exportingControl 是 control 主机的假 SSH：外部调用方密钥导出。
type exportingControl struct {
	scriptedSSH
	caller string
}

func (c *exportingControl) Run(ctx context.Context, argv []string, stdin io.Reader) (setupssh.Result, error) {
	keyFile := "/data/moox/run/external-callers/" + c.caller + "/caller-" + c.caller + ".key"
	switch {
	case len(argv) > 3 && argv[3] == "moox-external-export":
		c.runs = append(c.runs, argv)
		return setupssh.Result{Stdout: `{"caller":"` + c.caller + `","key_id":"assigned-key-17","key_file":"` + keyFile + `"}`}, nil
	case len(argv) > 3 && argv[3] == "moox-read-private-file":
		c.runs = append(c.runs, argv)
		return setupssh.Result{Stdout: callerKey + "\n"}, nil
	}
	return c.scriptedSSH.Run(ctx, argv, stdin)
}

func externalFixture(t *testing.T, profile string, disabled bool) (externalInstall, *scriptedSSH, *exportingControl) {
	t.Helper()
	clients := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(clients, "factor-eventbus.yaml"), []byte("version: 1\nca_file: ca.pem\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(clients, "ca.pem"), []byte("CA PEM"), 0o600))
	manifest := setupconfig.Manifest{
		Paths: setupconfig.Paths{DeployRoot: "/data/moox", ControlRoot: "/data/moox/prod"},
		HostCatalog: map[string]setupconfig.HostDefinition{
			"control": {Address: "198.51.100.10"}, "storage": {Address: "198.51.100.11"}, "factor": {Address: "192.168.0.103", Reachability: "outbound"},
		},
		Deployments: map[string][]string{"control": {"admin"}, "storage": {"storage-primary", "access"}, "factor": {"factor-engine", "archive-worker"}},
	}
	target := &scriptedSSH{uploads: map[string][]byte{}}
	control := &exportingControl{scriptedSSH: scriptedSSH{uploads: map[string][]byte{}}, caller: profile}
	archive := filepath.Join(t.TempDir(), profile+".tar.gz")
	require.NoError(t, os.WriteFile(archive, []byte("package bytes"), 0o600))
	digest, err := fileSHA(archive)
	require.NoError(t, err)
	in := externalInstall{
		options:      UnitOptions{HostID: "factor", Profile: profile},
		manifest:     manifest,
		host:         manifest.HostByID("factor"),
		control:      manifest.HostByID("control"),
		identity:     runtimeIdentity{HealthAccessKey: "health-access-17", HealthSecret: strings.Repeat("h", 40), StoragePrimarySecret: storageSecret},
		operation:    unitOperation{Profile: profile, Package: unitpackage.Result{Archive: archive, SHA256: "sha256:" + digest}},
		localClients: clients,
		target:       target,
		source:       control,
		disabled:     disabled,
	}
	in.operation.Source.ExportID = profile + "-export1"
	return in, target, control
}

func TestDeployExternalInstallsFactorEngineWithCredentialsAndStartsIt(t *testing.T) {
	in, target, control := externalFixture(t, "factor-engine", false)
	result, err := deployExternal(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, "factor-engine-ready", result.Stage)
	assert.Equal(t, "/data/moox/factor-engine", result.Deployment.Directory)
	require.Len(t, result.Deployment.Components, 1)
	assert.True(t, result.Deployment.Components[0].Ready)

	stage := "/data/moox/bootstrap-input/external/factor-engine-export1"
	assert.Equal(t, []byte(callerKey+"\n"), target.uploads[stage+"/secrets/access-factor-engine.key"])
	assert.Equal(t, []byte(storageSecret+"\n"), target.uploads[stage+"/secrets/storage-primary-auth.secret"])
	assert.Contains(t, string(target.uploads[stage+"/secrets/health-auth.env"]), "MOOX_HEALTH_AUTH_ACCESS_KEY=health-access-17")
	assert.Equal(t, []byte("CA PEM"), target.uploads[stage+"/secrets/ca.pem"])
	assert.Contains(t, string(target.uploads[stage+"/secrets/factor-eventbus.yaml"]), "ca_file: ca.pem")
	assert.Contains(t, target.uploads, stage+"/factor-engine.tar.gz")

	var script []string
	for _, argv := range target.runs {
		if argv[0] == "bash" {
			script = argv
		}
	}
	require.NotEmpty(t, script, "必须运行包里的安装脚本")
	line := strings.Join(script, " ")
	for _, want := range []string{
		stage + "/software/factor-engine/scripts/deploy/deploy-factor-engine.sh",
		"--dir /data/moox/factor-engine", "--binary " + stage + "/software/bin/moox-factor-engine",
		"--health-env " + stage + "/secrets/health-auth.env",
		"--access-address 198.51.100.11:11004", "--access-id access@storage", "--access-key-id assigned-key-17",
		"--engine-id factor-engine@factor", "--eventbus-url tls://198.51.100.10:4222",
	} {
		assert.Contains(t, line, want)
	}
	assert.NotContains(t, line, "--no-start")
	assert.NotContains(t, line, callerKey, "密钥不出现在命令行")
	assert.NotContains(t, line, storageSecret, "密钥不出现在命令行")
	assert.True(t, target.ran("rm -rf -- \"$1\""), "暂存目录里的凭据与解包内容用完即删")
	assert.True(t, target.ran("is-active"), "核对服务状态")
	assert.True(t, target.ran("Linger"), "核对 linger")
	assert.True(t, control.ran("moox-external-export-cleanup"), "control 上导出的密钥副本立即删除")
}

func TestDeployExternalDisabledInstallsFilesButKeepsServiceStopped(t *testing.T) {
	in, target, _ := externalFixture(t, "factor-engine", true)
	result, err := deployExternal(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, "factor-engine-paused", result.Stage)
	assert.False(t, result.Deployment.Components[0].Ready)
	var script string
	for _, argv := range target.runs {
		if argv[0] == "bash" {
			script = strings.Join(argv, " ")
		}
	}
	assert.Contains(t, script, "--no-start")
	assert.True(t, target.ran("systemctl --user disable"), "停用状态要确保不开机自启")
	assert.False(t, target.ran("Linger"), "停用状态不要求 linger")
}

func TestDeployExternalArchiveWorkerUsesArchiveRootAndNoEventBus(t *testing.T) {
	in, target, _ := externalFixture(t, "archive-worker", false)
	in.manifest.Paths.ArchiveRoot = "/data/moox/archive"
	_, err := deployExternal(t.Context(), in)
	require.NoError(t, err)
	stage := "/data/moox/bootstrap-input/external/archive-worker-export1"
	assert.Contains(t, target.uploads, stage+"/secrets/access-archive-worker.key")
	assert.NotContains(t, target.uploads, stage+"/secrets/ca.pem", "归档 worker 不用 EventBus")
	var script string
	for _, argv := range target.runs {
		if argv[0] == "bash" {
			script = strings.Join(argv, " ")
		}
	}
	assert.Contains(t, script, "--worker-id archive-worker@factor")
	assert.Contains(t, script, "--root-dir /data/moox/archive")
	assert.NotContains(t, script, "--eventbus-url")
}

func TestDeployExternalReportsFailuresWithoutLeakingSecrets(t *testing.T) {
	in, target, _ := externalFixture(t, "factor-engine", false)
	target.failWhen = func(argv []string) bool { return argv[0] == "bash" }
	_, err := deployExternal(t.Context(), in)
	require.ErrorContains(t, err, "installation failed")
	assert.NotContains(t, err.Error(), callerKey)
	assert.NotContains(t, err.Error(), storageSecret)
	assert.True(t, target.ran("rm -rf -- \"$1\""), "失败时也要清掉暂存的凭据")

	in, target, _ = externalFixture(t, "factor-engine", false)
	target.failWhen = func(argv []string) bool { return strings.Contains(strings.Join(argv, " "), "Linger") }
	_, err = deployExternal(t.Context(), in)
	require.ErrorContains(t, err, "lingering")

	in, _, _ = externalFixture(t, "factor-engine", false)
	in.identity.StoragePrimarySecret = "short"
	_, err = deployExternal(t.Context(), in)
	require.ErrorContains(t, err, "Storage primary secret")
}

func TestExternalAccessPrefersTheStorageHost(t *testing.T) {
	manifest := setupconfig.Manifest{
		HostCatalog: map[string]setupconfig.HostDefinition{"storage": {Address: "198.51.100.11"}, "overseas": {Address: "198.51.100.12"}},
		Deployments: map[string][]string{"storage": {"storage-primary", "access"}, "overseas": {"access"}},
	}
	access, err := externalAccess(manifest)
	require.NoError(t, err)
	assert.Equal(t, "198.51.100.11:11004", access.address)
	assert.Equal(t, "access@storage", access.id)
	manifest.Deployments["storage"] = []string{"storage-primary"}
	access, err = externalAccess(manifest)
	require.NoError(t, err)
	assert.Equal(t, "access@overseas", access.id)
	delete(manifest.Deployments, "overseas")
	_, err = externalAccess(manifest)
	require.Error(t, err)
}

func TestExternalProfilesAreRecognised(t *testing.T) {
	assert.True(t, isExternalProfile("factor-engine"))
	assert.True(t, isExternalProfile("archive-worker"))
	assert.False(t, isExternalProfile("access"))
}
