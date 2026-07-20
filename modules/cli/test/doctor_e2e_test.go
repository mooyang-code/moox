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

func (c e2eContextClient) GetDoctorContext(context.Context, *monitorpb.GetDoctorContextReq) (*monitorpb.GetDoctorContextRsp, error) {
	return c.rsp, nil
}

func TestDoctorDiagnoseDistinguishesBusinessHealthFromReporterFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("moox_module_runs_total 1\n"))
	}))
	defer server.Close()
	components := []*monitorpb.DoctorExpectedComponent{
		{ComponentId: "eventbus", ServiceName: "eventbus", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "active", HealthUrl: server.URL + "/readyz"},
		{ComponentId: "moox_monitor", ServiceName: "moox_monitor", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "active", HealthUrl: server.URL + "/readyz"},
		{ComponentId: "moox_factor", ServiceName: "moox_factor", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "active", HealthUrl: server.URL + "/readyz"},
	}
	health := []*monitorpb.DoctorObservation{}
	reporters := []*monitorpb.DoctorObservation{}
	for _, component := range components {
		health = append(health, &monitorpb.DoctorObservation{Kind: "health", ComponentId: component.GetComponentId(), Status: "OK"})
		if component.GetComponentId() != "moox_factor" {
			reporters = append(reporters, &monitorpb.DoctorObservation{Kind: "reporter", ComponentId: component.GetComponentId(), Status: "FRESH"})
		}
	}
	snapshot := &monitorpb.GetDoctorContextRsp{ManifestChecksum: "sha256:test", ExpectedComponents: components, HealthObservations: health, ReporterObservations: reporters, MissingObservations: []*monitorpb.DoctorObservation{{Kind: "reporter", ComponentId: "moox_factor", Status: "MISSING"}}}
	report, err := doctorcli.RunDiagnose(context.Background(), doctorcli.DiagnoseOptions{
		NodeID: "node-a", CheckIDs: []string{"monitor.reporter_coverage:moox_factor@node-a"}, Client: e2eContextClient{rsp: snapshot},
		Prober: doctorcli.HTTPProber{Auth: doctorcli.HealthAuth{AccessKey: "monitor", SecretKey: "secret"}},
	})
	require.NoError(t, err)
	require.Equal(t, core.ConclusionDegraded, report.Conclusion)
	require.Equal(t, core.StatusPass, report.CheckByID("service.health:moox_factor@node-a").Status)
	require.Equal(t, core.StatusWarn, report.CheckByID("monitor.reporter_coverage:moox_factor@node-a").Status)
	raw, err := doctorcli.Render(report, "json")
	require.NoError(t, err)
	require.Contains(t, string(raw), `"schema_version": "doctor.moox.dev/v1"`)
}

func TestDoctorDiagnoseFailsClosedOnIdentityConflict(t *testing.T) {
	snapshot := &monitorpb.GetDoctorContextRsp{ManifestChecksum: "sha256:test", ExpectedComponents: []*monitorpb.DoctorExpectedComponent{
		{ComponentId: "eventbus", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "active"},
		{ComponentId: "moox_monitor", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "active"},
		{ComponentId: "moox_factor", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "active"},
	}, HealthObservations: []*monitorpb.DoctorObservation{
		{ComponentId: "eventbus", Status: "OK"}, {ComponentId: "moox_monitor", Status: "OK"}, {ComponentId: "moox_factor", Status: "OK"},
	}, ReporterObservations: []*monitorpb.DoctorObservation{
		{ComponentId: "eventbus", Status: "FRESH"}, {ComponentId: "moox_monitor", Status: "FRESH"}, {ComponentId: "moox_factor", Status: "CONFLICT", Conflict: true},
	}}
	report, err := doctorcli.RunDiagnose(context.Background(), doctorcli.DiagnoseOptions{NodeID: "node-a", CheckIDs: []string{"monitor.reporter_coverage:moox_factor@node-a"}, Client: e2eContextClient{rsp: snapshot}})
	require.NoError(t, err)
	require.Equal(t, core.ConclusionUnhealthy, report.Conclusion)
	require.Equal(t, core.StatusFail, report.CheckByID("monitor.reporter_coverage:moox_factor@node-a").Status)
}

func TestDoctorDiagnoseEscalatesReporterThatNeverAppeared(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("moox_module_runs_total 1\n"))
	}))
	defer server.Close()
	snapshot := &monitorpb.GetDoctorContextRsp{
		ManifestChecksum:    "sha256:test",
		ExpectedComponents:  []*monitorpb.DoctorExpectedComponent{{ComponentId: "moox_factor", ServiceName: "moox_factor", NodeId: "node-a", Expected: true, Transport: "reporter", FunctionalObservability: "active", HealthUrl: server.URL + "/readyz"}},
		MissingObservations: []*monitorpb.DoctorObservation{{Kind: "reporter", ComponentId: "moox_factor", Status: "FAIL", Stale: true, AgeSeconds: 121, IntervalSeconds: 30}},
	}
	report, err := doctorcli.RunDiagnose(context.Background(), doctorcli.DiagnoseOptions{NodeID: "node-a", CheckIDs: []string{"monitor.reporter_coverage:moox_factor@node-a"}, Client: e2eContextClient{rsp: snapshot}, Prober: doctorcli.HTTPProber{Auth: doctorcli.HealthAuth{AccessKey: "monitor", SecretKey: "secret"}}})
	require.NoError(t, err)
	require.Equal(t, core.StatusFail, report.CheckByID("monitor.reporter_coverage:moox_factor@node-a").Status)
}
