package dnsproxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/mooyang-code/moox/modules/collector/pkg/config"
	"trpc.group/trpc-go/trpc-go/log"
)

// getProbeConfig 根据域名查找探测配置
func getProbeConfig(domain string) *config.ProbeConfig {
	// 确保本地配置已初始化
	if config.LocalAppConfig == nil {
		config.InitLocalAppConfig()
	}

	// 如果没有 DNSProxy 配置，返回 nil（使用默认 TCP 探测）
	if config.LocalAppConfig.DNSProxy == nil {
		return nil
	}

	// 查找匹配的域名配置
	for i := range config.LocalAppConfig.DNSProxy.ProbeConfigs {
		cfg := &config.LocalAppConfig.DNSProxy.ProbeConfigs[i]
		if cfg.Domain == domain {
			return cfg
		}
	}

	// 没有找到配置，返回 nil（使用默认 TCP 探测）
	return nil
}

// probeIP 统一探测入口
func probeIP(ctx context.Context, ip, domain string, cfg *config.ProbeConfig) (latency int64, available bool) {
	// 如果没有配置或配置类型为 tcp，使用 TCP 探测
	if cfg == nil || cfg.ProbeType == "tcp" {
		port := 443
		timeout := 2 * time.Second

		if cfg != nil {
			if cfg.TCPPort > 0 {
				port = cfg.TCPPort
			}
			if cfg.Timeout > 0 {
				timeout = time.Duration(cfg.Timeout) * time.Second
			}
		}

		latency, available := probeTCP(ctx, ip, port, timeout)
		if available {
			log.DebugContextf(ctx, "[DNSProxy] 探测 %s - IP: %s, 类型: TCP(%d), 延迟: %dμs, 状态: 可用",
				domain, ip, port, latency)
		} else {
			log.DebugContextf(ctx, "[DNSProxy] 探测 %s - IP: %s, 类型: TCP(%d), 延迟: 0μs, 状态: 不可用",
				domain, ip, port)
		}
		return latency, available
	}

	// 使用 HTTPS 业务探测
	if cfg.ProbeType == "https" && cfg.ProbeAPI != nil {
		latency, available := probeHTTPS(ctx, ip, domain, cfg.ProbeAPI)
		if available {
			log.DebugContextf(ctx, "[DNSProxy] 探测 %s - IP: %s, 类型: HTTPS(%s), 延迟: %dμs, 状态: 可用",
				domain, ip, cfg.ProbeAPI.Path, latency)
		} else {
			log.DebugContextf(ctx, "[DNSProxy] 探测 %s - IP: %s, 类型: HTTPS(%s), 延迟: 0μs, 状态: 不可用",
				domain, ip, cfg.ProbeAPI.Path)
		}
		return latency, available
	}

	// 未知配置类型，降级到 TCP 探测
	log.WarnContextf(ctx, "[DNSProxy] 未知探测类型 %s，降级到 TCP 探测", cfg.ProbeType)
	return probeTCP(ctx, ip, 443, 2*time.Second)
}

// probeHTTPS 使用业务 API 进行 HTTPS 探测
func probeHTTPS(ctx context.Context, ip, domain string, cfg *config.ProbeAPIConfig) (latency int64, available bool) {
	// 默认值
	timeout := 3 * time.Second
	if cfg.Timeout > 0 {
		timeout = time.Duration(cfg.Timeout) * time.Second
	}

	method := "GET"
	if cfg.Method != "" {
		method = cfg.Method
	}

	expectedStatus := 200
	if cfg.ExpectedStatus > 0 {
		expectedStatus = cfg.ExpectedStatus
	}

	// 创建带超时的 Context
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 记录开始时间
	start := time.Now()

	// 构建完整 URL（使用域名，保证 TLS SNI 正确）
	fullURL := fmt.Sprintf("https://%s%s", domain, cfg.Path)

	// 创建自定义 Transport，将域名解析到指定 IP
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			// 跳过证书验证（因为证书是域名，但连接的是 IP）
			InsecureSkipVerify: true,
		},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// 提取端口号
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				port = "443" // 默认 HTTPS 端口
			}

			// 使用指定 IP 进行连接
			targetAddr := net.JoinHostPort(ip, port)
			dialer := &net.Dialer{
				Timeout: timeout,
			}
			return dialer.DialContext(ctx, network, targetAddr)
		},
	}

	// 创建临时 HTTP 客户端
	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}

	// 创建请求
	req, err := http.NewRequestWithContext(timeoutCtx, method, fullURL, nil)
	if err != nil {
		log.WarnContextf(ctx, "[DNSProxy] 创建 HTTPS 探测请求失败: %v", err)
		return 0, false
	}

	// 设置 User-Agent
	req.Header.Set("User-Agent", "data-collector-probe/1.0")

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		log.DebugContextf(ctx, "[DNSProxy] HTTPS 探测请求失败 (IP: %s, URL: %s): %v", ip, fullURL, err)
		return 0, false
	}
	defer resp.Body.Close()

	// 计算延迟（微秒）
	latency = time.Since(start).Microseconds()

	// 检查状态码
	if resp.StatusCode != expectedStatus {
		log.DebugContextf(ctx, "[DNSProxy] HTTPS 探测状态码不匹配 (IP: %s, 期望: %d, 实际: %d)",
			ip, expectedStatus, resp.StatusCode)
		return 0, false
	}

	return latency, true
}

// probeTCP 使用 TCP 连接测试（复用现有的 pingIP 逻辑）
func probeTCP(ctx context.Context, ip string, port int, timeout time.Duration) (latency int64, available bool) {
	start := time.Now()
	address := fmt.Sprintf("%s:%d", ip, port)

	// 创建带超时的 Context
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
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
