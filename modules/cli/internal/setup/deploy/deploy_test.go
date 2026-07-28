package deploy

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/stretchr/testify/require"
)

func TestControlOrdersSafeDeployment(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archive := filepath.Join(dir, "control.tar.gz")
	require.NoError(t, os.WriteFile(archive, []byte("control-package"), 0o600))

	events := []string{}
	packager := &fakePackager{path: archive, events: &events}
	transport := &fakeTransport{events: &events}
	probe := &fakeProbe{events: &events}
	opts := Options{
		RepositoryRoot: dir, PublicHost: "203.0.113.8", BrowserPort: 9527, TargetGOOS: "linux", TargetGOARCH: "amd64",
		EventBusPublicAddress: "eventbus.example.test", EventBusPort: 4222, EventBusTLSEnabled: true,
	}

	err := Control(context.Background(), transport, opts, Dependencies{Packager: packager, Probe: probe, CAStore: &fakeCAStore{events: &events}})
	require.NoError(t, err)
	require.Equal(t, []string{
		"package", "upload", "install", "start", "admin_ready", "setup_ready",
		"gateway_ready", "eventbus_ready", "cloudnode_ready", "collector_ready",
		"monitor_ready", "web_ready", "browser_https_ready", "ca", "finalize", "cleanup",
	}, events)
	require.Equal(t, remoteArchiveNext, transport.uploadPath)
	require.Equal(t, fs.FileMode(0o600), transport.uploadMode)
	require.Equal(t, "control-package", transport.uploaded.String())
	require.Contains(t, strings.Join(transport.commands[0], " "), "sh -lc")

	captured := packager.captured + transport.uploaded.String()
	for _, command := range transport.commands {
		captured += strings.Join(command, " ")
	}
	for _, secret := range []string{"admin-secret", "ssh-secret", "AKID-secret", "cloud-secret"} {
		require.NotContains(t, captured, secret)
	}
}

func TestControlRollsBackWhenEventServiceIsNotReady(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "control.tar.gz")
	require.NoError(t, os.WriteFile(archive, []byte("package"), 0o600))
	events := []string{}
	transport := &fakeTransport{events: &events}
	err := Control(context.Background(), transport, Options{
		RepositoryRoot: t.TempDir(), PublicHost: "control.example.test", BrowserPort: 9527,
		TargetGOOS: "linux", TargetGOARCH: "amd64",
		EventBusPublicAddress: "eventbus.example.test", EventBusPort: 4222, EventBusTLSEnabled: true,
	}, Dependencies{
		Packager: &fakePackager{path: archive, events: &events},
		Probe:    &fakeProbe{events: &events, failAt: CloudNodeReady},
		CAStore:  &fakeCAStore{events: &events},
	})
	require.EqualError(t, err, "control_deploy_not_ready")
	require.Contains(t, events, "eventbus_ready")
	require.Contains(t, events, "cloudnode_ready")
	require.NotContains(t, events, "collector_ready")
	require.Contains(t, events, "rollback")
}

func TestControlPassesResetDataAsBoundedPositionalFlag(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "control.tar.gz")
	require.NoError(t, os.WriteFile(archive, []byte("package"), 0o600))
	events := []string{}
	transport := &fakeTransport{events: &events}
	err := Control(context.Background(), transport, Options{
		RepositoryRoot: t.TempDir(), PublicHost: "control.example.test", BrowserPort: 9527,
		TargetGOOS: "linux", TargetGOARCH: "amd64", ResetControlData: true,
		EventBusPublicAddress: "eventbus.example.test", EventBusPort: 4222, EventBusTLSEnabled: true,
	}, Dependencies{
		Packager: &fakePackager{path: archive, events: &events},
		Probe:    &fakeProbe{events: &events},
		CAStore:  &fakeCAStore{events: &events},
	})
	require.NoError(t, err)
	var install []string
	for _, command := range transport.commands {
		if len(command) >= 5 && strings.Contains(command[2], "install_control") {
			install = command
		}
	}
	require.NotEmpty(t, install)
	require.Equal(t, "1", install[len(install)-1], "reset is a bounded positional flag, not shell text")
}

func TestEventBusCommandEnvPreservesBaseAndAddsEndpoint(t *testing.T) {
	env, err := eventBusCommandEnv([]string{"PATH=/bin", "HOME=/tmp/home"}, Options{
		EventBusPublicAddress: "eventbus.example.test",
		EventBusPort:          4333,
		EventBusTLSEnabled:    true,
	})
	require.NoError(t, err)
	require.Contains(t, env, "PATH=/bin")
	require.Contains(t, env, "HOME=/tmp/home")
	require.Contains(t, env, "MOOX_EVENTBUS_ENABLE_TLS=1")
	require.Contains(t, env, "MOOX_EVENTBUS_PUBLIC_IP=eventbus.example.test")
	require.Contains(t, env, "MOOX_EVENTBUS_PORT=4333")
}

func TestMonitoringCommandEnvOverridesAmbientWebhook(t *testing.T) {
	env := monitoringCommandEnv([]string{
		"MOOX_MSGBOX_WECOM_WEBHOOK=https://example.test/ambient",
		"PATH=/bin",
	}, "")
	require.NotContains(t, env, "MOOX_MSGBOX_WECOM_WEBHOOK=https://example.test/ambient")
	require.Contains(t, env, "MOOX_MSGBOX_WECOM_WEBHOOK=")
	require.Contains(t, env, "PATH=/bin")
}

func TestEventBusCommandEnvRejectsIncompleteEndpoint(t *testing.T) {
	_, err := eventBusCommandEnv(nil, Options{EventBusPublicAddress: "eventbus.example.test", EventBusPort: 4222})
	require.EqualError(t, err, "control_deploy_invalid")
}

func TestCommandPackagerPassesMonitoringWebhook(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "scripts"), 0o700))
	script := `#!/bin/sh
set -eu
test "$MOOX_MSGBOX_WECOM_WEBHOOK" = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test"
while [ "$#" -gt 0 ]; do
  if [ "$1" = --archive ]; then printf package >"$2"; exit 0; fi
  shift
done
exit 2
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "deploy-moox.sh"), []byte(script), 0o700))

	archive, err := (CommandPackager{}).Package(context.Background(), Options{
		RepositoryRoot: root, PublicHost: "203.0.113.9", TargetGOOS: "linux", TargetGOARCH: "amd64",
		EventBusPublicAddress: "eventbus.example.test", EventBusPort: 4222, EventBusTLSEnabled: true,
		MonitoringWeComWebhook: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test",
	})
	require.NoError(t, err)
	defer os.Remove(archive)
	require.Equal(t, "package", string(requireFile(t, archive)))
}

func TestStorageDeploysAllComponentsAsOneUnit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archive := filepath.Join(dir, "storage.tar.gz")
	require.NoError(t, os.WriteFile(archive, []byte("storage-package"), 0o600))
	events := []string{}
	transport := &fakeTransport{events: &events}
	opts := Options{RepositoryRoot: dir, PublicHost: "203.0.113.9", TargetGOOS: "linux", TargetGOARCH: "amd64"}

	err := Storage(context.Background(), transport, opts, Dependencies{
		Packager: &fakePackager{path: archive, events: &events}, Probe: &fakeProbe{events: &events},
	})
	require.NoError(t, err)
	require.Equal(t, []string{
		"package", "upload", "install_storage", "storage_primary_ready", "storage_view_ready", "finalize_storage", "cleanup",
	}, events)
	require.Equal(t, remoteStorageArchiveNext, transport.uploadPath)
}

func TestStoragePackagerUsesCompileHostBuildForLinuxCrossBuild(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("a native Linux host does not need compile_host")
	}
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "scripts"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bin"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "custom.toml"), []byte("placeholder"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "bin", "moox-cli"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "build-storage-linux.sh"), []byte("#!/bin/sh\nset -eu\n: \"${MOOX_CLI:?}\"\n: \"${CONFIG:?}\"\ntouch ./compiled\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "deploy-moox.sh"), []byte("#!/bin/sh\nset -eu\ntest -f ./compiled\ntest \"$MOOX_EVENTBUS_ENABLE_TLS\" = 1\ntest \"$MOOX_EVENTBUS_PUBLIC_IP\" = eventbus.example.test\ntest \"$MOOX_EVENTBUS_PORT\" = 4222\ncase \" $* \" in *' --skip-build '*) ;; *) exit 2 ;; esac\ncase \" $* \" in *' --no-gateway '*) ;; *) exit 4 ;; esac\nwhile [ \"$#\" -gt 0 ]; do\n  if [ \"$1\" = --archive ]; then printf package >\"$2\"; exit 0; fi\n  shift\ndone\nexit 3\n"), 0o700))

	archive, err := (StoragePackager{}).Package(context.Background(), Options{
		RepositoryRoot: root, PublicHost: "203.0.113.9", TargetGOOS: "linux", TargetGOARCH: "amd64",
		UseControlGateway: true, EventBusPublicAddress: "eventbus.example.test",
		EventBusPort: 4222, EventBusTLSEnabled: true,
	})
	require.NoError(t, err)
	defer os.Remove(archive)
	require.Equal(t, "package", string(requireFile(t, archive)))
}

func TestStoragePassesResetStorageDataAsBoundedPositionalFlag(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "storage.tar.gz")
	require.NoError(t, os.WriteFile(archive, []byte("storage-package"), 0o600))
	events := []string{}
	transport := &fakeTransport{events: &events}
	err := Storage(context.Background(), transport, Options{
		RepositoryRoot: t.TempDir(), PublicHost: "203.0.113.9", TargetGOOS: "linux", TargetGOARCH: "amd64", ResetStorageData: true,
	}, Dependencies{Packager: &fakePackager{path: archive, events: &events}, Probe: &fakeProbe{events: &events}})
	require.NoError(t, err)
	var install []string
	for _, command := range transport.commands {
		if len(command) >= 5 && strings.Contains(command[2], "install_storage") {
			install = command
		}
	}
	require.NotEmpty(t, install)
	require.Equal(t, "1", install[len(install)-2], "reset is a bounded positional flag, not shell text")
	require.Equal(t, "0", install[len(install)-1])
}

func TestStorageInstallerResetPreservesSecretsButDropsData(t *testing.T) {
	home := t.TempDir()
	deploy := filepath.Join(home, "moox", "storage")
	require.NoError(t, os.MkdirAll(filepath.Join(deploy, "data"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(deploy, "secrets"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "data", "old.db"), []byte("old"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "secrets", "auth.env"), []byte("secret"), 0o600))
	archiveDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(archiveDir, "data"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(archiveDir, "secrets"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "data", "new.db"), []byte("new"), 0o600))
	start := filepath.Join(archiveDir, "start.sh")
	require.NoError(t, os.WriteFile(start, []byte("#!/bin/sh\nset -eu\ntest -s \"$(dirname \"$0\")/secrets/health-auth.env\"\n"), 0o700))
	archive := filepath.Join(t.TempDir(), "storage.tar.gz")
	command := exec.Command("tar", "-C", archiveDir, "-czf", archive, ".")
	require.NoError(t, command.Run())
	previousArchive := remoteStorageArchiveNext
	defer os.Remove(previousArchive)
	require.NoError(t, copyFileForTest(archive, previousArchive))
	cmd := exec.Command("bash", "-c", installStorageScript, "moox-install-storage", "1", "0")
	cmd.Env = append(os.Environ(), "HOME="+home)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	_, err = os.Stat(filepath.Join(deploy, "data", "old.db"))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.FileExists(t, filepath.Join(deploy, "data", "new.db"))
	require.Equal(t, "secret", string(requireFile(t, filepath.Join(deploy, "secrets", "auth.env"))))
}

func TestStorageInstallerDefaultPreservesExistingData(t *testing.T) {
	home := t.TempDir()
	deploy := filepath.Join(home, "moox", "storage")
	require.NoError(t, os.MkdirAll(filepath.Join(deploy, "data"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "data", "old.db"), []byte("old"), 0o600))
	archiveDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(archiveDir, "data"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "data", "new.db"), []byte("new"), 0o600))
	start := filepath.Join(archiveDir, "start.sh")
	require.NoError(t, os.WriteFile(start, []byte("#!/bin/sh\nexit 0\n"), 0o700))
	archive := filepath.Join(t.TempDir(), "storage.tar.gz")
	require.NoError(t, exec.Command("tar", "-C", archiveDir, "-czf", archive, ".").Run())
	previousArchive := remoteStorageArchiveNext
	defer os.Remove(previousArchive)
	require.NoError(t, copyFileForTest(archive, previousArchive))
	cmd := exec.Command("bash", "-c", installStorageScript, "moox-install-storage", "0", "0")
	cmd.Env = append(os.Environ(), "HOME="+home)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	require.FileExists(t, filepath.Join(deploy, "data", "old.db"))
	require.FileExists(t, filepath.Join(deploy, "data", "new.db"))
}

func TestStorageInstallerUsesControlGatewayCredentials(t *testing.T) {
	home := t.TempDir()
	controlSecrets := filepath.Join(home, "moox", "prod", "secrets")
	require.NoError(t, os.MkdirAll(controlSecrets, 0o700))
	for name, contents := range map[string]string{
		"gateway-service.env":         "MOOX_GATEWAY_NODE_ID=control\n",
		"gateway-storage-primary.key": "primary-key\n",
		"gateway-storage-view.key":    "view-key\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(controlSecrets, name), []byte(contents), 0o600))
	}
	archiveDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(archiveDir, "secrets"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "start.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	archive := filepath.Join(t.TempDir(), "storage.tar.gz")
	require.NoError(t, exec.Command("tar", "-C", archiveDir, "-czf", archive, ".").Run())
	defer os.Remove(remoteStorageArchiveNext)
	require.NoError(t, copyFileForTest(archive, remoteStorageArchiveNext))

	cmd := exec.Command("bash", "-c", installStorageScript, "moox-install-storage", "0", "1")
	cmd.Env = append(os.Environ(), "HOME="+home)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	for name, contents := range map[string]string{
		"gateway-service.env":         "MOOX_GATEWAY_NODE_ID=control\n",
		"gateway-storage-primary.key": "primary-key\n",
		"gateway-storage-view.key":    "view-key\n",
	} {
		require.Equal(t, contents, string(requireFile(t, filepath.Join(home, "moox", "storage", "secrets", name))))
	}
}

func TestControlInstallerResetPreservesSecretsButDropsData(t *testing.T) {
	home := t.TempDir()
	deploy := filepath.Join(home, "moox", "prod")
	require.NoError(t, os.MkdirAll(filepath.Join(deploy, "data"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(deploy, "secrets"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "data", "old.db"), []byte("old"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "secrets", "keep.env"), []byte("secret"), 0o600))

	archiveDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(archiveDir, "bin"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(archiveDir, "config", "caddy"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(archiveDir, "data"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(archiveDir, "lib"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(archiveDir, "secrets"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "data", "new.db"), []byte("new"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "config", "caddy", "Caddyfile.next"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "bin", "moox-admin-cli"), []byte("#!/bin/sh\nprintf '{\"secret\":\"generated\"}\\n'\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "lib", "caddy-managed.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "start.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "stop.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	archive := filepath.Join(t.TempDir(), "control.tar.gz")
	require.NoError(t, exec.Command("tar", "-C", archiveDir, "-czf", archive, ".").Run())
	defer os.Remove(remoteArchiveNext)
	require.NoError(t, copyFileForTest(archive, remoteArchiveNext))

	cmd := exec.Command("bash", "-c", installControlScript, "moox-install-control", "control.example.test", "9527", "amd64", "1")
	cmd.Env = append(os.Environ(), "HOME="+home)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	require.NoFileExists(t, filepath.Join(deploy, "data", "old.db"))
	require.FileExists(t, filepath.Join(deploy, "data", "new.db"))
	require.Equal(t, "secret", string(requireFile(t, filepath.Join(deploy, "secrets", "keep.env"))))
}

func TestStorageRollsBackAfterReadinessFailure(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "storage.tar.gz")
	require.NoError(t, os.WriteFile(archive, []byte("package"), 0o600))
	events := []string{}
	transport := &fakeTransport{events: &events}
	err := Storage(context.Background(), transport, Options{RepositoryRoot: t.TempDir(), PublicHost: "203.0.113.9", TargetGOOS: "linux", TargetGOARCH: "amd64"}, Dependencies{
		Packager: &fakePackager{path: archive, events: &events},
		Probe:    &fakeProbe{events: &events, failAt: StoragePrimaryReady},
	})
	require.EqualError(t, err, "storage_deploy_not_ready")
	require.Contains(t, events, "rollback_storage")
	require.Equal(t, "cleanup", events[len(events)-1])
}

func TestStorageRollbackDoesNotDeleteDeploymentWhenInstallerAlreadyRestoredPrevious(t *testing.T) {
	home := t.TempDir()
	deploy := filepath.Join(home, "moox", "storage")
	require.NoError(t, os.MkdirAll(deploy, 0o700))
	marker := filepath.Join(home, "restored")
	start := filepath.Join(deploy, "start.sh")
	require.NoError(t, os.WriteFile(start, []byte("#!/bin/sh\nprintf restored >\""+marker+"\"\n"), 0o700))
	command := exec.Command("bash", "-c", rollbackStorageScript)
	command.Env = append(os.Environ(), "HOME="+home)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	require.FileExists(t, start)
	require.NoFileExists(t, marker)
}

func copyFileForTest(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func TestControlCleansRemoteArchiveAfterFailure(t *testing.T) {
	t.Parallel()
	archive := filepath.Join(t.TempDir(), "control.tar.gz")
	require.NoError(t, os.WriteFile(archive, []byte("package"), 0o600))
	events := []string{}
	transport := &fakeTransport{events: &events}
	probe := &fakeProbe{events: &events, failAt: AdminReady}

	err := Control(context.Background(), transport, Options{
		RepositoryRoot: t.TempDir(), PublicHost: "control.example.test", BrowserPort: 9527, TargetGOOS: "linux", TargetGOARCH: "amd64",
		EventBusPublicAddress: "eventbus.example.test", EventBusPort: 4222, EventBusTLSEnabled: true,
	}, Dependencies{Packager: &fakePackager{path: archive, events: &events}, Probe: probe, CAStore: &fakeCAStore{events: &events}})
	require.EqualError(t, err, "control_deploy_not_ready")
	require.Contains(t, events, "rollback")
	require.Equal(t, "cleanup", events[len(events)-1])
	_, statErr := os.Stat(archive)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestControlDetectsARM64BeforePackaging(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "control.tar.gz")
	require.NoError(t, os.WriteFile(archive, []byte("package"), 0o600))
	events := []string{}
	packager := &fakePackager{path: archive, events: &events}
	transport := &fakeTransport{events: &events, unameOS: "Linux\n", unameArch: "aarch64\n"}
	err := Control(context.Background(), transport, Options{
		RepositoryRoot: t.TempDir(), PublicHost: "control.example.test", BrowserPort: 9527,
		EventBusPublicAddress: "eventbus.example.test", EventBusPort: 4222, EventBusTLSEnabled: true,
	}, Dependencies{Packager: packager, Probe: &fakeProbe{events: &events}, CAStore: &fakeCAStore{events: &events}})
	require.NoError(t, err)
	require.Equal(t, "linux", packager.opts.TargetGOOS)
	require.Equal(t, "arm64", packager.opts.TargetGOARCH)
}

func TestFinalizeResponseLossNeverRollsBackHealthyDeployment(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "control.tar.gz")
	require.NoError(t, os.WriteFile(archive, []byte("package"), 0o600))
	events := []string{}
	transport := &fakeTransport{events: &events, failFinalize: true}
	err := Control(context.Background(), transport, Options{
		RepositoryRoot: t.TempDir(), PublicHost: "control.example.test", BrowserPort: 9527,
		TargetGOOS: "linux", TargetGOARCH: "amd64",
		EventBusPublicAddress: "eventbus.example.test", EventBusPort: 4222, EventBusTLSEnabled: true,
	}, Dependencies{Packager: &fakePackager{path: archive, events: &events}, Probe: &fakeProbe{events: &events}, CAStore: &fakeCAStore{events: &events}})
	require.NoError(t, err)
	require.NotContains(t, events, "rollback")
}

func TestRemoteInstallerScriptsParse(t *testing.T) {
	require.NotContains(t, installControlScript, "--label", "moox-admin-cli random-secret does not support labels")
	require.Contains(t, installControlScript, `"$deploy/lib/caddy-managed.sh" stop --deploy-dir "$deploy"`, "atomic control replacement must stop managed Caddy before changing deploy paths")
	require.Contains(t, rollbackControlScript, `"$deploy/lib/caddy-managed.sh" stop --deploy-dir "$deploy"`, "control rollback must stop the active Caddy before restoring the previous path")
	require.Contains(t, rollbackControlScript, `"$deploy/lib/caddy-managed.sh" start --deploy-dir "$deploy"`, "control rollback must restart Caddy after restoring the previous path")
	require.NotContains(t, rollbackControlScript, `caddy-managed.sh" stop --deploy-dir "$deploy" --os linux --arch "$(uname -m)" || true`, "control rollback must not ignore a Caddy stop failure")
	for name, script := range map[string]string{
		"install": installControlScript, "rollback": rollbackControlScript, "finalize": finalizeControlScript,
		"install-storage": installStorageScript, "rollback-storage": rollbackStorageScript, "finalize-storage": finalizeStorageScript,
		"prepare-service": prepareServiceScript, "activate-service": activateServiceScript,
		"rollback-service": rollbackServiceScript, "finalize-service": finalizeServiceScript,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name+".sh")
			require.NoError(t, os.WriteFile(path, []byte(script), 0o600))
			output, err := exec.Command("bash", "-n", path).CombinedOutput()
			require.NoError(t, err, string(output))
		})
	}
}

func TestRollbackScriptRestoresAndStartsPreviousDeployment(t *testing.T) {
	home := t.TempDir()
	deploy := filepath.Join(home, "moox", "prod")
	previous := filepath.Join(home, "moox", "prod.previous")
	require.NoError(t, os.MkdirAll(deploy, 0o700))
	require.NoError(t, os.MkdirAll(previous, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "stop.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(previous, "start.sh"), []byte("#!/bin/sh\nprintf started >\"$HOME/rollback-event\"\n"), 0o700))
	command := exec.Command("bash", "-c", rollbackControlScript)
	command.Env = append(os.Environ(), "HOME="+home)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Equal(t, "started", string(requireFile(t, filepath.Join(home, "rollback-event"))))
	_, err = os.Stat(previous)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.FileExists(t, filepath.Join(deploy, "start.sh"))
}

func requireFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return raw
}

type fakePackager struct {
	path     string
	events   *[]string
	captured string
	opts     Options
}

func (f *fakePackager) Package(_ context.Context, opts Options) (string, error) {
	*f.events = append(*f.events, "package")
	f.opts = opts
	f.captured = opts.RepositoryRoot + opts.PublicHost
	return f.path, nil
}

type fakeTransport struct {
	events       *[]string
	uploadPath   string
	uploadMode   fs.FileMode
	uploaded     bytes.Buffer
	commands     [][]string
	unameOS      string
	unameArch    string
	failFinalize bool
}

func (f *fakeTransport) Check(context.Context) error { return nil }
func (f *fakeTransport) ForwardLocal(context.Context, string) (net.Listener, error) {
	return nil, nil
}
func (f *fakeTransport) Upload(_ context.Context, src io.Reader, _ int64, dst string, mode fs.FileMode) error {
	*f.events = append(*f.events, "upload")
	f.uploadPath, f.uploadMode = dst, mode
	_, _ = io.Copy(&f.uploaded, src)
	return nil
}
func (f *fakeTransport) Run(_ context.Context, argv []string, _ io.Reader) (setupssh.Result, error) {
	f.commands = append(f.commands, append([]string(nil), argv...))
	if len(argv) == 2 && argv[0] == "uname" && argv[1] == "-s" {
		return setupssh.Result{Stdout: f.unameOS}, nil
	}
	if len(argv) == 2 && argv[0] == "uname" && argv[1] == "-m" {
		return setupssh.Result{Stdout: f.unameArch}, nil
	}
	if len(argv) >= 3 && strings.Contains(argv[2], "install_control") {
		*f.events = append(*f.events, "install", "start")
	} else if len(argv) >= 3 && strings.Contains(argv[2], "install_storage") {
		*f.events = append(*f.events, "install_storage")
	} else if len(argv) >= 3 && strings.Contains(argv[2], "storage.previous") && strings.Contains(argv[2], "rm -rf") && !strings.Contains(argv[2], "stop.sh") {
		*f.events = append(*f.events, "finalize_storage")
	} else if len(argv) >= 3 && strings.Contains(argv[2], "prod.previous") && strings.Contains(argv[2], "rm -rf") && !strings.Contains(argv[2], "stop.sh") {
		*f.events = append(*f.events, "finalize")
		if f.failFinalize {
			return setupssh.Result{}, os.ErrDeadlineExceeded
		}
	} else if len(argv) >= 3 && strings.Contains(argv[2], "stop.sh") && strings.Contains(argv[2], "prod.previous") {
		*f.events = append(*f.events, "rollback")
	} else if len(argv) >= 3 && strings.Contains(argv[2], "stop.sh") && strings.Contains(argv[2], "storage.previous") {
		*f.events = append(*f.events, "rollback_storage")
	} else if len(argv) > 0 && argv[0] == "rm" {
		*f.events = append(*f.events, "cleanup")
	}
	return setupssh.Result{}, nil
}

type fakeCAStore struct{ events *[]string }

func (f *fakeCAStore) Save(string, []byte) error {
	*f.events = append(*f.events, "ca")
	return nil
}
func (f *fakeTransport) Close() error { return nil }

type fakeProbe struct {
	events *[]string
	failAt ReadinessStage
}

func (f *fakeProbe) Wait(_ context.Context, _ setupssh.Client, stage ReadinessStage, _ Options) error {
	*f.events = append(*f.events, string(stage))
	if stage == f.failAt {
		return os.ErrDeadlineExceeded
	}
	return nil
}
