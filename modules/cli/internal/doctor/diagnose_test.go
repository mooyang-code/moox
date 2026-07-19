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

func TestRunDiagnoseMissingPipelineInputIsUnknown(t *testing.T) {
	pipeline := report.Pipeline{ID: "factor-calc", Module: "factor", LagTolerance: time.Minute, Enabled: true}
	snapshot := &monitorpb.GetDoctorContextRsp{ManifestChecksum: "sha256:test"}
	reportValue, err := RunDiagnose(context.Background(), DiagnoseOptions{CheckIDs: []string{"module.pipeline_lag:factor:factor-calc"}, Client: contextClientStub{rsp: snapshot}, Pipelines: report.PipelineConfig{Version: 1, Pipelines: []report.Pipeline{pipeline}}})
	require.NoError(t, err)
	require.Equal(t, core.StatusUnknown, reportValue.CheckByID("module.pipeline_lag:factor:factor-calc").Status)
}

func TestRunDiagnoseReporterConflictFailsClosed(t *testing.T) {
	snapshot := &monitorpb.GetDoctorContextRsp{
		ManifestChecksum:     "sha256:test",
		ExpectedComponents:   []*monitorpb.DoctorExpectedComponent{{ComponentId: "moox_factor", ServiceName: "moox-factor", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "active"}},
		ReporterObservations: []*monitorpb.DoctorObservation{{Kind: "reporter", ComponentId: "moox_factor", Status: "FRESH"}},
		MissingObservations:  []*monitorpb.DoctorObservation{{Kind: "identity", ComponentId: "moox_factor", Status: "CONFLICT", Conflict: true}},
	}
	reportValue, err := RunDiagnose(context.Background(), DiagnoseOptions{CheckIDs: []string{"monitor.reporter_coverage:moox_factor@node-a"}, Client: contextClientStub{rsp: snapshot}})
	require.NoError(t, err)
	require.Equal(t, core.StatusFail, reportValue.CheckByID("monitor.reporter_coverage:moox_factor@node-a").Status)
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
	snapshot := &monitorpb.GetDoctorContextRsp{ManifestChecksum: "sha256:test", ExpectedComponents: []*monitorpb.DoctorExpectedComponent{{ComponentId: "storage_primary", ServiceName: "storage-primary", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "deferred"}}}
	report, err := RunDiagnose(context.Background(), DiagnoseOptions{NodeID: "node-a", CheckIDs: []string{"module.freshness:storage_primary@node-a"}, Client: contextClientStub{rsp: snapshot}})
	require.NoError(t, err)
	check := report.CheckByID("module.freshness:storage_primary@node-a")
	require.NotNil(t, check)
	require.Equal(t, core.StatusSkipped, check.Status)
}
