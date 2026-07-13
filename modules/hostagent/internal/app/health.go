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
	handler, err := healthz.WrapFromEnv(healthHandler(agent))
	if err != nil {
		return err
	}
	return healthz.RegisterNoProtocolServiceMux(service, handler)
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
		ready := status.GetEventbusConnected() && status.GetLastError() == ""
		rsp.Ready = ready
		if ready {
			rsp.Status = "ok"
		}
		rsp.Details = map[string]any{
			"agent_id":           status.GetAgentId(),
			"eventbus_connected": status.GetEventbusConnected(),
			"last_collect_at":    status.GetLastCollectAt(),
			"last_publish_at":    status.GetLastPublishAt(),
			"last_error":         status.GetLastError(),
			"collected_total":    status.GetCollected(),
			"published_total":    status.GetPublished(),
			"dropped_total":      status.GetDropped(),
			"skipped_total":      status.GetSkipped(),
		}
		return rsp
	}
	mux := healthz.StandardMux(snapshot, promhttp.Handler())
	return mux
}
