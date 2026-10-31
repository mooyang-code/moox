package command

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"

	doctorcli "github.com/mooyang-code/moox/modules/cli/internal/doctor"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type remoteSSH struct {
	setupssh.Client
	ran    [][]string
	stdout string
	closed bool
}

func (r *remoteSSH) Run(_ context.Context, argv []string, _ io.Reader) (setupssh.Result, error) {
	r.ran = append(r.ran, argv)
	return setupssh.Result{Stdout: r.stdout}, nil
}
func (r *remoteSSH) Close() error { r.closed = true; return nil }

type fakeRelayGateway struct{ dialed []string }

func (f *fakeRelayGateway) DialRelay(_ context.Context, hostID string, _ int) (net.Conn, error) {
	f.dialed = append(f.dialed, hostID)
	return nil, errors.New("not connected")
}

func hostRemoteFixture() (*hostRemote, *remoteSSH, *[]string) {
	manifest := setupconfig.Manifest{
		HostCatalog: map[string]setupconfig.HostDefinition{
			"control": {Address: "198.51.100.10", SSH: setupconfig.SSHConfig{Port: 22, Username: "ubuntu"}},
			"factor":  {Address: "192.168.0.103", Reachability: "outbound", SSH: setupconfig.SSHConfig{Port: 22, Username: "builder"}},
		},
	}
	ssh := &remoteSSH{stdout: "enabled=enabled\nactive=active\nlinger=yes\n"}
	var local []string
	remote := newHostRemote(manifest, "control", &fakeRelayGateway{})
	remote.dial = func(context.Context, setupconfig.Host) (setupssh.Client, error) { return ssh, nil }
	remote.runLocal = func(_ context.Context, argv []string) (string, error) {
		local = append(local, argv[len(argv)-1])
		return "enabled=enabled\nactive=active\nlinger=yes\n", nil
	}
	return remote, ssh, &local
}

func TestHostRemoteRunsLocalNodeLocallyAndOthersOverSSH(t *testing.T) {
	remote, ssh, local := hostRemoteFixture()
	defer remote.Close()
	check := remote.Systemd()

	state, err := check(t.Context(), "factor", "moox-factor-engine")
	require.NoError(t, err)
	assert.True(t, state.OK())
	require.Len(t, ssh.ran, 1, "其他主机经 SSH 读取")
	assert.Equal(t, "moox-factor-engine", ssh.ran[0][len(ssh.ran[0])-1])
	assert.Empty(t, *local)

	_, err = check(t.Context(), "control", "moox-factor-engine")
	require.NoError(t, err)
	assert.Equal(t, []string{"moox-factor-engine"}, *local, "本节点直接执行")

	_, err = check(t.Context(), "nowhere", "moox-factor-engine")
	require.ErrorContains(t, err, "没有节点")

	_, _ = check(t.Context(), "factor", "moox-archive-worker")
	remote.Close()
	assert.True(t, ssh.closed)
}

func TestHostRemoteRelayOnlyForOutboundHosts(t *testing.T) {
	remote, _, _ := hostRemoteFixture()
	prober := remote.Relay(doctorcli.HealthAuth{AccessKey: "a", SecretKey: "b"})
	require.NotNil(t, prober)
	assert.True(t, prober.Outbound("factor"))
	assert.False(t, prober.Outbound("control"))
	assert.False(t, prober.Outbound("unknown"))

	withoutRelay := newHostRemote(remote.manifest, "control", struct{}{})
	assert.Nil(t, withoutRelay.Relay(doctorcli.HealthAuth{AccessKey: "a", SecretKey: "b"}), "操作员网关不支持中转时不提供中转探测")
}

// 诊断要用 SSH 密码连接主机：快照里的凭据被清掉后，远程执行器仍保留主机目录里的密码，直到关闭。
func TestHostRemoteKeepsHostPasswordsUntilClose(t *testing.T) {
	definition := setupconfig.HostDefinition{}
	definition.SSH.Password = "ssh-secret"
	snapshot := &setupconfig.Snapshot{Manifest: setupconfig.Manifest{HostCatalog: map[string]setupconfig.HostDefinition{"storage": definition}}}
	snapshot.Manifest.Admin.Password = "admin-secret"

	remote := newHostRemoteFromSnapshot(snapshot, "control", nil)
	assert.Empty(t, snapshot.Manifest.Admin.Password, "其余凭据照常清掉")
	assert.Empty(t, snapshot.Manifest.HostCatalog["storage"].SSH.Password, "快照里的 SSH 密码被清掉")
	assert.Equal(t, "ssh-secret", remote.manifest.HostCatalog["storage"].SSH.Password, "远程执行器建连时还要用")

	remote.Close()
	assert.Empty(t, remote.manifest.HostCatalog["storage"].SSH.Password, "关闭后清除")
}
