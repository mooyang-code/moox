package memadmit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReserveWithinBudgetAndRelease(t *testing.T) {
	a := New(100)
	a.SetResident("window", 30)
	r1, err := a.Reserve(context.Background(), 40)
	require.NoError(t, err)
	require.Equal(t, Stats{Budget: 100, Resident: 30, Reserved: 40}, a.Stats())
	r1()
	r1() // 重复释放无副作用
	require.Equal(t, int64(0), a.Stats().Reserved)
}

func TestReserveRejectsWhatCanNeverFit(t *testing.T) {
	a := New(100)
	a.SetResident("window", 70)
	_, err := a.Reserve(context.Background(), 31)
	require.ErrorIs(t, err, ErrTooLarge)
	_, err = a.Reserve(context.Background(), 30)
	require.NoError(t, err)
	// 预算 0 表示不限制。
	r, err := New(0).Reserve(context.Background(), 1<<40)
	require.NoError(t, err)
	r()
}

func TestReserveQueuesByPriorityAndHonorsContext(t *testing.T) {
	a := New(100)
	hold, err := a.Reserve(context.Background(), 100)
	require.NoError(t, err)

	var mu sync.Mutex
	var order []string
	var wg sync.WaitGroup
	start := func(name string, prio Priority) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := a.ReservePriority(context.Background(), 60, prio)
			require.NoError(t, err)
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			time.Sleep(10 * time.Millisecond)
			release()
		}()
		for a.Stats().Waiting == 0 || !waiting(a, name) { // 等它进入队列
			time.Sleep(time.Millisecond)
			if a.Stats().Waiting > 0 {
				break
			}
		}
	}
	start("background", PriorityBackground)
	waitFor(t, func() bool { return a.Stats().Waiting == 1 })
	start("realtime", PriorityRealtime)
	waitFor(t, func() bool { return a.Stats().Waiting == 2 })
	hold()
	wg.Wait()
	require.Equal(t, []string{"realtime", "background"}, order, "实时周期优先")

	// 等待中 ctx 结束：退出队列、返回错误，不占用预算。
	hold, err = a.Reserve(context.Background(), 100)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = a.Reserve(ctx, 10)
	require.True(t, errors.Is(err, context.DeadlineExceeded))
	require.Equal(t, 0, a.Stats().Waiting)
	hold()
	require.Equal(t, int64(0), a.Stats().Reserved)
}

func waiting(a *Admitter, _ string) bool { return a.Stats().Waiting > 0 }

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("等待条件超时")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestResidentGrowthChecksAndWakesWaiters(t *testing.T) {
	a := New(100)
	require.True(t, a.CanResident("w1", 60))
	a.SetResident("w1", 60)
	require.False(t, a.CanResident("w2", 50), "再加 50 会让常驻超过预算")
	require.True(t, a.CanResident("w1", 100), "同名覆盖时不重复计")

	hold, err := a.Reserve(context.Background(), 40)
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		release, err := a.Reserve(context.Background(), 30)
		require.NoError(t, err)
		release()
		close(done)
	}()
	waitFor(t, func() bool { return a.Stats().Waiting == 1 })
	a.SetResident("w1", 20) // 窗口缩小：排队的预留被满足
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("常驻减少后排队的预留没有被唤醒")
	}
	hold()
}

func TestReserveReadsPriorityFromContext(t *testing.T) {
	a := New(100)
	hold, err := a.Reserve(context.Background(), 100)
	require.NoError(t, err)
	got := make(chan string, 2)
	for _, c := range []struct {
		name string
		ctx  context.Context
	}{{"background", context.Background()}, {"realtime", WithPriority(context.Background(), PriorityRealtime)}} {
		go func() {
			release, err := a.Reserve(c.ctx, 60)
			require.NoError(t, err)
			got <- c.name
			time.Sleep(10 * time.Millisecond)
			release()
		}()
		waitFor(t, func() bool {
			return a.Stats().Waiting >= len(got)+1 || a.Stats().Waiting == 2 || c.name == "background" && a.Stats().Waiting == 1
		})
	}
	waitFor(t, func() bool { return a.Stats().Waiting == 2 })
	hold()
	require.Equal(t, "realtime", <-got)
	require.Equal(t, "background", <-got)
}
