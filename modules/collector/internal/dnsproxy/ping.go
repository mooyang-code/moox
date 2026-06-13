package dnsproxy

import (
	"context"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-go"
)

// probeAndSort 对 IP 列表进行探测并排序
// domain: 域名，用于查找探测配置
// ips: IP 列表
func probeAndSort(ctx context.Context, domain string, ips []string) []*IPInfo {
	var ipInfoList []*IPInfo
	var mu sync.Mutex

	// 获取该域名的探测配置
	probeConfig := getProbeConfig(domain)

	// 使用 trpc.GoAndWait 并发探测
	var handlers []func() error
	for _, ip := range ips {
		ipAddr := ip
		handlers = append(handlers, func() error {
			// 调用统一的探测接口
			latency, available := probeIP(ctx, ipAddr, domain, probeConfig)
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

	// 排序（可用的优先，延迟低的优先）
	sort.Slice(ipInfoList, func(i, j int) bool {
		if ipInfoList[i].Available != ipInfoList[j].Available {
			return ipInfoList[i].Available
		}
		return ipInfoList[i].Latency < ipInfoList[j].Latency
	})

	return ipInfoList
}

// pingIP 对单个 IP 进行 TCP 连接测试（端口 443）
func pingIP(ctx context.Context, ip string) (latency int64, available bool) {
	start := time.Now()
	address := fmt.Sprintf("%s:443", ip)

	// 创建带超时的 Context（2秒）
	timeoutCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	// 尝试建立 TCP 连接
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(timeoutCtx, "tcp", address)
	if err != nil {
		return 0, false
	}
	defer conn.Close()

	// 计算延迟（微秒）
	latency = time.Since(start).Microseconds()
	return latency, true
}
