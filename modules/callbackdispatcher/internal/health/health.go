// Package health 是事件中心的健康检查：只有数据库不可用才算没就绪，EventBus 连不上只在详情里体现
// （服务要能在 EventBus 之前启动，连上之后自动开始接收）。
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

// Handler 返回健康检查的 HTTP 处理器。
func Handler(state *State) http.Handler {
	return healthz.StandardMux(state.Snapshot, promhttp.Handler())
}

// Register 把健康检查挂到 tRPC 的 HTTP 服务上。
func Register(service server.Service, state *State) error {
	handler, err := healthz.WrapFromEnv(Handler(state))
	if err != nil {
		return err
	}
	return healthz.RegisterNoProtocolServiceMux(service, handler)
}
