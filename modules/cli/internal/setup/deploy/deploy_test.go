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
	opts := Options{RepositoryRoot: dir, PublicHost: "203.0.113.8", BrowserPort: 9527, TargetGOOS: "linux", TargetGOARCH: "amd64"}

	err := Control(context.Background(), transport, opts, Dependencies{Packager: packager, Probe: probe, CAStore: &fakeCAStore{events: &events}})
	require.NoError(t, err)
	require.Equal(t, []string{
		"package", "upload", "install", "start", "admin_ready", "setup_ready",
		"gateway_ready", "web_ready", "browser_https_ready", "ca", "finalize", "cleanup",
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
	require.Equal(t, "1", install[len(install)-1], "reset is a bounded positional flag, not shell text")
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
	require.NoError(t, os.WriteFile(start, []byte("#!/bin/sh\nexit 0\n"), 0o700))
	archive := filepath.Join(t.TempDir(), "storage.tar.gz")
	command := exec.Command("tar", "-C", archiveDir, "-czf", archive, ".")
	require.NoError(t, command.Run())
	previousArchive := remoteStorageArchiveNext
	defer os.Remove(previousArchive)
	require.NoError(t, copyFileForTest(archive, previousArchive))
	cmd := exec.Command("bash", "-c", installStorageScript, "moox-install-storage", "1")
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
	cmd := exec.Command("bash", "-c", installStorageScript, "moox-install-storage", "0")
	cmd.Env = append(os.Environ(), "HOME="+home)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	require.FileExists(t, filepath.Join(deploy, "data", "old.db"))
	require.FileExists(t, filepath.Join(deploy, "data", "new.db"))
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
