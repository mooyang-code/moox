package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type blockingSampler struct {
	mu      sync.Mutex
	started chan struct{}
	release chan struct{}
	sampled []string
}

func (b *blockingSampler) SampleAccount(ctx context.Context, id string) error {
	if b.started != nil {
		select {
		case b.started <- struct{}{}:
		default:
		}
		select {
		case <-b.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	b.mu.Lock()
	b.sampled = append(b.sampled, id)
	b.mu.Unlock()
	return nil
}

// 后台 worker 正在做（可能很慢的）采样时，带期限的定时器调用必须按期限返回，并且待采样账户保留在队列里，
// 由后台 worker 随后处理；不能无限排队，也不能随着过期调用被丢弃。
func TestEquitySamplerTimerCallHonorsDeadlineAndKeepsPendingAccounts(t *testing.T) {
	service := &blockingSampler{started: make(chan struct{}, 1), release: make(chan struct{})}
	sampler := NewEquitySampler(service)
	sampler.Enqueue("account-a")
	backgroundDone := make(chan error, 1)
	go func() { backgroundDone <- sampler.RunPending(context.Background()) }()
	<-service.started // 后台已持有运行权，正卡在采样里

	sampler.Enqueue("account-b")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sampler.RunPending(ctx) }()
	select {
	case err := <-done:
		require.True(t, errors.Is(err, context.DeadlineExceeded), "err = %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timer call kept waiting for the running sampler past its deadline")
	}

	close(service.release)
	require.NoError(t, <-backgroundDone)
	// account-b 没有因为过期的定时器调用而丢失：下一次运行仍会采样它
	require.NoError(t, sampler.RunPending(context.Background()))
	service.mu.Lock()
	defer service.mu.Unlock()
	require.ElementsMatch(t, []string{"account-a", "account-b"}, service.sampled)
}

// 期限已到时，剩余待采样账户重新入队，不随过期调用丢弃。
func TestEquitySamplerRequeuesPendingAccountsWhenContextExpires(t *testing.T) {
	service := &blockingSampler{}
	sampler := NewEquitySampler(service)
	sampler.Enqueue("account-a")
	sampler.Enqueue("account-b")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, sampler.RunPending(ctx), context.Canceled)
	require.NoError(t, sampler.RunPending(context.Background()))
	service.mu.Lock()
	defer service.mu.Unlock()
	require.ElementsMatch(t, []string{"account-a", "account-b"}, service.sampled)
}
