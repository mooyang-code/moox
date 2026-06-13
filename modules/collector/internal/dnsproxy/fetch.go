package dnsproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/avast/retry-go"
	"github.com/mooyang-code/moox/modules/collector/pkg/config"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// ServerResponse 服务端响应结构
type ServerResponse struct {
	Code    int               `json:"code"`
	Message string            `json:"message"`
	Data    []ServerDNSRecord `json:"data"`
	Total   int               `json:"total"`
}

// ServerDNSRecord 服务端 DNS 记录
type ServerDNSRecord struct {
	Domain    string    `json:"domain"`
	BestIPs   string    `json:"best_ips"` // "1.2.3.4+5.6.7.8"
	ResolveAt time.Time `json:"resolve_at"`
	Success   bool      `json:"success"`
}

// ScheduledResolveDNS 定时器入口函数 - 定时解析 DNS 记录（先本地解析，失败则请求远端）
func ScheduledResolveDNS(c context.Context, _ string) error {
	ctx := trpc.CloneContext(c)
	nodeID, version := config.GetNodeInfo()
	log.WithContextFields(ctx, "func", "ScheduledResolveDNS", "version", version, "nodeID", nodeID)

	log.DebugContext(ctx, "ScheduledResolveDNS Enter")
	if err := FetchDNSRecords(ctx); err != nil {
		log.ErrorContextf(ctx, "scheduled resolve DNS failed: %v", err)
		return err
	}
	log.DebugContext(ctx, "ScheduledResolveDNS Success")
	return nil
}

// FetchDNSRecords 获取 DNS 记录（先本地解析，失败则请求远端）
func FetchDNSRecords(ctx context.Context) error {
	// 1. 获取需要解析的域名列表
	domains := getScheduledDomains()
	if len(domains) == 0 {
		log.DebugContext(ctx, "no domains configured for DNS resolution")
		return nil
	}

	log.InfoContextf(ctx, "[DNSProxy] 开始解析 %d 个域名", len(domains))

	// 2. 存储所有解析结果
	allRecords := make([]*DNSRecord, 0, len(domains))

	// 3. 对每个域名进行处理
	for _, domain := range domains {
		record := resolveSingleDomain(ctx, domain)
		if record != nil {
			allRecords = append(allRecords, record)
		}
	}

	log.InfoContextf(ctx, "[DNSProxy] 解析完成: 总数=%d", len(allRecords))

	// 4. 更新全局 DNS 记录（用于 HTTP 请求和心跳上报）
	updateDNSRecords(allRecords)

	log.DebugContextf(ctx, "DNS records updated successfully, total: %d", len(allRecords))
	return nil
}

// resolveSingleDomain 解析单个域名（先本地解析，失败则远端获取）
func resolveSingleDomain(ctx context.Context, domain string) *DNSRecord {
	// 1. 尝试本地解析
	log.DebugContextf(ctx, "[DNSProxy] 开始本地解析域名: %s", domain)
	localRecord, err := LocalResolveDomain(ctx, domain)
	if err == nil && localRecord != nil && localRecord.Success {
		log.InfoContextf(ctx, "[DNSProxy] 本地解析成功: domain=%s", domain)
		return localRecord
	}

	// 2. 本地解析失败，降级到远端获取
	log.WarnContextf(ctx, "[DNSProxy] 本地解析失败，降级到远端获取: domain=%s, error=%v", domain, err)
	remoteRecord := fetchSingleDomainFromRemote(ctx, domain)
	if remoteRecord != nil {
		log.InfoContextf(ctx, "[DNSProxy] 远端获取成功: domain=%s", domain)
		return remoteRecord
	}

	// 3. 远端获取也失败
	log.ErrorContextf(ctx, "[DNSProxy] 域名解析完全失败（本地+远端）: domain=%s", domain)
	return nil
}

// fetchSingleDomainFromRemote 从远端获取单个域名的 DNS 记录
func fetchSingleDomainFromRemote(ctx context.Context, domain string) *DNSRecord {
	// 获取服务端地址
	serverIP, serverPort := config.GetServerInfo()
	if serverIP == "" {
		log.DebugContext(ctx, "no server IP configured, skipping remote DNS fetch")
		return nil
	}

	// 构建请求 URL
	url := fmt.Sprintf("http://%s:%d/gateway/dnsproxy/GetDNSRecordList", serverIP, serverPort)

	// 发送 HTTP 请求
	respData, err := fetchFromServer(ctx, url)
	if err != nil {
		log.ErrorContextf(ctx, "[DNSProxy] 远端获取失败: %v", err)
		return nil
	}

	// 解析响应
	serverRecords, err := parseServerResponse(respData)
	if err != nil {
		log.ErrorContextf(ctx, "[DNSProxy] 解析远端响应失败: %v", err)
		return nil
	}

	// 查找目标域名的记录
	for _, srvRecord := range serverRecords {
		if srvRecord.Domain == domain {
			// 从 best_ips 中解析 IP 列表
			ips := parseBestIPs(srvRecord.BestIPs)
			log.DebugContextf(ctx, "[DNSProxy] 远端返回域名 %s 的 %d 个 IP", domain, len(ips))

			// 调用 probeAndSort（传递域名，内部会查找探测配置）
			ipList := probeAndSort(ctx, domain, ips)

			// 记录可用 IP 数量
			availableCount := 0
			for _, ip := range ipList {
				if ip.Available {
					availableCount++
				}
			}
			log.DebugContextf(ctx, "[DNSProxy] 远端域名 %s: %d/%d IPs available", domain, availableCount, len(ips))

			// 创建 DNSRecord
			return &DNSRecord{
				Domain:    srvRecord.Domain,
				IPList:    ipList,
				ResolveAt: srvRecord.ResolveAt,
				Success:   srvRecord.Success,
			}
		}
	}

	// 未找到该域名的记录
	log.WarnContextf(ctx, "[DNSProxy] 远端未返回域名 %s 的记录", domain)
	return nil
}

// fetchFromServer 从服务端获取数据
func fetchFromServer(ctx context.Context, url string) ([]byte, error) {
	httpClient := &http.Client{Timeout: 5 * time.Second}

	var respData []byte
	err := retry.Do(
		func() error {
			return sendSingleRequest(ctx, url, httpClient, &respData)
		},
		retry.Attempts(3),
		retry.Delay(1*time.Second),
		retry.DelayType(retry.BackOffDelay),
		retry.LastErrorOnly(true),
		retry.OnRetry(func(n uint, err error) {
			log.WarnContextf(ctx, "retrying DNS fetch request, attempt: %d, error: %v", n+1, err)
		}),
		retry.Context(ctx),
	)
	return respData, err
}

// sendSingleRequest 发送单次 HTTP 请求
func sendSingleRequest(ctx context.Context, url string, httpClient *http.Client, respData *[]byte) error {
	// 创建 HTTP 请求
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer([]byte("{}")))
	if err != nil {
		return fmt.Errorf("failed to create DNS fetch request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// 发送请求
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyData, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("DNS fetch request failed with status: %d, response: %s", resp.StatusCode, string(bodyData))
	}

	// 读取响应
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	*respData = data
	return nil
}

// parseServerResponse 解析服务端响应
func parseServerResponse(respData []byte) ([]ServerDNSRecord, error) {
	var serverResp ServerResponse
	if err := json.Unmarshal(respData, &serverResp); err != nil {
		return nil, fmt.Errorf("failed to parse server response: %w", err)
	}

	// 检查响应状态码
	if serverResp.Code != 200 {
		return nil, fmt.Errorf("server returned error code: %d, message: %s", serverResp.Code, serverResp.Message)
	}

	return serverResp.Data, nil
}

// parseBestIPs 解析 best_ips 字符串为 IP 列表
// 格式: "1.2.3.4+5.6.7.8+9.10.11.12"
func parseBestIPs(bestIPs string) []string {
	if bestIPs == "" {
		return nil
	}

	parts := strings.Split(bestIPs, "+")
	ips := make([]string, 0, len(parts))
	for _, ip := range parts {
		if trimmed := strings.TrimSpace(ip); trimmed != "" {
			ips = append(ips, trimmed)
		}
	}
	return ips
}

// getScheduledDomains 获取需要定时解析的域名列表
func getScheduledDomains() []string {
	// 确保本地配置已初始化
	if config.LocalAppConfig == nil {
		config.InitLocalAppConfig()
	}

	// 如果没有 DNSProxy 配置，返回空列表
	if config.LocalAppConfig.DNSProxy == nil {
		return nil
	}

	return config.LocalAppConfig.DNSProxy.ScheduledDomains
}
