package command

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"

	doctorcli "github.com/mooyang-code/moox/modules/cli/internal/doctor"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type remoteSSH struct {
	setupssh.Client
	ran      [][]string
	stdout   string
	stderr   string
	err      error
	deadLine time.Time
	hasDL    bool
	closed   bool
}

func (r *remoteSSH) Run(ctx context.Context, argv []string, _ io.Reader) (setupssh.Result, error) {
	r.ran = append(r.ran, argv)
	r.deadLine, r.hasDL = ctx.Deadline()
	return setupssh.Result{Stdout: r.stdout, Stderr: r.stderr}, r.err
}
func (r *remoteSSH) Close() error { r.closed = true; return nil }

func hostRemoteFixture() (*hostRemote, *remoteSSH, *[]string) {
	manifest := &topology.Config{
		HostCatalog: map[string]topology.HostDefinition{
			"control": {Address: "198.51.100.10", SSH: topology.SSHConfig{Port: 22, Username: "ubuntu"}},
			"factor":  {Address: "192.168.0.103", Reachability: "outbound", SSH: topology.SSHConfig{Port: 22, Username: "builder"}},
		},
	}
	ssh := &remoteSSH{stdout: "enabled=enabled\nactive=active\nlinger=yes\n"}
	var local []string
	remote := newHostRemote(manifest, "control")
	remote.dial = func(context.Context, topology.Host) (setupssh.Client, error) { return ssh, nil }
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

// forwardSSH 模拟 SSH 的本地转发：把本地端口转到 target（代表目标机上的 127.0.0.1:{端口}）。
type forwardSSH struct {
	remoteSSH
	target    string
	forwarded []string
}

func (f *forwardSSH) ForwardLocal(ctx context.Context, remote string) (net.Listener, error) {
	f.forwarded = append(f.forwarded, remote)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	go func() {
		for {
			local, err := listener.Accept()
			if err != nil {
				return
			}
			upstream, err := net.Dial("tcp", f.target)
			if err != nil {
				_ = local.Close()
				continue
			}
			go func() { _, _ = io.Copy(upstream, local); _ = upstream.Close() }()
			go func() { _, _ = io.Copy(local, upstream); _ = local.Close() }()
		}
	}()
	return listener, nil
}

// 健康端口只听本机：本节点直接请求 127.0.0.1，其他主机经 SSH 转发到目标机的 127.0.0.1:{端口}。
func TestHostRemoteHealthProbeForwardsToTheTargetLoopbackOverSSH(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ready":true}`))
	}))
	defer server.Close()
	ssh := &forwardSSH{target: server.Listener.Addr().String()}
	remote, _, _ := hostRemoteFixture()
	remote.dial = func(context.Context, topology.Host) (setupssh.Client, error) { return ssh, nil }
	defer remote.Close()
	probe := remote.HealthProbe()

	result, err := probe(t.Context(), "factor", 11417, "/readyz")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, result.StatusCode)
	assert.Equal(t, []string{"127.0.0.1:11417"}, ssh.forwarded, "转发到目标机的本机健康端口")

	_, rawPort, err := net.SplitHostPort(server.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(rawPort)
	require.NoError(t, err)
	result, err = probe(t.Context(), "control", port, "/readyz")
	require.NoError(t, err, "本节点直接请求 127.0.0.1")
	assert.Equal(t, http.StatusOK, result.StatusCode)
	assert.Len(t, ssh.forwarded, 1)
}

// 只能出站的主机 SSH 连不上时：探测报不可达（诊断记为无法判断），错误里写明只能在局域网里检查和不在局域网时看什么。
func TestHostRemoteExplainsOutboundHostsAreLANOnly(t *testing.T) {
	remote, _, _ := hostRemoteFixture()
	remote.dial = func(context.Context, topology.Host) (setupssh.Client, error) {
		return nil, errors.New("dial timeout")
	}
	defer remote.Close()

	_, err := remote.HealthProbe()(t.Context(), "factor", 11417, "/readyz")
	require.ErrorIs(t, err, doctorcli.ErrNodeUnreachable)
	assert.ErrorContains(t, err, "局域网")
	assert.ErrorContains(t, err, "moox-cli host agent status --host factor")

	_, err = remote.Systemd()(t.Context(), "factor", "moox-factor-engine")
	assert.ErrorContains(t, err, "局域网", "systemd 检查同样经 SSH，提示一致")

	remote.manifest.HostCatalog["storage"] = topology.HostDefinition{Address: "198.51.100.11"}
	_, err = remote.HealthProbe()(t.Context(), "storage", 20210, "/readyz")
	require.ErrorIs(t, err, doctorcli.ErrNodeUnreachable)
	assert.NotContains(t, err.Error(), "局域网", "直连主机不加出站提示")
}

func TestHostRemoteProbeWritableSetsTimeoutSurfacesStderrAndDistinguishesErrors(t *testing.T) {
	remote, ssh, _ := hostRemoteFixture()
	defer remote.Close()

	// 1. 远端执行带超时且将 stderr 最后一行非空信息拼进错误。
	ssh.stderr = "Traceback...\nprobe path is not an existing directory\n\n"
	ssh.err = errors.New("exit status 1")
	err := remote.ProbeWritable("factor")(t.Context(), "/data/moox", "data/archive-worker")
	require.ErrorContains(t, err, "exit status 1")
	require.ErrorContains(t, err, "probe path is not an existing directory")
	require.True(t, ssh.hasDL, "远端目录可写探测必须设置超时")
	require.LessOrEqual(t, time.Until(ssh.deadLine), remoteWritableProbeTimeout+time.Second)

	// 2. 用本机 python3 实测 remoteWritableProbeScript：正常可写、目录缺失、越界符号链接三类分支区分明确。
	python, lookErr := exec.LookPath("python3")
	if lookErr != nil {
		t.Skip("本机没有 python3")
	}
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "data", "archive-worker"), 0o700))
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "data", "escaped")))

	runScript := func(rel string) (string, error) {
		cmd := exec.Command(python, "-I", "-c", remoteWritableProbeScript, root, rel)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := runScript("data/archive-worker")
	require.NoError(t, err, out)
	entries, err := os.ReadDir(filepath.Join(root, "data", "archive-worker"))
	require.NoError(t, err)
	require.Empty(t, entries, "探测临时文件必须清理干净")

	out, err = runScript("data/missing-component")
	require.Error(t, err)
	require.Contains(t, out, "probe path is not an existing directory")

	out, err = runScript("data/escaped")
	require.Error(t, err)
	require.Contains(t, out, "probe path escapes release root")
}

func TestHostRemoteReadFileReadsLocalDirectlyWithoutSwallowingNotExist(t *testing.T) {
	remote, ssh, _ := hostRemoteFixture()
	defer remote.Close()

	// 本机缺 version.json 必须返回 ErrNotExist，让 bootstrap 报“读不到部署记录”而不是“不是合法的 JSON”。
	root := t.TempDir()
	_, err := remote.ReadFile(t.Context(), "control", filepath.Join(root, "version.json"))
	require.ErrorIs(t, err, os.ErrNotExist)

	report, err := doctorcli.RunBootstrap(t.Context(), doctorcli.BootstrapOptions{
		NodeID:      "control",
		LocalNodeID: "control",
		DeployRoot:  root,
		CheckIDs:    []string{"bootstrap.release_contract"},
		ReadFile:    remote.ReadFile,
	})
	require.NoError(t, err)
	check := report.CheckByID("bootstrap.release_contract")
	require.NotNil(t, check)
	require.Equal(t, core.StatusFail, check.Status)
	require.Contains(t, check.Error, "读不到部署记录")
	require.NotContains(t, check.Error, "不是合法的 JSON")

	// 远端节点经 SSH 读取，并透传错误。
	ssh.stdout = `{"version":"dev"}`
	data, err := remote.ReadFile(t.Context(), "factor", "/data/moox/version.json")
	require.NoError(t, err)
	require.Equal(t, `{"version":"dev"}`, string(data))
}
