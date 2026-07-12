package health

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func Handler(startedAt time.Time) http.Handler {
	snapshot := func(_ context.Context) healthz.Response {
		return healthz.Base("admin", "admin-gateway", "", "", startedAt, true)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/metrics") {
			promhttp.Handler().ServeHTTP(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/readyz") {
			healthz.ReadinessHandler(snapshot).ServeHTTP(w, r)
			return
		}
		healthz.LivenessHandler(snapshot).ServeHTTP(w, r)
	})
}
