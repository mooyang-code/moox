package configinit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	xssh "golang.org/x/crypto/ssh"
)

// startKeyOnlySSHServer 起一个只接受公钥登录的 SSH 服务。这里不用 fakeProber，要走真实的 systemProber。
func startKeyOnlySSHServer(t *testing.T) SSHTarget {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := xssh.NewSignerFromKey(private)
	require.NoError(t, err)
	config := &xssh.ServerConfig{PublicKeyCallback: func(xssh.ConnMetadata, xssh.PublicKey) (*xssh.Permissions, error) { return nil, nil }}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				connection, channels, requests, err := xssh.NewServerConn(raw, config)
				if err != nil {
					_ = raw.Close()
					return
				}
				go xssh.DiscardRequests(requests)
				for channel := range channels {
					_ = channel.Reject(xssh.Prohibited, "no channels")
				}
				_ = connection.Close()
			}()
		}
	}()
	host, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	number, err := strconv.Atoi(port)
	require.NoError(t, err)
	return SSHTarget{Address: host, SSHPort: number, SSHUser: "ubuntu"}
}

// 新电脑上 ~/.config/moox 还不存在，要能走到核对指纹，不能报连不上。
func TestSystemProberFirstUseReachesFingerprintConfirmation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	target := startKeyOnlySSHServer(t)
	prober := systemProber{}
	report, err := prober.Login(context.Background(), target)
	require.NoError(t, err)
	require.NotEmpty(t, report.HostKey)
	require.False(t, report.Ready)
	require.NoError(t, prober.Trust(context.Background(), target, report.HostKey))
	report, err = prober.Login(context.Background(), target)
	require.NoError(t, err)
	assert.True(t, report.Ready)
}

func TestSystemProberReportsBrokenFingerprintStoreAsResettable(t *testing.T) {
	for name, prepare := range map[string]func(string) error{
		"权限不对": func(path string) error { return os.WriteFile(path, nil, 0o644) },
		"内容损坏": func(path string) error { return os.WriteFile(path, []byte("@cert-authority broken\n"), 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			path := filepath.Join(home, ".config", "moox", "known_hosts")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, prepare(path))
			target := startKeyOnlySSHServer(t)
			prober := systemProber{}
			_, err := prober.Login(context.Background(), target)
			var fixable *resettableError
			require.True(t, errors.As(err, &fixable), "got %v", err)
			assert.Contains(t, err.Error(), "known_hosts")
			assert.NotContains(t, err.Error(), "连不上")
			require.NoError(t, prober.Reset(context.Background(), target))
			report, err := prober.Login(context.Background(), target)
			require.NoError(t, err)
			assert.NotEmpty(t, report.HostKey)
		})
	}
}
