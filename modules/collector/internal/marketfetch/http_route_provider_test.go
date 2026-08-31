package marketfetch

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/packages/routeprobe"
)

func TestHTTPRouteProviderUsesProtocolProbeBeforeReturningIPs(t *testing.T) {
	var probeCalls atomic.Int32
	provider := &HTTPRouteProvider{
		Region: "ap-shanghai", Routes: map[string]sources.DNSResolution{
			"api.binance.com": {IPs: []string{"192.0.2.30", "192.0.2.31"}},
		}, ProbeParallel: 2, ProbeAttempts: 1, MaxFallback: 1,
		Prober: routeprobe.ProbeFunc(func(_ context.Context, request routeprobe.ProbeRequest) (routeprobe.ProbeResult, error) {
			probeCalls.Add(1)
			latency := 20 * time.Millisecond
			if request.Candidate.Address == "192.0.2.31" {
				latency = 5 * time.Millisecond
			}
			return routeprobe.ProbeResult{Success: true, FirstResponseLatency: latency, Latency: latency}, nil
		}),
	}
	addresses, err := provider.SelectRouteIPs(context.Background(), routeprobe.SourceKey{ProviderID: "binance", SourceID: "spot_http"}, routeprobe.TransportHTTPS, "api.binance.com", 443)
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 2 || addresses[0] != "192.0.2.31" || addresses[1] != "192.0.2.30" {
		t.Fatalf("selected addresses = %v, want protocol-ranked routes", addresses)
	}
	second, err := provider.SelectRouteIPs(context.Background(), routeprobe.SourceKey{ProviderID: "binance", SourceID: "spot_http"}, routeprobe.TransportHTTPS, "api.binance.com", 443)
	if err != nil {
		t.Fatal(err)
	}
	if probeCalls.Load() != 2 || len(second) != len(addresses) {
		t.Fatalf("route selection should cache one result per endpoint, calls=%d second=%v", probeCalls.Load(), second)
	}
}

func TestHTTPRouteProviderLeavesMissingDNSSnapshotToNormalResolver(t *testing.T) {
	provider := &HTTPRouteProvider{Routes: map[string]sources.DNSResolution{}}
	addresses, err := provider.SelectRouteIPs(context.Background(), routeprobe.SourceKey{ProviderID: "binance", SourceID: "spot_http"}, routeprobe.TransportHTTPS, "api.binance.com", 443)
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 0 {
		t.Fatalf("addresses = %v, want empty route hint", addresses)
	}
}

func TestHTTPRouteProviderSharesConcurrentInitialProbe(t *testing.T) {
	var probeCalls atomic.Int32
	provider := &HTTPRouteProvider{
		Region: "ap-shanghai", Routes: map[string]sources.DNSResolution{
			"api.binance.com": {IPs: []string{"192.0.2.40"}},
		}, ProbeParallel: 1, ProbeAttempts: 1,
		Prober: routeprobe.ProbeFunc(func(_ context.Context, request routeprobe.ProbeRequest) (routeprobe.ProbeResult, error) {
			probeCalls.Add(1)
			time.Sleep(10 * time.Millisecond)
			return routeprobe.ProbeResult{Success: true, FirstResponseLatency: time.Millisecond, Latency: time.Millisecond}, nil
		}),
	}
	key := routeprobe.SourceKey{ProviderID: "binance", SourceID: "spot_http"}
	var waitGroup sync.WaitGroup
	for i := 0; i < 4; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if _, err := provider.SelectRouteIPs(context.Background(), key, routeprobe.TransportHTTPS, "api.binance.com", 443); err != nil {
				t.Errorf("select route: %v", err)
			}
		}()
	}
	waitGroup.Wait()
	if probeCalls.Load() != 1 {
		t.Fatalf("concurrent callers should share the first probe, calls=%d", probeCalls.Load())
	}
}
