package common

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// GetInternalIP 获取本机内网IP地址
func GetInternalIP() string {
	// 通过UDP拨号获取本机出站IP（不会真正建立连接）
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "unknown"
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}

// GetPublicIP 获取本机公网IP地址
func GetPublicIP() string {
	// 创建HTTP客户端，设置超时
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	// 尝试多个服务来获取公网IP
	services := []struct {
		url     string
		headers map[string]string
	}{
		{
			url: "https://ifconfig.me/ip",
			headers: map[string]string{
				"User-Agent": "curl/7.68.0",
			},
		},
		{
			url: "https://api.ipify.org?format=text",
			headers: map[string]string{
				"User-Agent": "curl/7.68.0",
			},
		},
		{
			url: "https://checkip.amazonaws.com",
			headers: map[string]string{
				"User-Agent": "curl/7.68.0",
			},
		},
	}

	ctx := context.Background()
	for _, service := range services {
		req, err := http.NewRequestWithContext(ctx, "GET", service.url, nil)
		if err != nil {
			continue
		}

		// 设置请求头
		for key, value := range service.headers {
			req.Header.Set(key, value)
		}

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			continue
		}

		if resp.StatusCode == 200 {
			ip := strings.TrimSpace(string(body))
			// 验证IP格式是否正确（简单检查不包含HTML标签）
			if ip != "" && !strings.Contains(ip, "<") && !strings.Contains(ip, ">") {
				return ip
			}
		}
	}
	return "unknown"
}
