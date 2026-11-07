// Package netguard 是出站连接的地址防护：解析域名后逐个检查 IP，通过才拨号，并且直接用检查过的 IP 拨号，
// 不再做第二次域名解析，防止 DNS 重绑定。出口代理和事件中心的通知地址共用同一套规则。
package netguard

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"time"
)

// Class 是一个 IP 地址的分类。
type Class int

const (
	// Blocked 一律拒绝：回环、链路本地（含云元数据 169.254.169.254）、组播、未指定、保留与文档地址，
	// 以及落在内网段里的云元数据地址（阿里云 100.100.100.200、AWS IPv6 fd00:ec2::254），即使允许内网也不放行。
	Blocked Class = iota
	// Private 是内网地址：10/8、172.16/12、192.168/16、100.64/10 以及 IPv6 的 ULA。默认拒绝，调用方明确允许才放行。
	Private
	// Public 是可以在公网路由的地址。
	Public
)

var (
	// ErrPortNotAllowed 表示目标端口不在允许的范围内。
	ErrPortNotAllowed = errors.New("目标端口不被允许")
	// ErrLookupFailed 表示域名解析失败。
	ErrLookupFailed = errors.New("域名解析失败")
	// ErrAddressBlocked 表示目标地址（或域名解析出的全部地址）都被防护规则拒绝，重试也不会好。
	ErrAddressBlocked = errors.New("目标地址不被允许")
	// ErrNoReachableAddress 表示有允许的地址，但都连不上。
	ErrNoReachableAddress = errors.New("没有可以连接的允许地址")
)

var (
	// 非 2000::/3 的 IPv6 地址（回环、链路本地、ULA 之外的全部）都不是公网地址。
	ipv6GlobalUnicast = netip.MustParsePrefix("2000::/3")
	ipv6NotPublic     = []netip.Prefix{
		netip.MustParsePrefix("2001::/23"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("2002::/16"),
		netip.MustParsePrefix("3fff::/20"),
	}
	ipv6ULA = netip.MustParsePrefix("fc00::/7")

	// 落在内网段里的云元数据地址：允许内网时也不能访问，否则能从通知地址或代理偷到实例凭据。
	metadataAddresses = []netip.Addr{netip.MustParseAddr("100.100.100.200"), netip.MustParseAddr("fd00:ec2::254")}
)

// Classify 返回 ip 的分类。IPv4 映射的 IPv6 地址按 IPv4 处理。
func Classify(ip net.IP) Class {
	if address, ok := netip.AddrFromSlice(ip); ok && slices.Contains(metadataAddresses, address.Unmap()) {
		return Blocked
	}
	if ip4 := ip.To4(); ip4 != nil {
		return classifyIPv4(ip4)
	}
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return Blocked
	}
	if ipv6ULA.Contains(address) {
		return Private
	}
	if !ipv6GlobalUnicast.Contains(address) || slices.ContainsFunc(ipv6NotPublic, func(prefix netip.Prefix) bool { return prefix.Contains(address) }) {
		return Blocked
	}
	return Public
}

func classifyIPv4(ip net.IP) Class {
	if ip.IsPrivate() || (ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127) {
		return Private
	}
	if !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || isReservedIPv4(ip) {
		return Blocked
	}
	return Public
}

// 保留段：0/8、224/4 及以上、IETF 协议分配（192.0.0/24）、三个文档网段、基准测试网段。
func isReservedIPv4(ip net.IP) bool {
	first, second, third := ip[0], ip[1], ip[2]
	return first == 0 || first >= 224 ||
		(first == 192 && second == 0 && (third == 0 || third == 2)) ||
		(first == 198 && (second == 18 || second == 19 || (second == 51 && third == 100))) ||
		(first == 203 && second == 0 && third == 113)
}

// IsPublicIP 报告 ip 是否是公网地址。
func IsPublicIP(ip net.IP) bool { return Classify(ip) == Public }

// IsPublicIPv4 报告 ip 是否是公网 IPv4 地址；IPv6 一律返回 false。
func IsPublicIPv4(ip net.IP) bool { return ip.To4() != nil && Classify(ip) == Public }

// Allowed 报告在 allowPrivate 的设置下，ip 是否可以拨号。
func Allowed(ip net.IP, allowPrivate bool) bool {
	switch Classify(ip) {
	case Public:
		return true
	case Private:
		return allowPrivate
	}
	return false
}

// DialOptions 配置 NewDialContext。
type DialOptions struct {
	// Lookup 解析域名，默认用系统解析器。
	Lookup func(ctx context.Context, host string) ([]string, error)
	// Dial 建立连接，默认用 net.Dialer。
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// AllowedPorts 为空表示不限端口。
	AllowedPorts []string
	// AllowPrivate 允许拨号到内网地址（回环、链路本地等仍然拒绝）。
	AllowPrivate bool
	// LookupTimeout 默认 5 秒。
	LookupTimeout time.Duration
	// MaxAttempts 是最多尝试拨号的地址个数，默认 16。
	MaxAttempts int
}

// NewDialContext 返回带地址防护的 DialContext：host 是 IP 字面量时直接检查，是域名时先解析，
// 然后只对通过检查的地址拨号，并直接用该 IP 拨号。
func NewDialContext(opts DialOptions) func(ctx context.Context, network, address string) (net.Conn, error) {
	if opts.Lookup == nil {
		opts.Lookup = net.DefaultResolver.LookupHost
	}
	if opts.Dial == nil {
		opts.Dial = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	if opts.LookupTimeout <= 0 {
		opts.LookupTimeout = 5 * time.Second
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 16
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || (len(opts.AllowedPorts) != 0 && !slices.Contains(opts.AllowedPorts, port)) {
			return nil, ErrPortNotAllowed
		}
		addresses := []string{host}
		if net.ParseIP(host) == nil {
			lookupCtx, cancel := context.WithTimeout(ctx, opts.LookupTimeout)
			addresses, err = opts.Lookup(lookupCtx, host)
			cancel()
			if err != nil {
				return nil, ErrLookupFailed
			}
		}
		attempted := 0
		for _, raw := range addresses {
			ip := net.ParseIP(raw)
			if ip == nil || !Allowed(ip, opts.AllowPrivate) {
				continue
			}
			attempted++
			conn, err := opts.Dial(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			if ctx.Err() != nil || attempted >= opts.MaxAttempts {
				break
			}
		}
		if attempted == 0 {
			return nil, ErrAddressBlocked
		}
		return nil, ErrNoReachableAddress
	}
}
