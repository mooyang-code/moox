package binance

import "testing"

func TestClientEndpointDomainsDefaultAndConfigured(t *testing.T) {
	t.Parallel()

	client := NewClient()
	if client.SpotDomain() != SpotDomain {
		t.Fatalf("default spot domain = %q, want %q", client.SpotDomain(), SpotDomain)
	}
	if client.SwapDomain() != SwapDomain {
		t.Fatalf("default swap domain = %q, want %q", client.SwapDomain(), SwapDomain)
	}

	if err := client.SetSpotBaseURL("https://data-api.binance.vision"); err != nil {
		t.Fatalf("SetSpotBaseURL returned error: %v", err)
	}
	if got := client.SpotDomain(); got != "data-api.binance.vision" {
		t.Fatalf("spot domain = %q, want data-api.binance.vision", got)
	}

	if err := client.SetSwapBaseURL("fapi.binance.com"); err != nil {
		t.Fatalf("SetSwapBaseURL returned error: %v", err)
	}
	if got := client.SwapDomain(); got != "fapi.binance.com" {
		t.Fatalf("swap domain = %q, want fapi.binance.com", got)
	}
}

func TestClientEndpointRejectsMissingHost(t *testing.T) {
	t.Parallel()

	client := NewClient()
	if err := client.SetSpotBaseURL("https:///api"); err == nil {
		t.Fatalf("expected missing host error")
	}
}
