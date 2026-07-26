package validate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
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
	assert.Equal(t, "host_key_unknown", result.Checks[2].Code)
	assert.Equal(t, "SHA256:verified-out-of-band", result.Checks[2].Fingerprint)
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

func (f *fakeSSHChecker) Check(_ context.Context, host setupconfig.Host) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, host.Name)
	return f.errs[host.Name]
}

func validationSnapshot(t *testing.T) *setupconfig.Snapshot {
	t.Helper()
	root := t.TempDir()
	body := `[admin]
username = "admin"
password = "admin-password"

[tencent_cloud]
secret_id = "recognizable-secret-id"
secret_key = "recognizable-secret-key"

[eventbus]
public_address = "eventbus.example.test"
port = 4222
tls_enabled = true

[control_host]
name = "control"
address = "192.0.2.10"
username = "ubuntu"
password = "recognizable-control-password"

[[other_hosts]]
name = "compute-1"
address = "192.0.2.11"
username = "ubuntu"
password = "recognizable-compute-password"
`
	path := filepath.Join(root, "custom.toml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	snapshot, err := setupconfig.Load(path, root)
	require.NoError(t, err)
	return snapshot
}

func TestRunChecksIdentityThenHosts(t *testing.T) {
	snapshot := validationSnapshot(t)
	identity := &fakeIdentity{}
	sshChecker := &fakeSSHChecker{errs: map[string]error{}}

	result, err := Run(context.Background(), snapshot, Dependencies{Identity: identity, SSH: sshChecker})
	require.NoError(t, err)
	assert.Equal(t, []Check{
		{Name: "config", Status: "valid"},
		{Name: "tencent_cloud", Status: "valid"},
		{Name: "host:control", Status: "valid"},
		{Name: "host:compute-1", Status: "valid"},
	}, result.Checks)
	assert.Equal(t, 1, identity.calls)
	assert.Equal(t, []string{"control", "compute-1"}, sshChecker.calls)
}

func TestRunSSHSkipsTencentIdentityValidation(t *testing.T) {
	snapshot := validationSnapshot(t)
	identity := &fakeIdentity{err: fmt.Errorf("tencent credentials must not be used")}
	sshChecker := &fakeSSHChecker{errs: map[string]error{}}

	result, err := RunSSHHosts(context.Background(), snapshot, Dependencies{Identity: identity, SSH: sshChecker}, snapshot.Manifest.Hosts())
	require.NoError(t, err)
	assert.Equal(t, []Check{
		{Name: "config", Status: "valid"},
		{Name: "host:control", Status: "valid"},
		{Name: "host:compute-1", Status: "valid"},
	}, result.Checks)
	assert.Zero(t, identity.calls)
	assert.Equal(t, []string{"control", "compute-1"}, sshChecker.calls)
}

func TestRunSSHHostsOnlyChecksDeploymentTargets(t *testing.T) {
	snapshot := validationSnapshot(t)
	sshChecker := &fakeSSHChecker{errs: map[string]error{
		"compute-1": fmt.Errorf("unrelated host is unavailable"),
	}}

	result, err := RunSSHHosts(context.Background(), snapshot, Dependencies{
		Identity: &fakeIdentity{err: fmt.Errorf("Tencent STS must not be called")},
		SSH:      sshChecker,
	}, []setupconfig.Host{snapshot.Manifest.ControlHost})
	require.NoError(t, err)
	assert.Equal(t, []Check{
		{Name: "config", Status: "valid"},
		{Name: "host:control", Status: "valid"},
	}, result.Checks)
	assert.Equal(t, []string{"control"}, sshChecker.calls)
}

func TestRunStopsBeforeSSHWhenIdentityFails(t *testing.T) {
	snapshot := validationSnapshot(t)
	identity := &fakeIdentity{err: fmt.Errorf("recognizable-secret-key")}
	sshChecker := &fakeSSHChecker{errs: map[string]error{}}

	result, err := Run(context.Background(), snapshot, Dependencies{Identity: identity, SSH: sshChecker})
	require.Error(t, err)
	assert.Equal(t, []Check{
		{Name: "config", Status: "valid"},
		{Name: "tencent_cloud", Status: "invalid", Code: "tencent_auth_failed"},
	}, result.Checks)
	assert.Empty(t, sshChecker.calls)
	assert.NotContains(t, err.Error(), "recognizable-secret-key")
}

func TestRunPreservesHostOrderAndRedactsErrors(t *testing.T) {
	snapshot := validationSnapshot(t)
	identity := &fakeIdentity{}
	sshChecker := &fakeSSHChecker{errs: map[string]error{
		"compute-1": fmt.Errorf("recognizable-compute-password"),
	}}

	result, err := Run(context.Background(), snapshot, Dependencies{Identity: identity, SSH: sshChecker})
	require.Error(t, err)
	assert.Equal(t, []Check{
		{Name: "config", Status: "valid"},
		{Name: "tencent_cloud", Status: "valid"},
		{Name: "host:control", Status: "valid"},
		{Name: "host:compute-1", Status: "invalid", Code: "ssh_validation_failed"},
	}, result.Checks)
	assert.NotContains(t, err.Error(), "recognizable-compute-password")
}
