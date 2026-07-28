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

func TestRunDiagnoseMissingPipelineInputIsUnknown(t *testing.T) {
	pipeline := report.Pipeline{ID: "factor-calc", Module: "factor", LagTolerance: time.Minute, Enabled: true, WatermarkMonitoring: true}
	snapshot := &monitorpb.GetDoctorContextRsp{ManifestChecksum: embeddedManifestChecksum(t)}
	reportValue, err := RunDiagnose(context.Background(), DiagnoseOptions{CheckIDs: []string{"module.pipeline_lag:factor:factor-calc"}, Client: contextClientStub{rsp: snapshot}, Pipelines: report.PipelineConfig{Version: 1, Pipelines: []report.Pipeline{pipeline}}})
	require.NoError(t, err)
	require.Equal(t, core.StatusUnknown, reportValue.CheckByID("module.pipeline_lag:factor:factor-calc").Status)
}

func TestDiagnoseSpecsOnlyCreateChecksSupportedByBuiltInSignals(t *testing.T) {
	components := make([]*monitorpb.DoctorExpectedComponent, 0, len(report.BuiltInPipelines()))
	seenModules := map[string]bool{}
	for _, pipeline := range report.BuiltInPipelines() {
		if seenModules[pipeline.Module] {
			continue
		}
		seenModules[pipeline.Module] = true
		components = append(components, &monitorpb.DoctorExpectedComponent{
			ComponentId: "moox_" + pipeline.Module, NodeId: "node-a", Expected: true,
			Transport: "reporter", FunctionalObservability: "active",
		})
	}
	specs := diagnoseSpecs(
		&monitorpb.GetDoctorContextRsp{ExpectedComponents: components},
		report.PipelineConfig{Pipelines: report.BuiltInPipelines()},
	)
	ids := map[string]bool{}
	for _, spec := range specs {
		ids[spec.ID] = true
	}
	require.True(t, ids["module.freshness:moox_monitor@node-a"])
	require.False(t, ids["module.freshness:moox_collector@node-a"])
	require.False(t, ids["module.freshness:moox_factor@node-a"])
	for _, id := range []string{
		"module.pipeline_lag:monitor:monitor-metrics",
	} {
		require.True(t, ids[id], id)
	}
	for _, id := range []string{
		"module.pipeline_lag:archive:archive-materialize",
		"module.pipeline_lag:cloudnode:cloudnode-jobs",
		"module.pipeline_lag:collector:collector-market-data",
		"module.pipeline_lag:factor:factor-calculation",
		"module.pipeline_lag:strategy:strategy-targets",
		"module.pipeline_lag:trade:trade-rebalance",
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
		ExpectedComponents:   []*monitorpb.DoctorExpectedComponent{{ComponentId: "moox_factor", ServiceName: "moox-factor", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "active"}},
		ReporterObservations: []*monitorpb.DoctorObservation{{Kind: "reporter", ComponentId: "moox_factor", Status: "FRESH"}},
		MissingObservations:  []*monitorpb.DoctorObservation{{Kind: "identity", ComponentId: "moox_factor", Status: "CONFLICT", Conflict: true}},
	}
	reportValue, err := RunDiagnose(context.Background(), DiagnoseOptions{CheckIDs: []string{"monitor.reporter_coverage:moox_factor@node-a"}, Client: contextClientStub{rsp: snapshot}})
	require.NoError(t, err)
	require.Equal(t, core.StatusFail, reportValue.CheckByID("monitor.reporter_coverage:moox_factor@node-a").Status)
}

func TestRunDiagnoseSingleHealthFailureWarns(t *testing.T) {
	snapshot := &monitorpb.GetDoctorContextRsp{ManifestChecksum: embeddedManifestChecksum(t), ExpectedComponents: []*monitorpb.DoctorExpectedComponent{{ComponentId: "moox_factor", NodeId: "node-a", Expected: true}}, HealthObservations: []*monitorpb.DoctorObservation{{ComponentId: "moox_factor", Status: "DEGRADED"}}}
	reportValue, err := RunDiagnose(context.Background(), DiagnoseOptions{CheckIDs: []string{"service.health:moox_factor@node-a"}, Client: contextClientStub{rsp: snapshot}})
	require.NoError(t, err)
	require.Equal(t, core.StatusWarn, reportValue.CheckByID("service.health:moox_factor@node-a").Status)
}

func TestRunDiagnoseStalePipelineFactsNeverPass(t *testing.T) {
	pipeline := report.Pipeline{ID: "factor-calc", Module: "factor", LagTolerance: time.Minute, Enabled: true, WatermarkMonitoring: true}
	snapshot := &monitorpb.GetDoctorContextRsp{ManifestChecksum: embeddedManifestChecksum(t), ModuleObservations: []*monitorpb.DoctorObservation{{Summary: "moox_factor_input_watermark_timestamp_seconds", DetailsJson: `{"stage":"calculate","pipeline":"factor-calc"}`, Value: 100, Stale: true}}, Watermarks: []*monitorpb.DoctorWatermark{{Module: "factor", Pipeline: "factor-calc", Value: 100, Status: "STALE"}}}
	reportValue, err := RunDiagnose(context.Background(), DiagnoseOptions{CheckIDs: []string{"module.pipeline_lag:factor:factor-calc"}, Client: contextClientStub{rsp: snapshot}, Pipelines: report.PipelineConfig{Version: 1, Pipelines: []report.Pipeline{pipeline}}})
	require.NoError(t, err)
	require.Equal(t, core.StatusUnknown, reportValue.CheckByID("module.pipeline_lag:factor:factor-calc").Status)
}

func TestRunDiagnoseFreshScrapeDoesNotMaskStaleLastSuccess(t *testing.T) {
	now := time.Unix(10_000, 0).UTC()
	snapshot := &monitorpb.GetDoctorContextRsp{
		ManifestChecksum:     embeddedManifestChecksum(t),
		ExpectedComponents:   []*monitorpb.DoctorExpectedComponent{{ComponentId: "moox_factor", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "active"}},
		ReporterObservations: []*monitorpb.DoctorObservation{{Kind: "reporter", ComponentId: "moox_factor", Status: "FRESH", Stale: false, ObservedAt: now.Format(time.RFC3339Nano)}},
		ModuleObservations: []*monitorpb.DoctorObservation{
			{Kind: "module", ComponentId: "moox_factor", Summary: "moox_factor_last_success_timestamp_seconds", Status: "FRESH", Stale: false, ObservedAt: now.Format(time.RFC3339Nano), Value: float64(now.Add(-2 * time.Hour).Unix()), DetailsJson: `{"stage":"calculate","pipeline":"factor-calc"}`},
		},
	}
	pipelines := report.PipelineConfig{Pipelines: []report.Pipeline{{
		ID: "factor-calc", Module: "factor", Enabled: true, FreshnessMonitoring: true,
	}}}
	result, err := RunDiagnose(context.Background(), DiagnoseOptions{Now: func() time.Time { return now }, CheckIDs: []string{"module.freshness:moox_factor@node-a"}, Client: contextClientStub{rsp: snapshot}, Pipelines: pipelines})
	require.NoError(t, err)
	require.NotEqual(t, core.StatusPass, result.CheckByID("module.freshness:moox_factor@node-a").Status)
}

func TestRunDiagnosePipelineDoesNotTreatOldEqualWatermarksAsIdle(t *testing.T) {
	now := time.Unix(10_000, 0).UTC()
	pipeline := report.Pipeline{ID: "factor-calc", Module: "factor", LagTolerance: time.Minute, Enabled: true, WatermarkMonitoring: true}
	snapshot := &monitorpb.GetDoctorContextRsp{
		ManifestChecksum:   embeddedManifestChecksum(t),
		ModuleObservations: []*monitorpb.DoctorObservation{{Summary: "moox_factor_input_watermark_timestamp_seconds", DetailsJson: `{"stage":"calculate","pipeline":"factor-calc"}`, Value: float64(now.Add(-10 * time.Minute).Unix())}},
		Watermarks:         []*monitorpb.DoctorWatermark{{Module: "factor", Pipeline: "factor-calc", Value: float64(now.Add(-10 * time.Minute).Unix()), Status: "FRESH"}},
	}
	result, err := RunDiagnose(context.Background(), DiagnoseOptions{Now: func() time.Time { return now }, CheckIDs: []string{"module.pipeline_lag:factor:factor-calc"}, Client: contextClientStub{rsp: snapshot}, Pipelines: report.PipelineConfig{Version: 1, Pipelines: []report.Pipeline{pipeline}}})
	require.NoError(t, err)
	require.Equal(t, core.StatusFail, result.CheckByID("module.pipeline_lag:factor:factor-calc").Status)
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
	snapshot := &monitorpb.GetDoctorContextRsp{ManifestChecksum: embeddedManifestChecksum(t), ExpectedComponents: []*monitorpb.DoctorExpectedComponent{{ComponentId: "storage_primary", ServiceName: "storage-primary", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "deferred"}}}
	report, err := RunDiagnose(context.Background(), DiagnoseOptions{NodeID: "node-a", CheckIDs: []string{"module.freshness:storage_primary@node-a"}, Client: contextClientStub{rsp: snapshot}})
	require.NoError(t, err)
	check := report.CheckByID("module.freshness:storage_primary@node-a")
	require.NotNil(t, check)
	require.Equal(t, core.StatusSkipped, check.Status)
}

func TestRunDiagnoseDisabledModuleDoesNotFailPipeline(t *testing.T) {
	pipeline := report.Pipeline{ID: "trade-rebalance", Module: "trade", LagTolerance: time.Minute, Enabled: true, WatermarkMonitoring: true}
	snapshot := &monitorpb.GetDoctorContextRsp{
		ManifestChecksum:   embeddedManifestChecksum(t),
		ExpectedComponents: []*monitorpb.DoctorExpectedComponent{{ComponentId: "moox_trade", NodeId: "node-a", Expected: false}},
	}
	result, err := RunDiagnose(context.Background(), DiagnoseOptions{CheckIDs: []string{"module.pipeline_lag:trade:trade-rebalance"}, Client: contextClientStub{rsp: snapshot}, Pipelines: report.PipelineConfig{Version: 1, Pipelines: []report.Pipeline{pipeline}}})
	require.NoError(t, err)
	check := result.CheckByID("module.pipeline_lag:trade:trade-rebalance")
	require.NotNil(t, check)
	require.Equal(t, core.StatusSkipped, check.Status)
	require.Equal(t, core.ConclusionHealthy, result.Conclusion)
}
