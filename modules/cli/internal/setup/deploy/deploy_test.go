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
	"syscall"
	"testing"
	"time"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/stretchr/testify/require"
)

func TestCommandFailureIncludesBoundedCommandOutput(t *testing.T) {
	err := commandFailure("control_install_failed", setupssh.Result{Stderr: "remote install failed\nwith details"})
	require.ErrorContains(t, err, "control_install_failed: remote install failed with details")

	err = commandFailure("control_install_failed", setupssh.Result{})
	require.EqualError(t, err, "control_install_failed")

	err = commandFailure("control_install_failed", setupssh.Result{Stderr: strings.Repeat("a", 300) + " failure-tail"})
	require.ErrorContains(t, err, "failure-tail")
}

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
		TLSMode:               TLSModeInternal,
		EventBusPublicAddress: "eventbus.example.test", EventBusPort: 4222, EventBusTLSEnabled: true,
	}

	err := Control(context.Background(), transport, opts, Dependencies{Packager: packager, Probe: probe, CAStore: &fakeCAStore{events: &events}})
	require.NoError(t, err)
	require.Equal(t, []string{
		"package", "upload", "install", "start", "admin_ready", "setup_ready",
		"gateway_ready", "eventbus_ready", "cloudnode_ready", "collector_ready",
		"monitor_ready", "web_ready", "browser_https_ready", "ca", "finalize", "cleanup",
	}, events)
	require.Regexp(t, `^/tmp/moox-control-[0-9a-f]{32}\.tar\.gz$`, transport.uploadPath)
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

func TestControlRollbackBudgetCoversSerializedHealthcheck(t *testing.T) {
	require.GreaterOrEqual(t, controlRollbackTimeout, 5*time.Minute)
}

func TestControlDeploymentsUseIsolatedRemoteArchives(t *testing.T) {
	run := func() string {
		archive := filepath.Join(t.TempDir(), "control.tar.gz")
		require.NoError(t, os.WriteFile(archive, []byte("package"), 0o600))
		events := []string{}
		transport := &fakeTransport{events: &events}
		require.NoError(t, Control(context.Background(), transport, Options{
			RepositoryRoot: t.TempDir(), PublicHost: "106.53.107.122", BrowserPort: 9527,
			TargetGOOS: "linux", TargetGOARCH: "amd64", TLSMode: TLSModePublic,
			EventBusPublicAddress: "eventbus.example.test", EventBusPort: 4222, EventBusTLSEnabled: true,
		}, Dependencies{
			Packager: &fakePackager{path: archive, events: &events},
			Probe:    &fakeProbe{events: &events},
			CAStore:  &fakeCAStore{events: &events},
		}))
		var install []string
		for _, command := range transport.commands {
			if len(command) >= 3 && strings.Contains(command[2], "install_control") {
				install = command
				break
			}
		}
		require.NotEmpty(t, install)
		require.Equal(t, transport.uploadPath, install[len(install)-1])
		require.Equal(t, controlArchivePath(install[len(install)-2]), transport.uploadPath)
		return transport.uploadPath
	}

	require.NotEqual(t, run(), run())
}

func TestResolveTLSModeUsesPublicCertificatesForPublicHosts(t *testing.T) {
	t.Parallel()
	for _, host := range []string{"106.53.107.122", "moox.example.com"} {
		require.Equal(t, TLSModePublic, resolveTLSMode("", host))
	}
	for _, host := range []string{"127.0.0.1", "10.0.0.8", "192.168.1.8", "localhost"} {
		require.Equal(t, TLSModeInternal, resolveTLSMode("", host))
	}
	require.Equal(t, TLSModeInternal, resolveTLSMode(TLSModeInternal, "106.53.107.122"))
}

func TestBrowserHTTPSProbeKeepsPublicIdentityButConnectsLocally(t *testing.T) {
	command := probeCommand(BrowserHTTPSReady)
	require.Contains(t, command, `--resolve "$1:$2:127.0.0.1"`)
	require.Contains(t, command, `"https://$1:$2/"`)
	require.NotContains(t, command, "-k")
}

func TestControlPublicTLSDoesNotFetchPrivateCA(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "control.tar.gz")
	require.NoError(t, os.WriteFile(archive, []byte("package"), 0o600))
	events := []string{}
	transport := &fakeTransport{events: &events}
	err := Control(context.Background(), transport, Options{
		RepositoryRoot: t.TempDir(), PublicHost: "106.53.107.122", BrowserPort: 9527,
		TargetGOOS: "linux", TargetGOARCH: "amd64", TLSMode: TLSModePublic,
		EventBusPublicAddress: "eventbus.example.test", EventBusPort: 4222, EventBusTLSEnabled: true,
	}, Dependencies{
		Packager: &fakePackager{path: archive, events: &events},
		Probe:    &fakeProbe{events: &events},
		CAStore:  &fakeCAStore{events: &events},
	})
	require.NoError(t, err)
	require.NotContains(t, events, "ca")
	var install []string
	for _, command := range transport.commands {
		if len(command) >= 3 && strings.Contains(command[2], "install_control") {
			install = command
		}
	}
	require.NotEmpty(t, install)
	require.Contains(t, install, string(TLSModePublic))
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
	require.Equal(t, "1", install[len(install)-4], "reset is a bounded positional flag, not shell text")
	require.Equal(t, "public", install[len(install)-3])
	require.Regexp(t, `^[0-9a-f]{32}$`, install[len(install)-2])
	require.Equal(t, controlArchivePath(install[len(install)-2]), install[len(install)-1])
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

func TestStorageInstallsWatchdogWhenRequested(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "scripts"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "deploy", "systemd", "system"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "moox-storage-view-watchdog.sh"), []byte("#!/bin/sh\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "deploy", "systemd", "system", storageViewWatchdogService), []byte("User=__MOOX_USER__\nGroup=__MOOX_GROUP__\nEnvironment=HOME=__MOOX_HOME__\nEnvironment=MOOX_STORAGE_ROOT=__MOOX_STORAGE_ROOT__\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "deploy", "systemd", "system", storageViewWatchdogTimer), []byte("[Timer]\nOnUnitActiveSec=10s\n"), 0o600))
	archive := filepath.Join(root, "storage.tar.gz")
	require.NoError(t, os.WriteFile(archive, []byte("storage-package"), 0o600))
	events := []string{}
	transport := &fakeTransport{events: &events}
	err := Storage(context.Background(), transport, Options{
		RepositoryRoot: root, PublicHost: "203.0.113.9", TargetGOOS: "linux", TargetGOARCH: "amd64", InstallStorageWatchdog: true,
	}, Dependencies{Packager: &fakePackager{path: archive, events: &events}, Probe: &fakeProbe{events: &events}})
	require.NoError(t, err)
	require.Contains(t, events, "install_watchdog")
	require.Equal(t, 4, countEvent(events, "upload"), events)
	var install string
	for _, command := range transport.commands {
		if strings.Contains(strings.Join(command, " "), "moox-install-storage-watchdog") {
			install = strings.Join(command, " ")
			break
		}
	}
	require.Contains(t, install, "systemctl enable --now moox-storage-view-watchdog.timer")
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
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "deploy-moox.sh"), []byte("#!/bin/sh\nset -eu\ntest -f ./compiled\ntest \"$MOOX_EVENTBUS_ENABLE_TLS\" = 1\ntest \"$MOOX_EVENTBUS_PUBLIC_IP\" = eventbus.example.test\ntest \"$MOOX_EVENTBUS_PORT\" = 4222\ntest \"$MOOX_STORAGE_PRIMARY_AUTH_SECRET\" = primary-secret\ntest \"$MOOX_STORAGE_VIEW_AUTH_SECRET\" = view-secret\ntest \"$MOOX_HEALTH_AUTH_VERSION\" = moox-health-v1\ntest \"$MOOX_HEALTH_AUTH_ACCESS_KEY\" = monitor\ntest \"$MOOX_HEALTH_AUTH_SECRET_KEY\" = health-secret\ncase \" $* \" in *' --skip-build '*) ;; *) exit 2 ;; esac\ncase \" $* \" in *' --no-gateway '*) ;; *) exit 4 ;; esac\nwhile [ \"$#\" -gt 0 ]; do\n  if [ \"$1\" = --archive ]; then printf package >\"$2\"; exit 0; fi\n  shift\ndone\nexit 3\n"), 0o700))

	archive, err := (StoragePackager{}).Package(context.Background(), Options{
		RepositoryRoot: root, PublicHost: "203.0.113.9", TargetGOOS: "linux", TargetGOARCH: "amd64",
		UseControlGateway: true, EventBusPublicAddress: "eventbus.example.test",
		EventBusPort: 4222, EventBusTLSEnabled: true,
		StoragePrimarySecret: "primary-secret", StorageViewSecret: "view-secret",
		HealthAuthVersion: "moox-health-v1", HealthAuthAccessKey: "monitor", HealthAuthSecretKey: "health-secret",
	})
	require.NoError(t, err)
	defer os.Remove(archive)
	require.Equal(t, "package", string(requireFile(t, archive)))
}

func TestStorageNodeIDUsesSelectedHostName(t *testing.T) {
	require.Equal(t, "control", storageNodeID(" control "))
	require.Equal(t, "storage", storageNodeID(""))
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
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "secrets", "storage-internal-auth.env"), []byte("old-storage-secret"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "secrets", "health-auth.env"), []byte("old-health-secret"), 0o600))
	archiveDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(archiveDir, "data"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(archiveDir, "secrets"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "data", "new.db"), []byte("new"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "secrets", "storage-internal-auth.env"), []byte("control-owned-secret"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "secrets", "health-auth.env"), []byte("control-health-secret"), 0o600))
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
	require.Equal(t, "control-owned-secret", string(requireFile(t, filepath.Join(deploy, "secrets", "storage-internal-auth.env"))))
	require.Equal(t, "control-health-secret", string(requireFile(t, filepath.Join(deploy, "secrets", "health-auth.env"))))
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
		"storage-internal-auth.env":   "MOOX_STORAGE_PRIMARY_AUTH_SECRET=primary-control\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-control\n",
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
		"storage-internal-auth.env":   "MOOX_STORAGE_PRIMARY_AUTH_SECRET=primary-control\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-control\n",
	} {
		require.Equal(t, contents, string(requireFile(t, filepath.Join(home, "moox", "storage", "secrets", name))))
	}
}

func TestControlInstallerResetPreservesSecretsButDropsData(t *testing.T) {
	home := t.TempDir()
	deploy := filepath.Join(home, "moox", "prod")
	require.NoError(t, os.MkdirAll(filepath.Join(deploy, "data"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(deploy, "data", "caddy"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(deploy, "secrets"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "start.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "stop.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "data", "old.db"), []byte("old"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "data", "caddy", "acme-account.json"), []byte("account"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "secrets", "keep.env"), []byte("secret"), 0o600))
	require.NoError(t, os.WriteFile(
		filepath.Join(deploy, "secrets", "storage-internal-auth.env"),
		[]byte("MOOX_STORAGE_PRIMARY_AUTH_SECRET=primary-old"),
		0o600,
	))

	archiveDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(archiveDir, "bin"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(archiveDir, "config", "caddy"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(archiveDir, "data"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(archiveDir, "lib"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(archiveDir, "secrets"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "data", "new.db"), []byte("new"), 0o600))
	require.NoError(t, os.WriteFile(
		filepath.Join(archiveDir, "secrets", "storage-internal-auth.env"),
		[]byte("MOOX_STORAGE_PRIMARY_AUTH_SECRET=primary-new\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-new\n"),
		0o600,
	))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "config", "caddy", "Caddyfile.next"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "bin", "moox-admin-cli"), []byte("#!/bin/sh\nprintf '{\"secret\":\"generated\"}\\n'\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "lib", "caddy-managed.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "start.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "stop.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	archive := filepath.Join(t.TempDir(), "control.tar.gz")
	require.NoError(t, exec.Command("tar", "-C", archiveDir, "-czf", archive, ".").Run())
	activationToken := "test-reset"
	remoteArchive := controlArchivePath(activationToken)
	defer os.Remove(remoteArchive)
	require.NoError(t, copyFileForTest(archive, remoteArchive))

	crontabLog := filepath.Join(t.TempDir(), "crontab")
	crontabCommand := filepath.Join(t.TempDir(), "crontab")
	require.NoError(t, os.WriteFile(
		crontabCommand,
		[]byte("#!/bin/sh\nif [ \"$1\" = -l ]; then exit 0; fi\ncat >\"$MOOX_CRONTAB_LOG\"\n"),
		0o700,
	))
	flockCommand := fakeFlockCommand(t)
	cmd := exec.Command("bash", "-c", installControlScript, "moox-install-control", "control.example.test", "9527", "amd64", "1", "public", activationToken, remoteArchive)
	cmd.Env = append(
		os.Environ(),
		"HOME="+home,
		"MOOX_CRONTAB_COMMAND="+crontabCommand,
		"MOOX_CRONTAB_LOG="+crontabLog,
		"MOOX_CRON_DAEMON_CHECK_COMMAND=/usr/bin/true",
		"MOOX_FLOCK_COMMAND="+flockCommand,
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Contains(t, string(requireFile(t, crontabLog)), `* * * * * PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin $HOME/moox/prod/healthcheck.sh >/dev/null 2>&1`)
	require.NoFileExists(t, filepath.Join(deploy, "data", "old.db"))
	require.FileExists(t, filepath.Join(deploy, "data", "new.db"))
	require.Equal(t, "account", string(requireFile(t, filepath.Join(deploy, "data", "caddy", "acme-account.json"))))
	require.Equal(t, "secret", string(requireFile(t, filepath.Join(deploy, "secrets", "keep.env"))))
	require.Equal(
		t,
		"MOOX_STORAGE_PRIMARY_AUTH_SECRET=primary-old\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-new\n",
		string(requireFile(t, filepath.Join(deploy, "secrets", "storage-internal-auth.env"))),
	)

	// A client can disappear after activation but before finalize. A later setup
	// must take over without deleting the first transaction's rollback lineage.
	nextToken := "test-next"
	nextArchive := controlArchivePath(nextToken)
	defer os.Remove(nextArchive)
	require.NoError(t, copyFileForTest(archive, nextArchive))
	next := exec.Command("bash", "-c", installControlScript, "moox-install-control", "control.example.test", "9527", "amd64", "0", "public", nextToken, nextArchive)
	next.Env = cmd.Env
	output, err = next.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Equal(t, nextToken+"\n", string(requireFile(t, filepath.Join(deploy, ".control-activation-token"))))
	require.FileExists(t, filepath.Join(home, "moox", "prod.previous."+nextToken, ".control-activation-token"))
	require.DirExists(t, filepath.Join(home, "moox", "prod.previous."+activationToken))

	rollbackNext := exec.Command("bash", "-c", rollbackControlScript, "moox-rollback-control", nextToken)
	rollbackNext.Env = cmd.Env
	output, err = rollbackNext.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Equal(t, activationToken+"\n", string(requireFile(t, filepath.Join(deploy, ".control-activation-token"))))

	rollbackFirst := exec.Command("bash", "-c", rollbackControlScript, "moox-rollback-control", activationToken)
	rollbackFirst.Env = cmd.Env
	output, err = rollbackFirst.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Equal(t, "old", string(requireFile(t, filepath.Join(deploy, "data", "old.db"))))
}

func TestControlInstallerWaitsForStableMaintenanceLock(t *testing.T) {
	home := t.TempDir()
	lockPath := filepath.Join(home, "moox", "prod.maintenance.lock")
	require.NoError(t, os.MkdirAll(filepath.Dir(lockPath), 0o700))
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	defer lock.Close()
	require.NoError(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_EX))
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", installControlScript, "moox-install-control", "control.example.test", "9527", "amd64", "1", "public", "test-lock", controlArchivePath("test-lock"))
	cmd.Env = append(os.Environ(), "HOME="+home, "MOOX_CRONTAB_COMMAND=/bin/true", "MOOX_FLOCK_COMMAND="+fakeFlockCommand(t))
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		require.Failf(t, "installer exited instead of waiting for the maintenance lock", "error: %v", err)
	case <-time.After(200 * time.Millisecond):
		require.NoDirExists(t, filepath.Join(home, "moox", "prod.next"), "installer mutated deployment state before acquiring the shared lock")
	}
	cancel()
	<-done
}

func TestControlInstallerCreatesLockParentOnCleanHost(t *testing.T) {
	home := t.TempDir()
	token := "cold-start"
	cmd := exec.Command(
		"bash", "-c", installControlScript, "moox-install-control",
		"control.example.test", "9527", "amd64", "0", "public", token, controlArchivePath(token),
	)
	cmd.Env = append(
		os.Environ(),
		"HOME="+home,
		"MOOX_CRONTAB_COMMAND=/usr/bin/true",
		"MOOX_CRON_DAEMON_CHECK_COMMAND=/usr/bin/true",
		"MOOX_FLOCK_COMMAND="+fakeFlockCommand(t),
	)
	output, err := cmd.CombinedOutput()
	require.Error(t, err, "the deliberately absent archive must stop the installer")
	require.NotContains(t, string(output), "prod.maintenance.lock")
	require.DirExists(t, filepath.Join(home, "moox"))
	require.FileExists(t, filepath.Join(home, "moox", "prod.maintenance.lock"))
}

func TestControlInstallerSchedulerFailureLeavesExistingDeploymentUntouched(t *testing.T) {
	home := t.TempDir()
	deploy := filepath.Join(home, "moox", "prod")
	require.NoError(t, os.MkdirAll(deploy, 0o700))
	marker := filepath.Join(deploy, "existing")
	require.NoError(t, os.WriteFile(marker, []byte("keep"), 0o600))

	cmd := exec.Command(
		"bash", "-c", installControlScript, "moox-install-control",
		"control.example.test", "9527", "amd64", "1", "public", "test-cron-failure", controlArchivePath("test-cron-failure"),
	)
	cmd.Env = append(
		os.Environ(),
		"HOME="+home,
		"MOOX_CRONTAB_COMMAND=/usr/bin/false",
		"MOOX_CRON_DAEMON_CHECK_COMMAND=/usr/bin/true",
		"MOOX_FLOCK_COMMAND="+fakeFlockCommand(t),
	)
	output, err := cmd.CombinedOutput()
	require.Error(t, err, string(output))
	require.Equal(t, "keep", string(requireFile(t, marker)))
	require.NoDirExists(t, filepath.Join(home, "moox", "prod.next"))
	require.NoDirExists(t, filepath.Join(home, "moox", "prod.previous"))
}

func fakeFlockCommand(t *testing.T) string {
	t.Helper()
	command := filepath.Join(t.TempDir(), "flock")
	require.NoError(t, os.WriteFile(
		command,
		[]byte("#!/usr/bin/env python3\nimport fcntl, sys\nfcntl.flock(int(sys.argv[1]), fcntl.LOCK_EX)\n"),
		0o700,
	))
	return command
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
	require.Less(
		t,
		strings.Index(installControlScript, `"$deploy/lib/caddy-managed.sh" stop --deploy-dir "$deploy"`),
		strings.Index(installControlScript, `cp -R "$deploy/data/caddy/." "$next/data/caddy/"`),
		"managed Caddy must stop before its live ACME storage is copied",
	)
	require.Contains(t, installControlScript, "trap on_install_control_exit EXIT", "pre-activation failures must restart the stopped Caddy")
	require.Contains(t, installControlScript, "install_control_healthcheck_cron", "control install must schedule Caddy and service recovery after reboot")
	require.Contains(t, installControlScript, `* * * * * PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin $HOME/moox/prod/healthcheck.sh >/dev/null 2>&1`, "control healthcheck must run every minute with system administration tools available")
	require.Contains(t, installControlScript, "systemctl is-enabled --quiet", "control install must verify that cron survives reboot")
	require.Contains(t, installControlScript, `"$deploy.maintenance.lock"`, "installer and healthcheck must share a lock outside the renamed deployment directory")
	require.Contains(t, installControlScript, `"$deploy/start.sh" 8>&-`, "services must not inherit the deployment maintenance lock")
	require.Contains(t, installControlScript, `--config "$deploy/config/caddy/Caddyfile.next" 8>&-`, "Caddy must not inherit the deployment maintenance lock")
	require.Contains(t, rollbackControlScript, `"$deploy/lib/caddy-managed.sh" stop --deploy-dir "$deploy"`, "control rollback must stop the active Caddy before restoring the previous path")
	require.Contains(t, rollbackControlScript, `"$deploy/lib/caddy-managed.sh" start --deploy-dir "$deploy"`, "control rollback must restart Caddy after restoring the previous path")
	require.Contains(t, rollbackControlScript, `"$deploy/start.sh" 8>&-`, "restored services must not inherit the deployment maintenance lock")
	require.Contains(t, rollbackControlScript, `"$deploy.maintenance.lock"`, "control rollback must serialize with installer and healthcheck")
	require.Contains(t, finalizeControlScript, `"$deploy.maintenance.lock"`, "control finalize must serialize with installer and healthcheck")
	require.Contains(t, rollbackControlScript, ".control-activation-token", "rollback must not affect a superseding deployment")
	require.Contains(t, finalizeControlScript, ".control-activation-token", "finalize must not affect a superseding deployment")
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
	previous := filepath.Join(home, "moox", "prod.previous.rollback-test")
	require.NoError(t, os.MkdirAll(deploy, 0o700))
	require.NoError(t, os.MkdirAll(previous, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, ".control-activation-token"), []byte("rollback-test\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "stop.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(previous, "start.sh"), []byte("#!/bin/sh\nprintf started >\"$HOME/rollback-event\"\n"), 0o700))
	command := exec.Command("bash", "-c", rollbackControlScript, "moox-rollback-control", "rollback-test")
	command.Env = append(os.Environ(), "HOME="+home, "MOOX_FLOCK_COMMAND="+fakeFlockCommand(t))
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Equal(t, "started", string(requireFile(t, filepath.Join(home, "rollback-event"))))
	_, err = os.Stat(previous)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.FileExists(t, filepath.Join(deploy, "start.sh"))
}

func TestStaleControlTransactionCannotRollbackOrFinalizeNewerDeployment(t *testing.T) {
	home := t.TempDir()
	deploy := filepath.Join(home, "moox", "prod")
	previous := filepath.Join(home, "moox", "prod.previous.stale")
	require.NoError(t, os.MkdirAll(deploy, 0o700))
	require.NoError(t, os.MkdirAll(previous, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, ".control-activation-token"), []byte("newer\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "current"), []byte("current"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(previous, "rollback"), []byte("rollback"), 0o600))
	env := append(os.Environ(), "HOME="+home, "MOOX_FLOCK_COMMAND="+fakeFlockCommand(t))

	for _, script := range []string{rollbackControlScript, finalizeControlScript} {
		command := exec.Command("bash", "-c", script, "moox-control-transaction", "stale")
		command.Env = env
		output, err := command.CombinedOutput()
		require.NoError(t, err, string(output))
	}
	require.FileExists(t, filepath.Join(deploy, "current"))
	require.FileExists(t, filepath.Join(previous, "rollback"))
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
	if len(argv) >= 3 && strings.Contains(argv[2], "printf '%s\\n%s\\n%s\\n'") {
		return setupssh.Result{Stdout: "/home/ubuntu\nubuntu\nubuntu\n"}, nil
	}
	if strings.Contains(strings.Join(argv, " "), "moox-install-storage-watchdog") {
		*f.events = append(*f.events, "install_watchdog")
	} else if len(argv) >= 3 && strings.Contains(argv[2], "install_control") {
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

func countEvent(events []string, want string) int {
	count := 0
	for _, event := range events {
		if event == want {
			count++
		}
	}
	return count
}

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
