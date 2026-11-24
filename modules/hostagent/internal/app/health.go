package app

import (
	"context"
	"net/http"
	"time"

	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"trpc.group/trpc-go/trpc-go/server"
)

var agentStartedAt = time.Now().UTC()

func RegisterHealth(service server.Service, agent *Agent) error {
	return healthz.RegisterNoProtocolServiceMux(service, healthHandler(agent))
}

func healthHandler(agent *Agent) http.Handler {
	snapshot := func(ctx context.Context) healthz.Response {
		rsp := healthz.Base("hostagent", "hostagent", "", "", agentStartedAt, false)
		if agent == nil {
			rsp.Details = map[string]any{"agent_ready": false}
			return rsp
		}
		status, err := agent.GetStatus(ctx, nil)
		if err != nil {
			rsp.Details = map[string]any{"agent_ready": false, "error": err.Error()}
			return rsp
		}
		// 就绪只看采集：上报错误放在详情里，不参与就绪。
		ready, collectErr := agent.collectionState()
		rsp.Ready = ready
		if ready {
			rsp.Status = "ok"
		}
		rsp.Details = map[string]any{
			"host_id":         status.GetHostId(),
			"last_collect_at": status.GetLastCollectAt(),
			"last_report_at":  status.GetLastReportAt(),
			"last_error":      status.GetLastError(),
			"collect_error":   collectErr,
			"collected_total": status.GetCollected(),
			"reported_total":  status.GetReported(),
			"dropped_total":   status.GetDropped(),
			"skipped_total":   status.GetSkipped(),
		}
		return rsp
	}
	mux := healthz.StandardMux(snapshot, promhttp.Handler())
	return mux
}
