package bootstrap

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

func TestBackfillLoopFollowsLifecycleStagesAndManualPauses(t *testing.T) {
	var stageVal atomic.Value
	stageVal.Store(lifecycle.StagePaused)

	var mu sync.Mutex
	manual := map[string]bool{"stockcn": true}
	spaces := []string{"crypto", "stockcn"}
	var runningHits []string
	var resumingHits []string

	loop := newBackfillLoop(backfillLoopConfig{
		Spaces: func(context.Context) ([]string, error) {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), spaces...), nil
		},
		ManualPaused: func(context.Context) (map[string]bool, error) {
			mu.Lock()
			defer mu.Unlock()
			out := make(map[string]bool, len(manual))
			for k, v := range manual {
				out[k] = v
			}
			return out, nil
		},
		Stage: func() lifecycle.Stage {
			return stageVal.Load().(lifecycle.Stage)
		},
		RunRunning: func(_ context.Context, spaceID string) {
			mu.Lock()
			runningHits = append(runningHits, spaceID)
			mu.Unlock()
		},
		RunResuming: func(_ context.Context, spaceID string) {
			mu.Lock()
			resumingHits = append(resumingHits, spaceID)
			mu.Unlock()
		},
		SyncInterval:   20 * time.Millisecond,
		RunInterval:    25 * time.Millisecond,
		ResumeInterval: 15 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		loop.Run(ctx)
	}()

	// StagePaused：不跑任何补采。
	time.Sleep(60 * time.Millisecond)
	mu.Lock()
	require.Empty(t, runningHits)
	require.Empty(t, resumingHits)
	mu.Unlock()

	// StageResuming：只跑 RunResuming，且跳过手工暂停的 stockcn。
	stageVal.Store(lifecycle.StageResuming)
	loop.Wake()
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(resumingHits) >= 2
	}, time.Second, 10*time.Millisecond)
	mu.Lock()
	require.Empty(t, runningHits)
	for _, s := range resumingHits {
		require.Equal(t, "crypto", s, "手工暂停的 stockcn 在恢复追赶期不跑补采")
	}
	mu.Unlock()

	// StageRunning：只跑 RunRunning，仍跳过手工暂停的 stockcn。
	stageVal.Store(lifecycle.StageRunning)
	loop.Wake()
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(runningHits) >= 2
	}, time.Second, 10*time.Millisecond)
	mu.Lock()
	for _, s := range runningHits {
		require.Equal(t, "crypto", s)
	}
	// 放开 stockcn 的手工暂停后，stockcn 也开始跑 RunRunning。
	manual["stockcn"] = false
	mu.Unlock()
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, s := range runningHits {
			if s == "stockcn" {
				return true
			}
		}
		return false
	}, time.Second, 10*time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("backfillLoop did not stop cleanly")
	}
}
