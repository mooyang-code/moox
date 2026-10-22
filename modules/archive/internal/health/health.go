// Package health serves the moox-archive-mgr health, readiness and metrics endpoints.
package health

import (
	"net/http"

	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"trpc.group/trpc-go/trpc-go/server"
)

// State 是健康状态。
type State = healthz.State

// New 创建健康状态。
func New(module, instance, version, commit string) *State {
	return healthz.NewState(module, instance, version, commit)
}

// Route 是挂在健康检查端口上的额外路由（同样要过健康检查的签名鉴权，只监听本机）。
type Route struct {
	Path    string
	Handler http.Handler
}

// Handler 返回 /healthz /readyz /metrics 与额外路由的处理器。
func Handler(state *State, routes ...Route) http.Handler {
	standard := healthz.StandardMux(state.Snapshot, promhttp.Handler())
	mux, ok := standard.(*healthz.Mux)
	if !ok {
		return standard
	}
	for _, route := range routes {
		// 前缀路由不限方法：生命周期命令与重置都是 POST。
		mux.HandlePrefix(route.Path, route.Handler)
	}
	return mux
}

// Register 把健康服务挂到 tRPC 服务上。
func Register(service server.Service, state *State, routes ...Route) error {
	handler, err := healthz.WrapFromEnv(Handler(state, routes...))
	if err != nil {
		return err
	}
	return healthz.RegisterNoProtocolServiceMux(service, handler)
}
