package command

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/stretchr/testify/require"
)

type externalCallerSSH struct {
	localSkillSSH
	metadata string
	exitCode int
	calls    [][]string
}

func (s *externalCallerSSH) Run(ctx context.Context, argv []string, input io.Reader) (setupssh.Result, error) {
	s.calls = append(s.calls, append([]string(nil), argv...))
	if len(argv) > 3 && argv[3] == "moox-external-export" {
		return setupssh.Result{Stdout: s.metadata, ExitCode: s.exitCode}, nil
	}
	return s.localSkillSSH.Run(ctx, argv, input)
}

func TestExportRemoteExternalCallerUsesAssignedIdentityAndPrivateOpaqueKey(t *testing.T) {
	for _, caller := range []string{"scf-collector", "factor-engine", "moox-skill"} {
		t.Run(caller, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "secrets/external", caller, "caller-"+caller+".key")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			secret := strings.Repeat("opaqueGXz!", 5)
			require.NoError(t, os.WriteFile(path, []byte(secret+"\n"), 0o600))
			metadata, err := json.Marshal(map[string]string{"caller": caller, "key_id": "assigned-key-73", "key_file": path})
			require.NoError(t, err)
			ssh := &externalCallerSSH{metadata: string(metadata)}
			credential, err := exportRemoteExternalCaller(t.Context(), ssh, root, caller)
			require.NoError(t, err)
			require.Equal(t, caller, credential.Caller)
			require.Equal(t, "assigned-key-73", credential.KeyID)
			require.Equal(t, secret, credential.Secret)
			require.Len(t, ssh.calls, 2)
			require.Equal(t, []string{filepath.Join(root, "bin/moox-admin-cli"), caller, filepath.Dir(path), filepath.Join(root, "data/admin.db")}, ssh.calls[0][4:])
			for _, argv := range ssh.calls {
				require.NotContains(t, strings.Join(argv, " "), secret)
			}
			require.NoError(t, os.Chmod(path, 0o644))
			_, err = exportRemoteExternalCaller(t.Context(), ssh, root, caller)
			require.ErrorContains(t, err, "signing key unavailable")
			require.NotContains(t, err.Error(), secret)
		})
	}
}

func TestExportRemoteExternalCallerRejectsUntrustedMetadataBeforeReadingKey(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		name, metadata string
		exitCode       int
	}{
		{"missing identity", `{}`, 0},
		{"foreign caller", `{"caller":"moox-cli","key_id":"assigned-key","key_file":"/tmp/key"}`, 0},
		{"unknown secret field", `{"secret":"do-not-disclose-this-secret"}`, 0},
		{"multiple documents", `{} {}`, 0},
		{"bounded response", strings.Repeat("x", 16385), 0},
		{"nonzero exit", `{}`, 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			ssh := &externalCallerSSH{metadata: test.metadata, exitCode: test.exitCode}
			_, err := exportRemoteExternalCaller(t.Context(), ssh, root, "moox-skill")
			require.Error(t, err)
			require.Len(t, ssh.calls, 1)
			require.NotContains(t, err.Error(), "do-not-disclose-this-secret")
		})
	}
}
