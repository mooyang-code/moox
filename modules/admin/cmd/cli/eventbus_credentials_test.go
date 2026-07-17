package main

import (
	"bytes"
	"github.com/glebarez/sqlite"
	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEventBusCredentialsEnsureIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "admin.db")
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte("test-encryption-key-for-eventbus"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applySchema(dbPath, adminschema.AdminSQL()); err != nil {
		t.Fatal(err)
	}
	args := []string{"eventbus-credentials", "ensure", "--db-path", dbPath, "--encryption-key-file", keyPath, "--public-ip", "203.0.113.10"}
	var out bytes.Buffer
	if err := runEventBusCredentialsCommand(args, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	first := out.String()
	out.Reset()
	if err := runEventBusCredentialsCommand(args, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if first != out.String() {
		t.Fatalf("ensure metadata changed: %q vs %q", first, out.String())
	}
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Table("t_secrets").Where("c_category = ? AND c_provider = ? AND c_is_deleted = 0", "eventbus", "moox_eventbus").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 9 {
		t.Fatalf("eventbus records=%d, want 9", count)
	}
}

func TestEventBusCredentialsExportAndRotate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "admin.db")
	keyPath := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(keyPath, []byte("test-encryption-key-for-eventbus"), 0o600))
	require.NoError(t, applySchema(dbPath, adminschema.AdminSQL()))

	ensureArgs := []string{"eventbus-credentials", "ensure", "--db-path", dbPath, "--encryption-key-file", keyPath, "--public-ip", "203.0.113.10"}
	var out bytes.Buffer
	require.NoError(t, runEventBusCredentialsCommand(ensureArgs, &out, &bytes.Buffer{}))

	exportDir := filepath.Join(dir, "out")
	exportArgs := []string{"eventbus-credentials", "export", "--db-path", dbPath, "--encryption-key-file", keyPath, "--output-dir", exportDir, "--public-ip", "203.0.113.10"}
	out.Reset()
	require.NoError(t, runEventBusCredentialsCommand(exportArgs, &out, &bytes.Buffer{}))
	assert.FileExists(t, filepath.Join(exportDir, "users.yaml"))
	assert.FileExists(t, filepath.Join(exportDir, "ca.pem"))
	assert.FileExists(t, filepath.Join(exportDir, "server.pem"))
	assert.FileExists(t, filepath.Join(exportDir, "hostagent-publisher.yaml"))
	strategyCredential := filepath.Join(exportDir, "strategy-eventbus.yaml")
	assert.FileExists(t, strategyCredential)
	info, err := os.Stat(strategyCredential)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	yaml := usersYAML(map[string]string{
		"eventbus-internal-admin":      "a",
		"hostagent-publisher":          "b",
		"monitor-hostmetrics-consumer": "c",
		"storage-eventbus":             "d",
		"cloudnode-eventbus":           "e",
		"factor-eventbus":              "f",
		"strategy-eventbus":            "g",
	})
	assert.Contains(t, yaml, "eventbus-internal-admin")
	assert.Contains(t, yaml, "factor-eventbus")
	assert.Contains(t, yaml, "moox.strategy.signal.generated.v1")
	assert.Contains(t, yaml, "moox.strategy.action.accepted.v1")
	assert.Contains(t, yaml, "moox.strategy.run.completed.v1")
	strategyACL := yaml[strings.Index(yaml, "username: strategy-eventbus"):]
	assert.NotContains(t, strategyACL, "$JS.API.>")
	assert.Contains(t, strategyACL, `subscribe: {allow: ["_INBOX.>"]}`)

	bundle, err := makeTLSBundle("203.0.113.10")
	require.NoError(t, err)
	assert.Contains(t, bundle.CA, "BEGIN CERTIFICATE")
	assert.Contains(t, bundle.Cert, "BEGIN CERTIFICATE")
	assert.Contains(t, bundle.Key, "BEGIN RSA PRIVATE KEY")

	rotateArgs := []string{"eventbus-credentials", "rotate", "--db-path", dbPath, "--encryption-key-file", keyPath, "--credential", "hostagent-publisher"}
	err = runEventBusCredentialsCommand(rotateArgs, &bytes.Buffer{}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--confirm")

	rotateArgs = append(rotateArgs, "--confirm")
	out.Reset()
	require.NoError(t, runEventBusCredentialsCommand(rotateArgs, &out, &bytes.Buffer{}))

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	var count int64
	require.NoError(t, db.Table("t_secrets").Where("c_category = ? AND c_provider = ? AND c_is_deleted = 0", "eventbus", "moox_eventbus").Count(&count).Error)
	assert.GreaterOrEqual(t, count, int64(9))
}

func TestEventBusCredentialsHelpers(t *testing.T) {
	assert.False(t, isEventBusCredentialsCommand(nil))
	assert.False(t, isEventBusCredentialsCommand([]string{"moox-admin"}))
	assert.True(t, isEventBusCredentialsCommand([]string{"moox-admin", "eventbus-credentials"}))

	dir := t.TempDir()
	path := filepath.Join(dir, "secret.txt")
	require.NoError(t, atomicSecretFile(path, []byte("secret-data")))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "secret-data", string(data))

	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, map[string]string{"ok": "1"}))
	assert.Contains(t, buf.String(), `"ok"`)
}
