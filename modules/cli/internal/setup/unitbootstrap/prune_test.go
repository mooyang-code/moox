package unitbootstrap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitinstall"
	"github.com/stretchr/testify/require"
)

func TestPruneAttemptsKeepsRunningAndPreviousRelease(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	host, control := filepath.Join(root, "host"), filepath.Join(root, "prod")
	for _, directory := range []string{
		filepath.Join(host, "releases/bootstrap-OK"), filepath.Join(host, "releases/bootstrap-FAILED"),
		filepath.Join(control, "releases/bootstrap-OK"), filepath.Join(control, "releases/bootstrap-OLD"), filepath.Join(control, "releases/bootstrap-FAILED"),
		filepath.Join(root, "bootstrap/bootstrap-OK/offline"), filepath.Join(root, "bootstrap/bootstrap-OLD"), filepath.Join(root, "bootstrap/bootstrap-FAILED/offline"), filepath.Join(root, "bootstrap/initial"),
	} {
		require.NoError(t, os.MkdirAll(directory, 0o700))
	}
	raw, err := json.Marshal(unitinstall.Activation{Version: 1, HostID: "control", UnitRoot: control, PreviousDirectory: filepath.Join(control, "releases/bootstrap-OLD"), Directory: filepath.Join(control, "releases/bootstrap-OK"), Phase: "active", UpdatedAt: time.Unix(1, 0).UTC()})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(control, "activation.json"), append(raw, '\n'), 0o600))
	request := Request{DeploymentRoot: root, Host: Unit{UnitRoot: host}, Control: Unit{UnitRoot: control}}
	pruneAttempts(request, journal{Host: unitState{Previous: filepath.Join(host, "releases/bootstrap-OK")}, Control: unitState{Previous: filepath.Join(control, "releases/bootstrap-OK")}})
	for _, kept := range []string{"host/releases/bootstrap-OK", "prod/releases/bootstrap-OK", "prod/releases/bootstrap-OLD", "bootstrap/bootstrap-OK/offline", "bootstrap/bootstrap-OLD", "bootstrap/initial"} {
		require.DirExists(t, filepath.Join(root, kept))
	}
	for _, removed := range []string{"host/releases/bootstrap-FAILED", "prod/releases/bootstrap-FAILED", "bootstrap/bootstrap-FAILED"} {
		require.NoDirExists(t, filepath.Join(root, removed))
	}
}
