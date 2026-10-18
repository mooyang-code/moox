package dnsresolver

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/security/domainpolicy"
)

type DomainResolver interface {
	ResolveDomains(context.Context, []string) (map[string]sources.DNSResolution, error)
}

type EgressClient struct {
	client  gatewayclient.Invoker
	timeout time.Duration
}

func NewEgressClient(gateway gatewayclient.Invoker, timeout time.Duration) *EgressClient {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &EgressClient{client: gateway, timeout: timeout}
}

func (c *EgressClient) ResolveDomains(ctx context.Context, domains []string) (map[string]sources.DNSResolution, error) {
	if c == nil || c.client == nil {
		return nil, fmt.Errorf("egress DNS client is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	expected := make(map[string]struct{}, len(domains))
	requested := make([]string, 0, len(domains))
	for _, raw := range domains {
		host, valid := domainpolicy.Host(strings.TrimSpace(raw))
		if !valid {
			return nil, fmt.Errorf("invalid DNS domain %q", raw)
		}
		if _, exists := expected[host]; exists {
			continue
		}
		expected[host] = struct{}{}
		requested = append(requested, host)
	}
	if len(requested) == 0 || len(requested) > 16 {
		return nil, fmt.Errorf("DNS requires 1 to 16 unique domains")
	}
	var rsp egresspb.ResolveDomainsRsp
	err := c.client.Invoke(callCtx, "trpc.moox.egress.Proxy", "ResolveDomains", &egresspb.ResolveDomainsReq{Domains: requested, MaxIpsPerDomain: 4}, &rsp)
	if err != nil {
		return nil, fmt.Errorf("resolve domains RPC: %w", err)
	}
	if rsp.GetRetInfo() == nil {
		return nil, fmt.Errorf("resolve domains RPC returned empty response")
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		return nil, fmt.Errorf("resolve domains RPC failed: code=%d msg=%s", rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	}
	result := make(map[string]sources.DNSResolution, len(rsp.GetResolutions()))
	for _, resolution := range rsp.GetResolutions() {
		if resolution == nil {
			continue
		}
		host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(resolution.GetDomain()), "."))
		if host == "" || !validDomain(host) {
			continue
		}
		if _, ok := expected[host]; !ok {
			continue
		}
		if _, duplicate := result[host]; duplicate {
			continue
		}
		item := sources.DNSResolution{}
		seen := map[string]bool{}
		for _, resolved := range resolution.GetIps() {
			if resolved == nil {
				continue
			}
			ip := net.ParseIP(strings.TrimSpace(resolved.GetIp()))
			if !isPublicIPv4(ip) {
				continue
			}
			value := ip.To4().String()
			if seen[value] || len(item.IPs) == 4 {
				continue
			}
			seen[value] = true
			item.IPs = append(item.IPs, value)
			if item.LatencyMS == nil {
				item.LatencyMS = make(map[string]uint32)
			}
			item.LatencyMS[value] = resolved.GetTcpConnectLatencyMs()
		}
		if len(item.IPs) > 0 {
			result[host] = item
		}
	}
	return result, nil
}

func isPublicIPv4(ip net.IP) bool {
	if ip == nil || ip.To4() == nil {
		return false
	}
	ip = ip.To4()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	first, second, third := ip[0], ip[1], ip[2]
	return first != 0 && first < 224 &&
		!(first == 100 && second >= 64 && second <= 127) &&
		!(first == 192 && second == 0 && (third == 0 || third == 2)) &&
		!(first == 198 && (second == 18 || second == 19 || (second == 51 && third == 100))) &&
		!(first == 203 && second == 0 && third == 113)
}

func normalizeDomain(raw string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
}

func validDomain(host string) bool {
	if host == "" || len(host) > 253 || strings.Contains(host, "://") || net.ParseIP(host) != nil {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return false
		}
	}
	return true
}

var _ DomainResolver = (*EgressClient)(nil)
