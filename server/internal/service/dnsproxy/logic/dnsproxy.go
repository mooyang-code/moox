// Package logic DNS代理服务
package logic

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/dnsproxy/config"
	"github.com/patrickmn/go-cache"
	"trpc.group/trpc-go/trpc-go/log"
)

// IPInfo 存储IP地址及其延迟信息
type IPInfo struct {
	IP        string    `json:"ip"`        // IP地址
	Latency   int64     `json:"latency"`   // 网络延迟（微秒）
	Available bool      `json:"available"` // 是否可用
	LastPing  time.Time `json:"last_ping"` // 最后一次ping时间
}

// DNSProxyResult DNS代理解析结果
type DNSProxyResult struct {
	Domain    string    `json:"domain"`     // 解析的域名
	IPList    []*IPInfo `json:"ip_list"`    // IP列表（按延迟排序）
	ResolveAt time.Time `json:"resolve_at"` // 解析时间
	Success   bool      `json:"success"`    // 是否解析成功
}

// DNSProxy DNS代理服务
type DNSProxy struct {
	dnsServers     []string
	dnsTimeout     time.Duration
	latencyTimeout time.Duration
	latencyPort    int
}

// 全局变量
var (
	dnsCache   *cache.Cache
	dnsConfig  *config.Config
	initOnce   sync.Once
	configOnce sync.Once
)

// 初始化配置
func initConfig() {
	configOnce.Do(func() {
		cfg, err := config.LoadConfig()
		if err != nil {
			log.Errorf("加载DNS配置失败: %v", err)
			// 使用默认配置
			dnsConfig = config.GetDefaultConfig()
		} else {
			dnsConfig = cfg
		}
		log.Info("DNS配置初始化完成")
	})
}

// 初始化DNS缓存
func initDNSCache() {
	initOnce.Do(func() {
		initConfig()
		cacheTTL := dnsConfig.GetCacheTTL()
		cleanupInterval := dnsConfig.GetCleanupInterval()
		dnsCache = cache.New(cacheTTL, cleanupInterval)
		log.Info("DNS缓存初始化完成")
	})
}

// NewDNSProxy 创建DNS代理实例
func NewDNSProxy() *DNSProxy {
	// 初始化缓存
	initDNSCache()

	// 获取测试端口
	testPorts := dnsConfig.GetTestPorts()
	port := 80 // 默认端口
	if len(testPorts) > 0 {
		if p, err := strconv.Atoi(testPorts[0]); err == nil {
			port = p
		}
	}

	return &DNSProxy{
		dnsServers:     dnsConfig.GetEnabledDNSServers(),
		dnsTimeout:     dnsConfig.GetDNSTimeout(),
		latencyTimeout: dnsConfig.GetLatencyTimeout(),
		latencyPort:    port,
	}
}

// resolveDomain 解析域名并返回IP列表（按延迟排序）
func (d *DNSProxy) resolveDomain(ctx context.Context, domain string) (*DNSProxyResult, error) {
	result := &DNSProxyResult{
		Domain:    domain,
		ResolveAt: time.Now(),
		Success:   false,
	}

	// 检查缓存
	if cached, found := dnsCache.Get(domain); found {
		if cachedResult, ok := cached.(*DNSProxyResult); ok {
			log.InfoContextf(ctx, "从缓存获取域名解析结果: %s", domain)
			return cachedResult, nil
		}
	}

	// DNS解析
	ips, err := d.resolveWithMultipleDNS(ctx, domain)
	if err != nil {
		log.ErrorContextf(ctx, "DNS解析失败: %s, error: %v", domain, err)
		return result, err
	}

	if len(ips) == 0 {
		return result, fmt.Errorf("域名 %s 没有解析到任何IP地址", domain)
	}

	// 测试延迟并排序
	ipInfoList := d.testLatencyAndSort(ctx, ips)
	result.IPList = ipInfoList
	result.Success = true

	// 缓存结果
	dnsCache.Set(domain, result, cache.DefaultExpiration)

	log.InfoContextf(ctx, "成功解析域名 %s，获得 %d 个IP地址", domain, len(ipInfoList))
	return result, nil
}

// resolveWithMultipleDNS 使用多个DNS服务器解析域名
func (d *DNSProxy) resolveWithMultipleDNS(ctx context.Context, domain string) ([]string, error) {
	ipSet := make(map[string]bool)
	var allIPs []string

	for _, dnsServer := range d.dnsServers {
		resolver := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				dialer := net.Dialer{
					Timeout: d.dnsTimeout,
				}
				return dialer.DialContext(ctx, network, dnsServer+":53")
			},
		}

		ips, err := resolver.LookupIPAddr(ctx, domain)
		if err != nil {
			log.WarnContextf(ctx, "DNS服务器 %s 解析域名 %s 失败: %v", dnsServer, domain, err)
			continue
		}

		for _, ip := range ips {
			ipStr := ip.IP.String()
			if !ipSet[ipStr] {
				ipSet[ipStr] = true
				allIPs = append(allIPs, ipStr)
			}
		}
	}

	return allIPs, nil
}

// testLatencyAndSort 测试IP延迟并排序
func (d *DNSProxy) testLatencyAndSort(ctx context.Context, ips []string) []*IPInfo {
	var ipInfoList []*IPInfo
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, ip := range ips {
		wg.Add(1)
		go func(ipAddr string) {
			defer wg.Done()

			latency, available := d.testLatency(ctx, ipAddr)
			ipInfo := &IPInfo{
				IP:        ipAddr,
				Latency:   latency,
				Available: available,
				LastPing:  time.Now(),
			}

			mu.Lock()
			ipInfoList = append(ipInfoList, ipInfo)
			mu.Unlock()
		}(ip)
	}

	wg.Wait()

	// 按延迟排序（可用的IP优先，然后按延迟排序）
	sort.Slice(ipInfoList, func(i, j int) bool {
		if ipInfoList[i].Available != ipInfoList[j].Available {
			return ipInfoList[i].Available // 可用的IP排在前面
		}
		return ipInfoList[i].Latency < ipInfoList[j].Latency
	})

	return ipInfoList
}

// testLatency 测试IP地址的网络延迟
func (d *DNSProxy) testLatency(ctx context.Context, ip string) (int64, bool) {
	start := time.Now()
	address := fmt.Sprintf("%s:%d", ip, d.latencyPort)

	// 创建带超时的Context
	timeoutCtx, cancel := context.WithTimeout(ctx, d.latencyTimeout)
	defer cancel()

	// 尝试建立TCP连接
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(timeoutCtx, "tcp", address)
	if err != nil {
		return 0, false
	}
	defer conn.Close()

	latency := time.Since(start).Microseconds()
	return latency, true
}

// DnsproxySchedule 定时器入口函数 - 定时解析配置的域名并缓存结果
func DnsproxySchedule(ctx context.Context, params string) error {
	log.InfoContextf(ctx, "开始执行DNS代理定时任务，参数: %s", params)

	// 初始化DNS代理
	dnsProxy := NewDNSProxy()

	// 获取配置的域名列表
	domains := dnsConfig.GetScheduledDomains()
	if len(domains) == 0 {
		log.InfoContext(ctx, "没有配置需要定时解析的域名")
		return nil
	}

	// 并发解析所有配置的域名
	var wg sync.WaitGroup
	for _, domain := range domains {
		wg.Add(1)
		go func(domainName string) {
			defer wg.Done()

			result, err := dnsProxy.resolveDomain(ctx, domainName)
			if err != nil {
				log.ErrorContextf(ctx, "定时解析域名 %s 失败: %v", domainName, err)
				return
			}

			log.InfoContextf(ctx, "定时解析域名 %s 成功，获得 %d 个IP地址", domainName, len(result.IPList))
		}(domain)
	}

	wg.Wait()
	log.InfoContext(ctx, "DNS代理定时任务执行完成")
	return nil
}

// GetCachedResult 获取缓存的解析结果
func GetCachedResult(domain string) (*DNSProxyResult, bool) {
	if dnsCache == nil {
		return nil, false
	}

	if cached, found := dnsCache.Get(domain); found {
		if result, ok := cached.(*DNSProxyResult); ok {
			return result, true
		}
	}
	return nil, false
}

// ClearCache 清除指定域名的缓存
func ClearCache(domain string) {
	if dnsCache != nil {
		dnsCache.Delete(domain)
	}
}

// ClearAllCache 清除所有缓存
func ClearAllCache() {
	if dnsCache != nil {
		dnsCache.Flush()
	}
}