package observability

import (
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestGatewaySignalsDistinguishReplacementConflictAndTwoMinuteGrace(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	mismatch := now.Add(-2 * time.Minute)
	base := domain.GatewayObservation{HostID: "control", ObservedAt: &now, HostEnabledAt: now.Add(-time.Hour), ExpectedHash: "expected", AppliedHash: "expected"}
	status := &adminpb.HostGatewayRuntimeStatus{InstanceId: "new", PreviousInstanceId: "old", ReplacedAt: now.Format(time.RFC3339), LastSeenAt: now.Format(time.RFC3339)}
	signals := gatewayObservationSignals(base, status, now)
	for _, signal := range signals {
		require.Equal(t, "healthy", signal.Status, "legitimate replacement must not alert")
	}
	status.ConflictInstanceId, status.ConflictSeenAt = "old", now.Format(time.RFC3339)
	require.Equal(t, "down", gatewayObservationSignals(base, status, now)[1].Status)
	status.ConflictInstanceId = ""
	base.AppliedHash, base.HashMismatchSince = "old", &mismatch
	routeSignal := gatewayObservationSignals(base, status, now)[2]
	require.Equal(t, mismatch, routeSignal.PendingSince)
	require.Equal(t, "expected", routeSignal.ExpectedHash)
	require.Equal(t, "old", routeSignal.AppliedHash)
	require.Equal(t, "unknown", gatewayObservationSignals(base, status, now)[2].Status, "exactly two minutes remains inside grace")
	require.Equal(t, "down", gatewayObservationSignals(base, status, now.Add(time.Nanosecond))[2].Status)
	status.LastSeenAt = mismatch.Format(time.RFC3339Nano)
	require.Equal(t, "healthy", gatewayObservationSignals(base, status, now)[0].Status)
	require.Equal(t, "down", gatewayObservationSignals(base, status, now.Add(time.Nanosecond))[0].Status)
	status.LastSeenAt = ""
	base.HostEnabledAt = now
	require.Equal(t, "unknown", gatewayObservationSignals(base, status, now)[0].Status)
	require.Equal(t, "down", gatewayObservationSignals(base, status, now.Add(2*time.Minute+time.Nanosecond))[0].Status)
}

func TestGatewaySignalsKeepUnknownStatusOnDiscoveryFailure(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-3 * time.Minute)
	row := domain.GatewayObservation{HostID: "control", ObservedAt: &old, ExpectedHash: "expected", AppliedHash: "expected", ReadError: "GetHostRoutes: connection refused"}
	status := &adminpb.HostGatewayRuntimeStatus{InstanceId: "known", LastSeenAt: old.Format(time.RFC3339Nano)}
	signals := gatewayObservationSignals(row, status, now)
	require.Equal(t, "down", signals[0].Status, "last verified heartbeat still proves silence")
	require.Equal(t, "unknown", signals[1].Status)
	require.Equal(t, "unknown", signals[2].Status)
	require.True(t, signals[2].PendingSince.IsZero(), "failed reads must not label an unverified snapshot with a pending timestamp")
	for _, signal := range signals {
		require.Contains(t, signal.RawError, "connection refused")
	}
	row.ObservedAt = nil
	row.HostEnabledAt = old
	for _, signal := range gatewayObservationSignals(row, &adminpb.HostGatewayRuntimeStatus{}, now) {
		require.Equal(t, "unknown", signal.Status, "a failed first read must not invent a missing heartbeat")
	}
}
