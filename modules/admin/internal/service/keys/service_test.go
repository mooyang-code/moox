package keys

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	secretdao "github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "test-encryption-key-0123456789abcdef")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return NewService(secretdao.NewSecretDAO(db))
}

func TestEnsureReusesExistingKey(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	first, created, err := s.Ensure(ctx, CategoryCaller, "collector")
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "collector-1", first.KeyID)
	require.NotEmpty(t, first.Secret)
	second, created, err := s.Ensure(ctx, CategoryCaller, "collector")
	require.NoError(t, err)
	require.False(t, created, "重复部署时复用已有密钥")
	require.Equal(t, first.KeyID, second.KeyID)
	require.Equal(t, first.Secret, second.Secret)

	gateway, _, err := s.Ensure(ctx, CategoryCaller, "host-gateway@compute-1")
	require.NoError(t, err)
	require.Equal(t, "host-gateway@compute-1-1", gateway.KeyID)
	// 外部调用方与内部调用方分开存放。
	principal, created, err := s.Ensure(ctx, CategoryPrincipal, "collector")
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, first.Secret, principal.Secret)
}

func TestRotateKeepsOldKeyValidUntilRetired(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	old, _, err := s.Ensure(ctx, CategoryCaller, "strategy")
	require.NoError(t, err)
	next, err := s.Rotate(ctx, CategoryCaller, "strategy")
	require.NoError(t, err)
	require.Equal(t, "strategy-2", next.KeyID)

	keys, err := s.VerificationKeys(ctx, CategoryCaller, []string{"strategy"})
	require.NoError(t, err)
	require.Len(t, keys, 2, "轮换期间新旧密钥都能通过校验")
	_, err = gatewayauth.NewCredentialRegistry([]gatewayauth.Credentials{keys[0].Credentials(), keys[1].Credentials()})
	require.NoError(t, err, "同一调用方的两把密钥可以同时登记")
	signing, err := s.SigningKey(ctx, CategoryCaller, "strategy")
	require.NoError(t, err)
	require.Equal(t, next.KeyID, signing.KeyID, "新签名用最新的密钥")

	require.NoError(t, s.Retire(ctx, CategoryCaller, "strategy", old.KeyID))
	keys, err = s.VerificationKeys(ctx, CategoryCaller, []string{"strategy"})
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.Equal(t, next.KeyID, keys[0].KeyID)
	require.Error(t, s.Retire(ctx, CategoryCaller, "strategy", next.KeyID), "不能停用最后一把有效密钥")
}

func TestVerificationKeysOnlyForRequestedCallers(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	for _, caller := range []string{"console", "admin", "monitor"} {
		_, _, err := s.Ensure(ctx, CategoryCaller, caller)
		require.NoError(t, err)
	}
	keys, err := s.VerificationKeys(ctx, CategoryCaller, []string{"console", "monitor", "missing"})
	require.NoError(t, err)
	require.Len(t, keys, 2)
	for _, key := range keys {
		require.NotEqual(t, "admin", key.Caller)
		require.False(t, key.CreatedAt.After(time.Now().Add(time.Minute)))
	}
	_, err = s.Rotate(ctx, CategoryCaller, "missing")
	require.Error(t, err)
	_, err = s.SigningKey(ctx, CategoryCaller, "missing")
	require.ErrorIs(t, err, ErrNotFound)
}
