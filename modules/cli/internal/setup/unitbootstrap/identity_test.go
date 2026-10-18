package unitbootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/stretchr/testify/require"
)

func TestBootstrapRuntimeIdentityCannotChangeAfterInitialization(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(directory, 0o700))
	deployment, err := fsutil.OpenPhysicalRoot(directory, true)
	require.NoError(t, err)
	defer deployment.Close()
	for _, name := range []string{"host", "control"} {
		require.NoError(t, deployment.Mkdir(name, 0o700))
	}
	environment := func() map[string]string {
		return map[string]string{"MOOX_HEALTH_AUTH_VERSION": "moox-health-v1", "MOOX_HEALTH_AUTH_ACCESS_KEY": "synthetic-monitor", "MOOX_HEALTH_AUTH_SECRET_KEY": strings.Repeat("h", 32)}
	}
	admin := environment()
	admin["MOOX_ADMIN_JWT_SECRET_KEY"] = strings.Repeat("j", 32)
	input := inputs{request: Request{Topology: hostbundle.Topology{ControlHostID: "control"}, Host: Unit{UnitRoot: filepath.Join(directory, "host"), Environment: map[string]map[string]string{"host-gateway": environment(), "host-agent": environment()}}, Control: Unit{UnitRoot: filepath.Join(directory, "control"), Environment: map[string]map[string]string{"admin": admin, "eventbus": environment()}}}}
	require.NoError(t, bindRuntimeIdentity(deployment, input))
	require.NoError(t, fsutil.WritePrivate(deployment, "host/current", []byte("synthetic current marker"), false))
	require.NoError(t, bindRuntimeIdentity(deployment, input))
	raw, err := deployment.ReadFile("identity/runtime.json")
	require.NoError(t, err)
	require.NotContains(t, string(raw), strings.Repeat("j", 32))
	require.NotContains(t, string(raw), strings.Repeat("h", 32))
	admin["MOOX_ADMIN_JWT_SECRET_KEY"] = strings.Repeat("k", 32)
	require.ErrorContains(t, bindRuntimeIdentity(deployment, input), "restore original native state")
	admin["MOOX_ADMIN_JWT_SECRET_KEY"] = strings.Repeat("j", 32)
	require.NoError(t, deployment.Remove("identity/runtime.json"))
	require.ErrorContains(t, bindRuntimeIdentity(deployment, input), "lacks its persistent runtime identity")
}
