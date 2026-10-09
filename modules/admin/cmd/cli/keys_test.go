package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
)

type keysCLIFixture struct {
	db, master, dir string
}

func newKeysCLI(t *testing.T) keysCLIFixture {
	t.Helper()
	dir := t.TempDir()
	f := keysCLIFixture{db: filepath.Join(dir, "admin.db"), master: filepath.Join(dir, "master.key"), dir: dir}
	require.NoError(t, os.WriteFile(f.master, []byte("fixture-only-gateway-encryption-master-0123456789"), 0o600))
	require.NoError(t, applySchema(f.db, schema.AdminSQL()))
	db, err := openAdminCLIDB(f.db)
	require.NoError(t, err)
	require.NoError(t, db.Exec("INSERT INTO t_hosts (c_host_id, c_address) VALUES ('control', '192.0.2.1')").Error)
	closeAdminCLIDB(db)
	return f
}

func (f keysCLIFixture) run(t *testing.T, sub string, flags ...string) (string, error) {
	t.Helper()
	args := append([]string{"keys", sub, "--db-path", f.db, "--encryption-key-file", f.master}, flags...)
	var out, stderr bytes.Buffer
	err := runKeysCommand(args, &out, &stderr)
	return out.String(), err
}

func metadataField(t *testing.T, output, field string) string {
	t.Helper()
	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &metadata))
	value, ok := metadata[field].(string)
	require.True(t, ok)
	return value
}

func TestKeysCLIEnsureExportRotateAndConfirmedRetirement(t *testing.T) {
	f := newKeysCLI(t)
	first, err := f.run(t, "ensure", "--caller", "console")
	require.NoError(t, err)
	oldID := metadataField(t, first, "key_id")
	again, err := f.run(t, "ensure", "--caller", "console")
	require.NoError(t, err)
	require.Equal(t, first, again)
	exportDir := filepath.Join(f.dir, "secrets")
	metadata, err := f.run(t, "export", "--caller", "console", "--output-dir", exportDir)
	require.NoError(t, err)
	keyPath := metadataField(t, metadata, "key_file")
	oldKey, err := gatewayauth.CredentialsFromKeyFile(oldID, keyPath)
	require.NoError(t, err)
	require.Len(t, oldKey.Secret, 43)
	require.NotContains(t, first+metadata, oldKey.Secret)
	rotated, err := f.run(t, "rotate", "--caller", "console")
	require.NoError(t, err)
	newID := metadataField(t, rotated, "key_id")
	require.NotEqual(t, oldID, newID)
	// Rotating the master copy does not publish the signing file prematurely.
	stillOld, err := gatewayauth.CredentialsFromKeyFile(oldID, keyPath)
	require.NoError(t, err)
	require.Equal(t, oldKey.Secret, stillOld.Secret)
	_, err = f.run(t, "rotate", "--caller", "console")
	require.ErrorIs(t, err, keys.ErrRotationPending)
	_, err = f.run(t, "retire", "--caller", "console", "--key-id", oldID)
	require.ErrorContains(t, err, "--confirm")
	info, err := f.run(t, "info", "--caller", "console")
	require.NoError(t, err)
	require.Contains(t, info, oldID)
	require.Contains(t, info, newID)
	metadata, err = f.run(t, "export", "--caller", "console", "--output-dir", exportDir)
	require.NoError(t, err)
	newKey, err := gatewayauth.CredentialsFromKeyFile(newID, keyPath)
	require.NoError(t, err)
	require.NotEqual(t, oldKey.Secret, newKey.Secret)
	require.NotContains(t, rotated+info+metadata, newKey.Secret)
	_, err = f.run(t, "retire", "--caller", "console", "--key-id", newID, "--confirm")
	require.ErrorIs(t, err, keys.ErrActiveKey)
	_, err = f.run(t, "retire", "--caller", "console", "--key-id", oldID, "--confirm")
	require.NoError(t, err)
	info, err = f.run(t, "info", "--caller", "console")
	require.NoError(t, err)
	require.NotContains(t, info, oldID)
	require.Contains(t, info, newID)
	files, err := os.ReadDir(exportDir)
	require.NoError(t, err)
	require.Len(t, files, 1, "atomic writes must clean temporary secret files")
}

func TestKeysCLIAccessExportContainsOnlyExternalCallersAndOverlappingKeys(t *testing.T) {
	f := newKeysCLI(t)
	out, err := f.run(t, "ensure", "--all")
	require.NoError(t, err)
	require.Contains(t, out, "host-gateway@control")
	oldOutput, err := f.run(t, "ensure", "--caller", "factor-engine")
	require.NoError(t, err)
	oldID := metadataField(t, oldOutput, "key_id")
	dir := filepath.Join(f.dir, "access")
	_, err = f.run(t, "export", "--caller", "factor-engine", "--output-dir", dir)
	require.NoError(t, err)
	oldKey, err := gatewayauth.CredentialsFromKeyFile(oldID, filepath.Join(dir, "caller-factor-engine.key"))
	require.NoError(t, err)
	oldKey.Caller = "factor-engine"
	_, err = f.run(t, "rotate", "--caller", "factor-engine")
	require.NoError(t, err)
	out, err = f.run(t, "export-access", "--output-dir", dir)
	require.NoError(t, err)
	path := metadataField(t, out, "registry_file")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), "factor-engine")
	for _, internal := range []string{"console", "host-gateway@", "moox-cli", "storage-view"} {
		require.NotContains(t, string(raw), internal)
	}
	registry, err := gatewayauth.LoadCredentialRegistry(path)
	require.NoError(t, err)
	request := gatewayauth.Request{Method: "POST", Path: "/trpc.moox.storage.DataView/ListTables", TargetNode: "access@control", Callee: "trpc.moox.storage.DataView", Func: "ListTables"}
	header, err := gatewayauth.Sign(oldKey, request, time.Now())
	require.NoError(t, err)
	_, err = registry.Verify(request, header, time.Now())
	require.NoError(t, err, "retiring external key must remain valid in the exported Access registry")
	require.NotContains(t, out, oldKey.Secret)
	_, err = f.run(t, "retire", "--caller", "factor-engine", "--key-id", oldID, "--confirm")
	require.NoError(t, err)
	_, err = f.run(t, "export-access", "--output-dir", dir)
	require.NoError(t, err)
	registry, err = gatewayauth.LoadCredentialRegistry(path)
	require.NoError(t, err)
	_, err = registry.Verify(request, header, time.Now())
	require.Error(t, err)
}

func TestKeysCLIFailsBeforeCreatingOrChangingUnexpectedFiles(t *testing.T) {
	f := newKeysCLI(t)
	_, err := f.run(t, "export", "--caller", "console", "--output-dir", filepath.Join(f.dir, "absent"))
	require.ErrorIs(t, err, keys.ErrKeyNotFound)
	_, err = os.Stat(filepath.Join(f.dir, "absent"))
	require.True(t, os.IsNotExist(err))
	for _, args := range [][]string{
		{"keys", "unknown"}, {"keys", "ensure"}, {"keys", "ensure", "--caller", "console", "--all"},
		{"keys", "rotate", "--caller", "console", "--output-dir", f.dir},
		{"keys", "info", "--caller", "console", "unexpected"},
	} {
		require.Error(t, runKeysCommand(args, nil, nil))
	}
	_, err = f.run(t, "ensure", "--caller", "host-gateway@absent")
	require.ErrorIs(t, err, keys.ErrInvalidCaller)
	missing := f
	missing.db = filepath.Join(f.dir, "missing.db")
	_, err = missing.run(t, "ensure", "--caller", "console")
	require.Error(t, err)
	_, err = os.Stat(missing.db)
	require.True(t, os.IsNotExist(err), "key commands must not create an uninitialized database")
	missing.master = filepath.Join(f.dir, "missing.key")
	_, err = missing.run(t, "ensure", "--caller", "console")
	require.Error(t, err)
	_, err = os.Stat(missing.master)
	require.True(t, os.IsNotExist(err), "key commands must never silently replace the encryption master")
}

func TestPrivateCallerFilesRejectSymlinksLoosePermissionsAndOversize(t *testing.T) {
	f := newKeysCLI(t)
	require.NoError(t, os.Chmod(f.master, 0o644))
	_, err := f.run(t, "ensure", "--caller", "console")
	require.ErrorContains(t, err, "0600")
	info, err := os.Stat(f.master)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "validation must not silently chmod existing keys")
	require.NoError(t, os.Chmod(f.master, 0o600))
	link := filepath.Join(f.dir, "master-link")
	require.NoError(t, os.Symlink(f.master, link))
	_, err = readPrivateFile(link, 8192)
	require.Error(t, err)
	_, err = readPrivateFile(f.master, 2)
	require.Error(t, err)
	_, err = f.run(t, "ensure", "--caller", "console")
	require.NoError(t, err)
	loose := filepath.Join(f.dir, "loose")
	require.NoError(t, os.Mkdir(loose, 0o755))
	_, err = f.run(t, "export", "--caller", "console", "--output-dir", loose)
	require.ErrorContains(t, err, "0700")
	private := filepath.Join(f.dir, "private")
	require.NoError(t, os.Mkdir(private, 0o700))
	dirLink := filepath.Join(f.dir, "dir-link")
	require.NoError(t, os.Symlink(private, dirLink))
	_, err = f.run(t, "export", "--caller", "console", "--output-dir", dirLink)
	require.Error(t, err)
	outside := filepath.Join(f.dir, "outside.key")
	require.NoError(t, os.WriteFile(outside, []byte("unchanged-fixture"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(private, "caller-console.key")))
	_, err = f.run(t, "export", "--caller", "console", "--output-dir", private)
	require.Error(t, err)
	raw, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "unchanged-fixture", strings.TrimSpace(string(raw)))
	require.True(t, isKeysCommand([]string{"admin-cli", "keys"}))
	require.False(t, isKeysCommand([]string{"admin-cli", "init"}))
}
