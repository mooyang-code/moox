package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func TestDialUsesPrivateIdentityAndStillVerifiesHost(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := xssh.NewSignerFromKey(private)
	require.NoError(t, err)
	fixture := startSSHFixture(t, signer.PublicKey())
	block, err := xssh.MarshalPrivateKey(private, "test-only identity")
	require.NoError(t, err)
	identity := filepath.Join(t.TempDir(), "id_ed25519")
	require.NoError(t, os.WriteFile(identity, pem.EncodeToMemory(block), 0600))
	opts := Options{KnownHostsPath: knownHostsFile(t), IdentityFiles: []string{identity}, DisableAgent: true}
	_, err = Dial(context.Background(), fixture.target, "", opts)
	require.ErrorIs(t, err, ErrHostKeyUnknown)
	require.NoError(t, TrustHost(context.Background(), fixture.target, xssh.FingerprintSHA256(fixture.publicKey), opts))
	client, err := Dial(context.Background(), fixture.target, "", opts)
	require.NoError(t, err)
	require.NoError(t, client.Check(context.Background()))
	require.NoError(t, client.Close())
	// OpenSSH also rejects readable-by-others private identities.
	require.NoError(t, os.Chmod(identity, 0644))
	_, err = Dial(context.Background(), fixture.target, "", opts)
	require.ErrorIs(t, err, ErrAuthFailed)
}

func TestDialUsesSSHAgent(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := xssh.NewSignerFromKey(private)
	require.NoError(t, err)
	fixture := startSSHFixture(t, signer.PublicKey())
	keyring := agent.NewKeyring()
	require.NoError(t, keyring.Add(agent.AddedKey{PrivateKey: private}))
	socket := serveTestAgent(t, keyring)
	opts := Options{KnownHostsPath: knownHostsFile(t), IdentityFiles: []string{}, AgentSocket: socket}
	require.NoError(t, TrustHost(context.Background(), fixture.target, xssh.FingerprintSHA256(fixture.publicKey), opts))
	client, err := Dial(context.Background(), fixture.target, "", opts)
	require.NoError(t, err)
	require.NoError(t, client.Check(context.Background()))
	require.NoError(t, client.Close())
}

func TestDialUsesFileIdentityWithEmptyAgent(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := xssh.NewSignerFromKey(private)
	require.NoError(t, err)
	fixture := startSSHFixture(t, signer.PublicKey())
	block, err := xssh.MarshalPrivateKey(private, "test-only identity")
	require.NoError(t, err)
	identity := filepath.Join(t.TempDir(), "id_ed25519")
	require.NoError(t, os.WriteFile(identity, pem.EncodeToMemory(block), 0600))
	opts := Options{KnownHostsPath: knownHostsFile(t), IdentityFiles: []string{identity}, AgentSocket: serveTestAgent(t, agent.NewKeyring())}
	require.NoError(t, TrustHost(context.Background(), fixture.target, xssh.FingerprintSHA256(fixture.publicKey), opts))
	client, err := Dial(context.Background(), fixture.target, "", opts)
	require.NoError(t, err)
	require.NoError(t, client.Check(context.Background()))
	require.NoError(t, client.Close())
}

func serveTestAgent(t *testing.T, keyring agent.Agent) string {
	t.Helper()
	// Keep the socket path short enough for Unix's platform-specific limit.
	dir, err := os.MkdirTemp("", "moox-agent-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "agent.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = agent.ServeAgent(keyring, conn)
	}()
	return socket
}
