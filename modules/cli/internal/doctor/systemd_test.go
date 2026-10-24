package doctor

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixedRunner(output string, err error) CommandRunner {
	return func(context.Context, string, []string) (string, error) { return output, err }
}

func TestSystemdCheckerRequiresEnabledActiveAndLinger(t *testing.T) {
	for name, tc := range map[string]struct {
		output string
		want   SystemdState
		ok     bool
	}{
		"all good":            {"enabled=enabled\nactive=active\nlinger=yes\n", SystemdState{true, true, true}, true},
		"active but disabled": {"enabled=disabled\nactive=active\nlinger=yes\n", SystemdState{false, true, true}, false},
		"enabled but stopped": {"enabled=enabled\nactive=inactive\nlinger=yes\n", SystemdState{true, false, true}, false},
		"linger off":          {"enabled=enabled\nactive=active\nlinger=no\n", SystemdState{true, true, false}, false},
		"missing unit":        {"enabled=\nactive=inactive\nlinger=yes\n", SystemdState{false, false, true}, false},
	} {
		t.Run(name, func(t *testing.T) {
			state, err := NewSystemdChecker(fixedRunner(tc.output, nil))(t.Context(), "factor", "moox-factor-engine")
			require.NoError(t, err)
			assert.Equal(t, tc.want, state)
			assert.Equal(t, tc.ok, state.OK())
		})
	}
	_, err := NewSystemdChecker(fixedRunner("garbage", nil))(t.Context(), "factor", "moox-factor-engine")
	require.Error(t, err)
	_, err = NewSystemdChecker(fixedRunner("", errors.New("ssh down")))(t.Context(), "factor", "moox-factor-engine")
	require.Error(t, err)
}

func TestSupervisorResultMapsStates(t *testing.T) {
	pass := supervisorResult("service.supervisor:x@y", "moox-factor-engine", SystemdState{true, true, true}, nil)
	assert.Equal(t, core.StatusPass, pass.Status)
	fail := supervisorResult("service.supervisor:x@y", "moox-factor-engine", SystemdState{false, true, true}, nil)
	assert.Equal(t, core.StatusFail, fail.Status, "active 但没有开机自启不通过")
	assert.Contains(t, fail.Summary, "enabled=false active=true linger=true")
	unknown := supervisorResult("service.supervisor:x@y", "moox-factor-engine", SystemdState{}, errors.New("ssh down"))
	assert.Equal(t, core.StatusUnknown, unknown.Status)
}

func supervisorSnapshot(t *testing.T, node string) *monitorpb.GetDoctorContextRsp {
	t.Helper()
	return &monitorpb.GetDoctorContextRsp{
		ManifestChecksum: embeddedManifestChecksum(t),
		ExpectedComponents: []*monitorpb.DoctorExpectedComponent{
			{ComponentId: "factor-engine", NodeId: node, Expected: true, Transport: "health_only", FunctionalObservability: "deferred", HealthUrl: "http://192.168.0.103:11417"},
		},
	}
}

func TestDiagnoseChecksTheSystemdSupervisorOfExternalPrograms(t *testing.T) {
	snapshot := supervisorSnapshot(t, "factor")
	var asked []string
	options := DiagnoseOptions{
		NodeID: "factor", Client: contextClientStub{rsp: snapshot}, CheckIDs: []string{"service.supervisor:factor-engine@factor"},
		Systemd: func(_ context.Context, node, unit string) (SystemdState, error) {
			asked = append(asked, node+"/"+unit)
			return SystemdState{Enabled: false, Active: true, Linger: true}, nil
		},
	}
	result, err := RunDiagnose(t.Context(), options)
	require.NoError(t, err)
	check := result.CheckByID("service.supervisor:factor-engine@factor")
	require.NotNil(t, check)
	assert.Equal(t, core.StatusFail, check.Status, "active 但 disabled 不通过")
	assert.Equal(t, []string{"factor/moox-factor-engine"}, asked)

	options.Systemd = nil
	result, err = RunDiagnose(t.Context(), options)
	require.NoError(t, err)
	assert.Equal(t, core.StatusUnknown, result.CheckByID("service.supervisor:factor-engine@factor").Status)

	// 普通组件不产生托管检查。
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	_, systemd := systemdUnit(manifest, "access")
	assert.False(t, systemd)
}

func TestDiagnoseProbesOutboundHostHealthThroughTheRelay(t *testing.T) {
	snapshot := supervisorSnapshot(t, "factor")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" || r.Header.Get("X-Moox-Health-Auth") == "" {
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"ready":true}`))
	}))
	defer server.Close()
	snapshot.ExpectedComponents[0].HealthUrl = "http://192.168.0.103:11417/readyz"
	var dialed []string
	relay := &RelayProber{
		Auth:     HealthAuth{AccessKey: "access", SecretKey: strings.Repeat("s", 32)},
		Outbound: func(node string) bool { return node == "factor" },
		Dial: func(ctx context.Context, hostID string, port int) (net.Conn, error) {
			dialed = append(dialed, hostID)
			return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
		},
	}
	options := DiagnoseOptions{NodeID: "factor", Client: contextClientStub{rsp: snapshot}, CheckIDs: []string{"service.health:factor-engine@factor"}, Relay: relay}
	result, err := RunDiagnose(t.Context(), options)
	require.NoError(t, err)
	assert.Equal(t, core.StatusPass, result.CheckByID("service.health:factor-engine@factor").Status)
	assert.Equal(t, []string{"factor"}, dialed, "只能出站的主机经中转拨号")

	// 非出站节点不走中转：直连探测被默认策略拒绝（只允许本机）。
	relay.Outbound = func(string) bool { return false }
	result, err = RunDiagnose(t.Context(), options)
	require.NoError(t, err)
	assert.Equal(t, core.StatusFail, result.CheckByID("service.health:factor-engine@factor").Status)
	assert.Len(t, dialed, 1)
}
