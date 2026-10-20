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

func TestBootstrapLinuxProxyCAConsumesAuthorization(t *testing.T) {
	request, fixture := bootstrapFixture(t)
	extracted, err := unitpackage.Extract(t.Context(), unitpackage.ExtractOptions{Archive: request.Control.Archive, ExpectedSHA256: request.Control.SHA256, Profile: "control", GOOS: "linux", GOARCH: runtime.GOARCH, Destination: filepath.Join(fixture, "offline-software")})
	require.NoError(t, err)
	binary := filepath.Join(extracted.Directory, "bin/moox-console-proxy")
	for _, scenario := range []string{"generated", "public"} {
		t.Run(scenario, func(t *testing.T) {
			deployment := filepath.Join(fixture, scenario)
			require.NoError(t, os.MkdirAll(filepath.Join(deployment, "identity"), 0o700))
			attemptName := filepath.Join(deployment, "attempt")
			require.NoError(t, os.Mkdir(attemptName, 0o700))
			attempt, err := fsutil.OpenPhysicalRoot(attemptName, true)
			require.NoError(t, err)
			defer attempt.Close()
			candidate := filepath.Join(deployment, "candidate")
			require.NoError(t, os.MkdirAll(filepath.Join(candidate, "bin"), 0o700))
			require.NoError(t, os.Link(binary, filepath.Join(candidate, "bin/moox-console-proxy")))
			candidateRoot, err := fsutil.OpenPhysicalRoot(candidate, true)
			require.NoError(t, err)
			defer candidateRoot.Close()
			mode := "internal"
			if scenario == "public" {
				mode = "public"
			}
			require.NoError(t, fsutil.WritePrivate(candidateRoot, "console-proxy/config/app.yaml", []byte("tls:\n  mode: "+mode+"\n"), false))
			input := inputs{request: Request{DeploymentRoot: deployment}}
			j := journal{HostID: "control"}
			prepared := unitinstall.Prepared{Directory: candidate, Components: []string{"console-proxy"}}
			err = unitruntime.WithMaintenance(t.Context(), deployment, "control", unitruntime.Options{}, func(guard *unitruntime.Maintenance) error {
				return guard.UseLock(t.Context(), func(lock unitruntime.Options) error {
					checkpoint := func() (string, error) { return proxyCheckpoint(t.Context(), input, &j, prepared, attempt, lock) }
					receiptPath := filepath.Join(deployment, "identity/console-proxy/receipt.json")
					if scenario != "public" {
						_, err := checkpoint()
						require.Error(t, err, "missing files never authorize CA creation")
						_, err = os.Stat(receiptPath)
						require.True(t, os.IsNotExist(err), "refused input must not consume authorization")
					}
					if scenario == "generated" {
						input.request.ProxyCA.Create = true
					}
					material, err := checkpoint()
					require.NoError(t, err)
					root, err := fsutil.OpenPhysicalRoot(filepath.Dir(receiptPath), true)
					require.NoError(t, err)
					defer root.Close()
					raw, err := fsutil.ReadPrivate(root, "receipt.json", 4096)
					require.NoError(t, err)
					var receipt proxyReceipt
					require.NoError(t, decode(raw, &receipt))
					require.Equal(t, "ready", receipt.Phase)
					require.Equal(t, scenario, receipt.Origin)
					require.True(t, receipt.valid("control", mode))
					again, err := checkpoint()
					require.NoError(t, err)
					require.Equal(t, material, again)
					if scenario == "generated" {
						// Simulate SIGKILL after material publication but before the
						// final receipt write: the published CA must be reused.
						pending := receipt
						pending.Phase, pending.CA = "pending", ""
						require.NoError(t, writeProxyReceipt(root, pending, true))
						_, err := checkpoint()
						require.NoError(t, err)
						restored, err := fsutil.ReadPrivate(root, "receipt.json", 4096)
						require.NoError(t, err)
						require.Equal(t, raw, restored, "publication recovery must preserve the original CA")
					}
					// Deleting consumed material must never reuse the original
					// create flag, even when its receipt remains intact.
					require.NoError(t, os.Rename(material, material+".saved"))
					_, err = checkpoint()
					require.Error(t, err)
					_, err = os.Stat(material)
					require.True(t, os.IsNotExist(err))
					require.NoError(t, os.Rename(material+".saved", material))
					if scenario == "public" {
						require.Empty(t, receipt.CA)
						_, err := os.Stat(filepath.Join(material, "console-proxy/data/caddy/caddy/pki"))
						require.True(t, os.IsNotExist(err))
					}
					return nil
				})
			})
			require.NoError(t, err)
		})
	}
}
