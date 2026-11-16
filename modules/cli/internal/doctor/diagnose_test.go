package doctor

import (
	"context"
	"errors"
	"fmt"
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
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	specs := diagnoseSpecs(
		manifest,
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

// 上报缺失或过期时经 SSH 在目标机上请求本机 /readyz：就绪只记警告（Monitor 仍按停止上报算），端口不通或未就绪算失败，
// SSH 连不上记为无法判断。
func TestRunDiagnoseProbesMissingHealthReportOverSSH(t *testing.T) {
	snapshot := &monitorpb.GetDoctorContextRsp{ManifestChecksum: embeddedManifestChecksum(t), ExpectedComponents: []*monitorpb.DoctorExpectedComponent{{ComponentId: "factor-mgr", NodeId: "node-a", Expected: true}}}
	id := "service.health:factor-mgr@node-a"
	var asked []string
	run := func(probe HealthProbe) *core.CheckResult {
		t.Helper()
		reportValue, err := RunDiagnose(context.Background(), DiagnoseOptions{CheckIDs: []string{id}, Client: contextClientStub{rsp: snapshot}, Probe: probe})
		require.NoError(t, err)
		return reportValue.CheckByID(id)
	}
	probe := func(result error) HealthProbe {
		return func(_ context.Context, node string, port int, path string) (ProbeResult, error) {
			asked = append(asked, fmt.Sprintf("%s:%d%s", node, port, path))
			return ProbeResult{StatusCode: 200}, result
		}
	}
	ready := run(probe(nil))
	require.Equal(t, core.StatusWarn, ready.Status)
	require.Equal(t, "进程就绪，但给 Monitor 的上报缺失或过期", ready.Summary)
	require.Equal(t, []string{"node-a:11414/readyz"}, asked, "请求目标机上 factor-mgr 的本机健康端口")
	require.Equal(t, core.StatusFail, run(probe(errors.New("probe returned HTTP 503"))).Status)
	unreachable := run(probe(fmt.Errorf("%w node-a: dial timeout", ErrNodeUnreachable)))
	require.Equal(t, core.StatusUnknown, unreachable.Status)
	require.Contains(t, unreachable.Error, "dial timeout")
	require.Equal(t, core.StatusUnknown, run(nil).Status)
}

// host-agent（主机快照）、因子引擎与归档 worker（管理端代报）也有健康观察项：新鲜时通过，过期时不通过。
func TestRunDiagnoseHealthCoversHostSnapshotAndDelegatedComponents(t *testing.T) {
	for _, stale := range []bool{false, true} {
		snapshot := &monitorpb.GetDoctorContextRsp{ManifestChecksum: embeddedManifestChecksum(t)}
		var ids []string
		for _, id := range []string{"host-agent", "factor-engine", "archive-worker"} {
			snapshot.ExpectedComponents = append(snapshot.ExpectedComponents, &monitorpb.DoctorExpectedComponent{ComponentId: id, NodeId: "factor", Expected: true})
			snapshot.HealthObservations = append(snapshot.HealthObservations, &monitorpb.DoctorObservation{Kind: "health", ComponentId: id, NodeId: "factor", Status: "OK", Stale: stale})
			ids = append(ids, "service.health:"+id+"@factor")
		}
		reportValue, err := RunDiagnose(context.Background(), DiagnoseOptions{NodeID: "factor", CheckIDs: ids, Client: contextClientStub{rsp: snapshot}})
		require.NoError(t, err)
		for _, id := range ids {
			if stale {
				require.NotEqual(t, core.StatusPass, reportValue.CheckByID(id).Status, id)
			} else {
				require.Equal(t, core.StatusPass, reportValue.CheckByID(id).Status, id)
			}
		}
	}
}

// 上报过期、SSH 探测本机 /readyz 就绪时，整体结论不能是健康：host-agent（主机快照）、因子引擎与归档 worker（管理端代报）
// 没有 reporter_coverage 检查，这一项是它们上报中断时唯一的信号；走 Reporter 的组件同样不能判通过。上报新鲜且就绪才是健康。
func TestRunDiagnoseStaleReportWithReadyProbeIsNotHealthy(t *testing.T) {
	components := []struct{ id, node string }{{"host-agent", "factor"}, {"factor-engine", "factor"}, {"archive-worker", "factor"}, {"factor-mgr", "control"}}
	for _, stale := range []bool{true, false} {
		for _, component := range components {
			snapshot := &monitorpb.GetDoctorContextRsp{ManifestChecksum: embeddedManifestChecksum(t),
				ExpectedComponents: []*monitorpb.DoctorExpectedComponent{{ComponentId: component.id, NodeId: component.node, Expected: true}},
				HealthObservations: []*monitorpb.DoctorObservation{{Kind: "health", ComponentId: component.id, NodeId: component.node, Status: "OK", Stale: stale}},
			}
			id := "service.health:" + component.id + "@" + component.node
			probes := 0
			ready := func(context.Context, string, int, string) (ProbeResult, error) {
				probes++
				return ProbeResult{StatusCode: 200}, nil
			}
			reportValue, err := RunDiagnose(context.Background(), DiagnoseOptions{NodeID: component.node, CheckIDs: []string{id}, Client: contextClientStub{rsp: snapshot}, Probe: ready})
			require.NoError(t, err)
			check := reportValue.CheckByID(id)
			if stale {
				require.Equal(t, core.StatusWarn, check.Status, component.id)
				require.Equal(t, 1, probes, "上报过期时经 SSH 探测：%s", component.id)
				require.Equal(t, core.ConclusionDegraded, reportValue.Conclusion, component.id)
			} else {
				require.Equal(t, core.StatusPass, check.Status, component.id)
				require.Zero(t, probes, "上报新鲜时不探测：%s", component.id)
				require.Equal(t, core.ConclusionHealthy, reportValue.Conclusion, component.id)
			}
		}
	}
}
