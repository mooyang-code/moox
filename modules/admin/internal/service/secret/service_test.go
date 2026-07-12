package secret

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceImpl_CreateSecret_DelegatesToDAO(t *testing.T) {
	db := setupSecretTestDB(t)
	svc := NewService(dao.NewSecretDAO(db))

	secret := &model.Secret{
		SecretID:    dao.GenerateSecretID(),
		Name:        "test-key",
		Category:    "exchange",
		SecretValue: "plain-secret",
	}
	require.NoError(t, svc.CreateSecret(context.Background(), secret))

	got, err := svc.GetSecret(context.Background(), secret.SecretID)
	require.NoError(t, err)
	assert.Equal(t, "plain-secret", got.SecretValue)
}

func TestServiceImpl_ToggleSecretStatus_UpdatesStatus(t *testing.T) {
	db := setupSecretTestDB(t)
	svc := NewService(dao.NewSecretDAO(db))

	secret := &model.Secret{
		SecretID:    dao.GenerateSecretID(),
		Name:        "toggle-key",
		Category:    "exchange",
		SecretValue: "value",
		Status:      "active",
	}
	require.NoError(t, svc.CreateSecret(context.Background(), secret))
	require.NoError(t, svc.ToggleSecretStatus(context.Background(), secret.SecretID, "inactive"))

	got, err := svc.GetSecret(context.Background(), secret.SecretID)
	require.NoError(t, err)
	assert.Equal(t, "inactive", got.Status)
}

func TestServiceImpl_DeleteSecret_SoftDeletesRecord(t *testing.T) {
	db := setupSecretTestDB(t)
	secretDAO := dao.NewSecretDAO(db)
	svc := NewService(secretDAO)

	secret := &model.Secret{
		SecretID:    dao.GenerateSecretID(),
		Name:        "delete-key",
		Category:    "exchange",
		SecretValue: "value",
	}
	require.NoError(t, svc.CreateSecret(context.Background(), secret))
	require.NoError(t, svc.DeleteSecret(context.Background(), secret.SecretID))

	_, err := svc.GetSecret(context.Background(), secret.SecretID)
	require.Error(t, err)
}

func TestServiceImpl_ListSecrets_ClosedDB_ShouldError(t *testing.T) {
	db := setupSecretTestDB(t)
	svc := NewService(dao.NewSecretDAO(db))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	_, _, err = svc.ListSecrets(context.Background(), 0, 10, nil)
	require.Error(t, err)
}

func TestServiceImpl_GetSecret_NotFound_ShouldError(t *testing.T) {
	db := setupSecretTestDB(t)
	svc := NewService(dao.NewSecretDAO(db))

	_, err := svc.GetSecret(context.Background(), "missing")
	require.Error(t, err)
}
