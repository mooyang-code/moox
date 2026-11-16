package validate

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	cloudprovider "github.com/mooyang-code/moox/packages/cloudprovider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeIdentity struct {
	err   error
	calls int
}

func TestRunReportsUnknownHostFingerprint(t *testing.T) {
	snapshot := validationSnapshot(t)
	checker := &fakeSSHChecker{errs: map[string]error{
		"control": fmt.Errorf("%w: SHA256:verified-out-of-band", setupssh.ErrHostKeyUnknown),
	}}
	result, err := Run(context.Background(), snapshot, Dependencies{Identity: &fakeIdentity{}, SSH: checker})
	require.Error(t, err)
	assert.Equal(t, "host_key_unknown", result.Checks[3].Code)
	assert.Equal(t, "SHA256:verified-out-of-band", result.Checks[3].Fingerprint)
}

func (f *fakeIdentity) GetCallerIdentity(context.Context) (cloudprovider.CallerIdentity, error) {
	f.calls++
	return cloudprovider.CallerIdentity{Provider: "tencent", AccountID: "10001"}, f.err
}

type fakeSSHChecker struct {
	mu    sync.Mutex
	calls []string
	errs  map[string]error
}

func (f *fakeSSHChecker) Check(_ context.Context, host topology.Host) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, host.Name)
	return f.errs[host.Name]
}

func validationSnapshot(t *testing.T) *topology.Config {
	t.Helper()
	cfg, err := topology.Parse([]byte(`[hosts.control]
address = "eventbus.example.test"
ssh = { port = 22, username = "ubuntu" }

[hosts.overseas]
address = "192.0.2.11"
ssh = { port = 22, username = "ubuntu" }

[deployments]
control = ["admin", "eventbus", "collector", "factor-mgr", "archive-mgr", "storage-primary"]
overseas = []
`))
	require.NoError(t, err)
	return &cfg
}

func TestRunChecksIdentityThenHosts(t *testing.T) {
	snapshot := validationSnapshot(t)
	identity := &fakeIdentity{}
	sshChecker := &fakeSSHChecker{errs: map[string]error{}}

	result, err := Run(context.Background(), snapshot, Dependencies{Identity: identity, SSH: sshChecker})
	require.NoError(t, err)
	assert.Equal(t, []Check{
		{Name: "config", Status: "valid"},
		{Name: "deployments", Status: "valid"},
		{Name: "tencent_cloud", Status: "valid"},
		{Name: "host:control", Status: "valid"},
		{Name: "host:overseas", Status: "valid"},
	}, result.Checks)
	assert.Equal(t, 1, identity.calls)
	assert.Equal(t, []string{"control", "overseas"}, sshChecker.calls)
}

func TestRunStopsBeforeSSHWhenIdentityFails(t *testing.T) {
	snapshot := validationSnapshot(t)
	identity := &fakeIdentity{err: fmt.Errorf("recognizable-secret-key")}
	sshChecker := &fakeSSHChecker{errs: map[string]error{}}

	result, err := Run(context.Background(), snapshot, Dependencies{Identity: identity, SSH: sshChecker})
	require.Error(t, err)
	assert.Equal(t, []Check{
		{Name: "config", Status: "valid"},
		{Name: "deployments", Status: "valid"},
		{Name: "tencent_cloud", Status: "invalid", Code: "tencent_auth_failed"},
	}, result.Checks)
	assert.Empty(t, sshChecker.calls)
	assert.NotContains(t, err.Error(), "recognizable-secret-key")
}

func TestRunPreservesHostOrderAndRedactsErrors(t *testing.T) {
	snapshot := validationSnapshot(t)
	identity := &fakeIdentity{}
	sshChecker := &fakeSSHChecker{errs: map[string]error{
		"overseas": fmt.Errorf("recognizable-compute-password"),
	}}

	result, err := Run(context.Background(), snapshot, Dependencies{Identity: identity, SSH: sshChecker})
	require.Error(t, err)
	assert.Equal(t, []Check{
		{Name: "config", Status: "valid"},
		{Name: "deployments", Status: "valid"},
		{Name: "tencent_cloud", Status: "valid"},
		{Name: "host:control", Status: "valid"},
		{Name: "host:overseas", Status: "invalid", Code: "ssh_validation_failed"},
	}, result.Checks)
	assert.NotContains(t, err.Error(), "recognizable-compute-password")
}

func TestRunRejectsIncompleteDeploymentsBeforeTouchingHosts(t *testing.T) {
	snapshot := validationSnapshot(t)
	components := snapshot.Deployments["control"]
	snapshot.Deployments["control"] = slices.DeleteFunc(components, func(id string) bool { return id == "collector" })
	identity := &fakeIdentity{}
	sshChecker := &fakeSSHChecker{errs: map[string]error{}}

	result, err := Run(context.Background(), snapshot, Dependencies{Identity: identity, SSH: sshChecker})
	require.ErrorIs(t, err, ErrValidationFailed)
	// 外部身份 collector-operator 能调 Collector 的方法：没有 Collector，Access 会一直等不到这项服务。
	assert.ErrorContains(t, err, "collector")
	assert.ErrorContains(t, err, "给主机 control 加上 collector")
	assert.Equal(t, "deployment_incomplete", result.Checks[1].Code)
	assert.Empty(t, sshChecker.calls)
	assert.Zero(t, identity.calls)
}
