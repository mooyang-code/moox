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

// Handler 返回 /healthz /readyz /metrics 的处理器。
func Handler(state *State) http.Handler {
	return healthz.StandardMux(state.Snapshot, promhttp.Handler())
}

// Register 把健康服务挂到 tRPC 服务上。
func Register(service server.Service, state *State) error {
	handler, err := healthz.WrapFromEnv(Handler(state))
	if err != nil {
		return err
	}
	return healthz.RegisterNoProtocolServiceMux(service, handler)
}
