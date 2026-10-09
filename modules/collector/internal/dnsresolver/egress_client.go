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
	"trpc.group/trpc-go/trpc-go/client"
)

// DomainResolver 批量解析域名，返回每个域名可用的公网 IPv4 地址。
type DomainResolver interface {
	ResolveDomains(context.Context, []string) (map[string]sources.DNSResolution, error)
}

// EgressResolver 是出口代理 ResolveDomains 调用的最小接口，生产中是 egresspb.ProxyClientProxy。
type EgressResolver interface {
	ResolveDomains(ctx context.Context, req *egresspb.ResolveDomainsReq, opts ...client.Option) (*egresspb.ResolveDomainsRsp, error)
}

// EgressClient 向出口代理请求域名解析结果。出口代理在香港，与 SCF 的网络相近，它解析并探测过的地址
// 更适合写入 SCF 的 DNS 快照。
type EgressClient struct {
	proxy   EgressResolver
	timeout time.Duration
}

// NewEgressClient 创建经出口代理解析域名的客户端；timeout 不大于 0 时使用 3 秒。
func NewEgressClient(proxy EgressResolver, timeout time.Duration) *EgressClient {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &EgressClient{proxy: proxy, timeout: timeout}
}

// ResolveDomains 实现 DomainResolver。只保留请求过的域名和公网 IPv4 地址；没有解析结果的域名不出现在返回值里。
func (c *EgressClient) ResolveDomains(ctx context.Context, domains []string) (map[string]sources.DNSResolution, error) {
	if c == nil || c.proxy == nil {
		return nil, fmt.Errorf("出口代理的 DNS 客户端未初始化")
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
	rsp, err := c.proxy.ResolveDomains(callCtx, &egresspb.ResolveDomainsReq{Domains: requested, MaxIpsPerDomain: 4})
	if err != nil {
		return nil, fmt.Errorf("调用出口代理解析域名失败: %w", err)
	}
	if rsp.GetRetInfo() == nil {
		return nil, fmt.Errorf("出口代理解析域名返回了空响应")
	}
	if code := rsp.GetRetInfo().GetCode(); code != commonpb.ErrorCode_SUCCESS {
		return nil, fmt.Errorf("出口代理解析域名失败（%s）: %s", code, rsp.GetRetInfo().GetMsg())
	}
	result := make(map[string]sources.DNSResolution, len(rsp.GetResolutions()))
	for _, resolution := range rsp.GetResolutions() {
		host := normalizeDomain(resolution.GetDomain())
		if !egresspb.ValidDomain(host) {
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

func normalizeDomain(raw string) string {
	return egresspb.NormalizeHost(raw)
}

var _ DomainResolver = (*EgressClient)(nil)
