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
		require.Error(t, exportEventBusClients(t.Context(), db, "gateway-node-1", filepath.Join(directory, "refused"), roles, false, &bytes.Buffer{}))
		_, err := os.Lstat(filepath.Join(directory, "refused"))
		require.True(t, os.IsNotExist(err))
	}
}

// --server 只给运行 broker 的主机：多出服务端证书、私钥和用户表，清单里标记 Server；
// 不带 --server 的导出永远不含这三个文件；--server 只能用于 export-clients。
func TestEventBusClientExportServerFlagAddsBrokerMaterial(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(directory, 0o700))
	database, key := filepath.Join(directory, "admin.db"), filepath.Join(directory, "master.key")
	require.NoError(t, os.WriteFile(key, []byte("synthetic-eventbus-client-export-master-key"), 0o600))
	require.NoError(t, applySchema(database, adminschema.AdminSQL()))
	seedEventBusDeployment(t, database)
	base := []string{"--db-path", database, "--encryption-key-file", key, "--node-id", "gateway-node-1"}
	require.NoError(t, runEventBusCredentialsCommand(append([]string{"eventbus-credentials", "ensure"}, base...), &bytes.Buffer{}, &bytes.Buffer{}))

	export := func(name string, extra ...string) (eventBusClientsResult, string, error) {
		dir := filepath.Join(directory, name)
		args := append(append([]string{"eventbus-credentials", "export-clients"}, base...), "--roles", "internal-admin,metrics-publisher", "--output-dir", dir)
		args = append(args, extra...)
		var out bytes.Buffer
		err := runEventBusCredentialsCommand(args, &out, &bytes.Buffer{})
		var metadata eventBusClientsResult
		if err == nil {
			require.NoError(t, json.Unmarshal(out.Bytes(), &metadata))
		}
		return metadata, dir, err
	}

	plain, plainDir, err := export("plain")
	require.NoError(t, err)
	require.False(t, plain.Server)
	for _, name := range []string{"server.pem", "server-key.pem", "users.yaml"} {
		_, statErr := os.Lstat(filepath.Join(plainDir, name))
		require.True(t, os.IsNotExist(statErr), name)
	}

	server, serverDir, err := export("server", "--server")
	require.NoError(t, err)
	require.True(t, server.Server)
	require.Len(t, server.Files, 2+1+3)
	for _, name := range []string{"server.pem", "server-key.pem", "users.yaml"} {
		raw, readErr := os.ReadFile(filepath.Join(serverDir, name))
		require.NoError(t, readErr, name)
		require.NotEmpty(t, raw)
	}
	users, err := os.ReadFile(filepath.Join(serverDir, "users.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(users), "username: storage-eventbus")
	require.NotContains(t, string(users), "archive-eventbus")

	err = runEventBusCredentialsCommand(append([]string{"eventbus-credentials", "export"}, append(base, "--output-dir", filepath.Join(directory, "x"), "--server")...), &bytes.Buffer{}, &bytes.Buffer{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--server is only valid for export-clients")
}
