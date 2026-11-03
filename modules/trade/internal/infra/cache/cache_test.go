package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSnapshotLoadsOnceWithinTTL(t *testing.T) {
	snapshot := NewSnapshot[int](time.Hour)
	var loads atomic.Int32
	load := func(context.Context) (int, error) { loads.Add(1); return 7, nil }
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, at, err := snapshot.Get(context.Background(), load)
			require.NoError(t, err)
			require.Equal(t, 7, value)
			require.False(t, at.IsZero())
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, loads.Load())
}

func TestSnapshotReloadsAfterTTLAndDoesNotKeepFailures(t *testing.T) {
	snapshot := NewSnapshot[int](10 * time.Millisecond)
	value, _, err := snapshot.Get(context.Background(), func(context.Context) (int, error) { return 1, nil })
	require.NoError(t, err)
	require.Equal(t, 1, value)
	time.Sleep(20 * time.Millisecond)
	_, _, err = snapshot.Get(context.Background(), func(context.Context) (int, error) { return 0, errors.New("down") })
	require.Error(t, err)
	value, _, err = snapshot.Get(context.Background(), func(context.Context) (int, error) { return 2, nil })
	require.NoError(t, err)
	require.Equal(t, 2, value)
}

// 等待别人取数时要响应自己的上下文取消，不能卡在锁上。
func TestSnapshotWaitHonorsContext(t *testing.T) {
	snapshot := NewSnapshot[int](time.Hour)
	release := make(chan struct{})
	started := make(chan struct{})
	go func() {
		_, _, _ = snapshot.Get(context.Background(), func(context.Context) (int, error) {
			close(started)
			<-release
			return 1, nil
		})
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _, err := snapshot.Get(ctx, func(context.Context) (int, error) { return 2, nil })
	require.ErrorIs(t, err, context.DeadlineExceeded)
	close(release)
}

// 读取期间发生失效时不写回旧值。
func TestMemoDropsLoadRacingInvalidate(t *testing.T) {
	var memo Memo[string, int]
	value, err := memo.Get("k", func() (int, error) {
		memo.Invalidate()
		return 1, nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, value)
	value, err = memo.Get("k", func() (int, error) { return 2, nil })
	require.NoError(t, err)
	require.Equal(t, 2, value)
	value, err = memo.Get("k", func() (int, error) { return 3, nil })
	require.NoError(t, err)
	require.Equal(t, 2, value, "未失效时命中缓存")
	_, err = memo.Get("e", func() (int, error) { return 0, errors.New("x") })
	require.Error(t, err)
	value, err = memo.Get("e", func() (int, error) { return 5, nil })
	require.NoError(t, err)
	require.Equal(t, 5, value, "出错不缓存")
}

func TestSnapshotLoadPanicReleasesWaiters(t *testing.T) {
	snapshot := NewSnapshot[int](time.Minute)
	func() {
		defer func() { _ = recover() }()
		_, _, _ = snapshot.Get(context.Background(), func(context.Context) (int, error) { panic("boom") })
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	value, _, err := snapshot.Get(ctx, func(context.Context) (int, error) { return 7, nil })
	if err != nil || value != 7 {
		t.Fatalf("panic 后应能重新加载，得到 %d, %v", value, err)
	}
}

func TestSnapshotWaiterRetriesWhenLoaderCanceled(t *testing.T) {
	snapshot := NewSnapshot[int](time.Minute)
	loaderCtx, cancelLoader := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		_, _, _ = snapshot.Get(loaderCtx, func(ctx context.Context) (int, error) {
			close(started)
			<-ctx.Done()
			return 0, ctx.Err()
		})
	}()
	<-started
	result := make(chan error, 1)
	go func() {
		value, _, err := snapshot.Get(context.Background(), func(context.Context) (int, error) { return 9, nil })
		if err == nil && value != 9 {
			err = errors.New("值不对")
		}
		result <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancelLoader()
	if err := <-result; err != nil {
		t.Fatalf("加载方取消后等待者应重新取：%v", err)
	}
}
