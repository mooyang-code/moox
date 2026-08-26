package deploy

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

func TestServicePublishesPackageAndActivatesNamedService(t *testing.T) {
	archive := serviceArchive(t, map[string]string{
		"start.sh":            "#!/bin/sh\n",
		"stop.sh":             "#!/bin/sh\n",
		"healthcheck.sh":      "#!/bin/sh\n",
		"bin/moox-web-host":   "web-host",
		"config/app.yaml":     "port: 19527\n",
		"config/trpc_go.yaml": "server: web-host\n",
	})
	digest := sha256FilePath(t, archive)
	events := []string{}
	transport := &fakeServiceTransport{events: &events, home: "/home/ubuntu", digest: digest}

	result, err := Service(context.Background(), transport, ServiceOptions{
		PackagePath: archive, ServiceName: "web-host", DeployDir: "~/moox/prod",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"home", "upload", "digest", "prepare", "activate", "finalize", "cleanup"}, events)
	require.Equal(t, "/home/ubuntu/moox/prod", result.DeployDir)
	require.Equal(t, digest, result.LocalSHA256)
	require.Equal(t, digest, result.RemoteSHA256)
	require.Equal(t, fs.FileMode(0o600), transport.uploadMode)
	require.True(t, strings.HasPrefix(transport.uploadPath, "/tmp/moox-service-"))
}

func TestServiceRejectsUnsafeZipEntryBeforeSSH(t *testing.T) {
	archive := serviceArchive(t, map[string]string{
		"start.sh":       "#!/bin/sh\n",
		"stop.sh":        "#!/bin/sh\n",
		"healthcheck.sh": "#!/bin/sh\n",
		"bin/service":    "service",
		"../escape":      "must not extract",
	})
	events := []string{}
	_, err := Service(context.Background(), &fakeServiceTransport{events: &events}, ServiceOptions{
		PackagePath: archive, ServiceName: "service", DeployDir: "/home/ubuntu/moox/prod",
	})
	require.EqualError(t, err, "service_package_invalid")
	require.Empty(t, events)
}

func TestServiceRollsBackWhenActivationFails(t *testing.T) {
	archive := serviceArchive(t, map[string]string{
		"start.sh":        "#!/bin/sh\n",
		"stop.sh":         "#!/bin/sh\n",
		"healthcheck.sh":  "#!/bin/sh\n",
		"bin/service":     "service",
		"config/app.yaml": "service: test\n",
	})
	events := []string{}
	transport := &fakeServiceTransport{events: &events, digest: sha256FilePath(t, archive), failActivate: true}
	_, err := Service(context.Background(), transport, ServiceOptions{
		PackagePath: archive, ServiceName: "service", DeployDir: "/home/ubuntu/moox/prod",
	})
	require.EqualError(t, err, "service_activate_failed")
	require.Contains(t, events, "rollback")
	require.Contains(t, events, "cleanup")
}

func TestPrepareServiceAcceptsZipDirectoryEntries(t *testing.T) {
	archive := serviceArchive(t, map[string]string{
		"bin/":            "",
		"config/":         "",
		"start.sh":        "#!/bin/sh\nexit 0\n",
		"stop.sh":         "#!/bin/sh\nexit 0\n",
		"healthcheck.sh":  "#!/bin/sh\nexit 0\n",
		"bin/service":     "service",
		"config/app.yaml": "service: test\n",
	})
	deploy := filepath.Join(t.TempDir(), "prod")
	require.NoError(t, os.MkdirAll(filepath.Join(deploy, "bin"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "stop.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "start.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(deploy, "healthcheck.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700))

	command := exec.Command("bash", "-c", prepareServiceScript, "prepare", deploy, "service", archive)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	require.FileExists(t, filepath.Join(deploy, "bin", "service"))
	require.FileExists(t, filepath.Join(deploy, "config", "app.yaml"))
}

func TestPrepareTradeServiceRunsEventBusPreflightBeforeStopping(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "stopped")
	archive := serviceArchive(t, map[string]string{
		"start.sh":           "#!/bin/sh\nexit 0\n",
		"stop.sh":            fmt.Sprintf("#!/bin/sh\necho stopped > %q\n", marker),
		"healthcheck.sh":     "#!/bin/sh\nexit 0\n",
		"bin/moox-trade":     "trade",
		"bin/moox-trade-cli": "#!/bin/sh\nexit 1\n",
		"config/app.yaml":    "eventbus:\n  enabled: true\n",
	})
	deploy := filepath.Join(t.TempDir(), "prod")
	command := exec.Command("bash", "-c", prepareServiceScript, "prepare", deploy, "trade", archive)
	output, err := command.CombinedOutput()
	require.Error(t, err, string(output))
	_, statErr := os.Stat(marker)
	require.ErrorIs(t, statErr, os.ErrNotExist, "existing service must not stop before EventBus preflight")
}

func serviceArchive(t *testing.T, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "service.zip")
	file, err := os.Create(path)
	require.NoError(t, err)
	writer := zip.NewWriter(file)
	for name, content := range entries {
		entry, err := writer.Create(name)
		require.NoError(t, err)
		_, err = io.WriteString(entry, content)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	require.NoError(t, file.Close())
	return path
}

func sha256FilePath(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

type fakeServiceTransport struct {
	events       *[]string
	home         string
	digest       string
	failActivate bool
	uploadPath   string
	uploadMode   fs.FileMode
	uploaded     bytes.Buffer
}

func (f *fakeServiceTransport) Check(context.Context) error { return nil }
func (f *fakeServiceTransport) ForwardLocal(context.Context, string) (net.Listener, error) {
	return nil, nil
}
func (f *fakeServiceTransport) Upload(_ context.Context, src io.Reader, _ int64, dst string, mode fs.FileMode) error {
	*f.events = append(*f.events, "upload")
	f.uploadPath, f.uploadMode = dst, mode
	_, _ = io.Copy(&f.uploaded, src)
	return nil
}
func (f *fakeServiceTransport) Run(_ context.Context, argv []string, _ io.Reader) (setupssh.Result, error) {
	command := strings.Join(argv, " ")
	switch {
	case strings.Contains(command, "printf '%s' \"$HOME\""):
		*f.events = append(*f.events, "home")
		return setupssh.Result{Stdout: f.home}, nil
	case len(argv) == 2 && argv[0] == "sha256sum":
		*f.events = append(*f.events, "digest")
		return setupssh.Result{Stdout: f.digest + "  " + argv[1] + "\n"}, nil
	case strings.Contains(command, "moox-prepare-service"):
		*f.events = append(*f.events, "prepare")
	case strings.Contains(command, "moox-activate-service"):
		*f.events = append(*f.events, "activate")
		if f.failActivate {
			return setupssh.Result{}, io.ErrUnexpectedEOF
		}
	case strings.Contains(command, "moox-rollback-service"):
		*f.events = append(*f.events, "rollback")
	case strings.Contains(command, "moox-finalize-service"):
		*f.events = append(*f.events, "finalize")
	case strings.Contains(command, "rm -f"):
		*f.events = append(*f.events, "cleanup")
	}
	return setupssh.Result{}, nil
}
func (f *fakeServiceTransport) Close() error { return nil }
