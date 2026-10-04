package tdx

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/packages/routeprobe"
)

func TestRouteProberSeparatesTDXSourceVariants(t *testing.T) {
	for source, want := range map[string]ProtocolVariant{"normal_7709": ProtocolNormal, "ex_classic_7727": ProtocolExClassic, "ex_mac_7727": ProtocolExMAC} {
		got, err := variantForSource(source)
		if err != nil || got != want {
			t.Fatalf("variantForSource(%q) = %q, %v", source, got, err)
		}
	}
	prober := RouteProber{}
	_, err := prober.Probe(context.Background(), routeprobe.ProbeRequest{Candidate: routeprobe.Candidate{SourceKey: routeprobe.SourceKey{ProviderID: "tdx", SourceID: "normal_7709"}, Transport: routeprobe.TransportHTTPS}})
	if err == nil {
		t.Fatal("non-TCP route should be rejected before dialing")
	}
}
