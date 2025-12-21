package api

import (
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	apperrors "github.com/mooyang-code/moox/server/internal/errors"
	"github.com/mooyang-code/moox/server/internal/service/dnsproxy"
	"trpc.group/trpc-go/trpc-database/localcache"
)

// DNSRecordHandler DNS解析记录处理器
type DNSRecordHandler struct{}

// NewDNSRecordHandler 创建DNS解析记录处理器
func NewDNSRecordHandler() *DNSRecordHandler {
	return &DNSRecordHandler{}
}

// DNSRecordWithBestIPs DNS解析记录（包含最优IP列表）
type DNSRecordWithBestIPs struct {
	*dnsproxy.DNSProxyResult
	BestIPs string `json:"best_ips"` // 所有有效IP按延迟排序后用+号连接
}

// GetDNSRecordList 获取所有DNS解析记录列表
func (h *DNSRecordHandler) GetDNSRecordList(c *gin.Context) {
	// 从配置中获取所有域名
	cfg := dnsproxy.GetConfig()
	if cfg == nil {
		HandleAppError(c, apperrors.Internal("配置未初始化", nil))
		return
	}

	domains := cfg.DNSProxy.Domains
	if len(domains) == 0 {
		// 没有配置域名，返回空列表
		PaginatedListResponse(c, "查询成功", []interface{}{}, 0)
		return
	}

	// 遍历所有域名，从缓存中获取解析结果
	var results []*DNSRecordWithBestIPs
	for _, domain := range domains {
		if cached, ok := localcache.Get(domain); ok {
			if result, ok := cached.(*dnsproxy.DNSProxyResult); ok {
				// 构建包含 best_ips 的响应
				record := &DNSRecordWithBestIPs{
					DNSProxyResult: result,
					BestIPs:        buildBestIPs(result.IPList),
				}
				results = append(results, record)
			}
		}
	}

	// 计算总数
	total := int64(len(results))

	// 使用分页列表响应格式
	PaginatedListResponse(c, "查询成功", results, total)
}

// buildBestIPs 构建最优IP列表字符串
// 筛选所有有效(Available=true)的IP，按延迟从小到大排序，用+号连接
func buildBestIPs(ipList []*dnsproxy.IPInfo) string {
	if len(ipList) == 0 {
		return ""
	}

	// 筛选有效的IP
	var availableIPs []*dnsproxy.IPInfo
	for _, ip := range ipList {
		if ip.Available {
			availableIPs = append(availableIPs, ip)
		}
	}

	if len(availableIPs) == 0 {
		return ""
	}

	// 按延迟从小到大排序
	sort.Slice(availableIPs, func(i, j int) bool {
		return availableIPs[i].Latency < availableIPs[j].Latency
	})

	// 用+号连接IP地址
	var ips []string
	for _, ip := range availableIPs {
		ips = append(ips, ip.IP)
	}

	return strings.Join(ips, "+")
}

// GetDNSRecordDetail 获取指定域名的DNS解析记录详情
func (h *DNSRecordHandler) GetDNSRecordDetail(c *gin.Context) {
	domain := c.Param("domain")
	if domain == "" {
		HandleAppError(c, apperrors.InvalidParam("domain", "域名参数不能为空"))
		return
	}

	// 从缓存中获取解析结果
	cached, ok := localcache.Get(domain)
	if !ok {
		HandleAppError(c, apperrors.NotFound("DNS解析记录"))
		return
	}

	result, ok := cached.(*dnsproxy.DNSProxyResult)
	if !ok {
		HandleAppError(c, apperrors.Internal("缓存数据格式错误", nil))
		return
	}

	SuccessResponse(c, "查询成功", result)
}
