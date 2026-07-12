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

func setupSecretDAOTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
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
		Status:      "active",
	}
}

func TestSecretDAO_Create_FindByID_RoundTrip_ShouldDecrypt(t *testing.T) {
	d := NewSecretDAO(setupSecretDAOTestDB(t))
	secret := sampleSecret()
	require.NoError(t, d.Create(context.Background(), secret))

	got, err := d.FindByID(context.Background(), secret.SecretID)
	require.NoError(t, err)
	assert.Equal(t, "plain-secret", got.SecretValue)
	assert.NotEqual(t, "plain-secret", secret.SecretValue) // 入库后已加密
}

func TestSecretDAO_Create_MaskedValue_ShouldReject(t *testing.T) {
	d := NewSecretDAO(setupSecretDAOTestDB(t))
	secret := sampleSecret()
	secret.SecretValue = "abc•def"
	err := d.Create(context.Background(), secret)
	require.ErrorIs(t, err, ErrMaskedValue)
}

func TestSecretDAO_Update_NotFound_ShouldReturnErrSecretNotFound(t *testing.T) {
	d := NewSecretDAO(setupSecretDAOTestDB(t))
	secret := sampleSecret()
	err := d.Update(context.Background(), secret)
	require.ErrorIs(t, err, ErrSecretNotFound)
}

func TestSecretDAO_Delete_NotFound_ShouldReturnErrSecretNotFound(t *testing.T) {
	d := NewSecretDAO(setupSecretDAOTestDB(t))
	err := d.Delete(context.Background(), "missing")
	require.ErrorIs(t, err, ErrSecretNotFound)
}

func TestSecretDAO_List_WithFilters_ShouldReturnMatches(t *testing.T) {
	d := NewSecretDAO(setupSecretDAOTestDB(t))
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
}

func TestSecretDAO_UpdateStatus_ValidSecret_ShouldUpdate(t *testing.T) {
	d := NewSecretDAO(setupSecretDAOTestDB(t))
	secret := sampleSecret()
	require.NoError(t, d.Create(context.Background(), secret))
	require.NoError(t, d.UpdateStatus(context.Background(), secret.SecretID, "inactive"))

	got, err := d.FindByID(context.Background(), secret.SecretID)
	require.NoError(t, err)
	assert.Equal(t, "inactive", got.Status)
}

func TestSecretDAO_UpdateLastUsed_ValidSecret_ShouldUpdate(t *testing.T) {
	d := NewSecretDAO(setupSecretDAOTestDB(t))
	secret := sampleSecret()
	require.NoError(t, d.Create(context.Background(), secret))
	require.NoError(t, d.UpdateLastUsed(context.Background(), secret.SecretID, "trade"))

	got, err := d.FindByID(context.Background(), secret.SecretID)
	require.NoError(t, err)
	assert.Equal(t, "trade", got.LastUsedBy)
	assert.NotNil(t, got.LastUsedAt)
}

func TestGenerateSecretID_ShouldReturnUUID(t *testing.T) {
	id1 := GenerateSecretID()
	id2 := GenerateSecretID()
	assert.NotEmpty(t, id1)
	assert.NotEqual(t, id1, id2)
}
