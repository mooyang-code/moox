// Package api DNS代理服务
package api

import (
	"context"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/api/config"
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
	Message   string    `json:"message"`    // 消息说明
}

// DNSProxyConfig DNS代理配置
type DNSProxyConfig struct {
	Domains               []string `yaml:"domains"`                 // 需要定时解析的域名列表
	CacheTTLMinutes       int      `yaml:"cache_ttl_minutes"`       // 缓存过期时间（分钟）
	DNSServers            []string `yaml:"dns_servers"`             // DNS服务器列表
	DNSTimeoutSeconds     int      `yaml:"dns_timeout_seconds"`     // DNS查询超时时间（秒）
	LatencyTimeoutSeconds int      `yaml:"latency_timeout_seconds"` // 延迟检测超时时间（秒）
	LatencyPort           string   `yaml:"latency_port"`            // 延迟检测端口
}

// DNSProxy DNS代理服务处理器
type DNSProxy struct {
	// DNS服务器列表
	dnsServers []string
	// DNS查询超时时间
	dnsTimeout time.Duration
	// 延迟检测超时时间
	latencyTimeout time.Duration
	// 延迟检测端口
	latencyPort string
}

// 全局缓存实例
var (
	dnsCache  *cache.Cache
	dnsConfig *config.Config
	cacheOnce sync.Once
)

// loadDNSProxyConfig 从配置文件加载DNS代理配置
func loadDNSProxyConfig() (*config.Config, error) {
	// 从配置文件加载DNS代理配置
	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("加载DNS代理配置失败: %v", err)
	}

	log.Infof("DNS代理配置加载完成: 域名数量=%d, 缓存TTL=%v, DNS服务器数量=%d",
		len(cfg.DNSProxy.Domains), cfg.GetCacheTTL(), len(cfg.GetEnabledDNSServers()))
	return cfg, nil
}

// initDNSCache 初始化DNS缓存
func initDNSCache() {
	cacheOnce.Do(func() {
		// 加载配置
		var err error
		dnsConfig, err = loadDNSProxyConfig()
		if err != nil {
			log.Errorf("加载DNS代理配置失败: %v", err)
			// 程序无法继续运行，直接退出
			panic(fmt.Sprintf("DNS代理配置加载失败: %v", err))
		}

		// 创建缓存实例
		cacheTTL := dnsConfig.GetCacheTTL()
		cleanupInterval := dnsConfig.GetCleanupInterval()
		dnsCache = cache.New(cacheTTL, cleanupInterval)
		log.Info("DNS缓存初始化完成")
	})
}

var NewDNSProxy = func() SchemaHandler {
	// 初始化缓存
	initDNSCache()

	return &DNSProxy{
		dnsServers:     dnsConfig.GetEnabledDNSServers(),
		dnsTimeout:     dnsConfig.GetDNSTimeout(),
		latencyTimeout: dnsConfig.GetLatencyTimeout(),
		latencyPort:    dnsConfig.GetTestPorts()[0], // 使用第一个测试端口
	}
}

// RegisterDNSProxyHandler 注册DNS代理处理器到API入口
func RegisterDNSProxyHandler() {
	// 注册DNS代理服务处理器
	GetAPIHandleInstance().Register(NewDNSProxy())
}

// InterfaceID 实现SchemaHandler接口
func (DNSProxy) InterfaceID() string {
	return "dnsproxy"
}

// GetHandle DNS代理服务的GET请求处理
func (s DNSProxy) GetHandle(ctx context.Context, params map[string]string) (*APIRsp, error) {
	log.InfoContextf(ctx, "# DNS代理服务 - GET请求，用户输入参数: %+v", params)

	// 获取域名参数
	domain, ok := params["domain"]
	if !ok || domain == "" {
		return &APIRsp{
			Code: 400,
			Data: []any{DNSProxyResult{
				Success: false,
				Message: "缺少域名参数，请提供domain参数",
			}},
		}, nil
	}

	// 确保缓存已初始化
	initDNSCache()

	// 先从缓存中查找
	cacheKey := fmt.Sprintf("dns:%s", domain)
	if cached, found := dnsCache.Get(cacheKey); found {
		log.InfoContextf(ctx, "从缓存中获取域名 %s 的解析结果", domain)
		if result, ok := cached.(*DNSProxyResult); ok {
			return &APIRsp{
				Code: 200,
				Data: []any{result},
			}, nil
		}
	}

	// 缓存中没有，执行实时DNS解析
	log.InfoContextf(ctx, "# 缓存中未找到域名 %s，执行实时解析", domain)
	result, err := s.resolveDomain(ctx, domain)
	if err != nil {
		log.ErrorContextf(ctx, "DNS解析失败: %v", err)
		return &APIRsp{
			Code: 500,
			Data: []any{DNSProxyResult{
				Domain:  domain,
				Success: false,
				Message: fmt.Sprintf("DNS解析失败: %v", err),
			}},
		}, nil
	}

	// 将结果存入缓存
	if result.Success {
		cacheTTL := dnsConfig.GetCacheTTL()
		dnsCache.Set(cacheKey, result, cacheTTL)
		log.InfoContextf(ctx, "域名 %s 解析结果已存入缓存", domain)
	}
	return &APIRsp{
		Code: 200,
		Data: []any{result},
	}, nil
}

// PostHandle DNS代理服务的POST请求处理
func (s DNSProxy) PostHandle(ctx context.Context, params map[string]string) (*APIRsp, error) {
	log.InfoContextf(ctx, "DNS代理服务 - POST请求，用户输入参数: %+v", params)

	// POST请求使用相同的逻辑
	return s.GetHandle(ctx, params)
}

// resolveDomain 解析域名并测试延迟
func (s DNSProxy) resolveDomain(ctx context.Context, domain string) (*DNSProxyResult, error) {
	log.InfoContextf(ctx, "开始解析域名: %s", domain)
	result := &DNSProxyResult{
		Domain:    domain,
		ResolveAt: time.Now(),
		Success:   false,
	}

	// 解析域名获取IP列表
	allIPs, err := s.resolveIPs(ctx, domain)
	if err != nil {
		result.Message = fmt.Sprintf("域名解析失败: %v", err)
		return result, err
	}
	if len(allIPs) == 0 {
		result.Message = "未解析到任何IP地址"
		return result, fmt.Errorf("no IPs resolved for domain: %s", domain)
	}
	log.InfoContextf(ctx, "域名 %s 解析到 %d 个IP地址: %v", domain, len(allIPs), allIPs)

	// 测试每个IP的延迟
	ipInfos := s.testLatency(ctx, allIPs)

	// 按延迟排序（延迟小的在前）
	s.sortByLatency(ipInfos)
	result.IPList = ipInfos
	result.Success = true
	result.Message = fmt.Sprintf("成功解析到 %d 个IP地址", len(ipInfos))

	log.InfoContextf(ctx, "域名 %s 解析完成，共 %d 个IP", domain, len(ipInfos))
	return result, nil
}

// resolveIPs 使用多个DNS服务器解析域名获取IP列表
func (s DNSProxy) resolveIPs(ctx context.Context, domain string) ([]string, error) {
	var allIPs []string
	ipSet := make(map[string]bool) // 用于去重

	for _, dnsServer := range s.dnsServers {
		ips, err := s.resolveWithDNS(ctx, domain, dnsServer)
		if err != nil {
			log.WarnContextf(ctx, "使用DNS服务器 %s 解析 %s 失败: %v", dnsServer, domain, err)
			continue
		}

		// 去重并添加到结果列表
		for _, ip := range ips {
			if !ipSet[ip] {
				ipSet[ip] = true
				allIPs = append(allIPs, ip)
				log.DebugContextf(ctx, "通过DNS %s 解析到IP: %s", dnsServer, ip)
			}
		}
	}
	return allIPs, nil
}

// resolveWithDNS 使用指定的DNS服务器解析域名
func (s DNSProxy) resolveWithDNS(ctx context.Context, domain, dnsServer string) ([]string, error) {
	log.DebugContextf(ctx, "使用DNS服务器 %s 解析域名 %s", dnsServer, domain)
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{
				Timeout: s.dnsTimeout,
			}
			return d.DialContext(ctx, network, dnsServer)
		},
	}

	resolveCtx, cancel := context.WithTimeout(ctx, s.dnsTimeout)
	defer cancel()

	ips, err := resolver.LookupIPAddr(resolveCtx, domain)
	if err != nil {
		return nil, fmt.Errorf("DNS解析失败: %v", err)
	}

	var result []string
	for _, ip := range ips {
		// 只使用IPv4地址
		if ip.IP.To4() != nil {
			ipStr := ip.IP.String()
			result = append(result, ipStr)
		}
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("未找到IPv4地址")
	}
	log.DebugContextf(ctx, "DNS服务器 %s 解析 %s 成功: %v", dnsServer, domain, result)
	return result, nil
}

// testLatency 测试IP列表的网络延迟
func (s DNSProxy) testLatency(ctx context.Context, ips []string) []*IPInfo {
	if len(ips) == 0 {
		return nil
	}
	log.DebugContextf(ctx, "开始测试 %d 个IP的网络延迟", len(ips))

	// 使用带缓冲的channel控制并发数
	semaphore := make(chan struct{}, 5) // 最多5个并发连接
	var wg sync.WaitGroup
	ipInfos := make([]*IPInfo, len(ips))

	for i, ip := range ips {
		wg.Add(1)
		go func(index int, ipAddr string) {
			defer wg.Done()

			// 获取信号量
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			ipInfo := &IPInfo{
				IP:       ipAddr,
				LastPing: time.Now(),
			}

			latency, err := s.measureLatency(ctx, ipAddr)
			if err != nil {
				ipInfo.Available = false
				ipInfo.Latency = 0
				log.DebugContextf(ctx, "IP %s 延迟测试失败: %v", ipAddr, err)
			} else {
				ipInfo.Available = true
				ipInfo.Latency = latency.Microseconds() // 转换为微秒
				log.DebugContextf(ctx, "IP %s 延迟: %dμs", ipAddr, ipInfo.Latency)
			}

			ipInfos[index] = ipInfo
		}(i, ip)
	}

	wg.Wait()
	log.DebugContextf(ctx, "延迟测试完成")
	return ipInfos
}

// measureLatency 测量到指定IP的网络延迟
func (s DNSProxy) measureLatency(ctx context.Context, ip string) (time.Duration, error) {
	start := time.Now()

	// 创建专用的拨号器
	dialer := &net.Dialer{
		Timeout:   s.latencyTimeout,
		KeepAlive: -1, // 禁用keep-alive
	}

	// 使用TCP连接测试延迟
	conn, err := dialer.Dial("tcp", net.JoinHostPort(ip, s.latencyPort))
	if err != nil {
		return 0, fmt.Errorf("连接失败: %v", err)
	}

	// 立即关闭连接
	conn.Close()

	latency := time.Since(start)
	return latency, nil
}

// sortByLatency 按延迟对IP进行排序（延迟小的在前）
func (s DNSProxy) sortByLatency(ipInfos []*IPInfo) {
	if len(ipInfos) == 0 {
		return
	}

	// 按延迟排序，可用的IP优先，然后按延迟从小到大排序
	sort.Slice(ipInfos, func(i, j int) bool {
		ipA, ipB := ipInfos[i], ipInfos[j]

		// 可用的IP优先
		if ipA.Available != ipB.Available {
			return ipA.Available
		}

		// 如果都可用，按延迟排序（延迟小的在前）
		if ipA.Available && ipB.Available {
			return ipA.Latency < ipB.Latency
		}

		// 如果都不可用，保持原有顺序
		return false
	})
}

// DnsproxySchedule 定时器入口函数 - 定时解析配置的域名并缓存结果
func DnsproxySchedule(ctx context.Context, params string) error {
	log.Infof("DNS代理定时任务开始执行，参数: %s", params)

	// 确保缓存已初始化
	initDNSCache()

	// 创建DNS代理实例
	dnsProxy := &DNSProxy{
		dnsServers:     dnsConfig.GetEnabledDNSServers(),
		dnsTimeout:     dnsConfig.GetDNSTimeout(),
		latencyTimeout: dnsConfig.GetLatencyTimeout(),
		latencyPort:    dnsConfig.GetTestPorts()[0], // 使用第一个测试端口
	}

	// 解析配置中的所有域名
	successCount := 0
	failCount := 0
	for _, domain := range dnsConfig.DNSProxy.Domains {
		log.Infof("开始定时解析域名: %s", domain)
		result, err := dnsProxy.resolveDomain(ctx, domain)
		if err != nil {
			log.Errorf("定时解析域名 %s 失败: %v", domain, err)
			failCount++
			continue
		}

		// 存入缓存
		cacheKey := fmt.Sprintf("dns:%s", domain)
		if result.Success {
			cacheTTL := dnsConfig.GetCacheTTL()
			dnsCache.Set(cacheKey, result, cacheTTL)
			log.Infof("域名 %s 定时解析成功，已更新缓存，IP数量: %d", domain, len(result.IPList))
			successCount++
		} else {
			log.Warnf("域名 %s 定时解析失败: %s", domain, result.Message)
			failCount++
		}
	}

	log.Infof("DNS代理定时任务执行完成，成功: %d，失败: %d", successCount, failCount)
	return nil
}

// GetCacheStats 获取缓存统计信息
func GetCacheStats() map[string]interface{} {
	if dnsCache == nil {
		return map[string]interface{}{
			"error": "缓存未初始化",
		}
	}

	return map[string]interface{}{
		"item_count": dnsCache.ItemCount(),
		"config":     dnsConfig,
	}
}

// ClearCache 清空缓存
func ClearCache() {
	if dnsCache != nil {
		dnsCache.Flush()
		log.Info("DNS缓存已清空")
	}
}

// SetCacheConfig 设置缓存配置
func SetCacheConfig(domains []string, ttlMinutes int) {
	initDNSCache()
	dnsConfig.DNSProxy.Domains = domains
	dnsConfig.DNSProxy.Cache.TTLMinutes = ttlMinutes
	log.Infof("DNS缓存配置已更新，域名数量: %d，TTL: %d分钟", len(domains), ttlMinutes)
}
