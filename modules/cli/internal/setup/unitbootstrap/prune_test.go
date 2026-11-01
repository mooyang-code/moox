package unitbootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPruneAttemptsKeepsOnlyTheRunningRelease(t *testing.T) {
	root := t.TempDir()
	host, control := filepath.Join(root, "host"), filepath.Join(root, "prod")
	for _, directory := range []string{
		filepath.Join(host, "releases/bootstrap-OK"), filepath.Join(host, "releases/bootstrap-FAILED"),
		filepath.Join(control, "releases/bootstrap-OK"), filepath.Join(control, "releases/bootstrap-FAILED"),
		filepath.Join(root, "bootstrap/bootstrap-OK/offline"), filepath.Join(root, "bootstrap/bootstrap-FAILED/offline"), filepath.Join(root, "bootstrap/initial"),
	} {
		require.NoError(t, os.MkdirAll(directory, 0o700))
	}
	request := Request{DeploymentRoot: root, Host: Unit{UnitRoot: host}, Control: Unit{UnitRoot: control}}
	pruneAttempts(request, journal{Host: unitState{Previous: filepath.Join(host, "releases/bootstrap-OK")}, Control: unitState{Previous: filepath.Join(control, "releases/bootstrap-OK")}})
	for _, kept := range []string{"host/releases/bootstrap-OK", "prod/releases/bootstrap-OK", "bootstrap/bootstrap-OK/offline", "bootstrap/initial"} {
		require.DirExists(t, filepath.Join(root, kept))
	}
	for _, removed := range []string{"host/releases/bootstrap-FAILED", "prod/releases/bootstrap-FAILED", "bootstrap/bootstrap-FAILED"} {
		require.NoDirExists(t, filepath.Join(root, removed))
	}
}
