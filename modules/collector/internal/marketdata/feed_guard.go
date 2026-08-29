package marketdata

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
}

type SleepFunc func(time.Duration)

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now()
}

type FeedGuard struct {
	policy RateLimitPolicy
	clock  Clock
	sleep  SleepFunc

	sem chan struct{}

	mu            sync.Mutex
	initialized   bool
	tokens        float64
	lastRefill    time.Time
	cooldownUntil time.Time
}

func NewFeedGuard(policy RateLimitPolicy, clock Clock, sleep SleepFunc) (*FeedGuard, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if clock == nil {
		clock = realClock{}
	}
	if sleep == nil {
		sleep = func(time.Duration) {}
	}
	return &FeedGuard{
		policy: policy,
		clock:  clock,
		sleep:  sleep,
		sem:    make(chan struct{}, policy.MaxConcurrent),
	}, nil
}

func (g *FeedGuard) Do(ctx context.Context, fn func(context.Context) error) error {
	if err := g.wait(ctx); err != nil {
		return err
	}

	select {
	case g.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() {
		<-g.sem
	}()

	err := fn(ctx)
	if errors.Is(err, ErrRateLimited) {
		g.setCooldown()
	}
	return err
}

func (g *FeedGuard) wait(ctx context.Context) error {
	for {
		now := g.clock.Now()

		g.mu.Lock()
		wait := time.Duration(0)
		switch {
		case !g.cooldownUntil.IsZero() && now.Before(g.cooldownUntil):
			wait = g.cooldownUntil.Sub(now)
		default:
			if !g.initialized {
				g.initialized = true
				g.tokens = float64(g.policy.Burst)
				g.lastRefill = now
			} else {
				elapsed := now.Sub(g.lastRefill)
				if elapsed > 0 {
					g.tokens = math.Min(float64(g.policy.Burst), g.tokens+elapsed.Seconds()*g.policy.RequestsPerSecond)
					g.lastRefill = now
				}
			}
			if g.tokens >= 1 {
				g.tokens--
				g.mu.Unlock()
				return nil
			}
			wait = tokenWaitDuration(g.tokens, g.policy.RequestsPerSecond)
		}
		g.mu.Unlock()

		if wait <= 0 {
			wait = time.Nanosecond
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		g.sleep(wait)
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func (g *FeedGuard) setCooldown() {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock.Now()
	next := now.Add(g.policy.Cooldown)
	if next.After(g.cooldownUntil) {
		g.cooldownUntil = next
	}
}

func tokenWaitDuration(tokens float64, rate float64) time.Duration {
	if tokens >= 1 {
		return 0
	}
	if rate <= 0 {
		return time.Second
	}
	need := 1 - tokens
	seconds := need / rate
	return time.Duration(math.Ceil(seconds * float64(time.Second)))
}

type InvocationBreaker struct {
	threshold int
}

func NewInvocationBreaker(threshold int) *InvocationBreaker {
	if threshold <= 0 {
		threshold = 1
	}
	return &InvocationBreaker{threshold: threshold}
}

func (b *InvocationBreaker) NewSession() *InvocationBreakerSession {
	return &InvocationBreakerSession{
		threshold: b.threshold,
		streaks:   make(map[string]int),
	}
}

type InvocationBreakerSession struct {
	threshold int
	streaks   map[string]int
}

func (s *InvocationBreakerSession) ShouldSkip(providerID string) bool {
	return s.streaks[providerID] >= s.threshold
}

func (s *InvocationBreakerSession) Observe(providerID string, err error) {
	if err == nil {
		s.streaks[providerID] = 0
		return
	}
	if isBreakerFailure(err) {
		s.streaks[providerID]++
		return
	}
	s.streaks[providerID] = 0
}

func isBreakerFailure(err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, ErrRateLimited):
		return true
	case errors.Is(err, ErrProtocol):
		return true
	case errors.Is(err, ErrHTTPStatus):
		status, ok := httpStatusCode(err)
		return ok && status >= 500 && status <= 599
	default:
		return false
	}
}

var httpStatusPattern = regexp.MustCompile(`(?i)\bstatus=(\d{3})\b`)

func httpStatusCode(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	matches := httpStatusPattern.FindStringSubmatch(err.Error())
	if len(matches) != 2 {
		return 0, false
	}
	status, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, false
	}
	return status, true
}

type Router struct {
	registry       *Registry
	clock          Clock
	sleep          SleepFunc
	breaker        *InvocationBreaker
	guardsMu       sync.Mutex
	feedGuardsByID map[string]*FeedGuard
}

func NewRouter(registry *Registry, breakerThreshold int, clock Clock, sleep SleepFunc) (*Router, error) {
	if registry == nil {
		return nil, fmt.Errorf("%w: registry is nil", ErrInvalidRequest)
	}
	if clock == nil {
		clock = realClock{}
	}
	if sleep == nil {
		sleep = func(time.Duration) {}
	}
	return &Router{
		registry:       registry,
		clock:          clock,
		sleep:          sleep,
		breaker:        NewInvocationBreaker(breakerThreshold),
		feedGuardsByID: make(map[string]*FeedGuard),
	}, nil
}

func (r *Router) FetchKlines(ctx context.Context, req KlineRequest, candidateChain []string) ([]NormalizedKline, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if len(candidateChain) == 0 {
		return nil, fmt.Errorf("%w: candidate chain is empty", ErrInvalidRequest)
	}

	session := r.breaker.NewSession()
	var lastErr error
	attempts := 0

	for _, providerID := range candidateChain {
		if attempts >= 2 {
			break
		}
		providerID = strings.TrimSpace(providerID)
		if providerID == "" {
			lastErr = fmt.Errorf("%w: empty provider id in candidate chain", ErrInvalidRequest)
			continue
		}
		if session.ShouldSkip(providerID) {
			continue
		}

		fetcher, err := r.registry.KlineFetcher(providerID)
		if err != nil {
			return nil, err
		}

		guard, err := r.feedGuard(providerID, fetcher.KlineSpec().RateLimit)
		if err != nil {
			return nil, err
		}

		var rows []NormalizedKline
		err = guard.Do(ctx, func(ctx context.Context) error {
			var fetchErr error
			rows, fetchErr = fetcher.FetchKlines(ctx, req)
			return fetchErr
		})
		session.Observe(providerID, err)
		attempts++

		if err == nil {
			return rows, nil
		}
		if !CanFallback(ctx, err) {
			return nil, err
		}
		lastErr = err
	}

	if lastErr == nil {
		lastErr = ErrProviderNotFound
	}
	return nil, lastErr
}

func (r *Router) feedGuard(providerID string, policy RateLimitPolicy) (*FeedGuard, error) {
	r.guardsMu.Lock()
	defer r.guardsMu.Unlock()

	guard, ok := r.feedGuardsByID[providerID]
	if ok {
		return guard, nil
	}
	created, err := NewFeedGuard(policy, r.clock, r.sleep)
	if err != nil {
		return nil, err
	}
	r.feedGuardsByID[providerID] = created
	return created, nil
}
