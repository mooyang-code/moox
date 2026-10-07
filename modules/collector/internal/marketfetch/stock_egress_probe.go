package marketfetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model"
)

// StockEgressIdentityProbe is an optional diagnostic. Provider reachability is
// exercised by the market canary; a blocked IP reflector must not block a
// release or turn into a false statement about function identity.
func StockEgressIdentityProbe(ctx context.Context) (*model.Response, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	return stockEgressIdentityProbeWithClient(ctx, client, []string{
		"https://api.ipify.org?format=text",
		"https://ifconfig.me/ip",
		"https://checkip.amazonaws.com/",
		"https://icanhazip.com/",
	}...)
}

func stockEgressIdentityProbeWithClient(ctx context.Context, client *http.Client, reflectors ...string) (*model.Response, error) {
	var failures []string
	for _, endpoint := range reflectors {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", endpoint, err))
			continue
		}
		request.Header.Set("User-Agent", "moox-collector/1.0")
		response, err := client.Do(request)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", endpoint, err))
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 128<<10))
		response.Body.Close()
		if readErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", endpoint, readErr))
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			failures = append(failures, fmt.Sprintf("%s: HTTP %d", endpoint, response.StatusCode))
			continue
		}
		if err := validatePublicIPAddress(body); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", endpoint, err))
			continue
		}
		return &model.Response{Success: true, Message: "stockcn egress identity probe ok", Data: map[string]interface{}{
			"provider": "multi", "market": "stockcn", "details": map[string]string{"public_ip": strings.TrimSpace(string(body))},
		}, Timestamp: time.Now().UTC()}, nil
	}
	return nil, fmt.Errorf("public_ip reflectors unavailable: %s", strings.Join(failures, "; "))
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"), netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("ff00::/8"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("100:0:0:1::/64"),
	netip.MustParsePrefix("2001:2::/48"), netip.MustParsePrefix("2001:10::/28"),
	netip.MustParsePrefix("2001:20::/28"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"), netip.MustParsePrefix("5f00::/16"),
}

func validatePublicIPAddress(body []byte) error {
	address, err := netip.ParseAddr(strings.TrimSpace(string(body)))
	if err != nil {
		return fmt.Errorf("not a valid IP address")
	}
	if address.Zone() != "" {
		return fmt.Errorf("zoned address is not public")
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() {
		return fmt.Errorf("address %s is not public", address)
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(address) {
			return fmt.Errorf("address %s is private, loopback, or reserved", address)
		}
	}
	return nil
}
