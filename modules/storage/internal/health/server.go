package health

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"trpc.group/trpc-go/trpc-go/server"
)

func Handler(state *State) http.Handler {
	return healthz.StandardMux(state.Snapshot, promhttp.Handler())
}

const (
	InstanceStorageView           = "storage-view"
	defaultOldestPendingThreshold = 5 * time.Minute
)

type RoleOptions struct {
	OldestPendingThreshold time.Duration
}

// SnapshotForRole 将进程存活与流式就绪分开判断：Storage View 绑定 Consumer 后才就绪。
// 主服务的 Outbox 积压与 EventBus 连接只作为明细上报、不影响就绪：EventBus 故障时元数据与读写照常服务。
func SnapshotForRole(instance string, metrics *observability.ViewMetrics) healthz.SnapshotFunc {
	threshold := defaultOldestPendingThreshold
	if raw := strings.TrimSpace(os.Getenv("MOOX_STORAGE_OUTBOX_OLDEST_PENDING_THRESHOLD")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			threshold = parsed
		}
	}
	return SnapshotForRoleWithOptions(instance, metrics, RoleOptions{OldestPendingThreshold: threshold})
}

func SnapshotForRoleWithOptions(instance string, metrics *observability.ViewMetrics, options RoleOptions) healthz.SnapshotFunc {
	threshold := options.OldestPendingThreshold
	if threshold <= 0 {
		threshold = defaultOldestPendingThreshold
	}
	return func(context.Context) healthz.Response {
		snapshot := metrics.Snapshot()
		consumerBound := instance != InstanceStorageView || snapshot.ConsumerBound
		// View backlog is partitioned by durable. A system-metrics poison message
		// must not make the Kline View fail readiness (and trigger a whole-process
		// restart); bound/restore state is the liveness gate. Partition backlog and
		// age remain exposed in readiness details for operators.
		consumerDraining := true
		ready := consumerBound && consumerDraining
		rsp := healthz.Base("storage", instance, "", "", time.Time{}, ready)
		rsp.Details = map[string]any{
			"process_alive":                            true,
			"consumer_bound":                           consumerBound,
			"consumer_draining":                        consumerDraining,
			"outbox_pending_entries":                   snapshot.OutboxPendingEntries,
			"outbox_oldest_age_seconds":                snapshot.OutboxOldestAge.Seconds(),
			"outbox_publisher_ready":                   snapshot.OutboxPublisherReady,
			"outbox_publisher_unavailable_age_seconds": snapshot.OutboxPublisherUnavailableAge.Seconds(),
			"outbox_last_publish_success":              snapshot.OutboxLastPublishSuccess,
			"outbox_last_reconnect":                    snapshot.OutboxLastReconnectAt,
			"outbox_reconnect_status":                  snapshot.OutboxReconnectStatus,
			"outbox_reconnect_successes":               snapshot.OutboxReconnectSuccesses,
			"outbox_reconnect_failures":                snapshot.OutboxReconnectFailures,
			"oldest_pending_age_seconds":               snapshot.OldestPendingAge.Seconds(),
			"oldest_pending_threshold_seconds":         threshold.Seconds(),
			"restore_duration_seconds":                 snapshot.RestoreDuration.Seconds(),
			"restore_ready":                            snapshot.RestoreReady,
			"restore_failures":                         snapshot.RestoreFailures,
			"consumer_partitions":                      metrics.ConsumerPartitionsSnapshot(),
			"ready_queue_pending":                      snapshot.ReadyQueuePending,
			"ready_queue_oldest_age_seconds":           snapshot.ReadyQueueOldestAge.Seconds(),
			"ready_publish_backoff":                    snapshot.ReadyPublishBackoff,
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
