package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEventBusClientExportContainsOnlyRequestedRolesAndDoesNotRotate(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(directory, 0o700))
	database, key := filepath.Join(directory, "admin.db"), filepath.Join(directory, "master.key")
	require.NoError(t, os.WriteFile(key, []byte("synthetic-eventbus-client-export-master-key"), 0o600))
	require.NoError(t, applySchema(database, adminschema.AdminSQL()))
	seedEventBusDeployment(t, database)
	base := []string{"--db-path", database, "--encryption-key-file", key, "--node-id", "gateway-node-1"}
	var output bytes.Buffer
	require.NoError(t, runEventBusCredentialsCommand(append([]string{"eventbus-credentials", "ensure"}, base...), &output, &bytes.Buffer{}))
	db, err := gorm.Open(sqlite.Open(database), &gorm.Config{})
	require.NoError(t, err)
	defer closeAdminCLIDB(db)
	var before []map[string]any
	require.NoError(t, db.Table("t_secrets").Order("c_id").Find(&before).Error)
	clientDir := filepath.Join(directory, "clients")
	args := append([]string{"eventbus-credentials", "export-clients"}, base...)
	args = append(args, "--roles", "metrics-publisher,hostagent-publisher", "--output-dir", clientDir)
	output.Reset()
	require.NoError(t, runEventBusCredentialsCommand(args, &output, &bytes.Buffer{}))
	var metadata eventBusClientsResult
	require.NoError(t, json.Unmarshal(output.Bytes(), &metadata))
	require.Equal(t, []string{"hostagent-publisher", "metrics-publisher"}, metadata.Roles)
	require.Len(t, metadata.Files, 3)
	entries, err := os.ReadDir(clientDir)
	require.NoError(t, err)
	require.Len(t, entries, 4)
	for _, file := range metadata.Files {
		raw, err := os.ReadFile(filepath.Join(clientDir, file.Path))
		require.NoError(t, err)
		require.Equal(t, file.SHA256, eventBusClientDigest(raw))
		require.Equal(t, file.Size, int64(len(raw)))
		require.NotContains(t, output.String(), string(raw))
		info, err := os.Stat(filepath.Join(clientDir, file.Path))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	var after []map[string]any
	require.NoError(t, db.Table("t_secrets").Order("c_id").Find(&after).Error)
	require.Equal(t, before, after)
	require.Error(t, runEventBusCredentialsCommand(args, &bytes.Buffer{}, &bytes.Buffer{}))
	for _, roles := range []string{"server", "ca-private", "hostagent-publisher,hostagent-publisher", ""} {
		require.Error(t, exportEventBusClients(t.Context(), db, "gateway-node-1", filepath.Join(directory, "refused"), roles, &bytes.Buffer{}))
		_, err := os.Lstat(filepath.Join(directory, "refused"))
		require.True(t, os.IsNotExist(err))
	}
}
