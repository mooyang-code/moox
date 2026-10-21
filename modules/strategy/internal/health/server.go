package health

import (
	"net/http"

	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"trpc.group/trpc-go/trpc-go/server"
)

// Route 是挂在健康检查端口上的额外路由（同样要过健康检查的签名鉴权，只监听本机）。
type Route struct {
	Path    string
	Handler http.Handler
}

func Handler(state *State, routes ...Route) http.Handler {
	standard := healthz.StandardMux(state.Snapshot, promhttp.Handler())
	mux, ok := standard.(*healthz.Mux)
	if !ok {
		return standard
	}
	for _, route := range routes {
		// 前缀路由不限方法：exact 路由只接受 GET，而本地写入、生命周期命令、回放都是 POST。
		mux.HandlePrefix(route.Path, route.Handler)
	}
	return mux
}

func Register(service server.Service, state *State, routes ...Route) error {
	handler, err := healthz.WrapFromEnv(Handler(state, routes...))
	if err != nil {
		return err
	}
	return healthz.RegisterNoProtocolServiceMux(service, handler)
}
