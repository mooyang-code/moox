package dao

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const testSecretKey = "0123456789abcdef0123456789abcdef"

func setupSecretDAOTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	return db
}

func sampleSecret() *model.Secret {
	return &model.Secret{
		SecretID:    GenerateSecretID(),
		Name:        "binance-main",
		Category:    "exchange",
		Provider:    "binance",
		SecretType:  "api_key",
		KeyID:       "key-id",
		SecretValue: "plain-secret",
		ExtraConfig: `{"passphrase":"okx-secret-passphrase"}`,
		Status:      "active",
	}
}

func TestSecretDAO_Create_FindByID_RoundTrip_ShouldEncryptBothValueAndExtraConfig(t *testing.T) {
	db := setupSecretDAOTestDB(t)
	d := NewSecretDAO(db, testSecretKey)
	secret := sampleSecret()
	require.NoError(t, d.Create(context.Background(), secret))
	// 调用方入参保持明文不被原地加密污染，同时回填 ID 与时间戳。
	assert.Positive(t, secret.ID)
	assert.False(t, secret.CreateTime.IsZero())
	assert.False(t, secret.ModifyTime.IsZero())
	assert.Equal(t, "plain-secret", secret.SecretValue)
	assert.Equal(t, `{"passphrase":"okx-secret-passphrase"}`, secret.ExtraConfig)

	// 数据库原始行里 secret_value 与 extra_config 均已 AES-GCM 加密，不能出现明文。
	var raw model.Secret
	require.NoError(t, db.Where("c_secret_id = ?", secret.SecretID).First(&raw).Error)
	assert.NotEqual(t, "plain-secret", raw.SecretValue)
	assert.NotContains(t, raw.ExtraConfig, "okx-secret-passphrase")
	assert.NotEqual(t, `{"passphrase":"okx-secret-passphrase"}`, raw.ExtraConfig)

	got, err := d.FindByID(context.Background(), secret.SecretID)
	require.NoError(t, err)
	assert.Equal(t, "plain-secret", got.SecretValue)
	assert.Equal(t, `{"passphrase":"okx-secret-passphrase"}`, got.ExtraConfig)

	// Update 同样不原地污染调用方入参。
	secret.SecretValue = "updated-secret"
	secret.ExtraConfig = `{"passphrase":"updated-pass"}`
	require.NoError(t, d.Update(context.Background(), secret))
	assert.Equal(t, "updated-secret", secret.SecretValue)
	assert.Equal(t, `{"passphrase":"updated-pass"}`, secret.ExtraConfig)
	got, err = d.FindByID(context.Background(), secret.SecretID)
	require.NoError(t, err)
	assert.Equal(t, "updated-secret", got.SecretValue)
	assert.Equal(t, `{"passphrase":"updated-pass"}`, got.ExtraConfig)
}

func TestSecretDAO_HardDeleteAllowsRepeatedDeleteAndRecreateAndManagesTombstone(t *testing.T) {
	db := setupSecretDAOTestDB(t)
	d := NewSecretDAO(db, testSecretKey)
	ctx := context.Background()

	// 普通随机 UUID 密钥硬删除不污染墓碑表。
	regular := sampleSecret()
	require.NoError(t, d.Create(ctx, regular))
	require.NoError(t, d.Delete(ctx, regular.SecretID))
	deleted, err := IsDeletedInitialSecret(ctx, db, regular.SecretID)
	require.NoError(t, err)
	assert.False(t, deleted, "删除普通 UUID 密钥不得写入墓碑表")

	// 初始固定 ID 密钥（tencent-default、exchange-*）可反复硬删除与重建，不再撞唯一约束；删除记墓碑，重建/导入清墓碑。
	for _, initialID := range []string{model.TencentDefaultSecretID, model.ExchangeSecretID("binance", "production", "main")} {
		for range 3 {
			rec := sampleSecret()
			rec.SecretID = initialID
			require.NoError(t, d.Create(ctx, rec))

			deleted, err := IsDeletedInitialSecret(ctx, db, initialID)
			require.NoError(t, err)
			assert.False(t, deleted, "创建或重新导入同 ID 时清除墓碑: %s", initialID)

			require.NoError(t, d.Delete(ctx, initialID))
			var count int64
			require.NoError(t, db.Model(&model.Secret{}).Where("c_secret_id = ?", initialID).Count(&count).Error)
			assert.Zero(t, count, "物理硬删除不留软删除行")

			deleted, err = IsDeletedInitialSecret(ctx, db, initialID)
			require.NoError(t, err)
			assert.True(t, deleted, "删除初始密钥后写入墓碑防止 setup init 静默复活: %s", initialID)
		}
	}
}

func TestSecretDAO_Create_MaskedValue_ShouldReject(t *testing.T) {
	d := NewSecretDAO(setupSecretDAOTestDB(t), testSecretKey)
	secret := sampleSecret()
	secret.SecretValue = "abc•def"
	err := d.Create(context.Background(), secret)
	require.ErrorIs(t, err, ErrMaskedValue)
}

func TestSecretDAO_Update_NotFound_ShouldReturnErrSecretNotFound(t *testing.T) {
	d := NewSecretDAO(setupSecretDAOTestDB(t), testSecretKey)
	secret := sampleSecret()
	err := d.Update(context.Background(), secret)
	require.ErrorIs(t, err, ErrSecretNotFound)
}

func TestSecretDAO_Delete_NotFound_ShouldReturnErrSecretNotFound(t *testing.T) {
	d := NewSecretDAO(setupSecretDAOTestDB(t), testSecretKey)
	err := d.Delete(context.Background(), "missing")
	require.ErrorIs(t, err, ErrSecretNotFound)
}

func TestSecretDAO_List_WithFilters_ShouldReturnMatches(t *testing.T) {
	d := NewSecretDAO(setupSecretDAOTestDB(t), testSecretKey)
	secret := sampleSecret()
	require.NoError(t, d.Create(context.Background(), secret))

	rows, total, err := d.List(context.Background(), 0, 10, &SecretFilters{
		Keyword:  "binance",
		Category: "exchange",
		Status:   "active",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, rows, 1)
	assert.Equal(t, secret.SecretID, rows[0].SecretID)
	assert.Equal(t, `{"passphrase":"okx-secret-passphrase"}`, rows[0].ExtraConfig)
}

func TestSecretDAO_UpdateStatus_ValidSecret_ShouldUpdate(t *testing.T) {
	d := NewSecretDAO(setupSecretDAOTestDB(t), testSecretKey)
	secret := sampleSecret()
	require.NoError(t, d.Create(context.Background(), secret))
	require.NoError(t, d.UpdateStatus(context.Background(), secret.SecretID, "inactive"))

	got, err := d.FindByID(context.Background(), secret.SecretID)
	require.NoError(t, err)
	assert.Equal(t, "inactive", got.Status)
}

func TestSystemSecretEncryptsExtraConfigToo(t *testing.T) {
	db := setupSecretDAOTestDB(t)
	id := model.SSHHostSecretID("storage")
	require.NoError(t, PutSystemSecret(db, testSecretKey, &model.Secret{
		SecretID: id, Name: "SSH storage", Category: model.SSHCategory, SecretType: "password",
		KeyID: "ubuntu", SecretValue: "ssh-pass", ExtraConfig: `{"note":"sensitive-meta"}`, Status: "active",
	}))
	var raw model.Secret
	require.NoError(t, db.Where("c_secret_id = ?", id).First(&raw).Error)
	assert.NotContains(t, raw.SecretValue, "ssh-pass")
	assert.NotContains(t, raw.ExtraConfig, "sensitive-meta")

	got, err := GetSystemSecret(db, testSecretKey, id)
	require.NoError(t, err)
	assert.Equal(t, "ssh-pass", got.SecretValue)
	assert.Equal(t, `{"note":"sensitive-meta"}`, got.ExtraConfig)
}

func TestGenerateSecretID_ShouldReturnUUID(t *testing.T) {
	id1 := GenerateSecretID()
	id2 := GenerateSecretID()
	assert.NotEmpty(t, id1)
	assert.NotEqual(t, id1, id2)
}

func TestSystemGatewayKeysAreInvisibleAndImmutableThroughGenericDAO(t *testing.T) {
	db := setupSecretDAOTestDB(t)
	d := NewSecretDAO(db, testSecretKey)
	ctx := context.Background()
	regular := sampleSecret()
	regular.Description = "visible description"
	require.NoError(t, d.Create(ctx, regular))
	for _, row := range []*model.Secret{
		{SecretID: model.SSHHostSecretID("storage"), SecretType: "password"},
	} {
		row.Name = "system"
		row.Description = "visible description"
		row.Category = "gateway"
		row.Status = "active"
		row.SecretValue = "deliberately-not-decryptable"
		require.ErrorIs(t, d.Create(ctx, row), ErrSystemSecret)
		require.NoError(t, db.Create(row).Error)
		_, err := d.FindByID(ctx, row.SecretID)
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		require.ErrorIs(t, d.Update(ctx, row), ErrSystemSecret)
		require.ErrorIs(t, d.Delete(ctx, row.SecretID), ErrSecretNotFound)
		require.ErrorIs(t, d.UpdateStatus(ctx, row.SecretID, "inactive"), ErrSecretNotFound)
		forged := sampleSecret()
		forged.SecretID = row.SecretID
		err = d.Update(ctx, forged)
		require.ErrorIs(t, err, ErrSystemSecret)
		var actual model.Secret
		require.NoError(t, db.First(&actual, row.ID).Error)
		require.Equal(t, row.SecretValue, actual.SecretValue)
		require.Equal(t, "active", actual.Status)
	}
	for _, filters := range []*SecretFilters{nil, {Keyword: "visible"}, {Category: "gateway"}} {
		rows, total, err := d.List(ctx, 0, 100, filters)
		require.NoError(t, err, "system rows must be excluded before decryption")
		if filters != nil && filters.Category == "gateway" {
			require.Zero(t, total)
			require.Empty(t, rows)
		} else {
			require.Equal(t, int64(1), total)
			require.Equal(t, regular.SecretID, rows[0].SecretID)
		}
	}
}
