package dnsresolver

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
)

type DomainResolver interface {
	ResolveDomains(context.Context, []string) (map[string]sources.DNSResolution, error)
}

type TradeClient struct {
	client  tradepb.TradeDNSResolverServiceClientProxy
	timeout time.Duration
}

func NewTradeClient(target, nodeID string, credentials gatewayauth.Credentials, timeout time.Duration) *TradeClient {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	credentials.KeyID = strings.TrimSpace(credentials.KeyID)
	credentials.Caller = strings.TrimSpace(credentials.Caller)
	credentials.Secret = strings.TrimSpace(credentials.Secret)
	options := gatewayauth.NewTRPCClientOptions(normalizeTarget(target), strings.TrimSpace(nodeID), credentials)
	return &TradeClient{
		client:  tradepb.NewTradeDNSResolverServiceClientProxy(options...),
		timeout: timeout,
	}
}

func (c *TradeClient) ResolveDomains(ctx context.Context, domains []string) (map[string]sources.DNSResolution, error) {
	if c == nil || c.client == nil {
		return nil, fmt.Errorf("trade DNS client is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	expected := make(map[string]struct{}, len(domains))
	requested := make([]string, 0, len(domains))
	for _, raw := range domains {
		host := normalizeDomain(raw)
		if host == "" {
			continue
		}
		if _, exists := expected[host]; exists {
			continue
		}
		expected[host] = struct{}{}
		requested = append(requested, host)
	}
	rsp, err := c.client.ResolveDomains(callCtx, &tradepb.ResolveDomainsReq{Domains: requested, MaxIpsPerDomain: 4})
	if err != nil {
		return nil, fmt.Errorf("resolve domains RPC: %w", err)
	}
	if rsp == nil || rsp.GetRetInfo() == nil {
		return nil, fmt.Errorf("resolve domains RPC returned empty response")
	}
	if rsp.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS {
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
		for _, resolved := range resolution.GetIps() {
			if resolved == nil {
				continue
			}
			ip := net.ParseIP(strings.TrimSpace(resolved.GetIp()))
			if !isPublicIPv4(ip) {
				continue
			}
			value := ip.To4().String()
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

func normalizeTarget(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return "ip://127.0.0.1:11003"
	}
	if strings.Contains(target, "://") {
		return target
	}
	return "ip://" + target
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

var _ DomainResolver = (*TradeClient)(nil)
