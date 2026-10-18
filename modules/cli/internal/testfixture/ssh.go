package testfixture

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/require"
	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"
)

// Each fixture represents a different SSH host; only the real loopback gateway
// destination is accepted, then mapped to its test server's ephemeral port.
func GatewaySSH(t *testing.T, name, backend, knownHostsPath string) setupconfig.Host {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := xssh.NewSignerFromKey(private)
	require.NoError(t, err)
	config := &xssh.ServerConfig{PasswordCallback: func(_ xssh.ConnMetadata, password []byte) (*xssh.Permissions, error) {
		if string(password) != "fixture-password" {
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
	return setupconfig.Host{Name: name, Address: host, Port: number, Username: "fixture", Password: "fixture-password"}
}
