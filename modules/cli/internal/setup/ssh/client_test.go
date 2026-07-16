package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	xssh "golang.org/x/crypto/ssh"
)

type sshFixture struct {
	target      Target
	password    string
	publicKey   xssh.PublicKey
	listener    net.Listener
	done        chan struct{}
	connections sync.WaitGroup
}

func startSSHFixture(t *testing.T) *sshFixture {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := xssh.NewSignerFromKey(privateKey)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	f := &sshFixture{
		target:    Target{Name: "fixture", Address: host, Port: port, Username: "ubuntu"},
		password:  "fixture-password",
		publicKey: signer.PublicKey(),
		listener:  listener,
		done:      make(chan struct{}),
	}
	serverConfig := &xssh.ServerConfig{
		PasswordCallback: func(metadata xssh.ConnMetadata, password []byte) (*xssh.Permissions, error) {
			if metadata.User() != f.target.Username || string(password) != f.password {
				return nil, fmt.Errorf("denied")
			}
			return nil, nil
		},
	}
	serverConfig.AddHostKey(signer)
	f.connections.Add(1)
	go func() {
		defer f.connections.Done()
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			f.connections.Add(1)
			go func() {
				defer f.connections.Done()
				handleFixtureConn(conn, serverConfig)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		f.connections.Wait()
		close(f.done)
	})
	return f
}

type directTCPIP struct {
	DestAddr   string
	DestPort   uint32
	OriginAddr string
	OriginPort uint32
}

func handleFixtureConn(raw net.Conn, cfg *xssh.ServerConfig) {
	serverConn, channels, requests, err := xssh.NewServerConn(raw, cfg)
	if err != nil {
		_ = raw.Close()
		return
	}
	defer serverConn.Close()
	go xssh.DiscardRequests(requests)
	for channel := range channels {
		if channel.ChannelType() == "session" {
			sshChannel, channelRequests, err := channel.Accept()
			if err != nil {
				continue
			}
			go handleFixtureSession(sshChannel, channelRequests)
			continue
		}
		if channel.ChannelType() != "direct-tcpip" {
			_ = channel.Reject(xssh.UnknownChannelType, "unsupported")
			continue
		}
		var target directTCPIP
		if xssh.Unmarshal(channel.ExtraData(), &target) != nil {
			_ = channel.Reject(xssh.ConnectionFailed, "invalid target")
			continue
		}
		upstream, err := net.Dial("tcp", net.JoinHostPort(target.DestAddr, strconv.Itoa(int(target.DestPort))))
		if err != nil {
			_ = channel.Reject(xssh.ConnectionFailed, "unreachable")
			continue
		}
		sshChannel, channelRequests, err := channel.Accept()
		if err != nil {
			_ = upstream.Close()
			continue
		}
		go xssh.DiscardRequests(channelRequests)
		go func() {
			defer sshChannel.Close()
			defer upstream.Close()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(sshChannel, upstream); done <- struct{}{} }()
			go func() { _, _ = io.Copy(upstream, sshChannel); done <- struct{}{} }()
			<-done
		}()
	}
}

func handleFixtureSession(channel xssh.Channel, requests <-chan *xssh.Request) {
	defer channel.Close()
	for request := range requests {
		if request.Type != "subsystem" {
			_ = request.Reply(false, nil)
			continue
		}
		var subsystem struct{ Name string }
		if xssh.Unmarshal(request.Payload, &subsystem) != nil || subsystem.Name != "sftp" {
			_ = request.Reply(false, nil)
			continue
		}
		_ = request.Reply(true, nil)
		server, err := sftp.NewServer(channel)
		if err != nil {
			return
		}
		_ = server.Serve()
		_ = server.Close()
		return
	}
}

func knownHostsFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	return path
}

func TestDialRejectsUnknownHostKey(t *testing.T) {
	fixture := startSSHFixture(t)
	password := fixture.password
	_, err := Dial(context.Background(), fixture.target, password, Options{KnownHostsPath: knownHostsFile(t)})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrHostKeyUnknown)
	assert.Contains(t, err.Error(), xssh.FingerprintSHA256(fixture.publicKey))
	assert.NotContains(t, err.Error(), password)
}

func TestTrustHostRequiresMatchingFingerprint(t *testing.T) {
	fixture := startSSHFixture(t)
	knownHosts := knownHostsFile(t)
	err := TrustHost(context.Background(), fixture.target, "SHA256:not-the-key", Options{KnownHostsPath: knownHosts})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrFingerprintMismatch)

	contents, readErr := os.ReadFile(knownHosts)
	require.NoError(t, readErr)
	assert.Empty(t, contents)
}

func TestDialTrustedHostAndRejectsWrongPassword(t *testing.T) {
	fixture := startSSHFixture(t)
	knownHosts := knownHostsFile(t)
	require.NoError(t, TrustHost(context.Background(), fixture.target, xssh.FingerprintSHA256(fixture.publicKey), Options{KnownHostsPath: knownHosts}))

	client, err := Dial(context.Background(), fixture.target, fixture.password, Options{KnownHostsPath: knownHosts})
	require.NoError(t, err)
	require.NoError(t, client.Check(context.Background()))
	require.NoError(t, client.Close())

	_, err = Dial(context.Background(), fixture.target, "wrong-password", Options{KnownHostsPath: knownHosts})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAuthFailed)
	assert.NotContains(t, err.Error(), "wrong-password")
}

func TestForwardLocalConnectsThroughSSH(t *testing.T) {
	fixture := startSSHFixture(t)
	knownHosts := knownHostsFile(t)
	require.NoError(t, TrustHost(context.Background(), fixture.target, xssh.FingerprintSHA256(fixture.publicKey), Options{KnownHostsPath: knownHosts}))
	client, err := Dial(context.Background(), fixture.target, fixture.password, Options{KnownHostsPath: knownHosts})
	require.NoError(t, err)
	defer client.Close()

	backend := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "setup-ready")
	})}
	backendListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go backend.Serve(backendListener)
	t.Cleanup(func() { _ = backend.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	forward, err := client.ForwardLocal(ctx, backendListener.Addr().String())
	require.NoError(t, err)
	defer forward.Close()

	httpClient := &http.Client{Timeout: 2 * time.Second}
	resp, err := httpClient.Get("http://" + forward.Addr().String())
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, "setup-ready", string(body))

	cancel()
	require.Eventually(t, func() bool {
		conn, dialErr := net.DialTimeout("tcp", forward.Addr().String(), 20*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
		}
		return dialErr != nil
	}, time.Second, 20*time.Millisecond)
}

func TestUploadInstallsFileAtomically(t *testing.T) {
	fixture := startSSHFixture(t)
	knownHosts := knownHostsFile(t)
	require.NoError(t, TrustHost(context.Background(), fixture.target, xssh.FingerprintSHA256(fixture.publicKey), Options{KnownHostsPath: knownHosts}))
	client, err := Dial(context.Background(), fixture.target, fixture.password, Options{KnownHostsPath: knownHosts})
	require.NoError(t, err)
	defer client.Close()

	directory := t.TempDir()
	destination := filepath.Join(directory, "release.tar.gz")
	payload := "release-archive"
	require.NoError(t, client.Upload(context.Background(), strings.NewReader(payload), int64(len(payload)), destination, 0o600))

	contents, err := os.ReadFile(destination)
	require.NoError(t, err)
	assert.Equal(t, payload, string(contents))
	info, err := os.Stat(destination)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	matches, err := filepath.Glob(destination + ".next-*")
	require.NoError(t, err)
	assert.Empty(t, matches)
}
