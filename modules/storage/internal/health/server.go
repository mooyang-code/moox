package health

import (
	"context"
	"net/http"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"trpc.group/trpc-go/trpc-go/server"
)

func Handler(state *State) http.Handler {
	return healthz.StandardMux(state.Snapshot, promhttp.Handler())
}

const InstanceStorageView = "storage-view"

// SnapshotForRole 将进程存活与就绪分开判断：Storage View 恢复活动索引并开始跟随变更日志后才就绪。
// 主服务的 Outbox 积压与 EventBus 连接只作为明细上报、不影响就绪：EventBus 故障时元数据与读写照常服务。
func SnapshotForRole(instance string, metrics *observability.ViewMetrics) healthz.SnapshotFunc {
	return func(context.Context) healthz.Response {
		snapshot := metrics.Snapshot()
		// View 恢复完活动索引、跟随器开始跟随变更日志后才就绪。
		ready := instance != InstanceStorageView || (snapshot.RestoreReady && snapshot.Following)
		rsp := healthz.Base("storage", instance, "", "", time.Time{}, ready)
		rsp.Details = map[string]any{
			"process_alive":                            true,
			"following":                                snapshot.Following,
			"outbox_pending_entries":                   snapshot.OutboxPendingEntries,
			"outbox_oldest_age_seconds":                snapshot.OutboxOldestAge.Seconds(),
			"outbox_publisher_ready":                   snapshot.OutboxPublisherReady,
			"outbox_publisher_unavailable_age_seconds": snapshot.OutboxPublisherUnavailableAge.Seconds(),
			"outbox_last_publish_success":              snapshot.OutboxLastPublishSuccess,
			"outbox_last_reconnect":                    snapshot.OutboxLastReconnectAt,
			"outbox_reconnect_status":                  snapshot.OutboxReconnectStatus,
			"outbox_reconnect_successes":               snapshot.OutboxReconnectSuccesses,
			"outbox_reconnect_failures":                snapshot.OutboxReconnectFailures,
			"restore_duration_seconds":                 snapshot.RestoreDuration.Seconds(),
			"restore_ready":                            snapshot.RestoreReady,
			"restore_failures":                         snapshot.RestoreFailures,
		}
		return rsp
	}
}

func Register(service server.Service, state *State) error {
	handler, err := healthz.WrapFromEnv(Handler(state))
	if err != nil {
		return err
	}
	return healthz.RegisterNoProtocolServiceMux(service, handler)
}
