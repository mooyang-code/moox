package gateway

import (
	"context"
	"net/http"
	"sync"

	"github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/spacecontext"

	"trpc.group/trpc-go/trpc-go/log"
)

// RawHandler 是经统一网关分派的裸 HTTP 处理器（用于 multipart/流式等不适合 PB RPC 的场景）。
// 它运行在 thttp http_no_protocol mux 之上，鉴权/trace/CORS 由网关前置统一完成，
// handler 自行从 r 读取请求（含 multipart）、自行向 w 写响应。
// ctx 已注入 space_id（X-Space-Id）和 trace_id，handler 可直接使用。
type RawHandler http.HandlerFunc

var (
	rawHandlers      = make(map[string]map[string]RawHandler)
	rawHandlersMutex sync.RWMutex
)

// RegisterRawHandler 注册某 service 的某 method 为裸 HTTP 处理器。
// 与 RegisterDispatcher 互斥：同一 (serviceID, method) 若已注册裸处理器，
// dispatchAndServe 不会再被触达（handleGatewayRequest 优先分派裸处理器）。
func RegisterRawHandler(serviceID, method string, h RawHandler) {
	rawHandlersMutex.Lock()
	defer rawHandlersMutex.Unlock()
	if _, ok := rawHandlers[serviceID]; !ok {
		rawHandlers[serviceID] = make(map[string]RawHandler)
	}
	rawHandlers[serviceID][method] = h
	log.Infof("[gateway] 已注册 raw handler: service=%s method=%s", serviceID, method)
}

// LookupRawHandler 查找 (serviceID, method) 的裸处理器。
func LookupRawHandler(serviceID, method string) (RawHandler, bool) {
	rawHandlersMutex.RLock()
	defer rawHandlersMutex.RUnlock()
	if methods, ok := rawHandlers[serviceID]; ok {
		if h, ok := methods[method]; ok {
			return h, true
		}
	}
	return nil, false
}

// rawAndServe 分派裸 HTTP 处理器。
// 返回 true 表示已处理（命中裸处理器），false 表示未命中、调用方应继续走 RPC dispatcher。
// 注意：调用方应在读取请求体之前调用本函数，避免 multipart body 被网关读干。
// 鉴权（JWT/HMAC）由调用方在调用本函数之前完成；space_id/trace_id 在此注入 ctx。
func rawAndServe(ctx context.Context, w http.ResponseWriter, r *http.Request, serviceID, method string, headers map[string]string) bool {
	h, ok := LookupRawHandler(serviceID, method)
	if !ok {
		return false
	}
	// 注入 space_id（硬隔离维度）与 trace_id，供裸 handler 使用
	ctx = spacecontext.InjectFromHeaders(ctx, headers)
	h(w, r.WithContext(ctx))
	return true
}
