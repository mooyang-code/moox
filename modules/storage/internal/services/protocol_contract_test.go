package services_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStorageProtocolUsesCanonicalSurface(t *testing.T) {
	root := moduleRoot(t)
	versionTag := "v" + "2"

	for _, name := range []string{"common.proto", "metadata.proto", "data.proto", "query.proto", "adapter.proto", "message.proto"} {
		require.FileExists(t, filepath.Join(root, "proto", name))
	}

	for _, path := range []string{
		"proto/gen" + versionTag,
		"proto/legacy",
		"internal/services/" + versionTag,
		"internal/services/access",
		"internal/services/dbmanager",
		"internal/services/metadata",
		"internal/services/adapter",
	} {
		require.NoDirExists(t, filepath.Join(root, filepath.FromSlash(path)))
	}

	for _, path := range []string{
		"proto/" + versionTag + "_common.proto",
		"proto/" + versionTag + "_metadata.proto",
		"proto/" + versionTag + "_data.proto",
		"proto/" + versionTag + "_query.proto",
		"proto/" + versionTag + "_adapter.proto",
		"proto/" + versionTag + "_message.proto",
		"proto/gen/access.pb.go",
		"proto/gen/access.trpc.go",
		"proto/gen/dbmanager.pb.go",
		"proto/gen/dbmanager.trpc.go",
	} {
		require.NoFileExists(t, filepath.Join(root, filepath.FromSlash(path)))
	}

	for _, needle := range []string{
		"proto/gen" + versionTag,
		"internal/services/" + versionTag,
		"package " + versionTag,
		"storage" + versionTag,
		versionTag + "_",
		" " + versionTag + " ",
		versionTag + " protocol",
		"trpc.storage.access.Access",
		"trpc.storage.dbmanager.DBTableManager",
		"RegisterAccessService",
		"RegisterDBTableManagerService",
	} {
		requireNoProjectSourceContains(t, root, needle)
	}

	for _, proto := range []string{"common.proto", "data.proto", "query.proto", "adapter.proto"} {
		requireProtocolFileNotContains(t, filepath.Join(root, "proto", proto), []string{
			"workspace_id",
			"DataRef",
			"instrument_id",
			"exchange_id",
			"DataView",
			"data_view",
			"FactorInstance",
			"factor_instance",
			"DataDomain",
			"data_domain",
			"DeleteRows",
			"WriteOptions",
			"affected",
			"RowChange",
			"previous_rows",
			"ExplainQuery",
			"Physical",
			"DeletePhysicalRows",
		})
	}

	requireProtocolFileContains(t, filepath.Join(root, "proto", "data.proto"), []string{
		"message DataSlice",
		"message DataRow",
		"参与逻辑定位",
		"不是普通过滤条件",
		"rpc WriteRows",
		"rpc ReadRows",
	})
	requireProtocolFileNotContains(t, filepath.Join(root, "proto", "query.proto"), []string{
		"SearchText",
		"TextSearch",
		"expression =",
	})
	requireProtocolFileContains(t, filepath.Join(root, "proto", "query.proto"), []string{
		"rpc QueryView",
		"rpc SearchRows",
		"message SearchRowsReq",
		"text_query",
		"repeated common.FilterExpr filters",
		"repeated common.SortSpec sorts",
		"repeated string column_names",
	})
	requireProtocolFileContains(t, filepath.Join(root, "proto", "adapter.proto"), []string{
		"message DeviceRef",
		"device_table",
		"rpc WriteDeviceRows",
		"rpc ReadDeviceRows",
	})
	requireProtocolFileContains(t, filepath.Join(root, "proto", "metadata.proto"), []string{
		"message DataSetColumn",
		"text_indexed",
	})
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		parent := filepath.Dir(wd)
		require.NotEqual(t, wd, parent, "go.mod not found")
		wd = parent
	}
}

func requireNoProjectSourceContains(t *testing.T, root, needle string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		require.NoError(t, err)
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "bin", "release", "cover.out.tmp":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Base(path) == "protocol_contract_test.go" {
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".proto":
		default:
			if filepath.Base(path) != "Makefile" {
				return nil
			}
		}
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NotContains(t, strings.ReplaceAll(string(data), "\r\n", "\n"), needle, path)
		return nil
	})
	require.NoError(t, err)
}

func requireProtocolFileNotContains(t *testing.T, path string, needles []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	for _, needle := range needles {
		require.NotContains(t, content, needle, path)
	}
}

func requireProtocolFileContains(t *testing.T, path string, needles []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	for _, needle := range needles {
		require.Contains(t, content, needle, path)
	}
}
