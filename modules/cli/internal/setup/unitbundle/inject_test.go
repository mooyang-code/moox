package unitbundle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostMaterialInjectsOnlySelectedUnitIdentities(t *testing.T) {
	f := newFixture(t, "control", []string{"admin", "console-proxy", "access"}, true)
	material, err := Load(t.Context(), f.write(t), f.options)
	require.NoError(t, err)
	for _, components := range [][]string{{"host-gateway", "host-agent"}, {"admin", "console-proxy"}, {"access"}} {
		t.Run(components[0], func(t *testing.T) {
			directory := privateParent(t)
			if components[0] == "host-gateway" {
				require.NoError(t, os.MkdirAll(filepath.Join(directory, "host-gateway/config"), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(directory, "host-gateway/config/app.yaml"), []byte("template"), 0o644))
			}
			projection, err := material.Inject(t.Context(), directory, components)
			require.NoError(t, err)
			raw, err := os.ReadFile(filepath.Join(directory, "identity.json"))
			require.NoError(t, err)
			var saved Projection
			require.NoError(t, json.Unmarshal(raw, &saved))
			require.Equal(t, projection, saved)
			for _, credential := range projection.Credentials {
				require.NotEqual(t, "moox-cli", credential.Caller)
				raw, err := os.ReadFile(filepath.Join(directory, credential.KeyFile))
				require.NoError(t, err)
				require.Equal(t, f.files[credential.KeyFile], raw)
			}
			if slices.Contains(components, "host-gateway") {
				require.Len(t, projection.Credentials, 2)
				require.FileExists(t, filepath.Join(directory, "certs/moox-ca.crt"))
			} else {
				_, err := os.Lstat(filepath.Join(directory, "certs"))
				require.True(t, os.IsNotExist(err))
			}
			if slices.Contains(components, "console-proxy") {
				require.Len(t, projection.Credentials, 3)
				require.FileExists(t, filepath.Join(directory, "secrets/caller-console.key"))
			}
			if slices.Contains(components, "access") {
				require.FileExists(t, filepath.Join(directory, f.metadata.VerificationFile))
			} else {
				_, err := os.Lstat(filepath.Join(directory, "secrets/access"))
				require.True(t, os.IsNotExist(err))
			}
			_, err = os.Lstat(filepath.Join(directory, "operator"))
			require.True(t, os.IsNotExist(err))
		})
	}
}

func TestHostMaterialInjectionRejectsUnrelatedUnitsAndUnsafeDestinations(t *testing.T) {
	f := newFixture(t, "compute1", []string{"access", "trade"}, false)
	material, err := Load(t.Context(), f.write(t), f.options)
	require.NoError(t, err)
	for _, components := range [][]string{nil, {"moox-cli"}, {"admin"}, {"access", "access"}, {"host-gateway"}, {"host-agent", "host-gateway", "access"}} {
		directory := privateParent(t)
		_, err := material.Inject(t.Context(), directory, components)
		require.Error(t, err)
		entries, err := os.ReadDir(directory)
		require.NoError(t, err)
		require.Empty(t, entries)
	}
	directory := privateParent(t)
	other := privateParent(t)
	require.NoError(t, os.Symlink(other, filepath.Join(directory, "secrets")))
	_, err = material.Inject(t.Context(), directory, []string{"trade"})
	require.Error(t, err)
	entries, err := os.ReadDir(other)
	require.NoError(t, err)
	require.Empty(t, entries)
}
