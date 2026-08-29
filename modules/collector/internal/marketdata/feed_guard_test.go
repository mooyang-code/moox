package marketdata

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type fakeSleeper struct {
	clock  *fakeClock
	mu     sync.Mutex
	sleeps []time.Duration
}

func (s *fakeSleeper) Sleep(d time.Duration) {
	s.mu.Lock()
	s.sleeps = append(s.sleeps, d)
	s.mu.Unlock()
	s.clock.Advance(d)
}

func (s *fakeSleeper) Durations() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.sleeps...)
}

func TestFeedGuardTokenBucketUsesBurstThenSleepsDeterministically(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC))
	sleeper := &fakeSleeper{clock: clock}
	guard, err := NewFeedGuard(RateLimitPolicy{
		RequestsPerSecond: 1,
		Burst:             2,
		MaxConcurrent:     1,
		Cooldown:          time.Second,
		RequestTimeout:    time.Second,
	}, clock, sleeper.Sleep)
	require.NoError(t, err)

	for i := 0; i < 2; i++ {
		require.NoError(t, guard.Do(context.Background(), func(context.Context) error { return nil }))
	}
	require.NoError(t, guard.Do(context.Background(), func(context.Context) error { return nil }))

	require.Equal(t, []time.Duration{time.Second}, sleeper.Durations())
}

func TestFeedGuardCooldownAfter429(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC))
	sleeper := &fakeSleeper{clock: clock}
	guard, err := NewFeedGuard(RateLimitPolicy{
		RequestsPerSecond: 100,
		Burst:             1,
		MaxConcurrent:     1,
		Cooldown:          2 * time.Second,
		RequestTimeout:    time.Second,
	}, clock, sleeper.Sleep)
	require.NoError(t, err)

	firstStarted := clock.Now()
	require.ErrorIs(t, guard.Do(context.Background(), func(context.Context) error {
		assert.Equal(t, firstStarted, clock.Now())
		return ErrRateLimited
	}), ErrRateLimited)

	var secondStarted time.Time
	require.NoError(t, guard.Do(context.Background(), func(context.Context) error {
		secondStarted = clock.Now()
		return nil
	}))

	require.Equal(t, []time.Duration{2 * time.Second}, sleeper.Durations())
	assert.Equal(t, firstStarted.Add(2*time.Second), secondStarted)
}

func TestFeedGuardMaxConcurrentBlocksSecondCall(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	sleeper := &fakeSleeper{clock: clock}
	guard, err := NewFeedGuard(RateLimitPolicy{
		RequestsPerSecond: 100,
		Burst:             2,
		MaxConcurrent:     1,
		Cooldown:          time.Second,
		RequestTimeout:    time.Second,
	}, clock, sleeper.Sleep)
	require.NoError(t, err)

	releaseFirst := make(chan struct{})
	firstEntered := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- guard.Do(context.Background(), func(context.Context) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()

	<-firstEntered

	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- guard.Do(context.Background(), func(context.Context) error {
			close(secondEntered)
			return nil
		})
	}()

	runtime.Gosched()
	select {
	case <-secondEntered:
		t.Fatal("second call entered while the first call still held the semaphore")
	default:
	}

	close(releaseFirst)
	require.NoError(t, <-firstDone)
	require.NoError(t, <-secondDone)
	select {
	case <-secondEntered:
	default:
		t.Fatal("second call never entered after the semaphore was released")
	}
}

func TestInvocationBreakerSkipsAfterRetryableStreak(t *testing.T) {
	breaker := NewInvocationBreaker(2)
	session := breaker.NewSession()

	assert.False(t, session.ShouldSkip("alpha"))
	session.Observe("alpha", ErrRateLimited)
	assert.False(t, session.ShouldSkip("alpha"))
	session.Observe("alpha", fmt.Errorf("%w: status=503", ErrHTTPStatus))
	assert.True(t, session.ShouldSkip("alpha"))

	session.Observe("beta", ErrProtocol)
	assert.False(t, session.ShouldSkip("beta"))
	session.Observe("beta", ErrInvalidRequest)
	assert.False(t, session.ShouldSkip("beta"))
	session.Observe("beta", ErrRateLimited)
	assert.False(t, session.ShouldSkip("beta"))
}

func TestRouterSessionSharesInvocationBreakerAcrossSubjects(t *testing.T) {
	registry := NewRegistry()
	clock := newFakeClock(time.Date(2026, 8, 29, 12, 30, 0, 0, time.UTC))
	sleeper := &fakeSleeper{clock: clock}
	spec := KlineSpec{Markets: []string{"stock_cn"}, Exchanges: []string{"XSHG"}, Frequencies: []string{"1m"}, CompleteOHLCV: true, HasAmount: true, MaxBarsPerRequest: 1, TimestampMode: TimestampModeOpen, RateLimit: RateLimitPolicy{RequestsPerSecond: 100, Burst: 10, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: time.Second}}
	failing := &stubKlineProvider{testProvider: testProvider{descriptor: ProviderDescriptor{ID: "alpha", DisplayName: "Alpha", Hosts: []string{"alpha.test"}}}, spec: spec, err: ErrProtocol}
	succeeding := &stubKlineProvider{testProvider: testProvider{descriptor: ProviderDescriptor{ID: "beta", DisplayName: "Beta", Hosts: []string{"beta.test"}}}, spec: spec, rows: []NormalizedKline{{SubjectID: "600000.XSHG"}}}
	require.NoError(t, registry.Register(failing))
	require.NoError(t, registry.Register(succeeding))
	router, err := NewRouter(registry, 2, clock, sleeper.Sleep)
	require.NoError(t, err)
	session := router.NewSession()
	for index := 0; index < 3; index++ {
		_, err := session.FetchKlines(context.Background(), KlineRequest{SubjectID: "600000.XSHG", ProviderSymbol: "sh600000", Frequency: "1m", Limit: 1, RequestID: fmt.Sprintf("req-%d", index)}, []string{"alpha", "beta"})
		require.NoError(t, err)
	}
	assert.Equal(t, 2, failing.Calls())
	assert.Equal(t, 3, succeeding.Calls())
}

type stubKlineProvider struct {
	testProvider
	spec      KlineSpec
	rows      []NormalizedKline
	err       error
	mu        sync.Mutex
	callCount int
}

func (p *stubKlineProvider) KlineSpec() KlineSpec { return p.spec }

func (p *stubKlineProvider) FetchKlines(context.Context, KlineRequest) ([]NormalizedKline, error) {
	p.mu.Lock()
	p.callCount++
	p.mu.Unlock()
	return append([]NormalizedKline(nil), p.rows...), p.err
}

func (p *stubKlineProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.callCount
}

func TestRouterStopsAfterTwoProvidersAndReturnsTheSuccessfulRowsOnly(t *testing.T) {
	registry := NewRegistry()
	clock := newFakeClock(time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC))
	sleeper := &fakeSleeper{clock: clock}

	first := &stubKlineProvider{
		testProvider: testProvider{descriptor: ProviderDescriptor{ID: "alpha", DisplayName: "Alpha", Hosts: []string{"alpha.test"}}},
		spec: KlineSpec{
			Markets:           []string{"stock_cn"},
			Exchanges:         []string{"XSHG"},
			Frequencies:       []string{"1m"},
			CompleteOHLCV:     true,
			HasAmount:         true,
			MaxBarsPerRequest: 1,
			TimestampMode:     TimestampModeOpen,
			RateLimit: RateLimitPolicy{
				RequestsPerSecond: 100,
				Burst:             1,
				MaxConcurrent:     1,
				Cooldown:          time.Second,
				RequestTimeout:    time.Second,
			},
		},
		err: ErrTimeout,
	}
	secondRows := []NormalizedKline{{SubjectID: "600000.XSHG"}, {SubjectID: "600001.XSHG"}}
	second := &stubKlineProvider{
		testProvider: testProvider{descriptor: ProviderDescriptor{ID: "beta", DisplayName: "Beta", Hosts: []string{"beta.test"}}},
		spec:         first.spec,
		rows:         secondRows,
	}
	third := &stubKlineProvider{
		testProvider: testProvider{descriptor: ProviderDescriptor{ID: "gamma", DisplayName: "Gamma", Hosts: []string{"gamma.test"}}},
		spec:         first.spec,
		rows:         []NormalizedKline{{SubjectID: "600002.XSHG"}},
	}

	require.NoError(t, registry.Register(first))
	require.NoError(t, registry.Register(second))
	require.NoError(t, registry.Register(third))

	router, err := NewRouter(registry, 2, clock, sleeper.Sleep)
	require.NoError(t, err)

	rows, err := router.FetchKlines(context.Background(), KlineRequest{
		SubjectID:      "600000.XSHG",
		ProviderSymbol: "sh600000",
		Frequency:      "1m",
		Limit:          1,
		RequestID:      "req-1",
	}, []string{"alpha", "beta", "gamma"})
	require.NoError(t, err)
	require.Equal(t, secondRows, rows)
	assert.Equal(t, 1, first.Calls())
	assert.Equal(t, 1, second.Calls())
	assert.Equal(t, 0, third.Calls())
}

func TestRouterStopsImmediatelyWhenFallbackIsNotAllowed(t *testing.T) {
	registry := NewRegistry()
	clock := newFakeClock(time.Date(2026, 8, 29, 14, 0, 0, 0, time.UTC))
	sleeper := &fakeSleeper{clock: clock}

	first := &stubKlineProvider{
		testProvider: testProvider{descriptor: ProviderDescriptor{ID: "alpha", DisplayName: "Alpha", Hosts: []string{"alpha.test"}}},
		spec: KlineSpec{
			Markets:           []string{"stock_cn"},
			Exchanges:         []string{"XSHG"},
			Frequencies:       []string{"1m"},
			CompleteOHLCV:     true,
			HasAmount:         true,
			MaxBarsPerRequest: 1,
			TimestampMode:     TimestampModeOpen,
			RateLimit: RateLimitPolicy{
				RequestsPerSecond: 100,
				Burst:             1,
				MaxConcurrent:     1,
				Cooldown:          time.Second,
				RequestTimeout:    time.Second,
			},
		},
		err: ErrInvalidRequest,
	}
	second := &stubKlineProvider{
		testProvider: testProvider{descriptor: ProviderDescriptor{ID: "beta", DisplayName: "Beta", Hosts: []string{"beta.test"}}},
		spec:         first.spec,
		rows:         []NormalizedKline{{SubjectID: "600001.XSHG"}},
	}

	require.NoError(t, registry.Register(first))
	require.NoError(t, registry.Register(second))

	router, err := NewRouter(registry, 2, clock, sleeper.Sleep)
	require.NoError(t, err)

	rows, err := router.FetchKlines(context.Background(), KlineRequest{
		SubjectID:      "600000.XSHG",
		ProviderSymbol: "sh600000",
		Frequency:      "1m",
		Limit:          1,
		RequestID:      "req-2",
	}, []string{"alpha", "beta"})
	require.ErrorIs(t, err, ErrInvalidRequest)
	require.Nil(t, rows)
	assert.Equal(t, 1, first.Calls())
	assert.Equal(t, 0, second.Calls())
}
