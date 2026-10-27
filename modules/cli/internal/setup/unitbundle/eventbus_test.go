package unitbundle

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
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

// 服务端导出（给运行 broker 的主机）多带证书、私钥和用户表；缺一个、多一个或清单没有声明 Server 都必须被拒绝，
// 普通的客户端导出也不能夹带这三个文件。
func TestEventBusServerExportCarriesBrokerMaterialOnlyWhenDeclared(t *testing.T) {
	f := newFixture(t, "storage", []string{"eventbus"}, false)
	roles := []string{"eventbus-internal-admin", "metrics-publisher"}
	build := func(server bool, extra map[string][]byte) (hostbundle.ClientMetadata, map[string][]byte) {
		files := map[string][]byte{"ca.pem": f.files["certs/moox-ca.crt"], "eventbus-internal-admin.yaml": []byte("synthetic-internal-admin\n"), "metrics-publisher.yaml": []byte("synthetic-metrics\n")}
		if server {
			files["server.pem"], files["server-key.pem"], files["users.yaml"] = []byte("cert\n"), []byte("key\n"), []byte("users:\n")
		}
		for name, raw := range extra {
			files[name] = raw
		}
		metadata := hostbundle.ClientMetadata{Version: 1, Status: "ok", OutputDir: "/storage/material-exports/eventbus", Roles: roles, CA: digest(f.files["certs/moox-ca.crt"]), Server: server}
		for name, raw := range files {
			metadata.Files = append(metadata.Files, hostbundle.File{Path: name, SHA256: digest(raw), Size: int64(len(raw))})
		}
		slices.SortFunc(metadata.Files, func(a, b hostbundle.File) int { return strings.Compare(a.Path, b.Path) })
		raw, err := json.Marshal(metadata)
		require.NoError(t, err)
		files["clients.json"] = append(raw, '\n')
		return metadata, files
	}
	load := func(metadata hostbundle.ClientMetadata, files map[string][]byte) error {
		dir := filepath.Join(privateParent(t), "export")
		return FetchEventBusClients(t.Context(), func(_ context.Context, filename string, out io.Writer) (int64, error) {
			content, ok := files[path.Base(filename)]
			if !ok {
				return 0, os.ErrNotExist
			}
			n, err := out.Write(content)
			return int64(n), err
		}, metadata, roles, dir)
	}

	metadata, files := build(true, nil)
	require.NoError(t, load(metadata, files))

	// 清单声明了 Server，却少了一个服务端文件。
	short := metadata
	short.Files = slices.DeleteFunc(slices.Clone(metadata.Files), func(f hostbundle.File) bool { return f.Path == "users.yaml" })
	require.Error(t, load(short, files))

	// 普通客户端导出里夹带服务端文件：清单没有声明 Server，文件数对不上。
	plain, plainFiles := build(false, map[string][]byte{"server-key.pem": []byte("key\n")})
	require.Error(t, load(plain, plainFiles))

	// 普通客户端导出仍然只有 CA 和所选角色。
	plain, plainFiles = build(false, nil)
	require.NoError(t, load(plain, plainFiles))
}
