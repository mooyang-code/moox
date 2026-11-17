package session

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T, ttl time.Duration) (*Store, *time.Time) {
	t.Helper()
	store, err := NewStore(ttl)
	require.NoError(t, err)
	now := time.Unix(1_800_000_000, 0)
	store.now = func() time.Time { return now }
	return store, &now
}

func TestStoreSessionExpiresAfterTTLWithoutSliding(t *testing.T) {
	store, now := newTestStore(t, 24*time.Hour)
	created, err := store.Create("user-1", "admin")
	require.NoError(t, err)
	require.Len(t, created.ID, 64)
	require.Equal(t, now.Add(24*time.Hour), created.ExpiresAt)

	*now = now.Add(23 * time.Hour)
	got, ok := store.Get(created.ID)
	require.True(t, ok)
	require.Equal(t, "user-1", got.UserID)
	require.Equal(t, created.ExpiresAt, got.ExpiresAt, "使用不顺延过期时间")

	*now = now.Add(time.Hour)
	_, ok = store.Get(created.ID)
	require.False(t, ok, "满 24 小时即失效")
	_, ok = store.Get(created.ID)
	require.False(t, ok)
}

func TestStoreDeleteInvalidatesSession(t *testing.T) {
	store, _ := newTestStore(t, time.Hour)
	created, err := store.Create("user-1", "admin")
	require.NoError(t, err)
	store.Delete(created.ID)
	_, ok := store.Get(created.ID)
	require.False(t, ok)
	_, ok = store.Get("")
	require.False(t, ok)
}

func TestStoreCreateSweepsExpiredSessions(t *testing.T) {
	store, now := newTestStore(t, time.Hour)
	old, err := store.Create("user-1", "admin")
	require.NoError(t, err)
	*now = now.Add(2 * time.Hour)
	fresh, err := store.Create("user-1", "admin")
	require.NoError(t, err)
	require.NotEqual(t, old.ID, fresh.ID)
	require.Len(t, store.sessions, 1)
}

func TestNewStoreRejectsNonPositiveTTL(t *testing.T) {
	_, err := NewStore(0)
	require.Error(t, err)
}

func TestLoginContextRoundTrip(t *testing.T) {
	require.Nil(t, LoginFrom(context.Background()))
	login := &Login{ClientIP: "203.0.113.9"}
	require.Same(t, login, LoginFrom(WithLogin(context.Background(), login)))
}
