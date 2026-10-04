package routeprobe

import (
	"context"
	"testing"
)

func TestResolverBuildsStableCandidatesFromDNSAddresses(t *testing.T) {
	resolver := DNSResolver{LookupHost: func(context.Context, string) ([]string, error) {
		return []string{"192.0.2.20", "192.0.2.10", "192.0.2.20"}, nil
	}}
	got, err := resolver.Resolve(context.Background(), ResolveRequest{
		SCFRegion: "ap-shanghai", EgressScope: "scf-public", SourceKey: SourceKey{ProviderID: "binance", SourceID: "spot_http"},
		Transport: TransportHTTPS, Host: "api.example", Port: 443,
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got, want := len(got), 2; got != want {
		t.Fatalf("Resolve() returned %d candidates, want %d", got, want)
	}
	if got[0].Address != "192.0.2.20" || got[1].Address != "192.0.2.10" {
		t.Fatalf("Resolve() order = [%s %s], want DNS order", got[0].Address, got[1].Address)
	}
	if got[0].RouteKey().String() != "ap-shanghai/binance/spot_http/https/api.example:443" {
		t.Fatalf("unexpected route key %q", got[0].RouteKey().String())
	}
}

func TestResolverDoesNotActAsADNSServer(t *testing.T) {
	resolver := DNSResolver{LookupHost: func(context.Context, string) ([]string, error) {
		return []string{"192.0.2.10"}, nil
	}}
	if _, err := resolver.Resolve(context.Background(), ResolveRequest{
		SCFRegion: "ap-shanghai",
		SourceKey: SourceKey{ProviderID: "tdx", SourceID: "normal_7709"}, Transport: TransportTCP,
		Host: "quotes.example", Port: 7709,
	}); err != nil {
		t.Fatalf("Resolve() unexpectedly required a DNS server: %v", err)
	}
}
