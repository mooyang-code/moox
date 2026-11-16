package testfixture

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
)

// Each fixture represents a different SSH host; only the real loopback gateway
// destination is accepted, then mapped to its test server's ephemeral port.
// 登录只接受夹具自己生成的私钥（写在临时目录、0600），和真实部署一样是免密的密钥登录。
func GatewaySSH(t *testing.T, name, backend, knownHostsPath string) topology.Host {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := xssh.NewSignerFromKey(private)
	require.NoError(t, err)
	_, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	clientSigner, err := xssh.NewSignerFromKey(clientPrivate)
	require.NoError(t, err)
	block, err := xssh.MarshalPrivateKey(clientPrivate, "")
	require.NoError(t, err)
	keyFile := filepath.Join(t.TempDir(), "id_fixture")
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600))
	allowed := clientSigner.PublicKey().Marshal()
	config := &xssh.ServerConfig{PublicKeyCallback: func(_ xssh.ConnMetadata, key xssh.PublicKey) (*xssh.Permissions, error) {
		if !bytes.Equal(key.Marshal(), allowed) {
			return nil, errors.New("denied")
		}
		return nil, nil
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	file, err := os.OpenFile(knownHostsPath, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = fmt.Fprintln(file, knownhosts.Line([]string{knownhosts.Normalize(listener.Addr().String())}, signer.PublicKey()))
	require.NoError(t, err)
	require.NoError(t, file.Close())
	var connections sync.WaitGroup
	connections.Add(1)
	go func() {
		defer connections.Done()
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			go func() {
				defer connections.Done()
				connection, channels, requests, err := xssh.NewServerConn(raw, config)
				if err != nil {
					_ = raw.Close()
					return
				}
				defer connection.Close()
				go func() {
					for request := range requests {
						_ = request.Reply(true, nil)
					}
				}()
				var forwards sync.WaitGroup
				defer forwards.Wait()
				for channel := range channels {
					var target struct {
						Host       string
						Port       uint32
						Origin     string
						OriginPort uint32
					}
					if channel.ChannelType() != "direct-tcpip" || xssh.Unmarshal(channel.ExtraData(), &target) != nil || target.Host != "127.0.0.1" || target.Port != 11002 {
						_ = channel.Reject(xssh.Prohibited, "only the local gateway is allowed")
						continue
					}
					upstream, err := net.Dial("tcp", backend)
					if err != nil {
						_ = channel.Reject(xssh.ConnectionFailed, "unavailable")
						continue
					}
					forward, reqs, err := channel.Accept()
					if err != nil {
						_ = upstream.Close()
						continue
					}
					go xssh.DiscardRequests(reqs)
					forwards.Add(1)
					go func() {
						defer forwards.Done()
						done := make(chan struct{})
						go func() { _, _ = io.Copy(forward, upstream); close(done) }()
						_, _ = io.Copy(upstream, forward)
						_ = upstream.Close()
						_ = forward.Close()
						<-done
					}()
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); connections.Wait() })
	host, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	number, err := strconv.Atoi(port)
	require.NoError(t, err)
	return topology.Host{Name: name, Address: host, Port: number, Username: "fixture", PrivateKeyFile: keyFile}
}
