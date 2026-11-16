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
	environment := func() map[string]string { return map[string]string{} }
	input := inputs{request: Request{RuntimeID: strings.Repeat("j", 32), Topology: hostbundle.Topology{ControlHostID: "control"}, Host: Unit{UnitRoot: filepath.Join(directory, "host"), Environment: map[string]map[string]string{"host-gateway": environment(), "host-agent": environment()}}, Control: Unit{UnitRoot: filepath.Join(directory, "control"), Environment: map[string]map[string]string{"admin": environment(), "eventbus": environment()}}}}
	require.NoError(t, bindRuntimeIdentity(deployment, input))
	require.NoError(t, fsutil.WritePrivate(deployment, "host/current", []byte("synthetic current marker"), false))
	require.NoError(t, bindRuntimeIdentity(deployment, input))
	raw, err := deployment.ReadFile("identity/runtime.json")
	require.NoError(t, err)
	require.NotContains(t, string(raw), strings.Repeat("j", 32))
	input.request.RuntimeID = strings.Repeat("k", 32)
	require.ErrorContains(t, bindRuntimeIdentity(deployment, input), "restore original native state")
	input.request.RuntimeID = strings.Repeat("j", 32)
	require.NoError(t, deployment.Remove("identity/runtime.json"))
	require.ErrorContains(t, bindRuntimeIdentity(deployment, input), "lacks its persistent runtime identity")
}
