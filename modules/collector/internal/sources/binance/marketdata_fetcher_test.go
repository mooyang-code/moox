package binance

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/packages/routeprobe"
)

func TestMarketDataFetcherSeparatesSpotAndSwapSourceIDs(t *testing.T) {
	spot := NewMarketDataFetcher(InstTypeSPOT)
	swap := NewMarketDataFetcher(InstTypeSWAP)
	if spot.Descriptor().SourceID != "spot_http" || swap.Descriptor().SourceID != "swap_http" {
		t.Fatalf("unexpected SourceIDs: spot=%s swap=%s", spot.Descriptor().SourceID, swap.Descriptor().SourceID)
	}
	if spot.KlineSpec().InstrumentType != "spot" || swap.KlineSpec().InstrumentType != "swap" {
		t.Fatalf("unexpected instrument specs: spot=%+v swap=%+v", spot.KlineSpec(), swap.KlineSpec())
	}
}

type testRouteProvider struct{}

func (testRouteProvider) SelectRouteIPs(_ context.Context, key routeprobe.SourceKey, transport routeprobe.Transport, host string, port int) ([]string, error) {
	if key.ProviderID != "binance" || transport != routeprobe.TransportHTTPS || host == "" || port != 443 {
		return nil, context.Canceled
	}
	return []string{"192.0.2.20"}, nil
}

func TestMarketDataFetcherUsesRouteProviderAtTheHTTPBoundary(t *testing.T) {
	fetcher := NewMarketDataFetcher(InstTypeSPOT)
	fetcher.Routes = testRouteProvider{}
	ips, err := fetcher.routeIPs(context.Background(), "spot_http")
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 1 || ips[0] != "192.0.2.20" {
		t.Fatalf("route IPs = %v", ips)
	}
}
