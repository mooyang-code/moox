package cloudnode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
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

	// 创建HTTP请求
	httpReq, err := http.NewRequestWithContext(ctx, "GET", req.ProbeURL, nil)
	if err != nil {
		return &ProbeResponse{
			NodeID:       req.NodeID,
			State:        "error",
			Timestamp:    time.Now().Format(time.RFC3339),
			OSName:       runtime.GOOS,
		}, fmt.Errorf("create http request failed: %w", err)
	}

	// 设置超时
	if req.Timeout > 0 {
		timeout := time.Duration(req.Timeout) * time.Second
		a.client.Timeout = timeout
	}

	// 执行请求
	resp, err := a.client.Do(httpReq)

	if err != nil {
		return &ProbeResponse{
			NodeID:       req.NodeID,
			State:        "error",
			Timestamp:    time.Now().Format(time.RFC3339),
			OSName:       runtime.GOOS,
		}, nil
	}
	defer resp.Body.Close()

	// 解析响应
	return a.parseProbeResponse(req, resp)
}

// parseProbeResponse 解析HTTP响应
func (a *HTTPHeartbeatProber) parseProbeResponse(req *ProbeRequest, resp *http.Response) (*ProbeResponse, error) {
	response := &ProbeResponse{
		NodeID:      req.NodeID,
		State:       mapStatusCodeToState(resp.StatusCode),
		Timestamp:   time.Now().Format(time.RFC3339),
		OSName:      runtime.GOOS,
		RequestID:   "",
	}

	// 如果状态码表示成功，尝试解析JSON响应
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if jsonData, parseError := a.parseJSONResponse(resp.Body); parseError == nil {
			// 成功解析JSON，提取结构化数据
			a.extractProbeFields(response, jsonData)
		}
	}

	return response, nil
}

// parseJSONResponse 尝试解析JSON响应
func (a *HTTPHeartbeatProber) parseJSONResponse(body io.ReadCloser) (map[string]interface{}, error) {
	defer body.Close()

	// 读取响应体
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read response body failed: %w", err)
	}

	// 检查响应体是否为空
	if len(data) == 0 {
		return nil, fmt.Errorf("empty response body")
	}

	// 尝试解析JSON
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		// 如果JSON解析失败，返回详细错误信息
		return nil, fmt.Errorf("parse JSON response failed: %w, raw response: %s", err, string(data))
	}

	return result, nil
}

// extractProbeFields 从JSON响应中提取探测字段
func (a *HTTPHeartbeatProber) extractProbeFields(response *ProbeResponse, data map[string]interface{}) {
	// 提取data字段（如果存在）
	if dataField, exists := data["data"]; exists {
		if dataMap, ok := dataField.(map[string]interface{}); ok {
			// 提取state（节点状态）
			if state, ok := dataMap["state"].(string); ok && state != "" {
				response.State = state
			}

			// 提取node_info中的信息
			if nodeInfo, exists := dataMap["node_info"]; exists {
				if nodeInfoMap, ok := nodeInfo.(map[string]interface{}); ok {
					// 提取node_id
					if nodeID, ok := nodeInfoMap["node_id"].(string); ok && nodeID != "" {
						response.NodeID = nodeID
					}

					// 提取version
					if version, ok := nodeInfoMap["version"].(string); ok && version != "" {
						response.FunctionVersion = version
					}

					// 提取metadata中的系统信息
					if metadata, exists := nodeInfoMap["metadata"]; exists {
						if metadataMap, ok := metadata.(map[string]interface{}); ok {
							// 提取操作系统信息
							if os, ok := metadataMap["os"].(string); ok && os != "" {
								response.OSName = os
							}
						}
					}
				}
			}

			// 提取timestamp
			if timestamp, exists := dataMap["timestamp"]; exists {
				if tsStr, ok := timestamp.(string); ok {
					response.Timestamp = tsStr
				} else if tsInt, ok := timestamp.(float64); ok {
					response.Timestamp = time.Unix(int64(tsInt), 0).Format(time.RFC3339)
				}
			}
		}
	}

	// 如果在data中没有找到timestamp，尝试在顶层查找
	if response.Timestamp == "" {
		if timestamp, exists := data["timestamp"]; exists {
			if tsStr, ok := timestamp.(string); ok {
				response.Timestamp = tsStr
			} else if tsInt, ok := timestamp.(float64); ok {
				response.Timestamp = time.Unix(int64(tsInt), 0).Format(time.RFC3339)
			}
		}
	}

	// 提取request_id
	if requestID, exists := data["request_id"].(string); exists && requestID != "" {
		response.RequestID = requestID
	}

	// 如果状态为空，设置为unknown
	if response.State == "" {
		response.State = "unknown"
	}
}

// mapStatusCodeToState 将HTTP状态码映射为状态字符串
func mapStatusCodeToState(statusCode int) string {
	switch {
	case statusCode >= 200 && statusCode < 300:
		return "healthy"
	case statusCode >= 400 && statusCode < 500:
		return "client_error"
	case statusCode >= 500:
		return "server_error"
	default:
		return "unknown"
	}
}
