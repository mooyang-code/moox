package unitbundle

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/stretchr/testify/require"
)

func TestEventBusClientRelayPinsFilesAndRejectsBrokerKeysAndChangedBytes(t *testing.T) {
	f := newFixture(t, "control", []string{"admin", "eventbus"}, false)
	roles := []string{"hostagent-publisher", "metrics-publisher"}
	metadata := hostbundle.ClientMetadata{Version: 1, Status: "ok", OutputDir: "/control/material-exports/client-only", Roles: roles, CA: digest(f.files["certs/moox-ca.crt"])}
	files := map[string][]byte{"ca.pem": f.files["certs/moox-ca.crt"], "hostagent-publisher.yaml": []byte("synthetic-private-host-token\n"), "metrics-publisher.yaml": []byte("synthetic-private-metrics-token\n")}
	for name, raw := range files {
		metadata.Files = append(metadata.Files, hostbundle.File{Path: name, SHA256: digest(raw), Size: int64(len(raw))})
	}
	slices.SortFunc(metadata.Files, func(a, b hostbundle.File) int {
		if a.Path < b.Path {
			return -1
		}
		if a.Path > b.Path {
			return 1
		}
		return 0
	})
	raw, err := json.Marshal(metadata)
	require.NoError(t, err)
	files["clients.json"] = append(raw, '\n')
	download := func(_ context.Context, filename string, out io.Writer) (int64, error) {
		require.Equal(t, metadata.OutputDir, path.Dir(filename))
		content, ok := files[path.Base(filename)]
		require.True(t, ok, "only client inventory may cross SSH")
		n, err := out.Write(content)
		return int64(n), err
	}
	destination := filepath.Join(privateParent(t), "clients")
	require.NoError(t, FetchEventBusClients(t.Context(), download, metadata, roles, destination))
	require.NoError(t, LoadEventBusClientsAt(t.Context(), metadata, roles, destination))
	entries, err := os.ReadDir(destination)
	require.NoError(t, err)
	require.Len(t, entries, 4)
	files["hostagent-publisher.yaml"][0] = 'X'
	refused := filepath.Join(privateParent(t), "refused")
	require.Error(t, FetchEventBusClients(t.Context(), download, metadata, roles, refused))
	_, err = os.Lstat(refused)
	require.True(t, os.IsNotExist(err))
	metadata.Files[0].Path = "server.key"
	require.Error(t, FetchEventBusClients(t.Context(), func(context.Context, string, io.Writer) (int64, error) {
		t.Fatal("invalid inventory must fail before SSH reads")
		return 0, nil
	}, metadata, roles, refused))
}
