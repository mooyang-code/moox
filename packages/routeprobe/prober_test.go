package routeprobe

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestProbeCandidatesUsesInjectedProtocolProbeWithBoundedConcurrency(t *testing.T) {
	candidates := []Candidate{
		{SCFRegion: "ap-shanghai", SourceKey: SourceKey{ProviderID: "tdx", SourceID: "normal_7709"}, Transport: TransportTCP, Host: "quotes.example", Address: "192.0.2.10", Port: 7709},
		{SCFRegion: "ap-shanghai", SourceKey: SourceKey{ProviderID: "tdx", SourceID: "normal_7709"}, Transport: TransportTCP, Host: "quotes.example", Address: "192.0.2.11", Port: 7709},
		{SCFRegion: "ap-shanghai", SourceKey: SourceKey{ProviderID: "tdx", SourceID: "normal_7709"}, Transport: TransportTCP, Host: "quotes.example", Address: "192.0.2.12", Port: 7709},
	}
	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0
	prober := ProbeFunc(func(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			inFlight--
			mu.Unlock()
		}()
		select {
		case <-time.After(5 * time.Millisecond):
		case <-ctx.Done():
			return ProbeResult{}, ctx.Err()
		}
		return ProbeResult{Success: true, Latency: 10 * time.Millisecond}, nil
	})

	results, err := ProbeCandidates(context.Background(), candidates, prober, ProbeOptions{Concurrency: 2, Attempts: 2})
	if err != nil {
		t.Fatalf("ProbeCandidates() error = %v", err)
	}
	if len(results) != len(candidates)*2 {
		t.Fatalf("ProbeCandidates() returned %d results, want %d", len(results), len(candidates)*2)
	}
	if maxInFlight > 2 {
		t.Fatalf("max probe concurrency = %d, want <= 2", maxInFlight)
	}
	for _, result := range results {
		if !result.Success || result.Candidate.Address == "" || result.Attempt < 1 {
			t.Fatalf("invalid probe result: %+v", result)
		}
	}
}

func TestProbeCandidatesClassifiesInjectedErrors(t *testing.T) {
	candidate := Candidate{SCFRegion: "ap-shanghai", SourceKey: SourceKey{ProviderID: "tdx", SourceID: "normal_7709"}, Transport: Transport("tdx-wire"), Host: "quotes.example", Address: "192.0.2.10", Port: 7709}
	results, err := ProbeCandidates(context.Background(), []Candidate{candidate}, ProbeFunc(func(context.Context, ProbeRequest) (ProbeResult, error) {
		return ProbeResult{}, errors.New("dial failed")
	}), ProbeOptions{})
	if err != nil {
		t.Fatalf("ProbeCandidates() error = %v", err)
	}
	if len(results) != 1 || results[0].Success || results[0].ErrorKind != ErrorConnection {
		t.Fatalf("unexpected classified result: %+v", results)
	}
}

func TestProbeCandidatesAppliesPerAttemptDeadline(t *testing.T) {
	candidate := Candidate{SCFRegion: "ap-shanghai", SourceKey: SourceKey{ProviderID: "tdx", SourceID: "normal_7709"}, Transport: TransportTCP, Host: "quotes.example", Address: "192.0.2.10", Port: 7709}
	results, err := ProbeCandidates(context.Background(), []Candidate{candidate}, ProbeFunc(func(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
		<-ctx.Done()
		return ProbeResult{}, ctx.Err()
	}), ProbeOptions{AttemptTimeout: time.Millisecond})
	if err != nil {
		t.Fatalf("ProbeCandidates() error = %v", err)
	}
	if len(results) != 1 || results[0].ErrorKind != ErrorTimeout || results[0].Success {
		t.Fatalf("unexpected deadline result: %+v", results)
	}
}
