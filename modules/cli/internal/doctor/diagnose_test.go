package doctor

import (
	"context"
	"errors"
	"testing"
	"time"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/stretchr/testify/require"
)

type contextClientStub struct {
	rsp *monitorpb.GetDoctorContextRsp
	err error
}

func embeddedManifestChecksum(t *testing.T) string {
	t.Helper()
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	return manifest.Checksum
}

func TestRunDiagnoseMissingHealthCheckInputIsUnknown(t *testing.T) {
	healthCheck := report.ModuleHealthCheck{ID: "factor-calc", Module: "factor", MaxLag: time.Minute, Enabled: true, CheckWatermark: true}
	snapshot := &monitorpb.GetDoctorContextRsp{ManifestChecksum: embeddedManifestChecksum(t)}
	reportValue, err := RunDiagnose(context.Background(), DiagnoseOptions{CheckIDs: []string{"module.health_check:factor:factor-calc"}, Client: contextClientStub{rsp: snapshot}, HealthChecks: []report.ModuleHealthCheck{healthCheck}})
	require.NoError(t, err)
	require.Equal(t, core.StatusUnknown, reportValue.CheckByID("module.health_check:factor:factor-calc").Status)
}

func TestDiagnoseSpecsOnlyCreateChecksSupportedByBuiltInSignals(t *testing.T) {
	components := make([]*monitorpb.DoctorExpectedComponent, 0, len(report.BuiltInModuleHealthChecks()))
	seenModules := map[string]bool{}
	for _, healthCheck := range report.BuiltInModuleHealthChecks() {
		if seenModules[healthCheck.Module] {
			continue
		}
		seenModules[healthCheck.Module] = true
		componentID := healthCheck.Module
		if componentID == "factor" {
			componentID = "factor-mgr"
		}
		components = append(components, &monitorpb.DoctorExpectedComponent{
			ComponentId: componentID, NodeId: "node-a", Expected: true,
			Transport: "reporter", FunctionalObservability: "active",
		})
	}
	specs := diagnoseSpecs(
		&monitorpb.GetDoctorContextRsp{ExpectedComponents: components},
		report.BuiltInModuleHealthChecks(),
	)
	ids := map[string]bool{}
	for _, spec := range specs {
		ids[spec.ID] = true
	}
	require.True(t, ids["module.freshness:monitor@node-a"])
	require.False(t, ids["module.freshness:collector@node-a"])
	require.False(t, ids["module.freshness:factor-mgr@node-a"])
	for _, id := range []string{
		"module.health_check:monitor:monitor-metrics",
	} {
		require.True(t, ids[id], id)
	}
	for _, id := range []string{
		"module.health_check:archive:archive-materialize",
		"module.health_check:cloudnode:cloudnode-jobs",
		"module.health_check:collector:collector-market-data",
		"module.health_check:factor:factor-calculation",
		"module.health_check:strategy:strategy-targets",
		"module.health_check:trade:trade-rebalance",
	} {
		require.False(t, ids[id], id)
	}
}

func TestRunDiagnoseRejectsManifestMismatch(t *testing.T) {
	reportValue, err := RunDiagnose(context.Background(), DiagnoseOptions{Client: contextClientStub{rsp: &monitorpb.GetDoctorContextRsp{ManifestChecksum: "sha256:stale"}}})
	require.NoError(t, err)
	check := reportValue.CheckByID("diagnose.context")
	require.NotNil(t, check)
	require.Equal(t, core.StatusFail, check.Status)
	require.Equal(t, core.ConclusionUnhealthy, reportValue.Conclusion)
	manifest, manifestErr := core.LoadEmbeddedManifest()
	require.NoError(t, manifestErr)
	require.Equal(t, manifest.Checksum, reportValue.ManifestChecksum)
}

func TestRunDiagnoseReporterConflictFailsClosed(t *testing.T) {
	snapshot := &monitorpb.GetDoctorContextRsp{
		ManifestChecksum:     embeddedManifestChecksum(t),
		ExpectedComponents:   []*monitorpb.DoctorExpectedComponent{{ComponentId: "factor-mgr", ServiceName: "moox-factor", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "active"}},
		ReporterObservations: []*monitorpb.DoctorObservation{{Kind: "reporter", ComponentId: "factor-mgr", Status: "FRESH"}},
		MissingObservations:  []*monitorpb.DoctorObservation{{Kind: "identity", ComponentId: "factor-mgr", Status: "CONFLICT", Conflict: true}},
	}
	reportValue, err := RunDiagnose(context.Background(), DiagnoseOptions{CheckIDs: []string{"monitor.reporter_coverage:factor-mgr@node-a"}, Client: contextClientStub{rsp: snapshot}})
	require.NoError(t, err)
	require.Equal(t, core.StatusFail, reportValue.CheckByID("monitor.reporter_coverage:factor-mgr@node-a").Status)
}

func TestRunDiagnoseSingleHealthFailureWarns(t *testing.T) {
	snapshot := &monitorpb.GetDoctorContextRsp{ManifestChecksum: embeddedManifestChecksum(t), ExpectedComponents: []*monitorpb.DoctorExpectedComponent{{ComponentId: "factor-mgr", NodeId: "node-a", Expected: true}}, HealthObservations: []*monitorpb.DoctorObservation{{ComponentId: "factor-mgr", Status: "DEGRADED"}}}
	reportValue, err := RunDiagnose(context.Background(), DiagnoseOptions{CheckIDs: []string{"service.health:factor-mgr@node-a"}, Client: contextClientStub{rsp: snapshot}})
	require.NoError(t, err)
	require.Equal(t, core.StatusWarn, reportValue.CheckByID("service.health:factor-mgr@node-a").Status)
}

func TestRunDiagnoseStaleHealthCheckFactsNeverPass(t *testing.T) {
	healthCheck := report.ModuleHealthCheck{ID: "factor-calc", Module: "factor", MaxLag: time.Minute, Enabled: true, CheckWatermark: true}
	snapshot := &monitorpb.GetDoctorContextRsp{ManifestChecksum: embeddedManifestChecksum(t), ModuleObservations: []*monitorpb.DoctorObservation{{Summary: "moox_factor_input_watermark_timestamp_seconds", DetailsJson: `{"stage":"calculate","health_check":"factor-calc"}`, Value: 100, Stale: true}}, Watermarks: []*monitorpb.DoctorWatermark{{Module: "factor", HealthCheckId: "factor-calc", Value: 100, Status: "STALE"}}}
	reportValue, err := RunDiagnose(context.Background(), DiagnoseOptions{CheckIDs: []string{"module.health_check:factor:factor-calc"}, Client: contextClientStub{rsp: snapshot}, HealthChecks: []report.ModuleHealthCheck{healthCheck}})
	require.NoError(t, err)
	require.Equal(t, core.StatusUnknown, reportValue.CheckByID("module.health_check:factor:factor-calc").Status)
}

func TestRunDiagnoseFreshScrapeDoesNotMaskStaleLastSuccess(t *testing.T) {
	now := time.Unix(10_000, 0).UTC()
	snapshot := &monitorpb.GetDoctorContextRsp{
		ManifestChecksum:     embeddedManifestChecksum(t),
		ExpectedComponents:   []*monitorpb.DoctorExpectedComponent{{ComponentId: "factor-mgr", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "active"}},
		ReporterObservations: []*monitorpb.DoctorObservation{{Kind: "reporter", ComponentId: "factor-mgr", Status: "FRESH", Stale: false, ObservedAt: now.Format(time.RFC3339Nano)}},
		ModuleObservations: []*monitorpb.DoctorObservation{
			{Kind: "module", ComponentId: "factor-mgr", Summary: "moox_factor_last_success_timestamp_seconds", Status: "FRESH", Stale: false, ObservedAt: now.Format(time.RFC3339Nano), Value: float64(now.Add(-2 * time.Hour).Unix()), DetailsJson: `{"stage":"calculate","health_check":"factor-calc"}`},
		},
	}
	healthChecks := []report.ModuleHealthCheck{{
		ID: "factor-calc", Module: "factor", Enabled: true, CheckFreshness: true,
	}}
	result, err := RunDiagnose(context.Background(), DiagnoseOptions{Now: func() time.Time { return now }, CheckIDs: []string{"module.freshness:factor-mgr@node-a"}, Client: contextClientStub{rsp: snapshot}, HealthChecks: healthChecks})
	require.NoError(t, err)
	require.NotEqual(t, core.StatusPass, result.CheckByID("module.freshness:factor-mgr@node-a").Status)
}

func TestRunDiagnoseHealthCheckDoesNotTreatOldEqualWatermarksAsIdle(t *testing.T) {
	now := time.Unix(10_000, 0).UTC()
	healthCheck := report.ModuleHealthCheck{ID: "factor-calc", Module: "factor", MaxLag: time.Minute, Enabled: true, CheckWatermark: true}
	snapshot := &monitorpb.GetDoctorContextRsp{
		ManifestChecksum:   embeddedManifestChecksum(t),
		ModuleObservations: []*monitorpb.DoctorObservation{{Summary: "moox_factor_input_watermark_timestamp_seconds", DetailsJson: `{"stage":"calculate","health_check":"factor-calc"}`, Value: float64(now.Add(-10 * time.Minute).Unix())}},
		Watermarks:         []*monitorpb.DoctorWatermark{{Module: "factor", HealthCheckId: "factor-calc", Value: float64(now.Add(-10 * time.Minute).Unix()), Status: "FRESH"}},
	}
	result, err := RunDiagnose(context.Background(), DiagnoseOptions{Now: func() time.Time { return now }, CheckIDs: []string{"module.health_check:factor:factor-calc"}, Client: contextClientStub{rsp: snapshot}, HealthChecks: []report.ModuleHealthCheck{healthCheck}})
	require.NoError(t, err)
	require.Equal(t, core.StatusFail, result.CheckByID("module.health_check:factor:factor-calc").Status)
}

func (s contextClientStub) GetDoctorContext(context.Context, *monitorpb.GetDoctorContextReq) (*monitorpb.GetDoctorContextRsp, error) {
	return s.rsp, s.err
}

func TestRunDiagnoseDoesNotFallbackWhenMonitorUnavailable(t *testing.T) {
	report, err := RunDiagnose(context.Background(), DiagnoseOptions{NodeID: "node-a", Client: contextClientStub{err: errors.New("offline")}})
	require.NoError(t, err)
	require.Equal(t, core.ConclusionInconclusive, report.Conclusion)
	require.Equal(t, []string{"run_bootstrap"}, report.Checks[0].RecoveryActionIDs)
}

func TestRunDiagnoseStorageChecksAreDeferred(t *testing.T) {
	snapshot := &monitorpb.GetDoctorContextRsp{ManifestChecksum: embeddedManifestChecksum(t), ExpectedComponents: []*monitorpb.DoctorExpectedComponent{{ComponentId: "storage-primary", ServiceName: "storage-primary", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "deferred"}}}
	report, err := RunDiagnose(context.Background(), DiagnoseOptions{NodeID: "node-a", CheckIDs: []string{"module.freshness:storage-primary@node-a"}, Client: contextClientStub{rsp: snapshot}})
	require.NoError(t, err)
	check := report.CheckByID("module.freshness:storage-primary@node-a")
	require.NotNil(t, check)
	require.Equal(t, core.StatusSkipped, check.Status)
}

func TestRunDiagnoseDisabledModuleDoesNotFailHealthCheck(t *testing.T) {
	healthCheck := report.ModuleHealthCheck{ID: "trade-rebalance", Module: "trade", MaxLag: time.Minute, Enabled: true, CheckWatermark: true}
	snapshot := &monitorpb.GetDoctorContextRsp{
		ManifestChecksum:   embeddedManifestChecksum(t),
		ExpectedComponents: []*monitorpb.DoctorExpectedComponent{{ComponentId: "trade", NodeId: "node-a", Expected: false}},
	}
	result, err := RunDiagnose(context.Background(), DiagnoseOptions{CheckIDs: []string{"module.health_check:trade:trade-rebalance"}, Client: contextClientStub{rsp: snapshot}, HealthChecks: []report.ModuleHealthCheck{healthCheck}})
	require.NoError(t, err)
	check := result.CheckByID("module.health_check:trade:trade-rebalance")
	require.NotNil(t, check)
	require.Equal(t, core.StatusSkipped, check.Status)
	require.Equal(t, core.ConclusionHealthy, result.Conclusion)
}
