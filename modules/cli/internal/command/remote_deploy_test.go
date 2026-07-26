package command

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupdeploy "github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
)

type remoteArchivePackager string

func (p remoteArchivePackager) Package(context.Context, setupdeploy.Options) (string, error) {
	return string(p), nil
}

func TestRemoteDeployCurrentControlArchive(t *testing.T) {
	if os.Getenv("MOOX_REMOTE_DEPLOY_E2E") != "1" {
		t.Skip("set MOOX_REMOTE_DEPLOY_E2E=1 to run the destructive remote deployment test")
	}
	archive := strings.TrimSpace(os.Getenv("MOOX_REMOTE_CONTROL_ARCHIVE"))
	if archive == "" || !filepath.IsAbs(archive) {
		t.Fatal("MOOX_REMOTE_CONTROL_ARCHIVE must be an absolute archive path")
	}
	root, err := filepath.Abs("../../../../")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := setupconfig.Load(filepath.Join(root, "custom.toml"), root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	transport, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	err = setupdeploy.Control(ctx, transport, setupdeploy.Options{
		RepositoryRoot:        root,
		PublicHost:            snapshot.Manifest.ControlHost.Address,
		BrowserPort:           9527,
		EventBusPublicAddress: snapshot.Manifest.EventBus.PublicAddress,
		EventBusPort:          snapshot.Manifest.EventBus.Port,
		EventBusTLSEnabled:    true,
	}, setupdeploy.Dependencies{Packager: remoteArchivePackager(archive)})
	if err != nil {
		t.Fatal(err)
	}
}
