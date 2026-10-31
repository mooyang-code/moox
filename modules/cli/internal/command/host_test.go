package command

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type hostFakeSSH struct {
	ran        [][]string
	result     setupssh.Result
	runErr     error
	remoteFile bool // test -e 是否成功
	uploaded   []byte
	uploadedTo string
	uploadMode fs.FileMode
	download   string
	forwarded  []string
	closed     bool
	runHook    func(argv []string) (setupssh.Result, error) // 非空时优先于默认行为
}

func (h *hostFakeSSH) Check(context.Context) error { return nil }
func (h *hostFakeSSH) ForwardLocal(_ context.Context, remote string) (net.Listener, error) {
	h.forwarded = append(h.forwarded, remote)
	return net.Listen("tcp", "127.0.0.1:0")
}
func (h *hostFakeSSH) Upload(_ context.Context, src io.Reader, _ int64, dst string, mode fs.FileMode) error {
	h.uploaded, _ = io.ReadAll(src)
	h.uploadedTo, h.uploadMode = dst, mode
	return nil
}
func (h *hostFakeSSH) Download(_ context.Context, _ string, dst io.Writer) (int64, error) {
	n, err := io.WriteString(dst, h.download)
	return int64(n), err
}
func (h *hostFakeSSH) Run(_ context.Context, argv []string, _ io.Reader) (setupssh.Result, error) {
	h.ran = append(h.ran, argv)
	if h.runHook != nil {
		return h.runHook(argv)
	}
	if argv[0] == "test" {
		if h.remoteFile {
			return setupssh.Result{}, nil
		}
		return setupssh.Result{ExitCode: 1}, errors.New("ssh_command_failed")
	}
	return h.result, h.runErr
}
func (h *hostFakeSSH) Close() error { h.closed = true; return nil }

func useHostFake(t *testing.T, fake *hostFakeSSH) *hostRemote {
	t.Helper()
	manifest := setupconfig.Manifest{HostCatalog: map[string]setupconfig.HostDefinition{
		"storage": {Address: "198.51.100.20", SSH: setupconfig.SSHConfig{Port: 22, Username: "ubuntu"}},
	}}
	remote := newHostRemote(manifest, "", nil)
	remote.dial = func(context.Context, setupconfig.Host) (setupssh.Client, error) { return fake, nil }
	previous := openHostRemoteForCommand
	openHostRemoteForCommand = func(string) (*hostRemote, error) { return remote, nil }
	t.Cleanup(func() { openHostRemoteForCommand = previous })
	return remote
}

func runHostCommand(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newHostExecCommand()
	switch args[0] {
	case "get":
		cmd = newHostGetCommand()
	case "put":
		cmd = newHostPutCommand()
	}
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args[1:])
	cmd.SetContext(context.Background())
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestHostExecPassesArgvOutputAndExitCode(t *testing.T) {
	fake := &hostFakeSSH{result: setupssh.Result{Stdout: "host-a\n", Stderr: "warn\n"}}
	useHostFake(t, fake)
	out, errOut, err := runHostCommand(t, "exec", "--host", "storage", "--", "uname", "-a")
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"uname", "-a"}}, fake.ran)
	assert.Equal(t, "host-a\n", out)
	assert.Equal(t, "warn\n", errOut)

	fake.result, fake.runErr = setupssh.Result{Stdout: "partial", ExitCode: 3}, errors.New("ssh_command_failed")
	_, _, err = runHostCommand(t, "exec", "--host", "storage", "--", "false")
	var exit *hostExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 3, exit.ExitCode(), "远端退出码透传")

	_, _, err = runHostCommand(t, "exec", "--", "ls")
	require.ErrorContains(t, err, "--host 必填")
	_, _, err = runHostCommand(t, "exec", "--host", "missing", "--", "ls")
	require.ErrorContains(t, err, "没有节点")
}

func TestHostPutPreviewsOverwriteAndUploadsWithYes(t *testing.T) {
	fake := &hostFakeSSH{remoteFile: true}
	useHostFake(t, fake)
	local := filepath.Join(t.TempDir(), "a.txt")
	require.NoError(t, os.WriteFile(local, []byte("hello"), 0o640))

	out, _, err := runHostCommand(t, "put", "--host", "storage", local, "/tmp/a.txt")
	require.NoError(t, err)
	assert.Nil(t, fake.uploaded, "远端已存在时不能覆盖")
	assert.Equal(t, "upload_overwrite", parsePreview(t, out)["action"])

	_, _, err = runHostCommand(t, "put", "--host", "storage", "--yes", local, "/tmp/a.txt")
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), fake.uploaded)
	assert.Equal(t, "/tmp/a.txt", fake.uploadedTo)
	assert.Equal(t, fs.FileMode(0o640), fake.uploadMode)

	fake.remoteFile, fake.uploaded = false, nil
	_, _, err = runHostCommand(t, "put", "--host", "storage", local, "/tmp/new.txt")
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), fake.uploaded, "远端不存在时直接上传")

	_, _, err = runHostCommand(t, "put", "--host", "storage", local, "relative/path")
	require.ErrorContains(t, err, "绝对路径")
}

func TestHostGetWritesFileAndProtectsExisting(t *testing.T) {
	fake := &hostFakeSSH{download: "remote-content"}
	useHostFake(t, fake)
	local := filepath.Join(t.TempDir(), "out.txt")
	out, _, err := runHostCommand(t, "get", "--host", "storage", "/var/log/x", local)
	require.NoError(t, err)
	raw, err := os.ReadFile(local)
	require.NoError(t, err)
	assert.Equal(t, "remote-content", string(raw))
	assert.Contains(t, out, `"downloaded"`)

	fake.download = "new"
	out, _, err = runHostCommand(t, "get", "--host", "storage", "/var/log/x", local)
	require.NoError(t, err)
	assert.Equal(t, "download_overwrite", parsePreview(t, out)["action"])
	raw, _ = os.ReadFile(local)
	assert.Equal(t, "remote-content", string(raw), "没有 --yes 不覆盖")
	_, _, err = runHostCommand(t, "get", "--host", "storage", "--yes", "/var/log/x", local)
	require.NoError(t, err)
	raw, _ = os.ReadFile(local)
	assert.Equal(t, "new", string(raw))
}

func TestHostRemoteLocalEndpointForwardsOverSSHAndCaches(t *testing.T) {
	fake := &hostFakeSSH{}
	remote := useHostFake(t, fake)
	first, err := remote.LocalEndpoint(context.Background(), "storage", 11417)
	require.NoError(t, err)
	second, err := remote.LocalEndpoint(context.Background(), "storage", 11417)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Equal(t, []string{"127.0.0.1:11417"}, fake.forwarded, "同一目标只转发一次")
	host, _, err := net.SplitHostPort(first)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", host, "只监听回环地址")
	remote.Close()
	assert.True(t, fake.closed)
	_, err = net.Dial("tcp", first)
	assert.Error(t, err, "关闭后监听端口释放")
}

type relayConnGateway struct{ server net.Conn }

func (g *relayConnGateway) DialRelay(context.Context, string, int) (net.Conn, error) {
	client, server := net.Pipe()
	g.server = server
	return client, nil
}

func TestHostRemoteLocalEndpointRelaysOutboundHosts(t *testing.T) {
	manifest := setupconfig.Manifest{HostCatalog: map[string]setupconfig.HostDefinition{
		"overseas": {Address: "203.0.113.5", Reachability: "outbound", SSH: setupconfig.SSHConfig{Port: 22, Username: "u"}},
	}}
	gateway := &relayConnGateway{}
	remote := newHostRemote(manifest, "", gateway)
	remote.dial = func(context.Context, setupconfig.Host) (setupssh.Client, error) {
		t.Fatal("出站主机不能走 SSH")
		return nil, nil
	}
	defer remote.Close()
	address, err := remote.LocalEndpoint(context.Background(), "overseas", 11210)
	require.NoError(t, err)
	conn, err := net.Dial("tcp", address)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.Write([]byte("ping"))
	require.NoError(t, err)
	buffer := make([]byte, 4)
	require.Eventually(t, func() bool { return gateway.server != nil }, 2e9, 10e6)
	_, err = io.ReadFull(gateway.server, buffer)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(buffer), "本机端口的数据经中转到达目标")

	noRelay := newHostRemote(manifest, "", struct{}{})
	_, err = noRelay.LocalEndpoint(context.Background(), "overseas", 11210)
	require.ErrorContains(t, err, "中转")
}
