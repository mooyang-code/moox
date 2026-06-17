package dnsproxy

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/pkg/config"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// LocalResolveDomain 本地解析单个域名
func LocalResolveDomain(ctx context.Context, domain string) (*DNSRecord, error) {
	result := &DNSRecord{
		Domain:    domain,
		ResolveAt: time.Now(),
		Success:   false,
	}

	// 1. 获取 DNS 服务器列表和超时配置
	dnsServers, dnsTimeout := getLocalDNSConfig()
	if len(dnsServers) == 0 {
		return result, fmt.Errorf("no DNS servers configured")
	}

	// 2. 使用多个 DNS 服务器并发解析
	ips, err := resolveWithMultipleDNS(ctx, domain, dnsServers, dnsTimeout)
	if err != nil {
		log.ErrorContextf(ctx, "[DNSProxy] 本地DNS解析失败: domain=%s, error=%v", domain, err)
		return result, err
	}

	if len(ips) == 0 {
		return result, fmt.Errorf("domain %s has no IP addresses resolved", domain)
	}

	log.InfoContextf(ctx, "[DNSProxy] 本地DNS解析成功: domain=%s, 解析到 %d 个 IP", domain, len(ips))

	// 3. 对解析出的 IP 列表进行探测和排序（复用现有逻辑）
	ipInfoList := probeAndSort(ctx, domain, ips)

	// 4. 统计可用 IP 数量
	availableCount := 0
	for _, ipInfo := range ipInfoList {
		if ipInfo.Available {
			availableCount++
		}
	}

	log.InfoContextf(ctx, "[DNSProxy] 本地DNS解析 - domain=%s, 可用IP: %d/%d", domain, availableCount, len(ips))

	// 5. 构建结果
	result.IPList = ipInfoList
	result.Success = true
	return result, nil
}

// resolveWithMultipleDNS 使用多个 DNS 服务器并发解析域名
func resolveWithMultipleDNS(ctx context.Context, domain string, dnsServers []string, timeout time.Duration) ([]string, error) {
	ipSet := &sync.Map{}

	// 准备并发处理器
	var handlers []func() error
	for _, dnsServer := range dnsServers {
		dnsServerCopy := dnsServer // 避免闭包问题
		handlers = append(handlers, func() error {
			return resolveWithDNS(ctx, domain, dnsServerCopy, timeout, ipSet)
		})
	}

	// 并发执行所有 DNS 解析任务
	if err := trpc.GoAndWait(handlers...); err != nil {
		log.WarnContextf(ctx, "[DNSProxy] 并发DNS解析过程中出现错误: %v", err)
	}

	// 将 sync.Map 中的所有 IP 转换为 slice
	allIPs := convertMapToSlice(ipSet)
	if len(allIPs) == 0 {
		return nil, fmt.Errorf("all DNS servers failed to resolve domain: %s", domain)
	}

	return allIPs, nil
}

// resolveWithDNS 使用指定 DNS 服务器解析域名并存储结果
func resolveWithDNS(ctx context.Context, domain, dnsServer string, timeout time.Duration, ipSet *sync.Map) error {
	resolver := createResolver(dnsServer, timeout)

	ips, err := resolver.LookupIPAddr(ctx, domain)
	if err != nil {
		log.WarnContextf(ctx, "[DNSProxy] DNS服务器 %s 解析域名 %s 失败: %v", dnsServer, domain, err)
		return nil // 继续处理其他服务器
	}

	// 存储 IP 到 sync.Map（原子操作）
	for _, ip := range ips {
		ipStr := ip.IP.String()
		ipSet.LoadOrStore(ipStr, struct{}{})
	}

	log.DebugContextf(ctx, "[DNSProxy] DNS服务器 %s 解析域名 %s 成功，得到 %d 个 IP", dnsServer, domain, len(ips))
	return nil
}

// createResolver 根据 DNS 服务器地址创建解析器
func createResolver(dnsServer string, timeout time.Duration) *net.Resolver {
	if dnsServer == "localhost" || dnsServer == "" {
		// 使用系统默认 DNS 解析
		return &net.Resolver{
			PreferGo: true,
		}
	}
	// 使用指定的 DNS 服务器
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialer := net.Dialer{
				Timeout: timeout,
			}
			return dialer.DialContext(ctx, network, dnsServer+":53")
		},
	}
}

// convertMapToSlice 将 sync.Map 转换为 string slice
func convertMapToSlice(ipSet *sync.Map) []string {
	var allIPs []string
	ipSet.Range(func(key, value interface{}) bool {
		if ipStr, ok := key.(string); ok {
			allIPs = append(allIPs, ipStr)
		}
		return true
	})
	return allIPs
}

// getLocalDNSConfig 获取本地 DNS 解析配置
func getLocalDNSConfig() (dnsServers []string, dnsTimeout time.Duration) {
	// 确保本地配置已初始化
	if config.LocalAppConfig == nil {
		config.InitLocalAppConfig()
	}

	// 如果没有 DNSProxy 配置，返回默认值
	if config.LocalAppConfig.DNSProxy == nil {
		return []string{"localhost"}, 5 * time.Second
	}

	// 获取 DNS 服务器列表
	dnsServers = config.LocalAppConfig.DNSProxy.DNSServers
	if len(dnsServers) == 0 {
		dnsServers = []string{"localhost"} // 默认使用系统 DNS
	}

	// 获取超时时间
	timeoutSec := config.LocalAppConfig.DNSProxy.DNSTimeout
	if timeoutSec <= 0 {
		timeoutSec = 5 // 默认 5 秒
	}
	dnsTimeout = time.Duration(timeoutSec) * time.Second

	return dnsServers, dnsTimeout
}
