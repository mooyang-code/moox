package dnsproxy

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-database/localcache"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// 全局变量
var (
	globalDNSInstance *DNSProxy // 全局DNS代理实例
)

// DNSProxy DNS代理服务
type DNSProxy struct {
	dnsServers    []string
	dnsTimeout    time.Duration
	pingTimeout   time.Duration
	pingPort      int
}

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

// InitDNSProxyInstance 初始化DNSProxy全局实例
func InitDNSProxyInstance() {
	globalDNSInstance = NewDNSProxy()
	// 这里没有context，使用基础的log.Info
	log.Info("[DNSProxy] DNSProxy实例初始化完成")
}

// NewDNSProxy 创建DNS代理实例
func NewDNSProxy() *DNSProxy {
	// 获取Ping端口
	testPorts := getPingPorts()
	port := 80 // 默认端口
	if len(testPorts) > 0 {
		if p, err := strconv.Atoi(testPorts[0]); err == nil {
			port = p
		}
	}

	return &DNSProxy{
		dnsServers:  getEnabledDNSServers(),
		dnsTimeout:  getDNSTimeout(),
		pingTimeout: getPingTimeout(),
		pingPort:    port,
	}
}

// HandleSchedule trpc定时器[入口函数] - 定时解析配置的域名并缓存结果
func HandleSchedule(ctx context.Context, params string) error {
	log.InfoContextf(ctx, "[DNSProxy] Starting DNS proxy schedule, params: %s", params)

	if globalDNSInstance == nil {
		err := fmt.Errorf("DNS proxy instance not initialized")
		log.ErrorContext(ctx, "[DNSProxy] "+err.Error())
		return err
	}

	// 获取配置的域名列表
	domains := getScheduledDomains()
	if len(domains) == 0 {
		log.InfoContextf(ctx, "[DNSProxy] No domains configured for scheduled resolution")
		return nil
	}

	// 使用 trpc.GoAndWait 并发处理
	maxConcurrent := getConcurrentLimit()
	if maxConcurrent <= 0 {
		maxConcurrent = 100 // 默认最大并发数
	}
	return resolveDomainsBatch(ctx, domains, maxConcurrent)
}

// resolveDomain 解析域名并返回IP列表（按延迟排序）
func (d *DNSProxy) resolveDomain(ctx context.Context, domain string) (*DNSProxyResult, error) {
	result := &DNSProxyResult{
		Domain:    domain,
		ResolveAt: time.Now(),
		Success:   false,
	}

	// 直接进行DNS解析（不检查缓存，缓存留给API接口读取）
	ips, err := d.resolveWithMultipleDNS(ctx, domain)
	if err != nil {
		log.ErrorContextf(ctx, "[DNSProxy] DNS resolution failed: %s, error: %v", domain, err)
		return result, err
	}

	if len(ips) == 0 {
		return result, fmt.Errorf("domain %s has no IP addresses resolved", domain)
	}

	// 测试延迟并排序
	ipInfoList := d.pingAndSort(ctx, ips)
	result.IPList = ipInfoList
	result.Success = true

	// 缓存结果
	cacheTTL := int64(getCacheTTL().Seconds())
	if cacheTTL <= 0 {
		cacheTTL = 36000 // 默认10小时(意图为 永不过期)
	}
	localcache.Set(domain, result, cacheTTL)
	log.InfoContextf(ctx, "[DNSProxy] Successfully resolved domain %s, got %d IP addresses", domain, len(ipInfoList))
	return result, nil
}

// resolveWithMultipleDNS 使用多个DNS服务器并发解析域名
func (d *DNSProxy) resolveWithMultipleDNS(ctx context.Context, domain string) ([]string, error) {
	ipSet := &sync.Map{}

	// 准备并发处理器
	var handlers []func() error
	for _, dnsServer := range d.dnsServers {
		dnsServerCopy := dnsServer // 避免闭包问题
		handlers = append(handlers, func() error {
			return d.resolveWithDNS(ctx, domain, dnsServerCopy, ipSet)
		})
	}

	// 并发执行所有DNS解析任务
	if err := trpc.GoAndWait(handlers...); err != nil {
		log.WarnContextf(ctx, "[DNSProxy] 并发DNS解析过程中出现错误: %v", err)
	}

	// 将sync.Map中的所有IP转换为slice
	return d.convertMapToSlice(ipSet), nil
}

// createResolver 根据DNS服务器地址创建解析器
func (d *DNSProxy) createResolver(dnsServer string) *net.Resolver {
	if dnsServer == "localhost" {
		// 使用系统默认DNS解析
		return &net.Resolver{
			PreferGo: true,
		}
	}
	// 使用指定的DNS服务器
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialer := net.Dialer{
				Timeout: d.dnsTimeout,
			}
			return dialer.DialContext(ctx, network, dnsServer+":53")
		},
	}
}

// resolveWithDNS 使用指定DNS服务器解析域名并存储结果
func (d *DNSProxy) resolveWithDNS(ctx context.Context, domain, dnsServer string, ipSet *sync.Map) error {
	resolver := d.createResolver(dnsServer)

	ips, err := resolver.LookupIPAddr(ctx, domain)
	if err != nil {
		log.WarnContextf(ctx, "[DNSProxy] DNS server %s failed to resolve domain %s: %v", dnsServer, domain, err)
		return nil // 继续处理其他服务器
	}

	// 存储IP到sync.Map（原子操作）
	for _, ip := range ips {
		ipStr := ip.IP.String()
		ipSet.LoadOrStore(ipStr, struct{}{})
	}
	return nil
}

// convertMapToSlice 将sync.Map转换为string slice
func (d *DNSProxy) convertMapToSlice(ipSet *sync.Map) []string {
	var allIPs []string
	ipSet.Range(func(key, value interface{}) bool {
		if ipStr, ok := key.(string); ok {
			allIPs = append(allIPs, ipStr)
		}
		return true
	})
	return allIPs
}

// pingAndSort 对IP进行ping测试并排序
func (d *DNSProxy) pingAndSort(ctx context.Context, ips []string) []*IPInfo {
	var ipInfoList []*IPInfo
	var mu sync.Mutex

	var handlers []func() error
	for _, ip := range ips {
		ipAddr := ip
		handlers = append(handlers, func() error {
			latency, available := d.pingIP(ctx, ipAddr)
			ipInfo := &IPInfo{
				IP:        ipAddr,
				Latency:   latency,
				Available: available,
				LastPing:  time.Now(),
			}

			mu.Lock()
			ipInfoList = append(ipInfoList, ipInfo)
			mu.Unlock()
			return nil
		})
	}
	_ = trpc.GoAndWait(handlers...)

	// 按延迟排序（可用的IP优先，然后按延迟排序）
	sort.Slice(ipInfoList, func(i, j int) bool {
		if ipInfoList[i].Available != ipInfoList[j].Available {
			return ipInfoList[i].Available
		}
		return ipInfoList[i].Latency < ipInfoList[j].Latency
	})
	return ipInfoList
}

// pingIP 对单个IP进行ping测试
func (d *DNSProxy) pingIP(ctx context.Context, ip string) (int64, bool) {
	start := time.Now()
	address := fmt.Sprintf("%s:%d", ip, d.pingPort)

	// 创建带超时的Context
	timeoutCtx, cancel := context.WithTimeout(ctx, d.pingTimeout)
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

// resolveDomainsBatch 使用 trpc.GoAndWait 并发解析域名，支持分批控制
func resolveDomainsBatch(ctx context.Context, domains []string, maxConcurrent int) error {
	if len(domains) == 0 {
		return nil
	}

	// 按照 maxConcurrent 分批处理
	for i := 0; i < len(domains); i += maxConcurrent {
		end := i + maxConcurrent
		if end > len(domains) {
			end = len(domains)
		}
		batch := domains[i:end]
		if err := resolveSingleBatch(ctx, batch); err != nil {
			log.ErrorContextf(ctx, "[DNSProxy] resolve batch failed: %v", err)
		}
	}
	return nil
}

// resolveSingleBatch 解析单个批次
func resolveSingleBatch(ctx context.Context, batch []string) error {
	var handlers []func() error

	for _, domain := range batch {
		d := domain // 避免闭包问题
		handlers = append(handlers, func() error {
			if _, err := globalDNSInstance.resolveDomain(ctx, d); err != nil {
				log.ErrorContextf(ctx, "[DNSProxy] resolve domain %s failed: %v", d, err)
				return nil
			}
			return nil
		})
	}
	return trpc.GoAndWait(handlers...)
}
