package health

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"trpc.group/trpc-go/trpc-go/server"
)

// Snapshot 是 Admin 的健康快照：进程能应答即就绪。/readyz 与指标上报（packages/report）用同一个。
func Snapshot(startedAt time.Time) healthz.SnapshotFunc {
	return func(_ context.Context) healthz.Response {
		return healthz.Base("admin", "admin-gateway", "", "", startedAt, true)
	}
}

func Handler(startedAt time.Time) http.Handler {
	snapshot := Snapshot(startedAt)
	readiness, liveness := healthz.ReadinessHandler(snapshot), healthz.LivenessHandler(snapshot)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/metrics") {
			promhttp.Handler().ServeHTTP(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/readyz") {
			readiness.ServeHTTP(w, r)
			return
		}
		liveness.ServeHTTP(w, r)
	})
}

func Register(service server.Service, startedAt time.Time) error {
	handler, err := healthz.WrapFromEnv(Handler(startedAt))
	if err != nil {
		return err
	}
	return healthz.RegisterNoProtocolServiceMux(service, handler)
}
