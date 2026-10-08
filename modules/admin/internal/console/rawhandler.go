package console

import (
	"context"
	"net/http"
	"sync"

	"trpc.group/trpc-go/trpc-go/log"
)

// RawHandler 是控制台进程内的原始 HTTP 处理器，用于 SSH 终端（WebSocket）和 SFTP 上传下载这类
// 不适合 RPC 的场景。鉴权、trace、CORS 由控制台前置完成，处理器自己读写请求和响应。
type RawHandler http.HandlerFunc

var (
	rawHandlers      = make(map[string]map[string]RawHandler)
	rawHandlersMutex sync.RWMutex
)

// RegisterRawHandler 把 /api/admin/<console_name>/<method> 登记为原始处理器。
func RegisterRawHandler(consoleName, method string, h RawHandler) {
	rawHandlersMutex.Lock()
	defer rawHandlersMutex.Unlock()
	if _, ok := rawHandlers[consoleName]; !ok {
		rawHandlers[consoleName] = make(map[string]RawHandler)
	}
	rawHandlers[consoleName][method] = h
	log.Infof("[console] 已登记原始处理器: %s/%s", consoleName, method)
}

// LookupRawHandler 查找原始处理器。
func LookupRawHandler(consoleName, method string) (RawHandler, bool) {
	rawHandlersMutex.RLock()
	defer rawHandlersMutex.RUnlock()
	if methods, ok := rawHandlers[consoleName]; ok {
		if h, ok := methods[method]; ok {
			return h, true
		}
	}
	return nil, false
}

// rawAndServe 分派原始处理器；返回 false 表示没有命中。调用方必须在读取请求体之前调用，并已完成鉴权。
func rawAndServe(ctx context.Context, w http.ResponseWriter, r *http.Request, consoleName, method string) bool {
	h, ok := LookupRawHandler(consoleName, method)
	if !ok {
		return false
	}
	h(w, r.WithContext(ctx))
	return true
}
