package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

// GatewayHandler handles gateway requests for collector service
type GatewayHandler struct {
	httpClient *http.Client
	baseURL    string
}

// NewGatewayHandler creates a new gateway handler
func NewGatewayHandler(baseURL string) *GatewayHandler {
	return &GatewayHandler{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		baseURL: baseURL,
	}
}

// ServiceID returns the service ID for registration
func (h *GatewayHandler) ServiceID() string {
	return "collector"
}

// ForwardRequest forwards gateway requests to internal API endpoints
func (h *GatewayHandler) ForwardRequest(ctx context.Context, method string, headers map[string]string, body []byte) ([]byte, error) {
	log.InfoContextf(ctx, "[Collector Gateway] ForwardRequest called - method: %s, headers: %+v, body: %s", method, headers, string(body))

	// Map gateway methods to internal API endpoints and HTTP methods
	var internalPath string
	var httpMethod string
	var requestBody []byte

	switch method {
	// Async task methods
	case "AsyncTaskCreate":
		internalPath = "/moox-api/async_task/create"
		httpMethod = "POST"
		requestBody = body
	case "AsyncTaskQuery":
		internalPath = "/moox-api/async_task/query"
		httpMethod = "GET"
		requestBody = nil
		// Extract task_id from body and add to URL as query parameter
		if len(body) > 0 {
			var params map[string]interface{}
			if err := json.Unmarshal(body, &params); err == nil {
				if taskID, ok := params["task_id"].(string); ok {
					internalPath = fmt.Sprintf("%s?task_id=%s", internalPath, url.QueryEscape(taskID))
				}
			}
		}

	// Node management methods
	case "ListNodes", "GetNodeList":
		internalPath = "/moox-api/t_cloud_nodes"
		httpMethod = "GET"
	case "GetNode":
		internalPath = "/moox-api/t_cloud_nodes"
		httpMethod = "GET"
		// Note: node_id parameter should be passed via query string
	case "RegisterNode":
		internalPath = "/moox-api/t_cloud_nodes"
		httpMethod = "POST"
		requestBody = h.wrapAction("register", body)
	case "UpdateNode":
		internalPath = "/moox-api/t_cloud_nodes"
		httpMethod = "POST"
		requestBody = h.wrapAction("update", body)
	case "DeleteNode":
		internalPath = "/moox-api/t_cloud_nodes"
		httpMethod = "POST"
		requestBody = h.wrapAction("delete", body)
	case "NodeHeartbeat":
		internalPath = "/moox-api/t_cloud_nodes"
		httpMethod = "POST"
		requestBody = h.wrapAction("heartbeat", body)
	case "UpdateNodeLoad":
		internalPath = "/moox-api/t_cloud_nodes"
		httpMethod = "POST"
		requestBody = h.wrapAction("update_load", body)

	// Task configuration methods
	case "ListTaskConfigs":
		internalPath = "/moox-api/t_collector_task_config"
		httpMethod = "GET"
	case "GetTaskConfig":
		internalPath = "/moox-api/t_collector_task_config"
		httpMethod = "GET"
	case "CreateTaskConfig":
		internalPath = "/moox-api/t_collector_task_config"
		httpMethod = "POST"
		requestBody = h.wrapAction("create", body)
	case "UpdateTaskConfig":
		internalPath = "/moox-api/t_collector_task_config"
		httpMethod = "POST"
		requestBody = h.wrapAction("update", body)
	case "DeleteTaskConfig":
		internalPath = "/moox-api/t_collector_task_config"
		httpMethod = "POST"
		requestBody = h.wrapAction("delete", body)
	case "BatchUpdateTaskConfigEnabled":
		internalPath = "/moox-api/t_collector_task_config"
		httpMethod = "POST"
		requestBody = h.wrapAction("batch_update_enabled", body)
	case "UpdateDispatchResult":
		internalPath = "/moox-api/t_collector_task_config"
		httpMethod = "POST"
		requestBody = h.wrapAction("update_dispatch_result", body)

	// Task instance methods
	case "ListTaskInstances":
		internalPath = "/moox-api/t_collector_task_instances"
		httpMethod = "GET"
	case "GetTaskInstance":
		internalPath = "/moox-api/t_collector_task_instances"
		httpMethod = "GET"
	case "CreateTaskInstance":
		internalPath = "/moox-api/t_collector_task_instances"
		httpMethod = "POST"
		requestBody = h.wrapAction("create", body)
	case "RetryTaskInstance":
		internalPath = "/moox-api/t_collector_task_instances"
		httpMethod = "POST"
		requestBody = h.wrapAction("retry", body)
	case "CancelTaskInstance":
		internalPath = "/moox-api/t_collector_task_instances"
		httpMethod = "POST"
		requestBody = h.wrapAction("cancel", body)
	case "GetTaskInstanceLogs":
		internalPath = "/moox-api/t_collector_task_instances"
		httpMethod = "POST"
		requestBody = h.wrapAction("get_logs", body)
	case "BatchCreateTaskInstances":
		internalPath = "/moox-api/t_collector_task_instances"
		httpMethod = "POST"
		requestBody = h.wrapAction("batch_create", body)
	case "StartTaskInstance":
		internalPath = "/moox-api/t_collector_task_instances"
		httpMethod = "POST"
		requestBody = h.wrapAction("start", body)
	case "CompleteTaskInstance":
		internalPath = "/moox-api/t_collector_task_instances"
		httpMethod = "POST"
		requestBody = h.wrapAction("complete", body)
	case "UpdateTaskInstanceStatus":
		internalPath = "/moox-api/t_collector_task_instances"
		httpMethod = "POST"
		requestBody = h.wrapAction("update_status", body)
	case "CleanupOldInstances":
		internalPath = "/moox-api/t_collector_task_instances"
		httpMethod = "POST"
		requestBody = h.wrapAction("cleanup", body)

	// Cloud account methods
	case "ListCloudAccounts":
		internalPath = "/moox-api/t_cloud_accounts"
		httpMethod = "GET"
	case "GetCloudAccount":
		internalPath = "/moox-api/t_cloud_accounts"
		httpMethod = "GET"
	case "CreateCloudAccount":
		internalPath = "/moox-api/t_cloud_accounts"
		httpMethod = "POST"
		requestBody = h.wrapAction("create", body)
	case "UpdateCloudAccount":
		internalPath = "/moox-api/t_cloud_accounts"
		httpMethod = "POST"
		requestBody = h.wrapAction("update", body)
	case "DeleteCloudAccount":
		internalPath = "/moox-api/t_cloud_accounts"
		httpMethod = "POST"
		requestBody = h.wrapAction("delete", body)
	case "ListCloudAccountsByProvider":
		internalPath = "/moox-api/t_cloud_accounts"
		httpMethod = "GET"

	case "UploadFunction":
		internalPath = "/moox-api/cloud_node/upload_function"
		httpMethod = "POST"
		requestBody = body

	default:
		return nil, fmt.Errorf("unsupported method: %s", method)
	}

	// Build the complete URL
	fullURL := h.baseURL + internalPath

	// For GET requests, convert body params to query params
	// Skip if query params are already in the path (e.g. AsyncTaskQuery)
	if httpMethod == "GET" && len(body) > 0 && !strings.Contains(internalPath, "?") {
		var params map[string]interface{}
		if err := json.Unmarshal(body, &params); err == nil {
			query := url.Values{}
			for key, value := range params {
				query.Set(key, fmt.Sprintf("%v", value))
			}
			if len(query) > 0 {
				fullURL += "?" + query.Encode()
			}
		}
	}

	log.InfoContextf(ctx, "[Collector Gateway] Forwarding to: %s %s with body: %s", httpMethod, fullURL, string(requestBody))

	// Create HTTP request
	var req *http.Request
	var err error
	if requestBody != nil {
		req, err = http.NewRequest(httpMethod, fullURL, bytes.NewReader(requestBody))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequest(httpMethod, fullURL, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
	}

	// Add headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// Execute request
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	log.InfoContextf(ctx, "[Collector Gateway] Response status: %d, body: %s", resp.StatusCode, string(respBody))

	// Check status code
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	// Handle empty response body
	if len(respBody) == 0 {
		log.ErrorContextf(ctx, "[Collector Gateway] Empty response body received")
		emptyResp := map[string]interface{}{
			"code": 500,
			"data": []interface{}{},
			"msg":  "Empty response from internal API",
		}
		return json.Marshal(emptyResp)
	}

	var apiResponse map[string]interface{}
	if err := json.Unmarshal(respBody, &apiResponse); err != nil {
		log.ErrorContextf(ctx, "[Collector Gateway] Failed to unmarshal response: %v, body: %s", err, string(respBody))
		// If we can't unmarshal, return as-is
		return respBody, nil
	}

	// Transform field names from uppercase to lowercase
	transformedResponse := make(map[string]interface{})
	if code, ok := apiResponse["Code"]; ok {
		transformedResponse["code"] = code
	}
	if data, ok := apiResponse["Data"]; ok {
		transformedResponse["data"] = data
	}
	if msg, ok := apiResponse["Msg"]; ok {
		transformedResponse["msg"] = msg
	}
	// Also check lowercase variants in case the API returns mixed case
	if code, ok := apiResponse["code"]; ok {
		transformedResponse["code"] = code
	}
	if data, ok := apiResponse["data"]; ok {
		transformedResponse["data"] = data
	}
	if msg, ok := apiResponse["msg"]; ok {
		transformedResponse["msg"] = msg
	}

	// Marshal the transformed response
	transformedBody, err := json.Marshal(transformedResponse)
	if err != nil {
		return respBody, nil
	}
	return transformedBody, nil
}

// wrapAction wraps request body with action parameter
func (h *GatewayHandler) wrapAction(action string, body []byte) []byte {
	var params map[string]interface{}
	if len(body) > 0 {
		json.Unmarshal(body, &params)
	}

	if params == nil {
		params = make(map[string]interface{})
	}

	params["_action"] = action
	wrapped, _ := json.Marshal(params)
	return wrapped
}
