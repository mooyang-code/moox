package secret

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 系统空间里的密钥（网关调用方签名密钥）只能由 keys 服务管理，秘钥管理接口既看不到也改不了。
func TestServiceImplHidesSystemSpaceSecrets(t *testing.T) {
	db := setupSecretTestDB(t)
	secretDAO := dao.NewSecretDAO(db)
	svc := NewService(secretDAO)
	ctx := context.Background()

	system := &model.Secret{
		SecretID: dao.GenerateSecretID(), SpaceID: dao.SystemSpaceID, Name: "签名密钥 admin-1",
		Category: "gateway_caller", Provider: "admin", KeyID: "admin-1", SecretValue: "system-secret", Status: "active",
	}
	require.NoError(t, secretDAO.Create(ctx, system))
	ordinary := &model.Secret{SecretID: dao.GenerateSecretID(), Name: "普通密钥", Category: "exchange", SecretValue: "value", Status: "active"}
	require.NoError(t, svc.CreateSecret(ctx, ordinary))

	rows, total, err := svc.ListSecrets(ctx, 0, 50, &dao.SecretFilters{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total, "列表里不能出现系统空间的密钥")
	require.Len(t, rows, 1)
	assert.Equal(t, ordinary.SecretID, rows[0].SecretID)

	_, err = svc.GetSecret(ctx, system.SecretID)
	assert.ErrorIs(t, err, dao.ErrSecretNotFound)

	replaced := *system
	replaced.SecretValue = "attacker-secret"
	assert.ErrorIs(t, svc.UpdateSecret(ctx, &replaced), dao.ErrSecretNotFound)
	assert.ErrorIs(t, svc.ToggleSecretStatus(ctx, system.SecretID, "inactive"), dao.ErrSecretNotFound)
	assert.ErrorIs(t, svc.DeleteSecret(ctx, system.SecretID), dao.ErrSecretNotFound)
	assert.ErrorIs(t, svc.CreateSecret(ctx, &model.Secret{
		SecretID: dao.GenerateSecretID(), SpaceID: dao.SystemSpaceID, Name: "伪造", Category: "other", SecretValue: "x",
	}), ErrSystemSpace)

	// 被拒绝的操作不能改动系统密钥。
	stored, err := secretDAO.FindByID(ctx, system.SecretID)
	require.NoError(t, err)
	assert.Equal(t, "system-secret", stored.SecretValue)
	assert.Equal(t, "active", stored.Status)
}
