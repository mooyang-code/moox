package dnsproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"trpc.group/trpc-go/trpc-database/localcache"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// 缓存key前缀和TTL配置
const (
	nodeDNSCacheKeyPrefix      = "dnsproxy:node_dns:"       // 终端DNS缓存key前缀
	nodeDNSCacheTTL      int64 = 365 * 24 * 3600            // 365天TTL（永不过期）
	mergedResultCacheKeyPrefix = "dnsproxy:merged_result:"  // 合并探测结果缓存key前缀
	mergedResultCacheTTL int64 = 300                        // 5分钟TTL
)

// GetActiveNodeIDsFunc 获取活跃节点ID列表的函数（由外部注入，避免循环依赖）
var GetActiveNodeIDsFunc func(ctx context.Context) ([]string, error)

// NodeDNSRecord 终端上报的DNS记录
type NodeDNSRecord struct {
	Domain    string    `json:"domain"`
	IPList    []string  `json:"ip_list"`
	ResolveAt time.Time `json:"resolve_at"`
}

// MergedDNSResult 合并探测后的DNS结果（用于缓存和API返回）
type MergedDNSResult struct {
	Domain  string    `json:"domain"`
	IPList  []*IPInfo `json:"ip_list"`
	ProbeAt time.Time `json:"probe_at"`
	Success bool      `json:"success"`
}

// UpdateNodeDNSRecords 更新节点DNS到缓存（365天TTL，永不过期）
// 该函数由心跳处理逻辑调用
func UpdateNodeDNSRecords(ctx context.Context, nodeID string, records []*NodeDNSRecord) error {
	if nodeID == "" {
		return fmt.Errorf("nodeID cannot be empty")
	}

	cacheKey := nodeDNSCacheKeyPrefix + nodeID

	// 序列化为JSON
	data, err := json.Marshal(records)
	if err != nil {
		return fmt.Errorf("failed to marshal node DNS records: %w", err)
	}

	// 存入localcache（365天TTL）
	localcache.Set(cacheKey, data, nodeDNSCacheTTL)

	log.DebugContextf(ctx, "[DNSProxy] Node DNS records updated: nodeID=%s, domains=%d, ttl=%ds",
		nodeID, len(records), nodeDNSCacheTTL)

	return nil
}

// GetNodeDNSRecords 获取指定节点的DNS记录
func GetNodeDNSRecords(ctx context.Context, nodeID string) ([]*NodeDNSRecord, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("nodeID cannot be empty")
	}

	cacheKey := nodeDNSCacheKeyPrefix + nodeID

	// 从localcache读取
	cached, ok := localcache.Get(cacheKey)
	if !ok {
		log.DebugContextf(ctx, "[DNSProxy] Node DNS cache miss: nodeID=%s", nodeID)
		return []*NodeDNSRecord{}, nil
	}

	// 反序列化
	data, ok := cached.([]byte)
	if !ok {
		return nil, fmt.Errorf("invalid cache data type for nodeID: %s", nodeID)
	}

	var records []*NodeDNSRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("failed to unmarshal node DNS records: %w", err)
	}

	return records, nil
}

// GetAllNodesDNSForDomain 获取所有活跃节点对指定域名的DNS记录
// 返回: map[nodeID][]string (IP列表)
func GetAllNodesDNSForDomain(ctx context.Context, domain string) (map[string][]string, error) {
	// 1. 获取所有活跃节点ID（通过导出的函数）
	nodeIDs, err := GetActiveNodeIDsFunc(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get active node IDs: %w", err)
	}

	if len(nodeIDs) == 0 {
		log.DebugContextf(ctx, "[DNSProxy] No active nodes found")
		return make(map[string][]string), nil
	}

	log.DebugContextf(ctx, "[DNSProxy] Found %d active nodes", len(nodeIDs))

	// 2. 并发获取所有节点的DNS记录
	nodeIPMap := make(map[string][]string)

	// 准备并发任务
	var handlers []func() error
	for _, nodeID := range nodeIDs {
		nodeIDCopy := nodeID // 避免闭包问题
		handlers = append(handlers, func() error {
			records, err := GetNodeDNSRecords(ctx, nodeIDCopy)
			if err != nil {
				log.WarnContextf(ctx, "[DNSProxy] Failed to get node DNS: nodeID=%s, error=%v", nodeIDCopy, err)
				return nil // 继续处理其他节点
			}

			// 查找该域名的记录
			for _, record := range records {
				if record.Domain == domain && len(record.IPList) > 0 {
					nodeIPMap[nodeIDCopy] = record.IPList
					break
				}
			}
			return nil
		})
	}

	// 并发执行
	if err := trpc.GoAndWait(handlers...); err != nil {
		log.WarnContextf(ctx, "[DNSProxy] Some nodes failed during concurrent fetch: %v", err)
	}

	log.DebugContextf(ctx, "[DNSProxy] Retrieved DNS from %d/%d nodes for domain: %s",
		len(nodeIPMap), len(nodeIDs), domain)

	return nodeIPMap, nil
}

// MergeAndDNSProbeAllDomains 定时器触发入口，并发处理所有域名
// 对每个域名执行: 合并本地+终端IP → 去重 → 探测 → 缓存(5分钟)
func MergeAndDNSProbeAllDomains(ctx context.Context) error {
	cfg := GetConfig()
	domains := cfg.DNSProxy.Domains

	if len(domains) == 0 {
		log.DebugContext(ctx, "[DNSProxy] No domains configured for DNS probe")
		return nil
	}

	log.InfoContextf(ctx, "[DNSProxy] Starting DNS probe for %d domains", len(domains))

	// 准备并发任务
	var handlers []func() error
	for _, domain := range domains {
		domainCopy := domain // 避免闭包问题
		handlers = append(handlers, func() error {
			return mergeAndDNSProbeDomain(ctx, domainCopy)
		})
	}

	// 并发执行所有域名的探测
	if err := trpc.GoAndWait(handlers...); err != nil {
		log.ErrorContextf(ctx, "[DNSProxy] Some domains failed during DNS probe: %v", err)
		// 不返回错误，避免导致服务启动失败，只记录错误日志
	}

	log.InfoContextf(ctx, "[DNSProxy] DNS probe completed for all domains")
	return nil
}

// mergeAndDNSProbeDomain 处理单个域名：合并本地+终端IP，去重，探测，缓存
func mergeAndDNSProbeDomain(ctx context.Context, domain string) error {
	startTime := time.Now()
	log.InfoContextf(ctx, "[DNSProxy] Starting DNS probe for domain: %s", domain)

	cfg := GetConfig()

	// 1. 根据配置决定是否获取本地DNS解析结果
	var localIPList []*IPInfo
	if cfg != nil && cfg.DNSProxy.EnableLocalDNSResolve {
		localIPList = GetLocalDNSResult(domain)
		log.DebugContextf(ctx, "[DNSProxy] Local DNS enabled, result for %s: %d IPs", domain, len(localIPList))
	} else {
		localIPList = []*IPInfo{}
		log.DebugContextf(ctx, "[DNSProxy] Local DNS disabled, skipping local DNS resolve for %s", domain)
	}

	// 2. 获取所有终端上报的DNS记录
	nodeIPMap, err := GetAllNodesDNSForDomain(ctx, domain)
	if err != nil {
		log.ErrorContextf(ctx, "[DNSProxy] Failed to get node DNS for domain %s: %v", domain, err)
		return err
	}
	log.DebugContextf(ctx, "[DNSProxy] Terminal DNS result for %s: %d nodes", domain, len(nodeIPMap))

	// 3. 合并IP列表并去重
	ipSet := make(map[string]struct{})

	// 添加本地DNS的IP
	for _, ipInfo := range localIPList {
		ipSet[ipInfo.IP] = struct{}{}
	}

	// 添加所有终端DNS的IP
	for nodeID, ips := range nodeIPMap {
		for _, ip := range ips {
			if ip != "" {
				ipSet[ip] = struct{}{}
			}
		}
		log.DebugContextf(ctx, "[DNSProxy] Node %s contributed %d IPs for domain %s", nodeID, len(ips), domain)
	}

	// 4. 转换为slice
	allIPs := make([]string, 0, len(ipSet))
	for ip := range ipSet {
		allIPs = append(allIPs, ip)
	}

	if len(allIPs) == 0 {
		enabledText := "disabled"
		if cfg != nil && cfg.DNSProxy.EnableLocalDNSResolve {
			enabledText = "enabled"
		}
		log.WarnContextf(ctx, "[DNSProxy] No IPs found for domain %s (local DNS: %s, terminal nodes: %d), skip DNS probe",
			domain, enabledText, len(nodeIPMap))
		// 不返回错误，避免导致服务启动失败，只记录警告
		return nil
	}

	log.InfoContextf(ctx, "[DNSProxy] Merged %d unique IPs for domain %s (local: %d, terminal nodes: %d)",
		len(allIPs), domain, len(localIPList), len(nodeIPMap))

	// 5. DNS探测所有IP
	ipInfoList := dnsProbeIPs(ctx, domain, allIPs)

	// 6. 统计可用IP数量
	availableCount := 0
	for _, ipInfo := range ipInfoList {
		if ipInfo.Available {
			availableCount++
		}
	}

	log.InfoContextf(ctx, "[DNSProxy] DNS probe completed for %s: %d/%d IPs available, took %v",
		domain, availableCount, len(allIPs), time.Since(startTime))

	// 7. 构建结果
	result := &MergedDNSResult{
		Domain:  domain,
		IPList:  ipInfoList,
		ProbeAt: time.Now(),
		Success: availableCount > 0,
	}

	// 8. 缓存结果（5分钟TTL）
	cacheKey := mergedResultCacheKeyPrefix + domain
	localcache.Set(cacheKey, result, mergedResultCacheTTL)

	log.InfoContextf(ctx, "[DNSProxy] Cached DNS probe result for domain %s (ttl=%ds)", domain, mergedResultCacheTTL)

	return nil
}

// dnsProbeIPs DNS探测IP列表
// 复用现有的探测逻辑，返回排序后的IP列表
func dnsProbeIPs(ctx context.Context, domain string, ips []string) []*IPInfo {
	if len(ips) == 0 {
		return []*IPInfo{}
	}

	log.DebugContextf(ctx, "[DNSProxy] Starting DNS probe for %d IPs of domain %s", len(ips), domain)

	// 调用现有的探测逻辑（已在dnsproxy.go中实现）
	// 该方法会根据配置的probe_configs进行探测和排序
	if GlobalDNSInstance == nil {
		log.ErrorContext(ctx, "[DNSProxy] GlobalDNSInstance is nil, cannot probe IPs")
		return []*IPInfo{}
	}

	ipInfoList := GlobalDNSInstance.PingAndSort(ctx, ips)

	// 统计可用IP数量
	availableCount := 0
	for _, ipInfo := range ipInfoList {
		if ipInfo.Available {
			availableCount++
		}
	}

	log.DebugContextf(ctx, "[DNSProxy] DNS probe result: %d/%d IPs available for domain %s",
		availableCount, len(ips), domain)

	return ipInfoList
}

// GetMergedDNSResult 获取指定域名的合并探测结果（从缓存读取）
// API接口调用此函数，直接读取缓存，不触发探测
func GetMergedDNSResult(ctx context.Context, domain string) (*MergedDNSResult, error) {
	cacheKey := mergedResultCacheKeyPrefix + domain

	// 从localcache读取
	cached, ok := localcache.Get(cacheKey)
	if !ok {
		log.DebugContextf(ctx, "[DNSProxy] Merged result cache miss for domain: %s", domain)
		return nil, fmt.Errorf("cache miss for domain: %s", domain)
	}

	result, ok := cached.(*MergedDNSResult)
	if !ok {
		return nil, fmt.Errorf("invalid cache data type for domain: %s", domain)
	}

	log.DebugContextf(ctx, "[DNSProxy] Merged result cache hit for domain: %s, success=%v, ips=%d",
		domain, result.Success, len(result.IPList))

	return result, nil
}
