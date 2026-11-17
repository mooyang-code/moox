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

// ensure 在一个事务里生成全部角色令牌：中途失败时一个都不留。
func TestEventBusIssuanceIsAtomic(t *testing.T) {
	root := t.TempDir()
	database, master := filepath.Join(root, "admin.db"), filepath.Join(root, "master.key")
	require.NoError(t, os.WriteFile(master, []byte("synthetic-eventbus-master-key-at-least-32-bytes"), 0o600))
	require.NoError(t, applySchema(database, adminschema.AdminSQL()))
	db, err := gorm.Open(sqlite.Open(database), &gorm.Config{})
	require.NoError(t, err)
	defer closeAdminCLIDB(db)
	last := eventBusKeys[eventBusRoles[len(eventBusRoles)-1]]
	require.NoError(t, db.Exec("CREATE TRIGGER reject_synthetic_role BEFORE INSERT ON t_secrets WHEN NEW.c_key_id = '"+last+"' BEGIN SELECT RAISE(ABORT, 'synthetic failure'); END").Error)
	args := []string{"eventbus-credentials", "ensure", "--db-path", database, "--encryption-key-file", master}
	var output bytes.Buffer
	require.Error(t, runEventBusCredentialsCommand(args, &output, &bytes.Buffer{}))
	require.Empty(t, output.String(), "success metadata must follow the commit")
	count := func() int64 {
		var count int64
		require.NoError(t, db.Table("t_secrets").Where("c_category = ?", "eventbus").Count(&count).Error)
		return count
	}
	require.Zero(t, count(), "最后一个角色写失败时，前面的角色令牌也要回滚")
	require.NoError(t, db.Exec("DROP TRIGGER reject_synthetic_role").Error)
	require.NoError(t, runEventBusCredentialsCommand(args, &output, &bytes.Buffer{}))
	require.EqualValues(t, len(eventBusRoles), count())
}
