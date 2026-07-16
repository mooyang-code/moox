package deploy

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"net"
	"os"
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
	opts := Options{RepositoryRoot: dir, PublicHost: "203.0.113.8", BrowserPort: 9527}

	err := Control(context.Background(), transport, opts, Dependencies{Packager: packager, Probe: probe})
	require.NoError(t, err)
	require.Equal(t, []string{
		"package", "upload", "install", "start", "admin_ready", "setup_ready",
		"gateway_ready", "web_ready", "browser_https_ready", "cleanup",
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

func TestControlCleansRemoteArchiveAfterFailure(t *testing.T) {
	t.Parallel()
	archive := filepath.Join(t.TempDir(), "control.tar.gz")
	require.NoError(t, os.WriteFile(archive, []byte("package"), 0o600))
	events := []string{}
	transport := &fakeTransport{events: &events}
	probe := &fakeProbe{events: &events, failAt: AdminReady}

	err := Control(context.Background(), transport, Options{
		RepositoryRoot: t.TempDir(), PublicHost: "control.example.test", BrowserPort: 9527,
	}, Dependencies{Packager: &fakePackager{path: archive, events: &events}, Probe: probe})
	require.EqualError(t, err, "control_deploy_not_ready")
	require.Equal(t, "cleanup", events[len(events)-1])
	_, statErr := os.Stat(archive)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

type fakePackager struct {
	path     string
	events   *[]string
	captured string
}

func (f *fakePackager) Package(_ context.Context, opts Options) (string, error) {
	*f.events = append(*f.events, "package")
	f.captured = opts.RepositoryRoot + opts.PublicHost
	return f.path, nil
}

type fakeTransport struct {
	events     *[]string
	uploadPath string
	uploadMode fs.FileMode
	uploaded   bytes.Buffer
	commands   [][]string
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
	if len(argv) >= 3 && strings.Contains(argv[2], "install_control") {
		*f.events = append(*f.events, "install", "start")
	} else {
		*f.events = append(*f.events, "cleanup")
	}
	return setupssh.Result{}, nil
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
