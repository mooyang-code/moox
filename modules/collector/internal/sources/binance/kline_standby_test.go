package binance

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	"github.com/stretchr/testify/require"
)

type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(domain string) {
	l.mu.Lock()
	l.calls = append(l.calls, domain)
	l.mu.Unlock()
}

func (l *callLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

var oneKline = []*exchange.Kline{{}}

func standbyCtx(t *testing.T, delay time.Duration) (context.Context, *httpclient.Trace) {
	t.Helper()
	trace := &httpclient.Trace{}
	ctx, cancel := context.WithTimeout(httpclient.WithTrace(context.Background(), trace), 5*time.Second)
	t.Cleanup(cancel)
	return httpclient.WithStandbyDelay(ctx, delay), trace
}

func TestTryEndpointsWithoutStandbyIsSequential(t *testing.T) {
	log := &callLog{}
	ctx, trace := standbyCtx(t, 0)
	klines, err := tryEndpoints(ctx, []string{"a", "b"}, func(_ context.Context, domain string) ([]*exchange.Kline, error) {
		log.add(domain)
		if domain == "a" {
			return nil, errors.New("a down")
		}
		return oneKline, nil
	})
	require.NoError(t, err)
	require.Len(t, klines, 1)
	require.Equal(t, []string{"a", "b"}, log.snapshot())
	require.Zero(t, trace.Snapshot().StandbyFired)
}

func TestTryEndpointsStandbysToNextEndpointWhenFirstStalls(t *testing.T) {
	log := &callLog{}
	ctx, trace := standbyCtx(t, 50*time.Millisecond)
	started := time.Now()
	klines, err := tryEndpoints(ctx, []string{"a", "b", "c"}, func(ctx context.Context, domain string) ([]*exchange.Kline, error) {
		log.add(domain)
		if domain == "a" {
			select { // 第一个端点停住，直到被取消。
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(3 * time.Second):
				return oneKline, nil
			}
		}
		return oneKline, nil
	})
	require.NoError(t, err)
	require.Len(t, klines, 1)
	require.Less(t, time.Since(started), time.Second, "备援请求先成功，不用等停住的请求")
	require.Equal(t, []string{"a", "b"}, log.snapshot())
	snap := trace.Snapshot()
	require.Equal(t, 1, snap.StandbyFired)
	require.Equal(t, 1, snap.StandbyWon)
}

func TestTryEndpointsStandbysTheSameEndpointWhenThereIsOnlyOne(t *testing.T) {
	log := &callLog{}
	ctx, trace := standbyCtx(t, 50*time.Millisecond)
	var first sync.Once
	klines, err := tryEndpoints(ctx, []string{"only"}, func(ctx context.Context, domain string) ([]*exchange.Kline, error) {
		log.add(domain)
		stall := false
		first.Do(func() { stall = true })
		if stall {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return oneKline, nil
	})
	require.NoError(t, err)
	require.Len(t, klines, 1)
	require.Equal(t, []string{"only", "only"}, log.snapshot())
	require.Equal(t, 1, trace.Snapshot().StandbyWon)
}

func TestTryEndpointsDoesNotStandbyWhenFirstSucceedsOrFailsBeforeTheDelay(t *testing.T) {
	// 在延迟之前成功：不备援。
	log := &callLog{}
	ctx, trace := standbyCtx(t, 300*time.Millisecond)
	_, err := tryEndpoints(ctx, []string{"a", "b"}, func(_ context.Context, domain string) ([]*exchange.Kline, error) {
		log.add(domain)
		return oneKline, nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, log.snapshot())
	require.Zero(t, trace.Snapshot().StandbyFired)

	// 在延迟之前失败：普通的顺序换端点，不算备援。
	log = &callLog{}
	ctx, trace = standbyCtx(t, 300*time.Millisecond)
	_, err = tryEndpoints(ctx, []string{"a", "b"}, func(_ context.Context, domain string) ([]*exchange.Kline, error) {
		log.add(domain)
		if domain == "a" {
			return nil, errors.New("reset")
		}
		return oneKline, nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, log.snapshot())
	require.Zero(t, trace.Snapshot().StandbyFired)
}

func TestTryEndpointsReturnsErrorWhenPrimaryAndStandbyBothFail(t *testing.T) {
	ctx, trace := standbyCtx(t, 30*time.Millisecond)
	_, err := tryEndpoints(ctx, []string{"a", "b"}, func(ctx context.Context, domain string) ([]*exchange.Kline, error) {
		if domain == "a" {
			time.Sleep(100 * time.Millisecond)
			return nil, errors.New("a failed late")
		}
		return nil, errors.New("b failed")
	})
	require.Error(t, err)
	snap := trace.Snapshot()
	require.Equal(t, 1, snap.StandbyFired)
	require.Zero(t, snap.StandbyWon)
}
