package gateway

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"sync"
	"testing"
	"time"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/stretchr/testify/require"
)

// fakeSSHClient 记录转发请求，返回一个真实的本地监听。
type fakeSSHClient struct {
	mu       sync.Mutex
	remotes  []string
	closed   bool
	failWith error
}

func (c *fakeSSHClient) Check(context.Context) error { return nil }

// ForwardLocal 和真实实现一样：传入的 ctx 结束时关闭监听。
func (c *fakeSSHClient) ForwardLocal(ctx context.Context, remote string) (net.Listener, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failWith != nil {
		return nil, c.failWith
	}
	c.remotes = append(c.remotes, remote)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	return listener, nil
}

func (c *fakeSSHClient) Upload(context.Context, io.Reader, int64, string, fs.FileMode) error {
	return nil
}

func (c *fakeSSHClient) Download(context.Context, string, io.Writer) (int64, error) { return 0, nil }

func (c *fakeSSHClient) Run(context.Context, []string, io.Reader) (setupssh.Result, error) {
	return setupssh.Result{}, nil
}

func (c *fakeSSHClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func TestSSHTunnelReusesOneForwardPerHost(t *testing.T) {
	clients := map[string]*fakeSSHClient{}
	dials := 0
	tunnel := NewSSHTunnel(func(_ context.Context, hostID string) (setupssh.Client, error) {
		dials++
		client := &fakeSSHClient{}
		clients[hostID] = client
		return client, nil
	})
	first, err := tunnel.Address(context.Background(), "control")
	require.NoError(t, err)
	again, err := tunnel.Address(context.Background(), "control")
	require.NoError(t, err)
	require.Equal(t, first, again, "同一台主机复用一条隧道")
	other, err := tunnel.Address(context.Background(), "storage")
	require.NoError(t, err)
	require.NotEqual(t, first, other)
	require.Equal(t, 2, dials)
	require.Equal(t, []string{"127.0.0.1:11002"}, clients["control"].remotes, "隧道转发到主机网关的本机入口")

	require.NoError(t, tunnel.Close())
	require.True(t, clients["control"].closed && clients["storage"].closed)
	_, err = tunnel.Address(context.Background(), "control")
	require.Error(t, err, "关闭后不能再建立隧道")
}

func TestSSHTunnelClosesConnectionWhenForwardFails(t *testing.T) {
	client := &fakeSSHClient{failWith: errors.New("administratively prohibited")}
	tunnel := NewSSHTunnel(func(context.Context, string) (setupssh.Client, error) { return client, nil })
	_, err := tunnel.Address(context.Background(), "control")
	require.ErrorContains(t, err, "administratively prohibited")
	require.True(t, client.closed, "转发失败时关闭 SSH 连接")
	_, err = tunnel.Address(context.Background(), "")
	require.Error(t, err)
}

// 隧道按主机缓存、被多次调用复用：首次调用用的是临时 ctx（例如服务目录后台刷新），它结束后隧道必须仍然可用。
func TestSSHTunnelSurvivesCancellationOfFirstCallContext(t *testing.T) {
	tunnel := NewSSHTunnel(func(context.Context, string) (setupssh.Client, error) { return &fakeSSHClient{}, nil })
	defer tunnel.Close()
	first, cancel := context.WithCancel(context.Background())
	address, err := tunnel.Address(first, "control")
	require.NoError(t, err)
	cancel()
	time.Sleep(50 * time.Millisecond)

	again, err := tunnel.Address(context.Background(), "control")
	require.NoError(t, err)
	require.Equal(t, address, again)
	connection, err := net.DialTimeout("tcp", again, time.Second)
	require.NoError(t, err, "首次调用的 ctx 取消之后，本地转发端口仍然要能连上")
	_ = connection.Close()
}
