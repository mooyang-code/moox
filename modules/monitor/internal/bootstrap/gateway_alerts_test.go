package bootstrap

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGatewayAlertsFreezeDuringUncertaintyAndRetireWhenHostDisabled(t *testing.T) {
	t.Setenv("MOOX_NOTIFICATION_WEBHOOK_URL", "")
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	repos := manager.Repositories()
	now := time.Now().UTC()
	raw, err := json.Marshal(&adminpb.HostGatewayRuntimeStatus{InstanceId: "new", ConflictInstanceId: "old", ConflictSeenAt: now.Format(time.RFC3339Nano), LastSeenAt: now.Format(time.RFC3339Nano)})
	require.NoError(t, err)
	row := domain.GatewayObservation{HostID: "control", StatusJSON: string(raw), ExpectedHash: "expected", AppliedHash: "expected", HostEnabledAt: now.Add(-time.Hour)}
	require.NoError(t, repos.Gateways.Reconcile(t.Context(), []domain.GatewayObservation{row}, now))
	builder := &observability.Builder{Checks: repos.Checks, Results: repos.Results, Gateways: repos.Gateways, Now: func() time.Time { return now }}
	runtime := &Runtime{Repositories: repos}
	run := buildBusinessFreshnessReporter(builder, repos, monitorResultHook(runtime))
	require.NoError(t, run(t.Context()))
	id := "gateway:control:instance_conflict"
	state, err := repos.Alerts.GetState(t.Context(), monmetrics.InternalMetricSpaceID, "default:"+id, id)
	require.NoError(t, err)
	require.Equal(t, domain.AlertStatusFiring, state.Status)
	results, err := repos.Results.Recent(t.Context(), monmetrics.InternalMetricSpaceID, id, 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Contains(t, results[0].RawError, "conflict_instance_id=old")

	failed := domain.GatewayObservation{HostID: "control", ReadError: "GetHostRoutes unavailable", HostEnabledAt: row.HostEnabledAt}
	require.NoError(t, repos.Gateways.Reconcile(t.Context(), []domain.GatewayObservation{failed}, now.Add(time.Second)))
	require.NoError(t, run(t.Context()))
	state, err = repos.Alerts.GetState(t.Context(), monmetrics.InternalMetricSpaceID, "default:"+id, id)
	require.NoError(t, err)
	require.Equal(t, domain.AlertStatusFiring, state.Status, "unknown gateway status cannot resolve an existing conflict")
	results, err = repos.Results.Recent(t.Context(), monmetrics.InternalMetricSpaceID, id, 10)
	require.NoError(t, err)
	require.Len(t, results, 1, "unknown status cannot synthesize a healthy probe")

	require.NoError(t, repos.Gateways.Reconcile(t.Context(), nil, now.Add(2*time.Second)))
	require.NoError(t, run(t.Context()))
	check, err := repos.Checks.Get(t.Context(), monmetrics.InternalMetricSpaceID, id)
	require.NoError(t, err)
	require.False(t, check.Enabled)
	_, err = repos.Alerts.GetRule(t.Context(), monmetrics.InternalMetricSpaceID, "default:"+id)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	row.StatusJSON = `{"instance_id":"replacement","previous_instance_id":"old"}`
	require.NoError(t, repos.Gateways.Reconcile(t.Context(), []domain.GatewayObservation{row}, now))
	require.NoError(t, run(t.Context()))
	check, err = repos.Checks.Get(t.Context(), monmetrics.InternalMetricSpaceID, id)
	require.NoError(t, err)
	require.True(t, check.Enabled, "re-enabled hosts must restore gateway checks")
	state, err = repos.Alerts.GetState(t.Context(), monmetrics.InternalMetricSpaceID, "default:"+id, id)
	require.NoError(t, err)
	require.NotEqual(t, domain.AlertStatusFiring, state.Status, "normal replacement is not a conflict")
}
