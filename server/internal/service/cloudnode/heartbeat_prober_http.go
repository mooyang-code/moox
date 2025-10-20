package cloudnode

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// HTTPHeartbeatProber HTTP心跳探测器
type HTTPHeartbeatProber struct {
	client *http.Client
}

// NewHTTPHeartbeatProber 创建HTTP心跳探测器
func NewHTTPHeartbeatProber() *HTTPHeartbeatProber {
	return &HTTPHeartbeatProber{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Name 探测器名称
func (a *HTTPHeartbeatProber) Name() string {
	return "http"
}

// Probe 执行HTTP探测
func (a *HTTPHeartbeatProber) Probe(ctx context.Context, req *ProbeRequest) (*ProbeResponse, error) {
	if req.ProbeURL == "" {
		return nil, fmt.Errorf("probe url is required for http prober")
	}

	startTime := time.Now()

	// 创建HTTP请求
	httpReq, err := http.NewRequestWithContext(ctx, "GET", req.ProbeURL, nil)
	if err != nil {
		return &ProbeResponse{
			Success:      false,
			ResponseTime: time.Since(startTime).Milliseconds(),
		}, fmt.Errorf("create http request failed: %w", err)
	}

	// 设置超时
	if req.Timeout > 0 {
		timeout := time.Duration(req.Timeout) * time.Second
		a.client.Timeout = timeout
	}

	// 执行请求
	resp, err := a.client.Do(httpReq)
	responseTime := time.Since(startTime).Milliseconds()

	if err != nil {
		return &ProbeResponse{
			Success:      false,
			ResponseTime: responseTime,
			Details: map[string]interface{}{
				"error": err.Error(),
			},
		}, nil
	}
	defer resp.Body.Close()

	// 判断探测是否成功（2xx状态码）
	success := resp.StatusCode >= 200 && resp.StatusCode < 300

	return &ProbeResponse{
		Success:      success,
		StatusCode:   resp.StatusCode,
		ResponseTime: responseTime,
		Details: map[string]interface{}{
			"status":  resp.Status,
			"headers": resp.Header,
			"proto":   resp.Proto,
			"method":  "GET",
			"url":     req.ProbeURL,
		},
	}, nil
}
