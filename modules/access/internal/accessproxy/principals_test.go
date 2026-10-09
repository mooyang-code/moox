package accessproxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
)

func writeKeySet(t *testing.T, mode os.FileMode, keys ...gatewayauth.CallerKey) string {
	t.Helper()
	raw, err := gatewayauth.MarshalKeySet(keys)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "access-principals.json")
	require.NoError(t, os.WriteFile(path, raw, mode))
	require.NoError(t, os.Chmod(path, mode))
	return path
}

func TestLoadPrincipalKeysAcceptsCatalogPrincipals(t *testing.T) {
	path := writeKeySet(t, 0o600,
		gatewayauth.CallerKey{Caller: "scf-collector", KeyID: "scf-collector-1", Secret: "s1"},
		gatewayauth.CallerKey{Caller: "scf-collector", KeyID: "scf-collector-2", Secret: "s2"},
		gatewayauth.CallerKey{Caller: "factor-engine", KeyID: "factor-engine-1", Secret: "s3"},
	)
	registry, principals, err := LoadPrincipalKeys(path, nil)
	require.NoError(t, err)
	require.NotNil(t, registry)
	require.Equal(t, []string{"factor-engine", "scf-collector"}, principals, "轮换期间同一外部调用方可以有多把密钥")
}

func TestLoadPrincipalKeysRejectsInternalCallersAndLooseFiles(t *testing.T) {
	_, _, err := LoadPrincipalKeys(writeKeySet(t, 0o600, gatewayauth.CallerKey{Caller: "collector", KeyID: "collector-1", Secret: "s"}), nil)
	require.ErrorContains(t, err, "不是组件目录中的外部调用方", "内部调用方不能经外部接入")

	_, _, err = LoadPrincipalKeys(writeKeySet(t, 0o644, gatewayauth.CallerKey{Caller: "moox-skill", KeyID: "moox-skill-1", Secret: "s"}), nil)
	require.ErrorContains(t, err, "0600")
}
