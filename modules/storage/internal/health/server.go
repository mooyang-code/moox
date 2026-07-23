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
	InstanceStorageNode           = "storage-node"
	defaultOldestPendingThreshold = 5 * time.Minute
)

type RoleOptions struct {
	OldestPendingThreshold time.Duration
}

// SnapshotForRole keeps process liveness separate from streaming readiness.
// A Storage View is not ready until its managed consumer is bound. A DataNode
// is not ready when its durable outbox has exceeded the pending-age budget.
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
		outboxDraining := instance != InstanceStorageNode || (snapshot.OutboxObserved && (snapshot.OutboxPendingEntries == 0 || snapshot.OutboxOldestAge <= threshold))
		ready := consumerBound && outboxDraining
		rsp := healthz.Base("storage", instance, "", "", time.Time{}, ready)
		rsp.Details = map[string]any{
			"process_alive":                    true,
			"consumer_bound":                   consumerBound,
			"outbox_draining":                  outboxDraining,
			"outbox_pending_entries":           snapshot.OutboxPendingEntries,
			"outbox_oldest_age_seconds":        snapshot.OutboxOldestAge.Seconds(),
			"oldest_pending_age_seconds":       snapshot.OldestPendingAge.Seconds(),
			"oldest_pending_threshold_seconds": threshold.Seconds(),
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
