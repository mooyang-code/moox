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

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/gatewayauth"
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

func TestEventBusProbeRequiresPersistedPublicTLSListener(t *testing.T) {
	command := probeCommandForOptions(EventBusReady, Options{EventBusPublicAddress: "eventbus.example.com", EventBusPort: 4222, EventBusTLSEnabled: true})
	require.Contains(t, command, `config/runtime.env`)
	require.Contains(t, command, `MOOX_EVENTBUS_ENABLE_TLS`)
	require.Contains(t, command, `http://127.0.0.1:11419/`)
	require.Contains(t, command, `MOOX_EVENTBUS_HOST`)
	require.Contains(t, command, `= 0.0.0.0`)
}

func TestEventBusCommandEnvRejectsUnroutableAddresses(t *testing.T) {
	for _, address := range []string{"localhost", "LOCALHOST", "localhost.", "127.0.0.1", "0.0.0.0", "::"} {
		_, err := eventBusCommandEnv(nil, Options{EventBusPublicAddress: address, EventBusPort: 4222, EventBusTLSEnabled: true})
		require.EqualError(t, err, "control_deploy_invalid", address)
	}
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
	require.Contains(t, env, "MOOX_STORAGE_EVENTBUS_URL=tls://eventbus.example.test:4333")
}

func TestMonitoringCommandEnvOverridesAmbientWebhook(t *testing.T) {
	env := notificationCommandEnv([]string{
		"MOOX_NOTIFICATION_WEBHOOK_URL=https://example.test/ambient",
		"PATH=/bin",
	}, "wecom", "")
	require.NotContains(t, env, "MOOX_NOTIFICATION_WEBHOOK_URL=https://example.test/ambient")
	require.Contains(t, env, "MOOX_NOTIFICATION_WEBHOOK_URL=")
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
	test "$MOOX_NOTIFICATION_WEBHOOK_URL" = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test"
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
		NotificationChannelType: "wecom",
		NotificationWebhookURL:  "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test",
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
	require.Regexp(t, `^/tmp/moox-storage-[A-Za-z0-9._-]+\.tar\.gz$`, transport.uploadPath)
}

func TestStorageInstallsWatchdogWhenRequested(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "scripts"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "deploy", "systemd", "system"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "moox-storage-view-watchdog.sh"), []byte("#!/bin/sh\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "deploy", "systemd", "system", storageViewWatchdogService), []byte("User=__MOOX_USER__\nGroup=__MOOX_GROUP__\nEnvironment=HOME=__MOOX_HOME__\nEnvironment=MOOX_STORAGE_ROOT=__MOOX_STORAGE_ROOT__\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "deploy", "systemd", "system", storageViewWatchdogTimer), []byte("[Timer]\nOnActiveSec=10s\nOnUnitActiveSec=10s\n"), 0o600))
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
	require.Contains(t, install, "systemctl enable moox-storage-view-watchdog.timer")
	require.Contains(t, install, "systemctl restart moox-storage-view-watchdog.timer")
	require.Contains(t, install, "NextElapseUSecMonotonic")
	require.NotContains(t, install, "systemctl start moox-storage-view-watchdog.timer")
	require.Contains(t, string(requireFile(t, filepath.Join(root, "deploy", "systemd", "system", storageViewWatchdogTimer))), "OnActiveSec=10s")
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
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "build-storage-linux.sh"), []byte("#!/bin/sh\nset -eu\n: \"${MOOX_CLI:?}\"\n: \"${CONFIG:?}\"\ntest \"$MOOX_SSH_PASSWORD\" = build-password\ntest \"$MOOX_STORAGE_BUILD_HOST\" = compile\ntest \"$MOOX_STORAGE_BUILD_HOST_ROLE\" = compile\ntest \"$MOOX_STORAGE_BUILD_GOARCH\" = amd64\ntouch ./compiled\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "deploy-moox.sh"), []byte("#!/bin/sh\nset -eu\ntest -f ./compiled\ntest \"$MOOX_EVENTBUS_ENABLE_TLS\" = 1\ntest \"$MOOX_EVENTBUS_PUBLIC_IP\" = eventbus.example.test\ntest \"$MOOX_EVENTBUS_PORT\" = 4222\ntest \"$MOOX_STORAGE_PRIMARY_AUTH_SECRET\" = primary-secret\ntest \"$MOOX_STORAGE_VIEW_AUTH_SECRET\" = view-secret\ntest -n \"$MOOX_STORAGE_VIEW_MAINTENANCE_POLICY_B64\"\ntest \"$MOOX_LOCAL_LOG_MAX_SIZE_MB\" = 88\ntest \"$MOOX_LOCAL_LOG_BACKUP_COUNT\" = 9\ntest \"$MOOX_HEALTH_AUTH_VERSION\" = moox-health-v1\ntest \"$MOOX_HEALTH_AUTH_ACCESS_KEY\" = monitor\ntest \"$MOOX_HEALTH_AUTH_SECRET_KEY\" = health-secret\ncase \" $* \" in *' --skip-build '*) ;; *) exit 2 ;; esac\ncase \" $* \" in *' --no-gateway '*) ;; *) exit 4 ;; esac\nwhile [ \"$#\" -gt 0 ]; do\n  if [ \"$1\" = --archive ]; then\n    dir=$(mktemp -d)\n    trap 'rm -rf \"$dir\"' EXIT\n    printf '%s' '{\"schema_version\":1,\"commit\":\"0123456789012345678901234567890123456789\",\"dirty\":false,\"binary_hashes\":{\"moox-storage-primary\":\"0000000000000000000000000000000000000000000000000000000000000000\",\"moox-storage-node\":\"0000000000000000000000000000000000000000000000000000000000000000\",\"moox-storage-view\":\"0000000000000000000000000000000000000000000000000000000000000000\"}}' >\"$dir/build-provenance.json\"\n    tar -czf \"$2\" -C \"$dir\" build-provenance.json\n    exit 0\n  fi\n  shift\ndone\nexit 3\n"), 0o700))

	archive, err := (StoragePackager{}).Package(context.Background(), Options{
		RepositoryRoot: root, PublicHost: "203.0.113.9", TargetGOOS: "linux", TargetGOARCH: "amd64", StorageBuildPassword: "build-password", StorageBuildHost: "compile", StorageBuildHostRole: "compile",
		UseControlGateway: true, EventBusPublicAddress: "eventbus.example.test",
		EventBusPort: 4222, EventBusTLSEnabled: true,
		StoragePrimarySecret: "primary-secret", StorageViewSecret: "view-secret",
		StorageViewPolicy: setupconfig.StorageView{MaintenanceCheckInterval: "1m", RebuildLookbackPeriods: 777, MaxPeriodsPerSeries: 1600, MaxViewFileBytes: 805306368},
		LocalLogs:         setupconfig.LocalLogs{MaxSizeMB: 88, BackupCount: 9},
		HealthAuthVersion: "moox-health-v1", HealthAuthAccessKey: "monitor", HealthAuthSecretKey: "health-secret",
	})
	require.NoError(t, err)
	defer os.Remove(archive)
	provenance, err := readArchiveMember(archive, "build-provenance.json")
	require.NoError(t, err)
	require.Contains(t, string(provenance), `"schema_version":1`)
}

func TestStorageNodeIDUsesSelectedHostName(t *testing.T) {
	require.Equal(t, "control", storageNodeID(" control "))
	require.Equal(t, "storage", storageNodeID(""))
}

func TestNormalizeDeployPathsDefaultsLocalLogs(t *testing.T) {
	opts := Options{}
	require.NoError(t, normalizeDeployPaths(&opts))
	require.Equal(t, setupconfig.LocalLogs{MaxSizeMB: 50, BackupCount: 5}, opts.LocalLogs)
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
	require.Equal(t, "1", install[len(install)-5], "reset is a bounded positional flag, not shell text")
	require.Equal(t, "0", install[len(install)-4], "view reset is a bounded positional flag")
	require.Equal(t, "0", install[len(install)-3])
	require.Regexp(t, `^[A-Za-z0-9._-]+$`, install[len(install)-2])
	require.Equal(t, storageArchivePath(install[len(install)-2]), install[len(install)-1])
}

func TestStoragePassesRemoteEventBusURLToInstaller(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "storage.tar.gz")
	require.NoError(t, os.WriteFile(archive, []byte("storage-package"), 0o600))
	events := []string{}
	transport := &fakeTransport{events: &events}
	err := Storage(context.Background(), transport, Options{
		RepositoryRoot: t.TempDir(), PublicHost: "203.0.113.9", TargetGOOS: "linux", TargetGOARCH: "amd64",
		EventBusPublicAddress: "eventbus.example.test", EventBusPort: 4333, EventBusTLSEnabled: true,
	}, Dependencies{Packager: &fakePackager{path: archive, events: &events}, Probe: &fakeProbe{events: &events}})
	require.NoError(t, err)
	var install []string
	for _, command := range transport.commands {
		if len(command) >= 3 && strings.Contains(command[2], "install_storage") {
			install = command
			break
		}
	}
	require.NotEmpty(t, install)
	require.Contains(t, install[2], "export MOOX_STORAGE_EVENTBUS_URL='tls://eventbus.example.test:4333'")
}

func TestStorageUploadsRemoteEventBusMaterialForSeparateHost(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "storage.tar.gz")
	require.NoError(t, os.WriteFile(archive, []byte("storage-package"), 0o600))
	events := []string{}
	transport := &fakeTransport{events: &events}
	err := Storage(context.Background(), transport, Options{
		RepositoryRoot: t.TempDir(), PublicHost: "203.0.113.9", TargetGOOS: "linux", TargetGOARCH: "amd64",
		StorageEventBusCredential: []byte("username: storage-eventbus\n token: token\n"),
		StorageEventBusCA:         []byte("-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----\n"),
	}, Dependencies{Packager: &fakePackager{path: archive, events: &events}, Probe: &fakeProbe{events: &events}})
	require.NoError(t, err)
	require.Equal(t, 3, countEvent(events, "upload"))
	var install []string
	for _, command := range transport.commands {
		if len(command) >= 3 && strings.Contains(command[2], "install_storage") {
			install = command
			break
		}
	}
	require.NotEmpty(t, install)
	require.Regexp(t, `^/tmp/moox-storage-eventbus-[A-Za-z0-9._-]+\.yaml$`, install[len(install)-2])
	require.Regexp(t, `^/tmp/moox-storage-eventbus-[A-Za-z0-9._-]+\.pem$`, install[len(install)-1])
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
	const token = "reset-test"
	previousArchive := storageArchivePath(token)
	defer os.Remove(previousArchive)
	require.NoError(t, copyFileForTest(archive, previousArchive))
	cmd := exec.Command("bash", "-c", installStorageScript, "moox-install-storage", "1", "0", "0", token, previousArchive)
	cmd.Env = storageInstallerEnv(t, home)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	_, err = os.Stat(filepath.Join(deploy, "data", "old.db"))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.FileExists(t, filepath.Join(deploy, "data", "new.db"))
	require.Equal(t, "secret", string(requireFile(t, filepath.Join(deploy, "secrets", "auth.env"))))
	require.Equal(t, "control-owned-secret", string(requireFile(t, filepath.Join(deploy, "secrets", "storage-internal-auth.env"))))
	require.Equal(t, "control-health-secret", string(requireFile(t, filepath.Join(deploy, "secrets", "health-auth.env"))))
}

func TestStorageInstallerCopiesEventBusMaterialForRestarts(t *testing.T) {
	home := t.TempDir()
	archiveDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(archiveDir, "secrets"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "start.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	archive := filepath.Join(t.TempDir(), "storage.tar.gz")
	require.NoError(t, exec.Command("tar", "-C", archiveDir, "-czf", archive, ".").Run())
	const token = "eventbus-material-test"
	remoteArchive := storageArchivePath(token)
	defer os.Remove(remoteArchive)
	require.NoError(t, copyFileForTest(archive, remoteArchive))
	credential := filepath.Join(t.TempDir(), "storage-eventbus.yaml")
	ca := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(credential, []byte("version: 1\nusername: storage-eventbus\ntoken: token\nca_file: ca.pem\n"), 0o600))
	require.NoError(t, os.WriteFile(ca, []byte("ca"), 0o600))

	storageRoot := filepath.Join(home, "moox", "storage")
	controlRoot := filepath.Join(home, "moox", "prod")
	cmd := exec.Command("bash", "-c", installStorageScript, "moox-install-storage", storageRoot, controlRoot, "0", "0", "0", token, remoteArchive, credential, ca)
	cmd.Env = storageInstallerEnv(t, home)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Equal(t, "version: 1\nusername: storage-eventbus\ntoken: token\nca_file: ca.pem\n", string(requireFile(t, filepath.Join(home, ".config", "moox", "eventbus", "storage-eventbus.yaml"))))
	require.Equal(t, "ca", string(requireFile(t, filepath.Join(home, ".config", "moox", "eventbus", "ca.pem"))))
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
	const token = "preserve-test"
	previousArchive := storageArchivePath(token)
	defer os.Remove(previousArchive)
	require.NoError(t, copyFileForTest(archive, previousArchive))
	cmd := exec.Command("bash", "-c", installStorageScript, "moox-install-storage", "0", "0", "0", token, previousArchive)
	cmd.Env = storageInstallerEnv(t, home)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	require.FileExists(t, filepath.Join(deploy, "data", "old.db"))
	require.NoFileExists(t, filepath.Join(deploy, "data", "new.db"))
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
	const token = "gateway-test"
	remoteArchive := storageArchivePath(token)
	defer os.Remove(remoteArchive)
	require.NoError(t, copyFileForTest(archive, remoteArchive))

	cmd := exec.Command("bash", "-c", installStorageScript, "moox-install-storage", "0", "0", "1", token, remoteArchive)
	cmd.Env = storageInstallerEnv(t, home)
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

func TestStorageInstallerUpgradeKeepsKeysButActivatesPackagedGatewayRegistry(t *testing.T) {
	home := t.TempDir()
	deploy := filepath.Join(home, "moox", "storage")
	prepareOldGatewayDeployment(t, deploy, "storage")
	archive := buildGatewayUpgradeArchive(t)
	const token = "storage-gateway-registry-upgrade"
	remoteArchive := storageArchivePath(token)
	defer os.Remove(remoteArchive)
	require.NoError(t, copyFileForTest(archive, remoteArchive))

	cmd := exec.Command("bash", "-c", installStorageScript, "moox-install-storage", "0", "0", "0", token, remoteArchive)
	cmd.Env = storageInstallerEnv(t, home)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	assertGatewayRegistryUpgrade(t, filepath.Join(deploy, "secrets"))
}

func TestControlInstallerUpgradeKeepsKeysButActivatesPackagedGatewayRegistry(t *testing.T) {
	home := t.TempDir()
	deploy := filepath.Join(home, "moox", "prod")
	prepareOldGatewayDeployment(t, deploy, "control")
	archive := buildGatewayUpgradeArchive(t)
	const token = "control-gateway-registry-upgrade"
	remoteArchive := controlArchivePath(token)
	defer os.Remove(remoteArchive)
	require.NoError(t, copyFileForTest(archive, remoteArchive))
	crontabCommand := filepath.Join(t.TempDir(), "crontab")
	require.NoError(t, os.WriteFile(crontabCommand, []byte("#!/bin/sh\n[ \"${1:-}\" = -l ] && exit 0\ncat >/dev/null\n"), 0o700))

	cmd := exec.Command("bash", "-c", installControlScript, "moox-install-control", "control.example.test", "9527", "amd64", "0", "public", token, remoteArchive)
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"MOOX_CRONTAB_COMMAND="+crontabCommand,
		"MOOX_CRON_DAEMON_CHECK_COMMAND=/usr/bin/true",
		"MOOX_FLOCK_COMMAND="+fakeFlockCommand(t),
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	assertGatewayRegistryUpgrade(t, filepath.Join(deploy, "secrets"))
}

func prepareOldGatewayDeployment(t *testing.T, deploy, nodeID string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(deploy, "secrets"), 0o700))
	for _, name := range []string{"start.sh", "stop.sh"} {
		require.NoError(t, os.WriteFile(filepath.Join(deploy, name), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "secrets", "gateway-collector.key"), []byte("old-collector-key\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "secrets", "gateway-service.env"), []byte("MOOX_GATEWAY_NODE_ID="+nodeID+"\nMOOX_GATEWAY_SERVICE_SECRET_KEY=old-root\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "secrets", "gateway-credentials.json"), []byte(`{"version":1,"credentials":[{"key_id":"collector","caller":"collector","secret_file":"gateway-collector.key"}]}`), 0o600))
}

func buildGatewayUpgradeArchive(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, path := range []string{"secrets", "lib", "config/caddy"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, path), 0o700))
	}
	for _, name := range []string{"start.sh", "stop.sh"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lib", "caddy-managed.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config", "caddy", "Caddyfile.next"), nil, 0o600))
	for name, contents := range map[string]string{
		"gateway-collector.key":    "new-collector-key\n",
		"gateway-moox-skill.key":   "new-skill-key\n",
		"gateway-service.env":      "MOOX_GATEWAY_NODE_ID=storage\nMOOX_GATEWAY_SERVICE_SECRET_KEY=new-root\n",
		"health-auth.env":          "health\n",
		"admin-jwt.env":            "admin\n",
		"gateway-credentials.json": `{"version":1,"credentials":[{"key_id":"collector","caller":"collector","secret_file":"gateway-collector.key"},{"key_id":"moox-skill","caller":"moox-skill","secret_file":"gateway-moox-skill.key"}]}`,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "secrets", name), []byte(contents), 0o600))
	}
	archive := filepath.Join(t.TempDir(), "gateway-upgrade.tar.gz")
	require.NoError(t, exec.Command("tar", "-C", dir, "-czf", archive, ".").Run())
	return archive
}

func assertGatewayRegistryUpgrade(t *testing.T, secrets string) {
	t.Helper()
	require.Equal(t, "old-collector-key\n", string(requireFile(t, filepath.Join(secrets, "gateway-collector.key"))))
	require.Equal(t, "new-skill-key\n", string(requireFile(t, filepath.Join(secrets, "gateway-moox-skill.key"))))
	serviceEnv := string(requireFile(t, filepath.Join(secrets, "gateway-service.env")))
	require.Contains(t, serviceEnv, "MOOX_GATEWAY_SERVICE_SECRET_KEY=old-root")
	require.NotContains(t, serviceEnv, "new-root")
	registry := string(requireFile(t, filepath.Join(secrets, "gateway-credentials.json")))
	require.Contains(t, registry, `"key_id":"moox-skill"`)
	require.NotEqual(t, `{"version":1,"credentials":[{"key_id":"collector","caller":"collector","secret_file":"gateway-collector.key"}]}`, registry)
	info, err := os.Stat(filepath.Join(secrets, "gateway-credentials.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	_, err = gatewayauth.LoadCredentialRegistry(filepath.Join(secrets, "gateway-credentials.json"))
	require.NoError(t, err, "the upgraded registry and preserved/new keys must be immediately consumable")
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
	require.Contains(t, string(requireFile(t, crontabLog)), `for healthcheck in "$HOME"/moox/*/healthcheck.sh`)
	require.Contains(t, string(requireFile(t, crontabLog)), `# moox-healthchecks`)
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

func TestStorageRollbackBudgetCoversSerializedServices(t *testing.T) {
	require.GreaterOrEqual(t, storageRollbackTimeout, 5*time.Minute)
}

func TestStorageReportsRollbackFailure(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "storage.tar.gz")
	require.NoError(t, os.WriteFile(archive, []byte("package"), 0o600))
	events := []string{}
	transport := &fakeTransport{events: &events, failStorageRollback: true}
	err := Storage(context.Background(), transport, Options{RepositoryRoot: t.TempDir(), PublicHost: "203.0.113.9", TargetGOOS: "linux", TargetGOARCH: "amd64"}, Dependencies{
		Packager: &fakePackager{path: archive, events: &events},
		Probe:    &fakeProbe{events: &events, failAt: StoragePrimaryReady},
	})
	require.EqualError(t, err, "storage_deploy_not_ready; storage_rollback_failed")
}

func TestStorageRollbackDoesNotDeleteDeploymentWhenInstallerAlreadyRestoredPrevious(t *testing.T) {
	home := t.TempDir()
	deploy := filepath.Join(home, "moox", "storage")
	require.NoError(t, os.MkdirAll(deploy, 0o700))
	marker := filepath.Join(home, "restored")
	start := filepath.Join(deploy, "start.sh")
	require.NoError(t, os.WriteFile(start, []byte("#!/bin/sh\nprintf restored >\""+marker+"\"\n"), 0o700))
	command := exec.Command("bash", "-c", rollbackStorageScript, "moox-rollback-storage", "rollback-test")
	command.Env = storageInstallerEnv(t, home)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	require.FileExists(t, start)
	require.NoFileExists(t, marker)
}

func TestStorageFirstDeploymentRollbackStopsAndRetainsFailedDeployment(t *testing.T) {
	home := t.TempDir()
	const token = "first-deploy-rollback"
	deploy := filepath.Join(home, "moox", "storage")
	require.NoError(t, os.MkdirAll(deploy, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, ".storage-activation-token"), []byte(token+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "stop.sh"), []byte("#!/bin/sh\nprintf stopped >\"$HOME/stopped\"\n"), 0o700))

	command := exec.Command("bash", "-c", rollbackStorageScript, "moox-rollback-storage", token)
	command.Env = storageInstallerEnv(t, home)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	require.FileExists(t, filepath.Join(home, "stopped"))
	require.NoDirExists(t, deploy)
	require.DirExists(t, filepath.Join(home, "moox", "storage.failed."+token))
}

func TestStorageDelayedRollbackPreservesMovedData(t *testing.T) {
	home := t.TempDir()
	deploy := filepath.Join(home, "moox", "storage")
	require.NoError(t, os.MkdirAll(filepath.Join(deploy, "data"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "data", "facts.db"), []byte("facts"), 0o600))
	for _, name := range []string{"start.sh", "stop.sh"} {
		require.NoError(t, os.WriteFile(filepath.Join(deploy, name), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	}
	archiveDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(archiveDir, "secrets"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "secrets", "health-auth.env"), []byte("health"), 0o600))
	for _, name := range []string{"start.sh", "stop.sh"} {
		require.NoError(t, os.WriteFile(filepath.Join(archiveDir, name), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	}
	archive := filepath.Join(t.TempDir(), "storage.tar.gz")
	require.NoError(t, exec.Command("tar", "-C", archiveDir, "-czf", archive, ".").Run())
	const token = "delayed-rollback-test"
	remoteArchive := storageArchivePath(token)
	defer os.Remove(remoteArchive)
	require.NoError(t, copyFileForTest(archive, remoteArchive))

	install := exec.Command("bash", "-c", installStorageScript, "moox-install-storage", "0", "0", "0", token, remoteArchive)
	install.Env = storageInstallerEnv(t, home)
	output, err := install.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Equal(t, "facts", string(requireFile(t, filepath.Join(deploy, "data", "facts.db"))))

	rollback := exec.Command("bash", "-c", rollbackStorageScript, "moox-rollback-storage", token)
	rollback.Env = storageInstallerEnv(t, home)
	output, err = rollback.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Equal(t, "facts", string(requireFile(t, filepath.Join(deploy, "data", "facts.db"))))
}

func TestStorageFinalizeUsesStagingMarkerAgeForCleanup(t *testing.T) {
	home := t.TempDir()
	const token = "finalize-marker-test"
	deploy := filepath.Join(home, "moox", "storage")
	other := filepath.Join(home, "moox", "storage.previous.other")
	require.NoError(t, os.MkdirAll(deploy, 0o700))
	require.NoError(t, os.MkdirAll(other, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, ".storage-activation-token"), []byte(token+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(other, ".storage-staged-at"), []byte("now\n"), 0o600))
	old := time.Now().Add(-72 * time.Hour)
	require.NoError(t, os.Chtimes(other, old, old))

	run := func() {
		command := exec.Command("bash", "-c", finalizeStorageScript, "moox-finalize-storage", token)
		command.Env = storageInstallerEnv(t, home)
		output, err := command.CombinedOutput()
		require.NoError(t, err, string(output))
	}
	run()
	require.DirExists(t, other, "a recently staged transaction must survive even when its renamed directory has an old mtime")
	require.NoError(t, os.Chtimes(filepath.Join(other, ".storage-staged-at"), old, old))
	run()
	require.NoDirExists(t, other)
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
	require.Contains(t, installControlScript, `for healthcheck in "$HOME"/moox/*/healthcheck.sh`, "the host watchdog must cover every installed MooX package root")
	require.Contains(t, installControlScript, `# moox-healthchecks`, "the host watchdog cron entry must be replaced idempotently")
	require.Contains(t, installControlScript, `*.next.*|*.previous|*.previous.*|*.failed`, "the host watchdog must ignore deployment staging and rollback directories")
	require.Contains(t, installStorageScript, `for healthcheck in "$HOME"/moox/*/healthcheck.sh`, "an independently installed Storage package must register the host watchdog")
	require.Contains(t, installStorageScript, `"$deploy.maintenance.lock"`, "storage install and host watchdogs must share the deployment maintenance lock")
	require.Contains(t, installStorageScript, `"$deploy/start.sh" 8>&-`, "storage services must not inherit the deployment maintenance lock")
	require.Contains(t, installStorageScript, `reset_view_data="$2"`, "view reset must be an explicit installer flag")
	require.Contains(t, installStorageScript, `moox-storage-cli" reset-view-consumers`, "view reset must run before the new Storage process starts")
	require.Contains(t, installStorageScript, `--maintenance-lock-held --yes`, "view reset must reuse the installer's maintenance lock")
	require.Contains(t, installStorageScript, `credential="$HOME/.config/moox/eventbus/internal-admin.yaml"`, "view reset must use JetStream management credentials")
	require.Contains(t, rollbackStorageScript, `"$deploy.maintenance.lock"`, "storage rollback must serialize with watchdogs")
	require.Contains(t, rollbackStorageScript, `"$deploy/start.sh" 8>&-`, "rolled-back services must not inherit the deployment maintenance lock")
	require.Contains(t, finalizeStorageScript, `-mtime +1`, "old tokenized staging directories must be reclaimed after a successful deployment")
	require.Contains(t, finalizeStorageScript, `storage.maintenance.lock`, "storage finalize must serialize with watchdogs")
	require.Contains(t, installControlScript, "systemctl is-enabled --quiet", "control install must verify that cron survives reboot")
	require.Contains(t, installControlScript, `"$deploy.maintenance.lock"`, "installer and healthcheck must share a lock outside the renamed deployment directory")
	require.Contains(t, installControlScript, `"$deploy/start.sh" 8>&-`, "services must not inherit the deployment maintenance lock")
	require.Contains(t, installControlScript, `--config "$deploy/config/caddy/Caddyfile.next" 8>&-`, "Caddy must not inherit the deployment maintenance lock")
	require.Contains(t, installControlScript, `MOOX_RESET_CONTROL_DATA="$reset_data" MOOX_EVENTBUS_ROTATE_CREDENTIALS="$rotate_eventbus" nohup "$deploy/start.sh" 8>&-`, "control reset must preserve or rotate external EventBus identities before startup")
	require.Contains(t, installControlScript, "MOOX_PRESERVE_EXTERNAL_EVENTBUS_CREDENTIALS=1", "control reset must persist external EventBus identity preservation for later restarts")
	require.Contains(t, installControlScript, `grep -q '^MOOX_PRESERVE_EXTERNAL_EVENTBUS_CREDENTIALS=1$' "$old_components"`, "subsequent control upgrades must retain the reset EventBus identity policy")
	require.Contains(t, installControlScript, `rotate_eventbus=1`, "reset with a changed EventBus endpoint must rotate the TLS bundle")
	require.Contains(t, installControlScript, `restore_eventbus_backup`, "failed endpoint rotation must restore the previous EventBus credentials")
	require.Contains(t, installControlScript, `eventbus.previous.$activation_token`, "EventBus rollback backup must be bound to the activation token")
	require.Contains(t, installControlScript, "rm -rf \"$deploy\"\n    restore_eventbus_backup\n    if [ -d \"$previous\" ]; then\n      mv \"$previous\" \"$deploy\"\n      mkdir -p \"$deploy/logs\"\n      \"$deploy/start.sh\"", "rollback must restore old EventBus credentials before restarting the previous deployment")
	require.Contains(t, rollbackControlScript, `mv "$eventbus_backup" "$HOME/.config/moox/eventbus"`, "outer rollback must restore EventBus credentials before starting the previous deployment")
	require.Contains(t, finalizeControlScript, `rm -rf "$eventbus_backup"`, "finalize must remove the retained EventBus rollback backup")
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

func storageInstallerEnv(t *testing.T, home string) []string {
	t.Helper()
	crontabLog := filepath.Join(t.TempDir(), "crontab")
	toolsDir := t.TempDir()
	crontabCommand := filepath.Join(toolsDir, "crontab")
	require.NoError(t, os.WriteFile(crontabCommand, []byte("#!/bin/sh\nif [ \"$1\" = -l ]; then exit 0; fi\ncat >\"$MOOX_CRONTAB_LOG\"\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(toolsDir, "flock"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	return append(os.Environ(),
		"HOME="+home,
		"PATH="+toolsDir+":"+os.Getenv("PATH"),
		"MOOX_CRONTAB_COMMAND="+crontabCommand,
		"MOOX_CRONTAB_LOG="+crontabLog,
		"MOOX_CRON_DAEMON_CHECK_COMMAND=/usr/bin/true",
	)
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
	events              *[]string
	uploadPath          string
	uploadMode          fs.FileMode
	uploaded            bytes.Buffer
	commands            [][]string
	unameOS             string
	unameArch           string
	failFinalize        bool
	failStorageRollback bool
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
		if f.failStorageRollback {
			return setupssh.Result{}, os.ErrDeadlineExceeded
		}
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
