package test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	doctorcli "github.com/mooyang-code/moox/modules/cli/internal/doctor"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/stretchr/testify/require"
)

type e2eContextClient struct {
	rsp *monitorpb.GetDoctorContextRsp
}

func embeddedManifestChecksum(t *testing.T) string {
	t.Helper()
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	return manifest.Checksum
}

func (c e2eContextClient) GetDoctorContext(context.Context, *monitorpb.GetDoctorContextReq) (*monitorpb.GetDoctorContextRsp, error) {
	return c.rsp, nil
}

func TestDoctorDiagnoseDistinguishesBusinessHealthFromReporterFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("moox_factor_runs_total 1\n"))
	}))
	defer server.Close()
	components := []*monitorpb.DoctorExpectedComponent{
		{ComponentId: "eventbus", ServiceName: "eventbus", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "active", HealthUrl: server.URL + "/readyz"},
		{ComponentId: "monitor", ServiceName: "monitor", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "active", HealthUrl: server.URL + "/readyz"},
		{ComponentId: "factor-mgr", ServiceName: "factor-mgr", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "active", HealthUrl: server.URL + "/readyz"},
	}
	health := []*monitorpb.DoctorObservation{}
	reporters := []*monitorpb.DoctorObservation{}
	for _, component := range components {
		health = append(health, &monitorpb.DoctorObservation{Kind: "health", ComponentId: component.GetComponentId(), Status: "OK"})
		if component.GetComponentId() != "factor-mgr" {
			reporters = append(reporters, &monitorpb.DoctorObservation{Kind: "reporter", ComponentId: component.GetComponentId(), Status: "FRESH"})
		}
	}
	snapshot := &monitorpb.GetDoctorContextRsp{ManifestChecksum: embeddedManifestChecksum(t), ExpectedComponents: components, HealthObservations: health, ReporterObservations: reporters, MissingObservations: []*monitorpb.DoctorObservation{{Kind: "reporter", ComponentId: "factor-mgr", Status: "MISSING"}}}
	report, err := doctorcli.RunDiagnose(context.Background(), doctorcli.DiagnoseOptions{
		NodeID: "node-a", CheckIDs: []string{"monitor.reporter_coverage:factor-mgr@node-a"}, Client: e2eContextClient{rsp: snapshot},
		Prober: doctorcli.HTTPProber{Auth: doctorcli.HealthAuth{AccessKey: "monitor", SecretKey: "secret"}},
	})
	require.NoError(t, err)
	require.Equal(t, core.ConclusionDegraded, report.Conclusion)
	require.Equal(t, core.StatusPass, report.CheckByID("service.health:factor-mgr@node-a").Status)
	require.Equal(t, core.StatusWarn, report.CheckByID("monitor.reporter_coverage:factor-mgr@node-a").Status)
	raw, err := doctorcli.Render(report, "json")
	require.NoError(t, err)
	require.Contains(t, string(raw), `"schema_version": "doctor.moox.dev/v1"`)
}

func TestDoctorDiagnoseFailsClosedOnIdentityConflict(t *testing.T) {
	snapshot := &monitorpb.GetDoctorContextRsp{ManifestChecksum: embeddedManifestChecksum(t), ExpectedComponents: []*monitorpb.DoctorExpectedComponent{
		{ComponentId: "eventbus", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "active"},
		{ComponentId: "monitor", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "active"},
		{ComponentId: "factor-mgr", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "active"},
	}, HealthObservations: []*monitorpb.DoctorObservation{
		{ComponentId: "eventbus", Status: "OK"}, {ComponentId: "monitor", Status: "OK"}, {ComponentId: "factor-mgr", Status: "OK"},
	}, ReporterObservations: []*monitorpb.DoctorObservation{
		{ComponentId: "eventbus", Status: "FRESH"}, {ComponentId: "monitor", Status: "FRESH"}, {ComponentId: "factor-mgr", Status: "CONFLICT", Conflict: true},
	}}
	report, err := doctorcli.RunDiagnose(context.Background(), doctorcli.DiagnoseOptions{NodeID: "node-a", CheckIDs: []string{"monitor.reporter_coverage:factor-mgr@node-a"}, Client: e2eContextClient{rsp: snapshot}})
	require.NoError(t, err)
	require.Equal(t, core.ConclusionUnhealthy, report.Conclusion)
	require.Equal(t, core.StatusFail, report.CheckByID("monitor.reporter_coverage:factor-mgr@node-a").Status)
}

func TestDoctorDiagnoseEscalatesReporterThatNeverAppeared(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("moox_factor_runs_total 1\n"))
	}))
	defer server.Close()
	snapshot := &monitorpb.GetDoctorContextRsp{
		ManifestChecksum:    embeddedManifestChecksum(t),
		ExpectedComponents:  []*monitorpb.DoctorExpectedComponent{{ComponentId: "factor-mgr", ServiceName: "factor-mgr", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "active", HealthUrl: server.URL + "/readyz"}},
		MissingObservations: []*monitorpb.DoctorObservation{{Kind: "reporter", ComponentId: "factor-mgr", Status: "FAIL", Stale: true, AgeSeconds: 121, IntervalSeconds: 30}},
	}
	report, err := doctorcli.RunDiagnose(context.Background(), doctorcli.DiagnoseOptions{NodeID: "node-a", CheckIDs: []string{"monitor.reporter_coverage:factor-mgr@node-a"}, Client: e2eContextClient{rsp: snapshot}, Prober: doctorcli.HTTPProber{Auth: doctorcli.HealthAuth{AccessKey: "monitor", SecretKey: "secret"}}})
	require.NoError(t, err)
	require.Equal(t, core.StatusFail, report.CheckByID("monitor.reporter_coverage:factor-mgr@node-a").Status)
}
