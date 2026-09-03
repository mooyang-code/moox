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

func TestRateBudgetRatioScalesProviderRequestPolicy(t *testing.T) {
	policy := scaleRateLimitPolicy(RateLimitPolicy{
		RequestsPerSecond: 10,
		Burst:             4,
		MaxConcurrent:     6,
		Cooldown:          time.Second,
		RequestTimeout:    time.Second,
	}, 0.25)

	require.Equal(t, 2.5, policy.RequestsPerSecond)
	require.Equal(t, 1, policy.Burst)
	require.Equal(t, 1, policy.MaxConcurrent)
	require.Equal(t, time.Second, policy.Cooldown)

	require.Equal(t, RateLimitPolicy{RequestsPerSecond: 10, Burst: 4, MaxConcurrent: 6, Cooldown: time.Second, RequestTimeout: time.Second}, scaleRateLimitPolicy(RateLimitPolicy{
		RequestsPerSecond: 10,
		Burst:             4,
		MaxConcurrent:     6,
		Cooldown:          time.Second,
		RequestTimeout:    time.Second,
	}, 0))
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

func TestFeedGuardAcquiresSemaphoreBeforeWaitingForRateLimit(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 8, 29, 12, 15, 0, 0, time.UTC))
	sleeper := &fakeSleeper{clock: clock}
	guard, err := NewFeedGuard(RateLimitPolicy{
		RequestsPerSecond: 1,
		Burst:             1,
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

	secondCtx, secondCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer secondCancel()
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- guard.Do(secondCtx, func(context.Context) error {
			return fmt.Errorf("second call entered while the semaphore was held")
		})
	}()
	require.ErrorIs(t, <-secondDone, context.DeadlineExceeded)
	require.Empty(t, sleeper.Durations())
	close(releaseFirst)
	require.NoError(t, <-firstDone)
}

func TestFeedGuardCanceledWaitDoesNotConsumeToken(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 8, 29, 12, 20, 0, 0, time.UTC))
	sleeper := &fakeSleeper{clock: clock}
	guard, err := NewFeedGuard(RateLimitPolicy{RequestsPerSecond: 1, Burst: 1, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: time.Second}, clock, sleeper.Sleep)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, guard.wait(ctx), context.Canceled)
	require.NoError(t, guard.Do(context.Background(), func(context.Context) error { return nil }))
	require.Empty(t, sleeper.Durations())
}

func TestFeedGuardRateLimitWaitIsCancelable(t *testing.T) {
	guard, err := NewFeedGuard(RateLimitPolicy{
		RequestsPerSecond: 1,
		Burst:             1,
		MaxConcurrent:     1,
		Cooldown:          time.Second,
		RequestTimeout:    time.Second,
	}, nil, nil)
	require.NoError(t, err)
	require.NoError(t, guard.Do(context.Background(), func(context.Context) error { return nil }))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- guard.Do(ctx, func(context.Context) error {
			return fmt.Errorf("canceled limiter wait entered provider call")
		})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("rate limiter wait ignored cancellation")
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

func TestInvocationBreakerAdmissionIsAtomic(t *testing.T) {
	session := NewInvocationBreaker(2).NewSession()
	start := make(chan struct{})
	results := make(chan bool, 16)
	var wg sync.WaitGroup
	for index := 0; index < cap(results); index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			admitted := session.Admit(context.Background(), "alpha")
			results <- admitted
			if admitted {
				session.Observe("alpha", ErrTimeout)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	admitted := 0
	for result := range results {
		if result {
			admitted++
		}
	}
	require.Equal(t, 2, admitted)
}

func TestInvocationBreakerCountsTimeoutFailures(t *testing.T) {
	session := NewInvocationBreaker(2).NewSession()
	require.True(t, session.Admit(context.Background(), "alpha"))
	session.Observe("alpha", ErrTimeout)
	require.True(t, session.Admit(context.Background(), "alpha"))
	session.Observe("alpha", context.DeadlineExceeded)
	require.False(t, session.Admit(context.Background(), "alpha"))
}

func TestInvocationBreakerSuccessWakesWaitingAdmission(t *testing.T) {
	session := NewInvocationBreaker(1).NewSession()
	require.True(t, session.Admit(context.Background(), "alpha"))
	admitted := make(chan bool, 1)
	go func() { admitted <- session.Admit(context.Background(), "alpha") }()
	session.Observe("alpha", nil)
	require.True(t, <-admitted)
	session.Observe("alpha", nil)
}

func TestInvocationBreakerAdmissionWaitIsCancelable(t *testing.T) {
	session := NewInvocationBreaker(1).NewSession()
	require.True(t, session.Admit(context.Background(), "alpha"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.False(t, session.Admit(ctx, "alpha"))
	session.Observe("alpha", nil)
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

func TestRouterSkipsProviderThatDoesNotAdvertiseExchange(t *testing.T) {
	registry := NewRegistry()
	clock := newFakeClock(time.Date(2026, 8, 29, 12, 30, 0, 0, time.UTC))
	sleeper := &fakeSleeper{clock: clock}
	spec := KlineSpec{Markets: []string{"stock_cn"}, Frequencies: []string{"1m"}, CompleteOHLCV: true, HasAmount: true, MaxBarsPerRequest: 1, TimestampMode: TimestampModeOpen, RateLimit: RateLimitPolicy{RequestsPerSecond: 100, Burst: 1, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: time.Second}}
	shanghai := &stubKlineProvider{
		testProvider: testProvider{descriptor: ProviderDescriptor{ID: "shanghai", DisplayName: "Shanghai", Hosts: []string{"shanghai.test"}}},
		spec:         spec,
		err:          ErrProtocol,
	}
	shanghai.spec.Exchanges = []string{"XSHG"}
	beijing := &stubKlineProvider{
		testProvider: testProvider{descriptor: ProviderDescriptor{ID: "beijing", DisplayName: "Beijing", Hosts: []string{"beijing.test"}}},
		spec:         spec,
		rows:         []NormalizedKline{{SubjectID: "920000.XBSE", Frequency: "1m", BarStart: clock.Now().Add(-time.Minute), BarEnd: clock.Now()}},
	}
	beijing.spec.Exchanges = []string{"XBSE"}
	require.NoError(t, registry.Register(shanghai))
	require.NoError(t, registry.Register(beijing))
	router, err := NewRouter(registry, 2, clock, sleeper.Sleep)
	require.NoError(t, err)

	rows, err := router.FetchKlines(context.Background(), KlineRequest{
		MarketID: "stock_cn", ExchangeID: "XBSE", SubjectID: "920000.XBSE", ProviderSymbol: "bj920000", Frequency: "1m", Limit: 1, RequestID: "req-xbse",
	}, []string{"shanghai", "beijing"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, 0, shanghai.Calls(), "unsupported exchange must not consume a provider request")
	assert.Equal(t, 1, beijing.Calls())
}

func TestRouterResolvesFallbackProviderByItsOwnSource(t *testing.T) {
	registry := NewRegistry()
	spec := KlineSpec{
		Markets: []string{"stock_cn"}, Exchanges: []string{"XSHG"}, Frequencies: []string{"1m"},
		CompleteOHLCV: true, HasAmount: true, MaxBarsPerRequest: 1, TimestampMode: TimestampModeOpen,
		RateLimit: RateLimitPolicy{RequestsPerSecond: 100, Burst: 2, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: time.Second},
	}
	require.NoError(t, registry.Register(&stubKlineProvider{
		testProvider: testProvider{descriptor: ProviderDescriptor{ID: "sina", SourceID: "stock_cn_minute_http", DisplayName: "Sina", Hosts: []string{"sina.test"}}},
		spec:         spec, err: ErrProtocol,
	}))
	require.NoError(t, registry.Register(&stubKlineProvider{
		testProvider: testProvider{descriptor: ProviderDescriptor{ID: "tdx", SourceID: "normal_7709", DisplayName: "TDX", Hosts: []string{"tdx.test"}}},
		spec:         spec, rows: []NormalizedKline{{SubjectID: "600000.XSHG"}},
	}))
	router, err := NewRouter(registry, 3, nil, nil)
	require.NoError(t, err)
	rows, err := router.FetchKlines(context.Background(), KlineRequest{
		MarketID: "stock_cn", ExchangeID: "XSHG", SubjectID: "600000.XSHG", ProviderSymbol: "sh600000",
		SourceID: "stock_cn_minute_http", Frequency: "1m", Limit: 1, RequestID: "source-fallback",
	}, []string{"sina", "tdx"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
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
