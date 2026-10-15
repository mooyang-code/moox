package setup

import (
	"context"
	"strings"
	"testing"

	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigBackupIsStoredEncryptedAndOverwritten(t *testing.T) {
	db := setupDB(t)
	service := NewService(db, testEncryptionKey)
	ctx := context.Background()

	_, err := service.GetBackup(ctx)
	assert.ErrorIs(t, err, ErrBackupMissing)

	first := configBackupMagic + "first-ciphertext"
	_, err = service.PutBackup(ctx, first)
	require.NoError(t, err)
	second := configBackupMagic + "second-ciphertext"
	_, err = service.PutBackup(ctx, second)
	require.NoError(t, err)

	backup, err := service.GetBackup(ctx)
	require.NoError(t, err)
	assert.Equal(t, second, backup.Envelope)
	assert.False(t, backup.UpdatedAt.IsZero())

	var rows []secretmodel.Secret
	require.NoError(t, db.Where("c_secret_id = ?", configBackupID).Find(&rows).Error)
	require.Len(t, rows, 1, "只保留一份备份")
	assert.NotContains(t, rows[0].SecretValue, "second-ciphertext", "入库时应再用主密钥加密")
}

func TestConfigBackupRejectsForeignOrOversizedEnvelopes(t *testing.T) {
	service := NewService(setupDB(t), testEncryptionKey)
	ctx := context.Background()
	for _, envelope := range []string{"", "[admin]\npassword = \"x\"\n", configBackupMagic + strings.Repeat("a", maxConfigBackup)} {
		_, err := service.PutBackup(ctx, envelope)
		assert.ErrorIs(t, err, ErrBackupInvalid)
	}
}
