package command

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/stretchr/testify/require"
	xssh "golang.org/x/crypto/ssh"
)

func startCollectorPublicationSSH(t *testing.T, ca []byte, reads *atomic.Int32, blacklists ...string) setupconfig.Host {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := xssh.NewSignerFromKey(private)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	config := &xssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer raw.Close()
				connection, channels, requests, err := xssh.NewServerConn(raw, config)
				if err != nil {
					return
				}
				defer connection.Close()
				go xssh.DiscardRequests(requests)
				for request := range channels {
					if request.ChannelType() != "session" {
						_ = request.Reject(xssh.UnknownChannelType, "unsupported")
						continue
					}
					channel, requests, err := request.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer channel.Close()
						for request := range requests {
							if request.Type != "exec" {
								_ = request.Reply(false, nil)
								continue
							}
							var payload struct{ Command string }
							_ = xssh.Unmarshal(request.Payload, &payload)
							reads.Add(1)
							_ = request.Reply(true, nil)
							var stdout string
							switch {
							case strings.Contains(payload.Command, "moox-collector-blacklist-preflight"):
								raw := "scf_region_blacklists:\n  stockcn: [" + strings.Join(blacklists, ", ") + "]\n  crypto: [" + strings.Join(blacklists, ", ") + "]\n"
								stdout = fmt.Sprintf("sha256:%x\n/data/moox/bin/moox-collector\n%s", sha256.Sum256([]byte(raw)), raw)
							case strings.Contains(payload.Command, "market-fetch-publisher.yaml"):
								stdout = "version: 1\nurls: [tls://203.0.113.10:4222]\nusername: publisher\npassword: publisher-secret\n"
							case strings.Contains(payload.Command, "ca.pem"), strings.Contains(payload.Command, "peers.pem"):
								stdout = string(ca)
							case strings.Contains(payload.Command, "storage-internal-auth.env"):
								stdout = "MOOX_STORAGE_PRIMARY_AUTH_SECRET=primary-secret\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-secret\n"
							case strings.Contains(payload.Command, "gateway-moox-cli.key"):
								stdout = "11223344"
							case strings.Contains(payload.Command, "gateway-collector.key"):
								stdout = "aabbccdd"
							case strings.Contains(payload.Command, "root.crt"):
							default:
								t.Errorf("unexpected SSH command: %s", payload.Command)
							}
							_, _ = io.WriteString(channel, stdout)
							_, _ = channel.SendRequest("exit-status", false, xssh.Marshal(struct{ Status uint32 }{}))
							return
						}
					}()
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); workers.Wait() })
	host := setupconfig.Host{Name: "control", Address: "127.0.0.1", Port: port, Username: "test", Password: "test"}
	require.NoError(t, setupssh.TrustHost(context.Background(), sshTarget(host), xssh.FingerprintSHA256(signer.PublicKey()), setupssh.Options{}))
	return host
}
