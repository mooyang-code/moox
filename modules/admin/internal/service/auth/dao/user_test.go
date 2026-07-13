package dao

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupUserTestDB(t *testing.T) (*gorm.DB, *CacheDB) {
	t.Helper()
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)

	cache, err := NewCacheDB(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = cache.Close() })
	return db, cache
}

func TestUserDAO_CreateUser_AutoID_ShouldPersist(t *testing.T) {
	db, cache := setupUserTestDB(t)
	d := NewUserDAO(db, cache)

	user := &model.User{
		Username:     "alice",
		PasswordHash: "hash",
	}
	require.NoError(t, d.CreateUser(context.Background(), user))
	assert.NotEmpty(t, user.UserID)

	got, err := d.GetUserByUsername(context.Background(), "alice")
	require.NoError(t, err)
	assert.Equal(t, user.UserID, got.UserID)
}

func TestUserDAO_GetUserByID_NotFound_ShouldError(t *testing.T) {
	db, cache := setupUserTestDB(t)
	d := NewUserDAO(db, cache)

	_, err := d.GetUserByID(context.Background(), "missing")
	require.Error(t, err)
}

func TestUserDAO_UpdateUserPassword_ValidUser_ShouldUpdate(t *testing.T) {
	db, cache := setupUserTestDB(t)
	d := NewUserDAO(db, cache)

	user := &model.User{Username: "bob", PasswordHash: "old"}
	require.NoError(t, d.CreateUser(context.Background(), user))

	require.NoError(t, d.UpdateUserPassword(context.Background(), user.UserID, "newhash"))
	got, err := d.GetUserByID(context.Background(), user.UserID)
	require.NoError(t, err)
	assert.Equal(t, "newhash", got.PasswordHash)
}

func TestUserDAO_UpdateUserLoginInfo_ValidUser_ShouldUpdate(t *testing.T) {
	db, cache := setupUserTestDB(t)
	d := NewUserDAO(db, cache)

	user := &model.User{Username: "carol", PasswordHash: "h"}
	require.NoError(t, d.CreateUser(context.Background(), user))
	require.NoError(t, d.UpdateUserLoginInfo(context.Background(), user.UserID, "10.0.0.1"))

	got, err := d.GetUserByID(context.Background(), user.UserID)
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1", got.LastLoginIP)
	assert.NotNil(t, got.LastLoginAt)
}

func TestUserDAO_CreateLoginHistory_ValidRecord_ShouldPersist(t *testing.T) {
	db, cache := setupUserTestDB(t)
	d := NewUserDAO(db, cache)

	user := &model.User{Username: "alice", PasswordHash: "h"}
	require.NoError(t, d.CreateUser(context.Background(), user))

	history := &model.LoginHistory{
		UserID:      user.UserID,
		Username:    "alice",
		ClientIP:    "127.0.0.1",
		LoginResult: model.LoginResultSuccess,
	}
	require.NoError(t, d.CreateLoginHistory(context.Background(), history))
	assert.False(t, history.CreatedAt.IsZero())
}

func TestUserDAO_SetGetLoginSalt_RoundTrip_ShouldWork(t *testing.T) {
	db, cache := setupUserTestDB(t)
	d := NewUserDAO(db, cache)

	salt := model.LoginSalt{
		Username:  "alice",
		Salt:      "random-salt",
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
	require.NoError(t, d.SetLoginSalt(context.Background(), "alice", salt))

	got, err := d.GetLoginSalt(context.Background(), "alice")
	require.NoError(t, err)
	assert.Equal(t, "random-salt", got.Salt)
}

func TestConsumeLoginSaltReturnsValueOnlyOnce(t *testing.T) {
	db, cache := setupUserTestDB(t)
	d := NewUserDAO(db, cache)
	ctx := context.Background()
	salt := model.LoginSalt{Username: "alice", Salt: "one-time", ExpiresAt: time.Now().Add(time.Minute)}
	require.NoError(t, d.SetLoginSalt(ctx, "alice", salt))

	got, err := d.ConsumeLoginSalt(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, salt.Salt, got.Salt)
	_, err = d.ConsumeLoginSalt(ctx, "alice")
	require.ErrorIs(t, err, ErrKeyNotFound)
}

func TestConsumeSessionNonceRejectsReplay(t *testing.T) {
	db, cache := setupUserTestDB(t)
	d := NewUserDAO(db, cache)
	ctx := context.Background()

	consumed, err := d.ConsumeSessionNonce(ctx, "session-1", "nonce-1", 2*time.Minute)
	require.NoError(t, err)
	assert.True(t, consumed)
	consumed, err = d.ConsumeSessionNonce(ctx, "session-1", "nonce-1", 2*time.Minute)
	require.NoError(t, err)
	assert.False(t, consumed)
}

func TestConsumeServiceNonceRejectsReplay(t *testing.T) {
	db, cache := setupUserTestDB(t)
	d := NewUserDAO(db, cache)
	ctx := context.Background()

	consumed, err := d.ConsumeServiceNonce(ctx, "access-key", "nonce-1", 2*time.Minute)
	require.NoError(t, err)
	assert.True(t, consumed)
	consumed, err = d.ConsumeServiceNonce(ctx, "access-key", "nonce-1", 2*time.Minute)
	require.NoError(t, err)
	assert.False(t, consumed)
}

func TestConsumeServiceNonceSurvivesCacheRestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	cache, err := NewCacheDB(dir)
	require.NoError(t, err)
	d := NewUserDAO(nil, cache)
	consumed, err := d.ConsumeServiceNonce(ctx, "access-key", "nonce-1", 2*time.Minute)
	require.NoError(t, err)
	assert.True(t, consumed)
	require.NoError(t, cache.Close())

	reopened, err := NewCacheDB(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	d = NewUserDAO(nil, reopened)
	consumed, err = d.ConsumeServiceNonce(ctx, "access-key", "nonce-1", 2*time.Minute)
	require.NoError(t, err)
	assert.False(t, consumed)
}

func TestUserDAOSigningSessionAndRawTicketLifecycle(t *testing.T) {
	db, cache := setupUserTestDB(t)
	d := NewUserDAO(db, cache)
	ctx := context.Background()
	session := model.RequestSigningSession{SessionID: "sid", UserID: "uid", EncryptedSecret: "encrypted", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, d.SetSigningSession(ctx, session))
	gotSession, err := d.GetSigningSession(ctx, "sid")
	require.NoError(t, err)
	assert.Equal(t, session.SessionID, gotSession.SessionID)
	require.NoError(t, d.DeleteSigningSession(ctx, "sid"))
	_, err = d.GetSigningSession(ctx, "sid")
	require.ErrorIs(t, err, ErrKeyNotFound)

	ticket := model.RawSessionTicket{TicketID: "ticket", SessionID: "sid", UserID: "uid", Operation: "ssh_ws", ExpiresAt: time.Now().Add(time.Minute)}
	require.NoError(t, d.SetRawSessionTicket(ctx, ticket))
	gotTicket, err := d.ConsumeRawSessionTicket(ctx, "ticket")
	require.NoError(t, err)
	assert.Equal(t, ticket.TicketID, gotTicket.TicketID)
	_, err = d.ConsumeRawSessionTicket(ctx, "ticket")
	require.ErrorIs(t, err, ErrKeyNotFound)
}

func TestUserDAO_SetGetLoginAttempt_RoundTrip_ShouldWork(t *testing.T) {
	db, cache := setupUserTestDB(t)
	d := NewUserDAO(db, cache)

	attempt := model.LoginAttempt{
		Username:  "alice",
		IP:        "1.2.3.4",
		Attempts:  2,
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}
	require.NoError(t, d.SetLoginAttempt(context.Background(), "alice", "1.2.3.4", attempt))

	got, err := d.GetLoginAttempt(context.Background(), "alice", "1.2.3.4")
	require.NoError(t, err)
	assert.Equal(t, 2, got.Attempts)
	require.NoError(t, d.DeleteLoginAttempt(context.Background(), "alice", "1.2.3.4"))
}
