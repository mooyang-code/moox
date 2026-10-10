package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEventBusIssuanceIsAtomicAndRefusesLostIdentity(t *testing.T) {
	root := t.TempDir()
	database, master := filepath.Join(root, "admin.db"), filepath.Join(root, "master.key")
	require.NoError(t, os.WriteFile(master, []byte("synthetic-eventbus-master-key-at-least-32-bytes"), 0o600))
	require.NoError(t, applySchema(database, adminschema.AdminSQL()))
	seedEventBusDeployment(t, database)
	db, err := gorm.Open(sqlite.Open(database), &gorm.Config{})
	require.NoError(t, err)
	defer closeAdminCLIDB(db)
	require.NoError(t, db.Exec("CREATE TRIGGER reject_synthetic_server BEFORE INSERT ON t_secrets WHEN NEW.c_key_id = 'eventbus_tls_server' BEGIN SELECT RAISE(ABORT, 'synthetic failure'); END").Error)
	args := []string{"eventbus-credentials", "ensure", "--db-path", database, "--encryption-key-file", master, "--node-id", "gateway-node-1"}
	var output bytes.Buffer
	require.Error(t, runEventBusCredentialsCommand(args, &output, &bytes.Buffer{}))
	require.Empty(t, output.String(), "success metadata must follow the commit")
	count := func() int64 {
		var count int64
		require.NoError(t, db.Table("t_secrets").Where("c_category = ?", "eventbus").Count(&count).Error)
		return count
	}
	require.Zero(t, count(), "a failed server insert must also roll back CA and role tokens")
	require.NoError(t, db.Exec("DROP TRIGGER reject_synthetic_server").Error)
	require.NoError(t, runEventBusCredentialsCommand(args, &output, &bytes.Buffer{}))
	require.EqualValues(t, 13, count())
	for _, lost := range []string{"eventbus_tls_server", "eventbus_tls_ca"} {
		t.Run(lost, func(t *testing.T) {
			require.NoError(t, db.Exec("UPDATE t_secrets SET c_is_deleted = 1 WHERE c_key_id = ?", lost).Error)
			output.Reset()
			require.Error(t, runEventBusCredentialsCommand(args, &output, &bytes.Buffer{}))
			require.Empty(t, output.String())
			require.EqualValues(t, 13, count(), "missing half of an issued TLS identity must never create a replacement")
			require.NoError(t, db.Exec("UPDATE t_secrets SET c_is_deleted = 0 WHERE c_key_id = ?", lost).Error)
		})
	}
}
