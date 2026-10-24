package conn

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"
	"time"

	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/model"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// 认证成功后服务器拒绝终端 Session：Connect 返回错误时必须关闭已建立的 SSH 连接，不能泄漏。
func TestConnectClosesSSHConnectionWhenSessionIsRejected(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(private)
	require.NoError(t, err)
	config := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	closed := make(chan struct{})
	go func() {
		raw, err := listener.Accept()
		if err != nil {
			return
		}
		serverConn, channels, requests, err := ssh.NewServerConn(raw, config)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(requests)
		go func() {
			for channel := range channels {
				_ = channel.Reject(ssh.ResourceShortage, "no sessions")
			}
		}()
		_ = serverConn.Wait()
		close(closed)
	}()
	address := listener.Addr().(*net.TCPAddr)
	conn := &SSHConn{
		Target:   model.Target{HostID: "storage", Address: "127.0.0.1", Port: address.Port, User: "u", Credential: secretmodel.SSHCredential{Password: "p"}},
		HostKeys: &memoryPins{pins: map[string]string{}},
	}
	require.Error(t, conn.Connect("127.0.0.1"))
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Connect 失败后服务器侧连接没有被关闭（连接泄漏）")
	}
}
