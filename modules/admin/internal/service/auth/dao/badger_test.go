package dao

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupCacheDB(t *testing.T) *CacheDB {
	t.Helper()
	return openTestCacheDB(t, t.TempDir())
}

func openTestCacheDB(t *testing.T, dir string) *CacheDB {
	t.Helper()
	db, err := badger.Open(badger.DefaultOptions(dir).WithLogger(nil))
	require.NoError(t, err)
	cdb, err := NewCacheDBFromBadger(db)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cdb.Close() })
	return cdb
}

func TestNewCacheDBFromBadger_NilDB_ShouldError(t *testing.T) {
	_, err := NewCacheDBFromBadger(nil)
	require.Error(t, err)
}

func TestCacheDB_SetGet_RoundTrip_ShouldWork(t *testing.T) {
	cdb := setupCacheDB(t)
	ctx := context.Background()

	require.NoError(t, cdb.Set(ctx, "k1", "v1", time.Minute))
	got, err := cdb.Get(ctx, "k1")
	require.NoError(t, err)
	assert.Equal(t, "v1", got)
}

func TestCacheDB_Get_MissingKey_ShouldReturnErrKeyNotFound(t *testing.T) {
	cdb := setupCacheDB(t)
	_, err := cdb.Get(context.Background(), "missing")
	require.ErrorIs(t, err, ErrKeyNotFound)
}

func TestCacheDB_Del_ExistingKey_ShouldRemove(t *testing.T) {
	cdb := setupCacheDB(t)
	ctx := context.Background()
	require.NoError(t, cdb.Set(ctx, "k2", "v2", time.Minute))
	require.NoError(t, cdb.Del(ctx, "k2"))
	_, err := cdb.Get(ctx, "k2")
	require.ErrorIs(t, err, ErrKeyNotFound)
}

func TestCacheDBDeleteRemovesValue(t *testing.T) {
	cdb := setupCacheDB(t)
	ctx := context.Background()
	require.NoError(t, cdb.Set(ctx, "delete-me", "value", time.Minute))
	require.NoError(t, cdb.Delete(ctx, "delete-me"))
	_, err := cdb.Get(ctx, "delete-me")
	require.ErrorIs(t, err, ErrKeyNotFound)
}

func TestCacheDBSetIfAbsentAllowsOnlyOneConcurrentWriter(t *testing.T) {
	cdb := setupCacheDB(t)
	ctx := context.Background()
	const writers = 20
	start := make(chan struct{})
	results := make(chan bool, writers)
	errors := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := cdb.SetIfAbsent(ctx, "winner", "value", time.Minute)
			results <- ok
			errors <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errors)

	for err := range errors {
		require.NoError(t, err)
	}
	winners := 0
	for ok := range results {
		if ok {
			winners++
		}
	}
	assert.Equal(t, 1, winners)
}

func TestCacheDB_Exists_ExistingKey_ShouldReturnTrue(t *testing.T) {
	cdb := setupCacheDB(t)
	ctx := context.Background()
	require.NoError(t, cdb.Set(ctx, "k3", "v3", time.Minute))
	ok, err := cdb.Exists(ctx, "k3")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestCacheDB_BatchSetGet_MultipleKeys_ShouldWork(t *testing.T) {
	cdb := setupCacheDB(t)
	ctx := context.Background()
	kvs := map[string]string{"a": "1", "b": "2"}
	require.NoError(t, cdb.BatchSet(ctx, kvs, time.Minute))
	got, err := cdb.BatchGet(ctx, []string{"a", "b", "c"})
	require.NoError(t, err)
	assert.Equal(t, "1", got["a"])
	assert.Equal(t, "2", got["b"])
}

func TestCacheDB_Incr_NewKey_ShouldReturnOne(t *testing.T) {
	cdb := setupCacheDB(t)
	val, err := cdb.Incr(context.Background(), "counter")
	require.NoError(t, err)
	assert.Equal(t, int64(1), val)
}

func TestCacheDB_ExpireAndTTL_ShouldUpdateRemainingTime(t *testing.T) {
	cdb := setupCacheDB(t)
	ctx := context.Background()

	require.NoError(t, cdb.Set(ctx, "ttl-key", "value", time.Hour))
	require.NoError(t, cdb.Expire(ctx, "ttl-key", 30*time.Minute))

	remaining, err := cdb.TTL(ctx, "ttl-key")
	require.NoError(t, err)
	assert.Greater(t, remaining, time.Duration(0))
	assert.LessOrEqual(t, remaining, 30*time.Minute)
}

func TestCacheDB_TTL_MissingKey_ShouldReturnNegativeTwo(t *testing.T) {
	cdb := setupCacheDB(t)
	remaining, err := cdb.TTL(context.Background(), "missing")
	require.NoError(t, err)
	assert.Equal(t, time.Duration(-2), remaining)
}

func TestCacheDB_LockUnlock_SingleHolder_ShouldWork(t *testing.T) {
	cdb := setupCacheDB(t)
	ctx := context.Background()
	assert.True(t, cdb.Lock(ctx, "job", time.Minute))
	assert.False(t, cdb.Lock(ctx, "job", time.Minute))
	require.NoError(t, cdb.Unlock(ctx, "job"))
	assert.True(t, cdb.Lock(ctx, "job", time.Minute))
}

func TestCacheDB_Scan_WithPrefix_ShouldReturnKeys(t *testing.T) {
	cdb := setupCacheDB(t)
	ctx := context.Background()
	require.NoError(t, cdb.Set(ctx, "prefix:a", "1", time.Minute))
	require.NoError(t, cdb.Set(ctx, "prefix:b", "2", time.Minute))
	require.NoError(t, cdb.Set(ctx, "other", "3", time.Minute))

	keys, err := cdb.Scan(ctx, "prefix:", 0)
	require.NoError(t, err)
	assert.Len(t, keys, 2)
}

func TestCacheDB_Ping_ShouldSucceed(t *testing.T) {
	cdb := setupCacheDB(t)
	require.NoError(t, cdb.Ping(context.Background()))
}

func TestCacheDBRunValueLogGCTreatsNoRewriteAsSuccess(t *testing.T) {
	cdb := setupCacheDB(t)
	require.NoError(t, cdb.RunValueLogGC(context.Background()))
}

func TestCacheDBRunValueLogGCRejectsCanceledContext(t *testing.T) {
	cdb := setupCacheDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, cdb.RunValueLogGC(ctx), context.Canceled)
}
