package lifecycle

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/mooyang-code/moox/packages/requestauth"
)

// 重置接口（健康检查端口，签名鉴权）：
//
//	POST /v1/lifecycle/reset/plan    {"scope":"data|all"}                         只读计划 + 一次性令牌
//	POST /v1/lifecycle/reset/apply   {"token":"…","scope":"…"} | {"operation_id":"…"}   发起或续做（后台执行）
//	GET  /v1/lifecycle/reset/status                                                  当前重置状态与步骤日志
//	POST /v1/lifecycle/reset/abort   {"by":"…"}                                       放弃失败的重置
//
// plan / apply / abort 要求请求带 reset 权限（用重置专用密钥签名，权限由服务端的密钥映射决定）；status 只读，运行时密钥也能看。

// ResetPrefix 是重置接口的路径前缀（用前缀路由注册）。
const ResetPrefix = "/v1/lifecycle/reset/"

// ResetHandler 处理重置接口。
func (c *Controller) ResetHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := strings.TrimPrefix(r.URL.Path, ResetPrefix)
		method := http.MethodPost
		if action == "status" {
			method = http.MethodGet
		}
		if r.Method != method {
			writeError(w, http.StatusMethodNotAllowed, errors.New(method+" only"))
			return
		}
		if action != "status" && !requestauth.HasPermission(r.Context(), requestauth.PermissionReset) {
			writeError(w, http.StatusForbidden, errors.New("没有 reset 权限：重置要用重置专用密钥签名，且模块进程要配置 MOOX_HEALTH_RESET_ACCESS_KEY / MOOX_HEALTH_RESET_SECRET_KEY"))
			return
		}
		switch action {
		case "plan":
			var req struct {
				Scope ResetScope `json:"scope"`
			}
			if !decodeBody(w, r, &req) {
				return
			}
			plan, err := c.ResetPlan(r.Context(), req.Scope)
			respond(w, plan, err)
		case "apply":
			var req ResetApplyRequest
			if !decodeBody(w, r, &req) {
				return
			}
			status, err := c.ResetApply(r.Context(), req)
			respond(w, status, err)
		case "abort":
			var req struct {
				By string `json:"by"`
			}
			if !decodeBody(w, r, &req) {
				return
			}
			status, err := c.ResetAbort(r.Context(), req.By)
			respond(w, status, err)
		case "status":
			write(w, http.StatusOK, c.ResetStatus())
		default:
			writeError(w, http.StatusNotFound, errors.New("未知的重置接口 "+action))
		}
	})
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

func respond(w http.ResponseWriter, v any, err error) {
	var denied *ErrReset
	switch {
	case errors.As(err, &denied):
		writeError(w, http.StatusConflict, err)
	case err != nil:
		writeError(w, http.StatusBadRequest, err)
	default:
		write(w, http.StatusOK, v)
	}
}
