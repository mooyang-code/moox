package doctor

import (
	"context"
	"errors"
	"testing"

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
	pass := supervisorResult("bootstrap.service_autostart:x@y", "moox-factor-engine", SystemdState{true, true, true}, nil)
	assert.Equal(t, core.StatusPass, pass.Status)
	fail := supervisorResult("bootstrap.service_autostart:x@y", "moox-factor-engine", SystemdState{false, true, true}, nil)
	assert.Equal(t, core.StatusFail, fail.Status, "active 但没有开机自启不通过")
	assert.Contains(t, fail.Summary, "enabled=false active=true linger=true")
	unknown := supervisorResult("bootstrap.service_autostart:x@y", "moox-factor-engine", SystemdState{}, errors.New("ssh down"))
	assert.Equal(t, core.StatusUnknown, unknown.Status)
}

func TestBootstrapChecksTheSystemdSupervisorOfEveryComponent(t *testing.T) {
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	root := t.TempDir()
	writeVersion(t, root, manifest.Checksum)
	var asked []string
	options := BootstrapOptions{
		NodeID:      "factor",
		LocalNodeID: "factor",
		DeployRoot:  root,
		Client: deploymentClientStub{rows: []Deployment{
			{ComponentID: "host-gateway", HostID: "factor", Status: "enabled"},
			{ComponentID: "host-agent", HostID: "factor", Status: "enabled"},
			{ComponentID: "factor-engine", HostID: "factor", Status: "enabled"},
		}},
		CheckIDs: []string{"bootstrap.service_autostart:factor-engine@factor"},
		Systemd: func(_ context.Context, node, unit string) (SystemdState, error) {
			asked = append(asked, node+"/"+unit)
			return SystemdState{Enabled: false, Active: true, Linger: true}, nil
		},
	}
	result, err := RunBootstrap(t.Context(), options)
	require.NoError(t, err)
	check := result.CheckByID("bootstrap.service_autostart:factor-engine@factor")
	require.NotNil(t, check)
	assert.Equal(t, core.StatusFail, check.Status, "active 但 disabled 不通过")
	assert.Equal(t, []string{"factor/moox-factor-engine.service"}, asked)

	options.Systemd = nil
	result, err = RunBootstrap(t.Context(), options)
	require.NoError(t, err)
	assert.Equal(t, core.StatusUnknown, result.CheckByID("bootstrap.service_autostart:factor-engine@factor").Status)
}
