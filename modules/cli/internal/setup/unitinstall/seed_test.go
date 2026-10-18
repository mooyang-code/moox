package unitinstall

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/stretchr/testify/require"
)

func TestStateSeedBindsIndependentBytesAndRefusesMutation(t *testing.T) {
	unit := privateParent(t)
	prepared := Prepared{Version: 1, HostID: "control", DeploymentRoot: unit, UnitRoot: unit, Profile: "control", Directory: filepath.Join(unit, "releases/new"), Components: []string{"admin", "web-host"}}
	seed := StateSeed{Version: 1, HostID: prepared.HostID, DeploymentRoot: unit, UnitRoot: unit, Profile: prepared.Profile, Directory: filepath.Join(unit, "state-imports/first"), ReleaseDirectory: prepared.Directory, Paths: []string{"admin/data"}}
	require.NoError(t, os.MkdirAll(filepath.Join(seed.Directory, "admin/data"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(prepared.Directory, "admin"), 0o700))
	file := filepath.Join(seed.Directory, "admin/data/admin.db")
	require.NoError(t, os.WriteFile(file, []byte("offline-initialized-snapshot"), 0o600))
	root, err := openSeedRoot(seed.Directory)
	require.NoError(t, err)
	defer root.Close()
	seed.Files, err = stateInventory(t.Context(), root, seed.Paths)
	require.NoError(t, err)
	raw, err := json.Marshal(seed)
	require.NoError(t, err)
	raw = append(raw, '\n')
	require.NoError(t, fsutil.WritePrivate(root, "state-seed.json", raw, false))
	reference := StateSeedReference{Directory: seed.Directory, SHA256: stateDigest(raw)}
	verified, err := loadStateSeed(t.Context(), &reference, prepared, "")
	require.NoError(t, err)
	require.NoError(t, importState(t.Context(), verified, prepared))
	copyPath := filepath.Join(prepared.Directory, "admin/data/admin.db")
	before, err := os.Stat(file)
	require.NoError(t, err)
	after, err := os.Stat(copyPath)
	require.NoError(t, err)
	require.False(t, os.SameFile(before, after))
	// A same-length change must fail, including a mutation that occurs after
	// preflight but before copying. Size-only checks cannot prove a snapshot.
	require.NoError(t, os.WriteFile(file, []byte("offline-initialized-SNAPSHOT"), 0o600))
	_, err = loadStateSeed(t.Context(), &reference, prepared, "")
	require.ErrorContains(t, err, "changed after sealing")
	require.NoError(t, os.RemoveAll(filepath.Join(prepared.Directory, "admin/data")))
	require.ErrorContains(t, importState(t.Context(), verified, prepared), "does not match")
	require.NoError(t, os.WriteFile(file, []byte("offline-initialized-snapshot"), 0o600))
	_, err = loadStateSeed(t.Context(), &reference, prepared, filepath.Join(unit, "releases/old"))
	require.ErrorContains(t, err, "previous current")
	other := prepared
	other.HostID = "other-host"
	_, err = loadStateSeed(t.Context(), &reference, other, "")
	require.Error(t, err)
	other = prepared
	other.Directory = filepath.Join(unit, "releases/other")
	_, err = loadStateSeed(t.Context(), &reference, other, "")
	require.Error(t, err)
	wrongDigest := reference
	wrongDigest.SHA256 = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, err = loadStateSeed(t.Context(), &wrongDigest, prepared, "")
	require.ErrorContains(t, err, "producer digest")
}

func TestStateSeedRefusesUnrelatedFilesAndUnsafeDirectories(t *testing.T) {
	prepared := Prepared{HostID: "control", DeploymentRoot: "/unit", UnitRoot: "/unit", Profile: "control", Directory: "/unit/releases/new", Components: []string{"admin"}}
	seed := StateSeed{Version: 1, HostID: "control", DeploymentRoot: "/unit", UnitRoot: "/unit", Profile: "control", Directory: "/unit/state-imports/first", ReleaseDirectory: prepared.Directory, Paths: []string{"admin/data"}}
	require.NoError(t, validateSeedBinding(seed, prepared, ""))
	for _, paths := range [][]string{{"admin/config"}, {"host-gateway/data"}, {"admin/data", "admin/data"}, {"../data"}, {"/admin/data"}, nil} {
		invalid := seed
		invalid.Paths = paths
		require.Error(t, validateSeedBinding(invalid, prepared, ""))
	}
	invalid := seed
	invalid.Directory = "/other/state-imports/first"
	require.Error(t, validateSeedBinding(invalid, prepared, ""))
	directory := privateParent(t)
	require.NoError(t, os.MkdirAll(filepath.Join(directory, "admin/data"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "admin/data/admin.db"), []byte("snapshot"), 0o600))
	root, err := fsutil.OpenPhysicalRoot(directory, true)
	require.NoError(t, err)
	defer root.Close()
	require.NoError(t, validateSeedTree(t.Context(), root, seed.Paths))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "private-master.key"), []byte("must-stay-outside-state"), 0o600))
	require.Error(t, validateSeedTree(t.Context(), root, seed.Paths))
	require.NoError(t, os.Remove(filepath.Join(directory, "private-master.key")))
	require.NoError(t, os.Symlink("admin.db", filepath.Join(directory, "admin/data/link")))
	require.Error(t, validateSeedTree(t.Context(), root, seed.Paths))
	_, err = stateInventory(t.Context(), root, seed.Paths)
	require.Error(t, err)
	require.NoError(t, os.Remove(filepath.Join(directory, "admin/data/link")))
	require.NoError(t, os.Chmod(filepath.Join(directory, "admin/data/admin.db"), 0o644))
	require.Error(t, validateSeedTree(t.Context(), root, seed.Paths))
	require.NoError(t, os.Chmod(filepath.Join(directory, "admin/data/admin.db"), 0o600))
	linked := filepath.Join(privateParent(t), "linked.db")
	require.NoError(t, os.Link(filepath.Join(directory, "admin/data/admin.db"), linked))
	require.Error(t, validateSeedTree(t.Context(), root, seed.Paths))
	_, err = stateInventory(t.Context(), root, seed.Paths)
	require.Error(t, err, "state producers must not alias a live database through a hard link")
	require.NoError(t, os.Remove(linked))
	_, err = stateInventory(t.Context(), root, []string{"admin/var"})
	require.Error(t, err, "missing declared directories cannot silently become empty imports")
}

func TestStateSeedRequestRejectsNonCanonicalPrivateInput(t *testing.T) {
	filename := filepath.Join(privateParent(t), "request.json")
	options := SealStateOptions{Directory: "/unit/state-imports/first", ReleaseDirectory: "/unit/releases/new", Paths: []string{"admin/data"}}
	raw, err := json.Marshal(options)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filename, append(raw, '\n'), 0o600))
	loaded, err := ReadSealStateRequest(filename)
	require.NoError(t, err)
	require.Equal(t, options, loaded)
	for _, raw := range []string{`{"unknown":"private-marker-must-not-echo"}`, `{"directory":"private-marker-must-not-echo","directory":"another"}`, `{} {"private":"private-marker-must-not-echo"}`} {
		require.NoError(t, os.WriteFile(filename, []byte(raw), 0o600))
		_, err := ReadSealStateRequest(filename)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "private-marker-must-not-echo")
	}
	require.NoError(t, os.Chmod(filename, 0o644))
	_, err = ReadSealStateRequest(filename)
	require.Error(t, err)
}
