package unitbundle

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/stretchr/testify/require"
)

// This fixture contains public metadata/options only, produced by the Linux
// gate from its isolated Admin DB. It is never an operator moox.toml.
type issuedFixture struct {
	Options  Options             `json:"options"`
	Metadata hostbundle.Metadata `json:"metadata"`
}

func TestHostMaterialConsumesActualAdminProducer(t *testing.T) {
	filename := os.Getenv("MOOX_HOST_MATERIAL_FIXTURE")
	if filename == "" {
		t.Skip("requires isolated real Admin CLI fixture; mandatory in the Linux bundle gate")
	}
	raw, err := os.ReadFile(filename)
	require.NoError(t, err)
	var fixtures []issuedFixture
	require.NoError(t, json.Unmarshal(raw, &fixtures))
	require.Len(t, fixtures, 4)
	var client setupssh.Client
	if address := os.Getenv("MOOX_HOST_MATERIAL_SSH_ADDRESS"); address != "" {
		client, err = setupssh.Dial(t.Context(), setupssh.Target{Name: "bundle-test", Address: address, Port: 22, Username: os.Getenv("MOOX_HOST_MATERIAL_SSH_USERNAME")}, "", setupssh.Options{KnownHostsPath: os.Getenv("MOOX_HOST_MATERIAL_KNOWN_HOSTS")})
		require.NoError(t, err)
		defer client.Close()
	}
	for _, fixture := range fixtures {
		name := fixture.Metadata.HostID
		if fixture.Options.AllowOperator {
			name += "-bootstrap"
		}
		t.Run(name, func(t *testing.T) {
			var material *Material
			var err error
			if client != nil {
				material, err = Fetch(t.Context(), client.Download, fixture.Metadata, fixture.Options, filepath.Join(privateParent(t), "ssh-fetched"))
			} else {
				material, err = Load(t.Context(), fixture.Metadata.BundleDir, fixture.Options)
			}
			require.NoError(t, err)
			require.Equal(t, fixture.Metadata, material.Metadata())
			if binary := os.Getenv("MOOX_RUNTIME_BINARY"); binary != "" {
				options := fixture.Options
				args := []string{"inspect-bundle", "--directory", material.Directory(), "--host-id", options.HostID, "--control-host-id", options.ControlHostID, "--address", options.Address, "--private-address", options.PrivateAddress, "--control-address", options.ControlAddress, "--ca-sha256", options.ExpectedCA, "--snapshot-sha256", options.ExpectedHash, "--components", strings.Join(options.Components, ",")}
				if options.AllowOperator {
					args = append(args, "--bootstrap-operator")
				}
				output, err := exec.CommandContext(t.Context(), binary, args...).CombinedOutput()
				require.NoError(t, err, "host helper must validate the real Admin bundle")
				require.NotContains(t, string(output), "PRIVATE KEY")
				var metadata hostbundle.Metadata
				require.NoError(t, json.Unmarshal(output, &metadata))
				require.Equal(t, fixture.Metadata, metadata)
				// The same payload cannot pass with an unrelated trust pin.
				for i, arg := range args {
					if arg == "--ca-sha256" {
						args[i+1] = strings.Repeat("b", 64)
						break
					}
				}
				output, err = exec.CommandContext(t.Context(), binary, args...).CombinedOutput()
				require.Error(t, err)
				require.NotContains(t, string(output), "PRIVATE KEY")
			}
			services, err := material.PublishServices(t.Context(), filepath.Join(privateParent(t), "services"))
			require.NoError(t, err)
			options := fixture.Options
			options.AllowOperator = false
			_, err = Load(t.Context(), services.Directory(), options)
			require.NoError(t, err)
		})
	}
}
