package health

import (
	"net/http"

	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"trpc.group/trpc-go/trpc-go/server"
)

func Handler(state *State) http.Handler {
	return healthz.StandardMux(state.Snapshot, promhttp.Handler())
}

func Register(service server.Service, state *State) error {
	return healthz.RegisterNoProtocolServiceMux(service, Handler(state))
}
