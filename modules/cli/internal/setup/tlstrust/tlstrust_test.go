package tlstrust

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequiresLocalCATrust(t *testing.T) {
	require.True(t, RequiresLocalCATrust(TLSModeInternal, "203.0.113.10"))
	require.False(t, RequiresLocalCATrust(TLSModePublic, "203.0.113.10"))
	require.False(t, RequiresLocalCATrust("", "203.0.113.8"))
	require.True(t, RequiresLocalCATrust("", "127.0.0.1"))
}

func TestEnsureLocalCATrustInstallsOnlyWhenCheckFails(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "scripts", "deploy", "install-caddy-ca.sh")
	marker := filepath.Join(root, "installed")
	require.NoError(t, os.MkdirAll(filepath.Dir(script), 0o755))
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nset -eu\ncase \" $* \" in\n  *' --check '*) test -f \"$MARKER\";;\n  *) : >\"$MARKER\";;\nesac\n"), 0o700))
	t.Setenv("MARKER", marker)

	require.NoError(t, EnsureLocalCATrust(context.Background(), root, filepath.Join(root, "root.crt")))
	require.FileExists(t, marker)
}

func TestEnsureLocalCATrustSkipsInstallWhenAlreadyTrusted(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "scripts", "deploy", "install-caddy-ca.sh")
	marker := filepath.Join(root, "installed")
	require.NoError(t, os.MkdirAll(filepath.Dir(script), 0o755))
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nset -eu\ncase \" $* \" in\n  *' --check '*) exit 0;;\n  *) : >\"$MARKER\";;\nesac\n"), 0o700))
	t.Setenv("MARKER", marker)

	require.NoError(t, EnsureLocalCATrust(context.Background(), root, filepath.Join(root, "root.crt")))
	_, err := os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestEnsureLocalCATrustForHostSkipsPublicTLS(t *testing.T) {
	require.NoError(t, EnsureLocalCATrustForHost(context.Background(), t.TempDir(), "203.0.113.8", TLSModePublic))
}
func TestLocalCATrustFailuresAreRecognizable(t *testing.T) {
	err := EnsureLocalCATrust(context.Background(), "", "")
	if !errors.Is(err, ErrBrowserCATrust) {
		t.Fatalf("EnsureLocalCATrust error %v does not wrap ErrBrowserCATrust", err)
	}
	if !strings.HasPrefix(err.Error(), "browser_ca_trust_failed: ") {
		t.Fatalf("error text changed: %q", err.Error())
	}
}
