package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/pki"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
)

const testBootstrapSpec = `{"hosts":[
 {"id":"storage","address":"146.56.196.204","private_address":"10.206.0.5","region":"ap-nanjing","components":["storage-primary","storage-node","storage-view","access"]},
 {"id":"control","address":"106.53.107.122","components":["console-proxy","web-host","admin","eventbus","monitor","collector","cloudnode","factor-mgr","strategy"]}
]}`

type bootstrapPaths struct{ root, db, key, pki, spec, out string }

func newBootstrapPaths(t *testing.T) bootstrapPaths {
	t.Helper()
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "")
	root := t.TempDir()
	paths := bootstrapPaths{
		root: root, db: filepath.Join(root, "data", "admin.db"), key: filepath.Join(root, "secrets", "admin-encryption.key"),
		pki: filepath.Join(root, "secrets", "pki"), spec: filepath.Join(root, "spec.json"), out: filepath.Join(root, "release"),
	}
	require.NoError(t, os.WriteFile(paths.spec, []byte(testBootstrapSpec), 0o600))
	return paths
}

func runCLI(t *testing.T, args ...string) map[string]any {
	t.Helper()
	var stdout, stderr bytes.Buffer
	var err error
	switch args[0] {
	case "bootstrap":
		err = runBootstrapCommand(args, &stdout, &stderr)
	case "keys":
		err = runKeysCommand(args, &stdout, &stderr)
	case "placement", "host":
		err = runPlacementCommand(args, &stdout, &stderr)
	case "pki":
		err = runPKICommand(args, &stdout, &stderr)
	}
	require.NoError(t, err, stderr.String())
	result := map[string]any{}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	return result
}

func TestBootstrapIsIdempotent(t *testing.T) {
	paths := newBootstrapPaths(t)
	args := []string{"bootstrap", "--db-path", paths.db, "--encryption-key-file", paths.key, "--pki-dir", paths.pki, "--spec", paths.spec, "--out-dir", paths.out}
	first := runCLI(t, args...)
	require.Equal(t, true, first["ca_created"])
	require.NotEmpty(t, first["keys_created"])
	caBefore, err := os.ReadFile(filepath.Join(paths.pki, pki.CACertFile))
	require.NoError(t, err)
	for _, file := range []string{"certs/host-gateway/server.crt", "certs/host-gateway/server.key", "certs/moox-ca.crt"} {
		info, err := os.Stat(filepath.Join(paths.out, file))
		require.NoError(t, err, file)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), file)
	}
	_, err = os.Stat(filepath.Join(paths.out, "secrets", "caller-host-gateway.key"))
	require.True(t, os.IsNotExist(err), "control 的主机网关直连网关控制，不导出调用方密钥")

	second := runCLI(t, args...)
	require.Equal(t, false, second["ca_created"], "重复执行不重建 CA")
	require.Nil(t, second["keys_created"], "重复执行不重建密钥")
	caAfter, err := os.ReadFile(filepath.Join(paths.pki, pki.CACertFile))
	require.NoError(t, err)
	require.Equal(t, caBefore, caAfter)

	keys := runCLI(t, "keys", "list", "--db-path", paths.db, "--encryption-key-file", paths.key)
	items := keys["keys"].([]any)
	callers := map[string]bool{}
	for _, item := range items {
		callers[item.(map[string]any)["caller"].(string)] = true
	}
	for _, caller := range []string{"console", "admin", "moox-cli", "collector", "access", "host-gateway@storage"} {
		require.True(t, callers[caller], caller)
	}
	require.False(t, callers["host-gateway@control"], "control 的主机网关不需要调用方密钥")
}

func TestOfflineRecoveryRestoresDisabledComponent(t *testing.T) {
	paths := newBootstrapPaths(t)
	runCLI(t, "bootstrap", "--db-path", paths.db, "--encryption-key-file", paths.key, "--pki-dir", paths.pki, "--spec", paths.spec, "--out-dir", paths.out)
	disabled := runCLI(t, "placement", "set-status", "--db-path", paths.db, "--host", "storage", "--component", "storage-view", "--status", "disabled")
	require.Equal(t, "disabled", disabled["placement_status"])
	restored := runCLI(t, "placement", "set-status", "--db-path", paths.db, "--host", "storage", "--component", "storage-view", "--status", "enabled")
	require.Equal(t, "enabled", restored["placement_status"])
	host := runCLI(t, "host", "set-status", "--db-path", paths.db, "--host", "storage", "--status", "enabled")
	require.Equal(t, "enabled", host["host_status"])

	var stdout, stderr bytes.Buffer
	err := runPlacementCommand([]string{"placement", "set-status", "--db-path", paths.db, "--host", "control", "--component", "admin", "--status", "disabled"}, &stdout, &stderr)
	require.Error(t, err, "离线命令同样不能停用受保护组件")
}

func TestKeysRotateAndExport(t *testing.T) {
	paths := newBootstrapPaths(t)
	runCLI(t, "bootstrap", "--db-path", paths.db, "--encryption-key-file", paths.key, "--pki-dir", paths.pki, "--spec", paths.spec, "--out-dir", paths.out)
	out := filepath.Join(paths.root, "caller-collector.key")
	rotated := runCLI(t, "keys", "rotate", "--db-path", paths.db, "--encryption-key-file", paths.key, "--caller", "collector", "--out", out)
	require.Equal(t, "collector-2", rotated["key_id"])
	key, err := gatewayauth.LoadCallerKey(out)
	require.NoError(t, err)
	require.Equal(t, "collector-2", key.KeyID)
	runCLI(t, "keys", "retire", "--db-path", paths.db, "--encryption-key-file", paths.key, "--caller", "collector", "--key-id", "collector-1")

	principals := filepath.Join(paths.root, "access-principals.json")
	exported := runCLI(t, "keys", "export-principals", "--db-path", paths.db, "--encryption-key-file", paths.key, "--out", principals)
	require.EqualValues(t, 3, exported["keys"])
	loaded, err := gatewayauth.LoadKeySet(principals)
	require.NoError(t, err)
	require.Len(t, loaded, 3)

	var stdout, stderr bytes.Buffer
	err = runKeysCommand([]string{"keys", "ensure", "--db-path", paths.db, "--encryption-key-file", paths.key, "--caller", "unknown-caller"}, &stdout, &stderr)
	require.Error(t, err, "不在组件目录中的身份不能生成密钥")
}

// 某台主机违反目录规则时整体失败：前面已经写过的主机也不能留在库里，库不会停在只写了一半的状态。
func TestBootstrapRollsBackAllHostsWhenOneIsInvalid(t *testing.T) {
	paths := newBootstrapPaths(t)
	invalid := `{"hosts":[
 {"id":"control","address":"106.53.107.122","components":["console-proxy","web-host","admin","eventbus"]},
 {"id":"storage","address":"146.56.196.204","components":["no-such-component"]}
]}`
	require.NoError(t, os.WriteFile(paths.spec, []byte(invalid), 0o600))
	var stdout, stderr bytes.Buffer
	err := runBootstrapCommand([]string{"bootstrap", "--db-path", paths.db, "--encryption-key-file", paths.key,
		"--pki-dir", paths.pki, "--spec", paths.spec, "--out-dir", paths.out}, &stdout, &stderr)
	require.Error(t, err)

	db, err := openAdminCLIDBWithPragmas(paths.db)
	require.NoError(t, err)
	defer closeAdminCLIDB(db)
	var hosts int64
	require.NoError(t, db.Table("t_hosts").Count(&hosts).Error)
	require.Zero(t, hosts, "control 已经写过，也要随失败一起回滚")
}
