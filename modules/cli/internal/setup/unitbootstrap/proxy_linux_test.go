//go:build linux

package unitbootstrap

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitinstall"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"github.com/stretchr/testify/require"
)

func TestBootstrapLinuxProxyCACreatedOnce(t *testing.T) {
	request, fixture := bootstrapFixture(t)
	extracted, err := unitpackage.Extract(t.Context(), unitpackage.ExtractOptions{Archive: request.Control.Archive, ExpectedSHA256: request.Control.SHA256, Profile: "control", GOOS: "linux", GOARCH: runtime.GOARCH, Destination: filepath.Join(fixture, "offline-software")})
	require.NoError(t, err)
	binary := filepath.Join(extracted.Directory, "bin/moox-console-proxy")
	deployment := filepath.Join(fixture, "generated")
	require.NoError(t, os.MkdirAll(filepath.Join(deployment, "identity"), 0o700))
	attemptName := filepath.Join(deployment, "attempt")
	require.NoError(t, os.Mkdir(attemptName, 0o700))
	attempt, err := fsutil.OpenPhysicalRoot(attemptName, true)
	require.NoError(t, err)
	defer attempt.Close()
	candidate := filepath.Join(deployment, "candidate")
	require.NoError(t, os.MkdirAll(filepath.Join(candidate, "bin"), 0o700))
	require.NoError(t, os.Link(binary, filepath.Join(candidate, "bin/moox-console-proxy")))
	input := inputs{request: Request{DeploymentRoot: deployment}}
	j := journal{HostID: "control"}
	prepared := unitinstall.Prepared{Directory: candidate, Components: []string{"console-proxy"}}
	err = unitruntime.WithMaintenance(t.Context(), deployment, "control", unitruntime.Options{}, func(guard *unitruntime.Maintenance) error {
		return guard.UseLock(t.Context(), func(lock unitruntime.Options) error {
			checkpoint := func() (string, error) { return proxyCheckpoint(t.Context(), input, &j, prepared, attempt, lock) }
			receiptPath := filepath.Join(deployment, "identity/console-proxy/receipt.json")
			material, err := checkpoint()
			require.NoError(t, err, "首次安装自动创建 CA")
			root, err := fsutil.OpenPhysicalRoot(filepath.Dir(receiptPath), true)
			require.NoError(t, err)
			defer root.Close()
			raw, err := fsutil.ReadPrivate(root, "receipt.json", 4096)
			require.NoError(t, err)
			var receipt proxyReceipt
			require.NoError(t, decode(raw, &receipt))
			require.Equal(t, "ready", receipt.Phase)
			require.True(t, receipt.valid("control"))
			again, err := checkpoint()
			require.NoError(t, err)
			require.Equal(t, material, again)
			// Simulate SIGKILL after material publication but before the
			// final receipt write: the published CA must be reused.
			pending := receipt
			pending.Phase, pending.CA = "pending", ""
			require.NoError(t, writeProxyReceipt(root, pending, true))
			_, err = checkpoint()
			require.NoError(t, err)
			restored, err := fsutil.ReadPrivate(root, "receipt.json", 4096)
			require.NoError(t, err)
			require.Equal(t, raw, restored, "publication recovery must preserve the original CA")
			// 回执还在时材料丢了，绝不能重新生成 CA。
			require.NoError(t, os.Rename(material, material+".saved"))
			_, err = checkpoint()
			require.Error(t, err)
			_, err = os.Stat(material)
			require.True(t, os.IsNotExist(err))
			require.NoError(t, os.Rename(material+".saved", material))
			return nil
		})
	})
	require.NoError(t, err)
}
