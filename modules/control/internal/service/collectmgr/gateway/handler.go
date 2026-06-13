package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"strings"

	"github.com/mooyang-code/moox/modules/control/internal/service/collectmgr"
	collectorapi "github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/api"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// customRecovery 自定义 Recovery 中间件，返回 JSON 格式错误响应
func customRecovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				// 记录详细的 panic 信息
				stack := string(debug.Stack())
				log.Errorf("[CollectMgr Gateway] Panic recovered: %v\nStack: %s", err, stack)

				// 返回 JSON 格式的错误响应
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"code":    500,
					"message": fmt.Sprintf("Internal server error: %v", err),
					"data":    []interface{}{},
				})
			}
		}()
		c.Next()
	}
}

// CollectorGatewayHandler 采集器网关处理器
type CollectorGatewayHandler struct {
	engine    *gin.Engine
	serviceID string
}

// NewGatewayHandler 创建采集器网关处理器
func NewGatewayHandler(taskRuleService collectmgr.TaskRuleService, taskInstanceService collectmgr.TaskInstanceService, dataTypeConfigService collectmgr.DataTypeConfigService, taskPlannerService collectmgr.TaskPlannerService) *CollectorGatewayHandler {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()

	// 添加自定义 Recovery 中间件，返回 JSON 格式错误响应
	engine.Use(customRecovery())

	// API路由组（包含版本号）
	api := engine.Group("/api/v1")

	// 注册采集器路由（任务规则、任务实例、数据类型配置和任务规划器）
	collectorapi.RegisterCollectorRoutes(api, taskRuleService, taskInstanceService, dataTypeConfigService, taskPlannerService)
	log.Info("[CollectMgr Gateway] 采集管理模块路由注册成功")

	return &CollectorGatewayHandler{
		engine:    engine,
		serviceID: "collectmgr",
	}
}

// ServiceID 实现ServiceHandler接口
func (h *CollectorGatewayHandler) ServiceID() string {
	return h.serviceID
}

// RouteInfo 路由信息结构
type RouteInfo struct {
	Path       string
	HTTPMethod string
	Body       []byte
}

// ForwardRequest 实现ServiceHandler接口，转发请求到内部引擎
func (h *CollectorGatewayHandler) ForwardRequest(ctx context.Context, method string, headers map[string]string, body []byte) ([]byte, error) {
	log.DebugContextf(ctx, "[CollectMgr Gateway] ForwardRequest called - method: %s, headers: %+v, body: %s", method, headers, string(body))

	// 解析方法并获取路由信息
	routeInfo, err := h.parseMethodToRoute(method, body)
	if err != nil {
		return nil, err
	}

	log.DebugContextf(ctx, "[CollectMgr Gateway] Forwarding to engine: %s %s with body: %s", routeInfo.HTTPMethod, routeInfo.Path, string(routeInfo.Body))

	// 创建并执行HTTP请求
	req, err := h.createHTTPRequest(routeInfo, headers)
	if err != nil {
		return nil, err
	}

	// 执行请求并处理响应
	return h.executeRequest(ctx, req)
}

// parseMethodToRoute 解析方法名并返回路由信息
func (h *CollectorGatewayHandler) parseMethodToRoute(method string, body []byte) (*RouteInfo, error) {
	route := &RouteInfo{}

	switch method {
	// Task Rule methods (任务规则)
	case "GetTaskRuleList", "ListTaskRules":
		return h.buildMultiQueryRoute("/api/v1/task-rule/list", body)
	case "GetTaskRuleDetail":
		route.Path = "/api/v1/task-rule"
		route.HTTPMethod = "GET"
		// Need to extract rule_id from body and add to path
		if len(body) > 0 {
			var params map[string]interface{}
			if err := json.Unmarshal(body, &params); err == nil {
				// Support both "id" and "rule_id" field names for backward compatibility
				var ruleID string
				if id, ok := params["id"].(string); ok && id != "" {
					ruleID = id
				} else if ruleIDField, ok := params["rule_id"].(string); ok && ruleIDField != "" {
					ruleID = ruleIDField
				}
				if ruleID != "" {
					route.Path = fmt.Sprintf("/api/v1/task-rule/%s", ruleID)
				}
			}
		}
	case "CreateTaskRule":
		route.Path = "/api/v1/task-rule/create"
		route.HTTPMethod = "POST"
		route.Body = body
	case "UpdateTaskRule":
		route.Path = "/api/v1/task-rule"
		route.HTTPMethod = "PUT"
		route.Body = body
		// Need to extract rule_id from body and add to path
		if len(body) > 0 {
			var params map[string]interface{}
			if err := json.Unmarshal(body, &params); err == nil {
				// Support both "id" and "rule_id" field names for backward compatibility
				var ruleID string
				if id, ok := params["id"].(string); ok && id != "" {
					ruleID = id
				} else if ruleIDField, ok := params["rule_id"].(string); ok && ruleIDField != "" {
					ruleID = ruleIDField
				}
				if ruleID != "" {
					route.Path = fmt.Sprintf("/api/v1/task-rule/%s", ruleID)
				}
			}
		}
	case "DisableTaskRule":
		route.Path = "/api/v1/task-rule"
		route.HTTPMethod = "DELETE"
		// Need to extract rule_id from body and add to path
		if len(body) > 0 {
			var params map[string]interface{}
			if err := json.Unmarshal(body, &params); err == nil {
				// Support both "id" and "rule_id" field names for backward compatibility
				var ruleID string
				if id, ok := params["id"].(string); ok && id != "" {
					ruleID = id
				} else if ruleIDField, ok := params["rule_id"].(string); ok && ruleIDField != "" {
					ruleID = ruleIDField
				}
				if ruleID != "" {
					route.Path = fmt.Sprintf("/api/v1/task-rule/%s", ruleID)
				}
			}
		}

	// Task Instance methods (任务实例)
	case "GetTaskInstanceList", "GetTaskInstanceListInner", "ListTaskInstances":
		return h.buildMultiQueryRoute("/api/v1/task-instance/list", body)
	case "GetTaskInstanceListCache":
		return h.buildMultiQueryRoute("/api/v1/task-instance/cache/list", body)
	case "GetTaskInstanceDetail":
		route.Path = "/api/v1/task-instance"
		route.HTTPMethod = "GET"
		// Need to extract instance_id from body and add to path
		if len(body) > 0 {
			var params map[string]interface{}
			if err := json.Unmarshal(body, &params); err == nil {
				if instanceID, ok := params["id"].(string); ok && instanceID != "" {
					route.Path = fmt.Sprintf("/api/v1/task-instance/%s", instanceID)
				}
			}
		}
	case "CreateTaskInstance":
		route.Path = "/api/v1/task-instance/create"
		route.HTTPMethod = "POST"
		route.Body = body
	case "UpdateTaskInstance":
		route.Path = "/api/v1/task-instance"
		route.HTTPMethod = "PUT"
		route.Body = body
		// Need to extract instance_id from body and add to path
		if len(body) > 0 {
			var params map[string]interface{}
			if err := json.Unmarshal(body, &params); err == nil {
				if instanceID, ok := params["id"].(string); ok && instanceID != "" {
					route.Path = fmt.Sprintf("/api/v1/task-instance/%s", instanceID)
				}
			}
		}
	case "DeleteTaskInstance", "RemoveTaskInstance":
		route.Path = "/api/v1/task-instance"
		route.HTTPMethod = "DELETE"
		// Need to extract instance_id from body and add to path
		if len(body) > 0 {
			var params map[string]interface{}
			if err := json.Unmarshal(body, &params); err == nil {
				if instanceID, ok := params["id"].(string); ok && instanceID != "" {
					route.Path = fmt.Sprintf("/api/v1/task-instance/%s", instanceID)
				}
			}
		}
	case "StartTaskInstance", "StartInstance":
		route.Path = "/api/v1/task-instance"
		route.HTTPMethod = "POST"
		route.Body = body
		// Need to extract instance_id from body and add to path
		if len(body) > 0 {
			var params map[string]interface{}
			if err := json.Unmarshal(body, &params); err == nil {
				if instanceID, ok := params["id"].(string); ok && instanceID != "" {
					route.Path = fmt.Sprintf("/api/v1/task-instance/%s/start", instanceID)
				}
			}
		}
	case "StopTaskInstance", "CompleteInstance":
		route.Path = "/api/v1/task-instance"
		route.HTTPMethod = "POST"
		route.Body = body
		// Need to extract instance_id from body and add to path
		if len(body) > 0 {
			var params map[string]interface{}
			if err := json.Unmarshal(body, &params); err == nil {
				if instanceID, ok := params["id"].(string); ok && instanceID != "" {
					route.Path = fmt.Sprintf("/api/v1/task-instance/%s/stop", instanceID)
				}
			}
		}
	case "ReportTaskStatus":
		route.Path = "/api/v1/task-instance"
		route.HTTPMethod = "POST"
		route.Body = body
		// Need to extract instance_id from body and add to path
		if len(body) > 0 {
			var params map[string]interface{}
			if err := json.Unmarshal(body, &params); err == nil {
				if instanceID, ok := params["id"].(string); ok && instanceID != "" {
					route.Path = fmt.Sprintf("/api/v1/task-instance/%s/report-status", instanceID)
				}
			}
		}
	case "InvalidateTaskInstance":
		route.Path = "/api/v1/task-instance/invalidate"
		route.HTTPMethod = "POST"
		route.Body = body

	// Data Type Config methods (数据类型配置)
	// 用于前端动态表单生成，根据数据类型获取参数字段配置
	case "GetDataTypeConfigs", "ListDataTypeConfigs":
		return h.buildMultiQueryRoute("/api/v1/data-type-config/list", body)
	case "GetDataTypeConfigWithFields":
		route.Path = "/api/v1/data-type-config"
		route.HTTPMethod = "GET"
		// Need to extract data_type from body and add to path
		if len(body) > 0 {
			var params map[string]interface{}
			if err := json.Unmarshal(body, &params); err == nil {
				if dataType, ok := params["data_type"].(string); ok && dataType != "" {
					route.Path = fmt.Sprintf("/api/v1/data-type-config/%s", dataType)
				}
			}
		}

	default:
		return nil, fmt.Errorf("unsupported method: %s", method)
	}
	return route, nil
}

// buildMultiQueryRoute 构建多参数查询路由
func (h *CollectorGatewayHandler) buildMultiQueryRoute(basePath string, body []byte) (*RouteInfo, error) {
	route := &RouteInfo{
		Path:       basePath,
		HTTPMethod: "GET",
	}

	if len(body) == 0 {
		return route, nil
	}

	var params map[string]interface{}
	if err := json.Unmarshal(body, &params); err != nil {
		return route, nil
	}

	var queryParams []string
	for key, value := range params {
		if value == nil {
			continue
		}
		// 过滤掉空字符串，但保留数字0和false等有效值
		if str, ok := value.(string); ok && str == "" {
			continue
		}
		queryParams = append(queryParams, fmt.Sprintf("%s=%v", key, value))
	}

	if len(queryParams) > 0 {
		route.Path = fmt.Sprintf("%s?%s", basePath, strings.Join(queryParams, "&"))
	}
	return route, nil
}

// createHTTPRequest 创建HTTP请求
func (h *CollectorGatewayHandler) createHTTPRequest(routeInfo *RouteInfo, headers map[string]string) (*http.Request, error) {
	var req *http.Request
	var err error

	if routeInfo.Body != nil {
		req, err = http.NewRequest(routeInfo.HTTPMethod, routeInfo.Path, bytes.NewReader(routeInfo.Body))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequest(routeInfo.HTTPMethod, routeInfo.Path, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
	}

	// 添加请求头（规范化header key）
	for key, value := range headers {
		// 将网关传来的key转换为标准HTTP header格式
		switch key {
		case "access_token":
			req.Header.Set("X-Access-Token", value)
		case "trace_id":
			req.Header.Set("X-Trace-Id", value)
		case "client_ip":
			req.Header.Set("X-Client-Ip", value)
		case "user_agent":
			req.Header.Set("User-Agent", value)
		default:
			req.Header.Set(key, value)
		}
	}
	return req, nil
}

// executeRequest 执行请求并处理响应
func (h *CollectorGatewayHandler) executeRequest(ctx context.Context, req *http.Request) ([]byte, error) {
	// 创建响应记录器
	recorder := httptest.NewRecorder()

	// 使用引擎处理请求
	h.engine.ServeHTTP(recorder, req)

	// 读取响应
	respBody := recorder.Body.Bytes()
	statusCode := recorder.Code

	log.DebugContextf(ctx, "[CollectMgr Gateway] Response status: %d", statusCode)

	// 检查状态码
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("request failed with status %d: %s", statusCode, string(respBody))
	}

	// 处理空响应体
	if len(respBody) == 0 {
		log.ErrorContextf(ctx, "[CollectMgr Gateway] Empty response body received")
		emptyResp := map[string]interface{}{
			"code": 500,
			"data": []interface{}{},
			"msg":  "Empty response from API",
		}
		return json.Marshal(emptyResp)
	}
	return respBody, nil
}
