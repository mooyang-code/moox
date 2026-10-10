package unitbundle

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestOperatorInstallationDownloadsOnlyAssignedIdentityAndPreservesPin(t *testing.T) {
	f := newFixture(t, "control", []string{"admin", "console-proxy", "access"}, true)
	allowed := map[string]bool{"bundle.json": true, "operator/gateway-client.yaml": true, "operator/caller-moox-cli.key": true, "certs/moox-ca.crt": true}
	download := func(ctx context.Context, name string, out io.Writer) (int64, error) {
		require.True(t, allowed[name[len(f.metadata.BundleDir)+1:]], "must not transfer another component's private material")
		return f.download(t)(ctx, name, out)
	}
	directory := privateParent(t)
	metadata, err := FetchMetadata(t.Context(), download, f.metadata.BundleDir, f.options)
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, InstallOperator(t.Context(), download, metadata, f.options, directory))
	}
	raw, err := os.ReadFile(filepath.Join(directory, "gateway-client.yaml"))
	require.NoError(t, err)
	var config hostbundle.OperatorConfig
	require.NoError(t, yaml.Unmarshal(raw, &config))
	require.Equal(t, "moox-cli", config.Caller)
	secret, err := gatewayauth.ReadSigningSecret(config.KeyFile)
	require.NoError(t, err)
	require.Equal(t, strings.TrimSpace(string(f.files["operator/caller-moox-cli.key"])), secret)
	for _, name := range []string{"gateway-client.yaml", "gateway-bootstrap.json", filepath.Base(config.KeyFile)} {
		info, err := os.Stat(filepath.Join(directory, name))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	other := newFixture(t, "control", []string{"admin", "console-proxy", "access"}, true)
	require.ErrorContains(t, InstallOperator(t.Context(), other.download(t), other.metadata, other.options, directory), "another control CA")
	after, err := os.ReadFile(filepath.Join(directory, "gateway-client.yaml"))
	require.NoError(t, err)
	require.Equal(t, raw, after)
	badDownload := func(ctx context.Context, name string, out io.Writer) (int64, error) {
		if path.Base(name) == "caller-moox-cli.key" {
			n, err := out.Write([]byte("tampered"))
			return int64(n), err
		}
		return download(ctx, name, out)
	}
	require.Error(t, InstallOperator(t.Context(), badDownload, metadata, f.options, privateParent(t)))
}
