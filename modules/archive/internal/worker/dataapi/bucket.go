package dataapi

import (
	"context"
	"sync"
	"time"
)

// bucket 是全部下载共享的令牌桶：容量为一秒的量，按 rate 字节/秒补充；不够时睡眠补足。
// 与运行用的节奏令牌桶分开计，下载不挤占回补的带宽，回补也不拖慢下载。
type bucket struct {
	rate  float64
	now   func() time.Time
	sleep func(context.Context, time.Duration) error

	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func newBucket(rate int64, now func() time.Time, sleep func(context.Context, time.Duration) error) *bucket {
	return &bucket{rate: float64(rate), now: now, sleep: sleep, tokens: float64(rate), last: now()}
}

// take 取走 n 个字节的令牌，超出时睡眠到补足为止。
func (b *bucket) take(ctx context.Context, n int64) error {
	b.mu.Lock()
	now := b.now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.rate {
		b.tokens = b.rate
	}
	b.last = now
	b.tokens -= float64(n)
	var wait time.Duration
	if b.tokens < 0 {
		wait = time.Duration(-b.tokens / b.rate * float64(time.Second))
	}
	b.mu.Unlock()
	return b.sleep(ctx, wait)
}
