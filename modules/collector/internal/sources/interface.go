package sources

import (
	"net"
	"strings"
	"time"
)

// CollectParams 采集参数
type CollectParams struct {
	SpaceID   string // 业务空间
	DatasetID string // 本次任务的真实写入目标数据集
	InstType  string // 产品类型: SPOT, SWAP
	Symbol    string // 外部交易对: BTCUSDT，用于请求交易所
	SubjectID string // MooX 数据对象ID: BTC-USDT，用于写入 storage
	Interval  string // 周期（K线用）: 1m, 5m, 1h
	Live      bool   // 是否由实时调度触发；闭合 K 线仍直接写入 Storage
	// DNSRoutes is the control-plane DNS snapshot carried by a short-lived
	// SCF task. The source keeps the original hostname for TLS SNI and uses the
	// resolved IPs only for the TCP dial.
	DNSRoutes map[string]DNSResolution
}

// DNSResolution is intentionally small: the scheduler only needs the
// hostname-to-address mapping captured before dispatching an SCF invocation.
type DNSResolution struct {
	IPs        []string          `json:"ips,omitempty"`
	ResolvedAt time.Time         `json:"resolved_at,omitempty"`
	LatencyMS  map[string]uint32 `json:"latency_ms,omitempty"`
}

const maxDNSIPsPerLookup = 8

// NormalizeDNSHost returns the canonical key used by both the control-plane
// snapshot and source clients. DNS names are case-insensitive and a trailing
// root dot is presentation-only; keeping one representation prevents a valid
// SCF snapshot from being silently missed during lookup.
func NormalizeDNSHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

// DNSIPs returns a defensive copy of the addresses for host.
func (p *CollectParams) DNSIPs(host string) []string {
	if p == nil || p.DNSRoutes == nil {
		return nil
	}
	canonicalHost := NormalizeDNSHost(host)
	route, ok := p.DNSRoutes[canonicalHost]
	if !ok {
		// Requests may come from older callers that did not canonicalize JSON
		// object keys. Accept those keys as a compatibility fallback while all
		// newly generated snapshots use canonical keys.
		for rawHost, candidate := range p.DNSRoutes {
			if NormalizeDNSHost(rawHost) == canonicalHost {
				route, ok = candidate, true
				break
			}
		}
	}
	if !ok {
		return nil
	}
	result := make([]string, 0, len(route.IPs))
	seen := make(map[string]struct{}, len(route.IPs))
	for _, rawIP := range route.IPs {
		if len(result) >= maxDNSIPsPerLookup {
			break
		}
		ip := net.ParseIP(strings.TrimSpace(rawIP))
		if ip == nil {
			continue
		}
		value := ip.String()
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
