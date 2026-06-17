package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	ssh "github.com/mooyang-code/moox/modules/control/internal/service/ssh"
	sshapi "github.com/mooyang-code/moox/modules/control/internal/service/ssh/api"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// SSHGatewayHandler SSH 网关处理器
type SSHGatewayHandler struct {
	engine    *gin.Engine
	serviceID string
}

// NewSSHGatewayHandler 创建 SSH 网关处理器
func NewSSHGatewayHandler(svc ssh.Service) *SSHGatewayHandler {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(gin.Recovery())

	api := engine.Group("/api/v1")
	sshapi.RegisterSSHRoutes(api, svc)
	log.Info("[SSH Gateway] SSH 路由注册成功")

	return &SSHGatewayHandler{
		engine:    engine,
		serviceID: "ssh",
	}
}

// ServiceID 实现 ServiceHandler 接口
func (h *SSHGatewayHandler) ServiceID() string {
	return h.serviceID
}

// ForwardRequest 实现 ServiceHandler 接口
func (h *SSHGatewayHandler) ForwardRequest(ctx context.Context, method string, headers map[string]string, body []byte) ([]byte, error) {
	log.DebugContextf(ctx, "[SSH Gateway] ForwardRequest: method=%s", method)

	routeInfo, err := h.parseMethodToRoute(method, body)
	if err != nil {
		return nil, err
	}

	req, err := h.createHTTPRequest(routeInfo, headers)
	if err != nil {
		return nil, err
	}

	return h.executeRequest(ctx, req)
}

// RouteInfo 路由信息
type RouteInfo struct {
	Path       string
	HTTPMethod string
	Body       []byte
}

func (h *SSHGatewayHandler) parseMethodToRoute(method string, body []byte) (*RouteInfo, error) {
	route := &RouteInfo{Body: body}

	switch method {
	// 主机配置
	case "ListHosts":
		route.Path = "/api/v1/ssh_host/list"
		route.HTTPMethod = "POST"
	case "CreateHost":
		route.Path = "/api/v1/ssh_host/create"
		route.HTTPMethod = "POST"
	case "UpdateHost":
		route.Path = "/api/v1/ssh_host/update"
		route.HTTPMethod = "PUT"
	case "DeleteHost":
		route.Path = "/api/v1/ssh_host/delete"
		route.HTTPMethod = "DELETE"
	case "GetHostDetail":
		return h.buildQueryRoute("/api/v1/ssh_host/detail", body)

	// SSH 会话
	case "CreateSession":
		route.Path = "/api/v1/ssh/create_session"
		route.HTTPMethod = "POST"
	case "DisconnectSession":
		route.Path = "/api/v1/ssh/disconnect"
		route.HTTPMethod = "POST"
	case "ResizeWindow":
		route.Path = "/api/v1/ssh/resize"
		route.HTTPMethod = "POST"
	case "ExecCommand":
		route.Path = "/api/v1/ssh/exec"
		route.HTTPMethod = "POST"

	// SFTP
	case "SftpList":
		route.Path = "/api/v1/sftp/list"
		route.HTTPMethod = "POST"
	case "SftpMkdir":
		route.Path = "/api/v1/sftp/mkdir"
		route.HTTPMethod = "POST"
	case "SftpDelete":
		route.Path = "/api/v1/sftp/delete"
		route.HTTPMethod = "POST"

	// 会话管理
	case "GetOnlineSessions":
		route.Path = "/api/v1/ssh_manage/online_sessions"
		route.HTTPMethod = "POST"
	case "ForceDisconnect":
		route.Path = "/api/v1/ssh_manage/force_disconnect"
		route.HTTPMethod = "POST"

	default:
		return nil, fmt.Errorf("unsupported method: %s", method)
	}

	return route, nil
}

func (h *SSHGatewayHandler) buildQueryRoute(basePath string, body []byte) (*RouteInfo, error) {
	route := &RouteInfo{
		Path:       basePath,
		HTTPMethod: "GET",
	}
	if len(body) > 0 {
		var params map[string]interface{}
		if err := json.Unmarshal(body, &params); err == nil {
			var queryParams []string
			for key, value := range params {
				if value != nil {
					if str, ok := value.(string); ok && str == "" {
						continue
					}
					queryParams = append(queryParams, fmt.Sprintf("%s=%v", key, value))
				}
			}
			if len(queryParams) > 0 {
				route.Path = fmt.Sprintf("%s?%s", basePath, strings.Join(queryParams, "&"))
			}
		}
	}
	return route, nil
}

func (h *SSHGatewayHandler) createHTTPRequest(routeInfo *RouteInfo, headers map[string]string) (*http.Request, error) {
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

	for key, value := range headers {
		switch key {
		case "access_token":
			req.Header.Set("X-Access-Token", value)
		case "trace_id":
			req.Header.Set("X-Trace-Id", value)
		case "client_ip":
			req.Header.Set("X-Client-Ip", value)
		default:
			req.Header.Set(key, value)
		}
	}
	return req, nil
}

func (h *SSHGatewayHandler) executeRequest(ctx context.Context, req *http.Request) ([]byte, error) {
	recorder := httptest.NewRecorder()
	h.engine.ServeHTTP(recorder, req)

	respBody := recorder.Body.Bytes()
	statusCode := recorder.Code

	log.DebugContextf(ctx, "[SSH Gateway] Response status: %d", statusCode)

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("request failed with status %d: %s", statusCode, string(respBody))
	}

	if len(respBody) == 0 {
		emptyResp := map[string]interface{}{
			"code": 500,
			"data": []interface{}{},
			"msg":  "Empty response from SSH API",
		}
		return json.Marshal(emptyResp)
	}

	return respBody, nil
}
