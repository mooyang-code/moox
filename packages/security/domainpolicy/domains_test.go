package domainpolicy

import "testing"

func TestSubdomainBoundaryAndDNSOnlyHosts(t *testing.T) {
	matcher, err := New([]string{"*.binance.com", "data-api.binance.vision"})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"api.binance.com", "a.b.binance.com", "API.BINANCE.COM.", "data-api.binance.vision"} {
		if !matcher.Allows(host) {
			t.Errorf("allowed hostname rejected: %q", host)
		}
	}
	for _, host := range []string{"binance.com", "evilbinance.com", "api.binance.com.evil", "api.binance.com:443", "https://api.binance.com", "api.binance.com@evil.test", "127.0.0.1", "[::1]", " api.binance.com", "api..binance.com", "*.binance.com", "api.binance.com/", "api.binance.com\n", "localhost"} {
		if matcher.Allows(host) {
			t.Errorf("forbidden hostname accepted: %q", host)
		}
	}
	for _, patterns := range [][]string{{"*"}, {"*binance.com"}, {"a.*.binance.com"}, {"https://binance.com"}, {"127.0.0.1"}, {"api.binance.com", "API.BINANCE.COM."}} {
		_, err := New(patterns)
		if err == nil {
			t.Errorf("invalid policy accepted: %v", patterns)
		}
	}
}
