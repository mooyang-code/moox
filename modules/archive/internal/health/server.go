package health

import (
	"context"
	"net/http"

	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func Handler(state *State) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/healthz", healthz.Handler(state.Snapshot))
	mux.Handle("/readyz", healthz.Handler(state.Snapshot))
	mux.Handle("/metrics", promhttp.Handler())
	return mux
}
func Start(ctx context.Context, addr string, state *State) (*http.Server, error) {
	return healthz.StartWithHandler(ctx, addr, Handler(state))
}
