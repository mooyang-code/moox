package ssh

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Authentication uses the operator's existing agent/default identities before
// the configured password, matching ordinary key-based SSH deployments. Host
// verification remains mandatory in Dial, independently of the auth method.
// Private bytes and parsing errors never enter diagnostics or command output.
func authentication(ctx context.Context, password string, opts Options) ([]xssh.AuthMethod, func()) {
	var methods []xssh.AuthMethod
	var agentSigners func() ([]xssh.Signer, error)
	closeAgent := func() {}
	if !opts.DisableAgent {
		socket := opts.AgentSocket
		if socket == "" {
			socket = os.Getenv("SSH_AUTH_SOCK")
		}
		if socket != "" {
			conn, err := (&net.Dialer{Timeout: timeout(opts)}).DialContext(ctx, "unix", socket)
			if err == nil {
				_ = conn.SetDeadline(time.Now().Add(timeout(opts)))
				agentSigners = agent.NewClient(conn).Signers
				closeAgent = func() { _ = conn.Close() }
			}
		}
	}
	files := opts.IdentityFiles
	if files == nil {
		if homeDir, err := os.UserHomeDir(); err == nil {
			// moox_ed25519 是 moox-cli config init 生成并装到各服务器上的 MooX 专用密钥。
			for _, name := range []string{"moox_ed25519", "id_ed25519", "id_ecdsa", "id_rsa"} {
				files = append(files, filepath.Join(homeDir, ".ssh", name))
			}
		}
	}
	var signers []xssh.Signer
	for _, path := range files {
		info, err := os.Stat(path)
		// Windows 不用 Unix 权限位表示私有文件，只在其他系统上要求私钥不对组和其他用户开放。
		if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		signer, err := xssh.ParsePrivateKey(raw)
		clear(raw)
		if err == nil {
			signers = append(signers, signer)
		}
		// Encrypted identities are handled by the SSH agent. Never interpret a
		// login password as a private-key passphrase or prompt interactively.
	}
	if agentSigners != nil || len(signers) != 0 {
		// x/crypto/ssh tries an authentication method only once. Combine agent
		// and file identities into one publickey method so an empty/unusable
		// agent does not prevent an otherwise valid default identity.
		methods = append(methods, xssh.PublicKeysCallback(func() ([]xssh.Signer, error) {
			if agentSigners != nil {
				fromAgent, err := agentSigners()
				if err == nil {
					return append(fromAgent, signers...), nil
				}
				if len(signers) == 0 {
					return nil, err
				}
			}
			return signers, nil
		}))
	}
	if password != "" {
		methods = append(methods, xssh.Password(password))
	}
	return methods, closeAgent
}
