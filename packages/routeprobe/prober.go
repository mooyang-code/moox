package routeprobe

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"
)

var ErrNoProber = errors.New("routeprobe: no protocol prober configured")

// Prober validates one route using the protocol appropriate for the Source.
// Implementations may be HTTP, TDX, or any other protocol and are injected at
// the call site rather than registered in this provider-neutral package.
type Prober interface {
	Probe(context.Context, ProbeRequest) (ProbeResult, error)
}

type ProbeFunc func(context.Context, ProbeRequest) (ProbeResult, error)

func (function ProbeFunc) Probe(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	if function == nil {
		return ProbeResult{}, ErrNoProber
	}
	return function(ctx, request)
}

type ProbeOptions struct {
	Concurrency    int
	Attempts       int
	AttemptTimeout time.Duration
	Clock          func() time.Time
}

// ProbeCandidates executes protocol probes with bounded concurrency. Attempts
// are deliberately per candidate: the results can therefore feed a success
// rate and latency score without any global rate-limit or quota behavior.
func ProbeCandidates(ctx context.Context, candidates []Candidate, prober Prober, options ProbeOptions) ([]ProbeResult, error) {
	if prober == nil {
		return nil, ErrNoProber
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := NormalizeCandidates(candidates)
	if err != nil {
		return nil, err
	}
	attempts := options.Attempts
	if attempts < 1 {
		attempts = 1
	}
	concurrency := options.Concurrency
	if concurrency < 1 {
		concurrency = len(normalized)
		if concurrency < 1 {
			concurrency = 1
		}
	}
	if concurrency > len(normalized)*attempts && len(normalized) > 0 {
		concurrency = len(normalized) * attempts
	}
	if concurrency < 1 {
		return []ProbeResult{}, nil
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}

	type job struct {
		index     int
		candidate Candidate
		attempt   int
	}
	total := len(normalized) * attempts
	jobs := make(chan job)
	results := make([]ProbeResult, total)
	var wg sync.WaitGroup
	worker := func() {
		defer wg.Done()
		for item := range jobs {
			started := clock()
			request := ProbeRequest{Candidate: item.candidate, Attempt: item.attempt, Timeout: options.AttemptTimeout}
			probeContext := ctx
			cancel := func() {}
			if options.AttemptTimeout > 0 {
				probeContext, cancel = context.WithTimeout(ctx, options.AttemptTimeout)
			}
			result, probeErr := prober.Probe(probeContext, request)
			result.Candidate = item.candidate
			result.Attempt = item.attempt
			if result.ObservedAt.IsZero() {
				result.ObservedAt = started
			}
			if result.Latency <= 0 {
				result.Latency = clock().Sub(started)
			}
			if probeErr != nil {
				result.Success = false
				if result.ErrorKind == ErrorNone {
					result.ErrorKind = classifyProbeError(probeContext, probeErr)
				}
				if result.ErrorMessage == "" {
					result.ErrorMessage = probeErr.Error()
				}
			} else if !result.Success && result.ErrorKind == ErrorNone {
				result.ErrorKind = ErrorProtocol
			}
			if result.Success {
				result.ErrorKind = ErrorNone
				result.RemoteError = false
			}
			cancel()
			results[item.index] = result
		}
	}
	wg.Add(concurrency)
	for index := 0; index < concurrency; index++ {
		go worker()
	}

	index := 0
	for _, candidate := range normalized {
		for attempt := 1; attempt <= attempts; attempt++ {
			select {
			case jobs <- job{index: index, candidate: candidate, attempt: attempt}:
				index++
			case <-ctx.Done():
				close(jobs)
				wg.Wait()
				return results[:index], ctx.Err()
			}
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return results, err
	}
	return results, nil
}

func classifyProbeError(ctx context.Context, err error) ErrorKind {
	if errors.Is(err, context.DeadlineExceeded) || (ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return ErrorTimeout
	}
	if errors.Is(err, context.Canceled) || (ctx != nil && errors.Is(ctx.Err(), context.Canceled)) {
		return ErrorContext
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return ErrorTimeout
		}
		return ErrorConnection
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "timeout") || strings.Contains(message, "deadline") {
		return ErrorTimeout
	}
	if strings.Contains(message, "protocol") || strings.Contains(message, "response") {
		return ErrorProtocol
	}
	return ErrorConnection
}
