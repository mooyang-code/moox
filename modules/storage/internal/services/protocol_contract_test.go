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
	repoRoot := filepath.Dir(filepath.Dir(root))
	versionTag := "v" + "2"
	legacyProxyName := "adapt" + "er"
	legacyServiceName := "Adapt" + "erService"
	legacyTargetName := "Device" + "Ref"

	for _, name := range []string{"common.proto", "metadata.proto", "data.proto", "query.proto", "primary.proto", "message.proto"} {
		require.FileExists(t, filepath.Join(root, "proto", name))
	}

	for _, path := range []string{
		"proto/gen" + versionTag,
		"proto/legacy",
		"pkg/quantstore",
		"internal/services/" + versionTag,
		"internal/services/storage",
		"internal/services/dbmanager",
	} {
		require.NoDirExists(t, filepath.Join(root, filepath.FromSlash(path)))
	}

	for _, path := range []string{
		"proto/" + versionTag + "_common.proto",
		"proto/" + versionTag + "_metadata.proto",
		"proto/" + versionTag + "_data.proto",
		"proto/" + versionTag + "_query.proto",
		"proto/" + versionTag + "_" + legacyProxyName + ".proto",
		"proto/" + versionTag + "_message.proto",
		"proto/" + legacyProxyName + ".proto",
		"proto/gen/access.pb.go",
		"proto/gen/access.trpc.go",
		"proto/gen/" + legacyProxyName + ".pb.go",
		"proto/gen/" + legacyProxyName + ".trpc.go",
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
		"trpc.storage." + legacyProxyName,
		"trpc.storage.dbmanager.DBTableManager",
		"RegisterAccessService",
		"Register" + legacyServiceName,
		"RegisterDBTableManagerService",
		"pkg/quantstore",
		legacyServiceName,
		legacyTargetName,
		"services/" + legacyProxyName,
	} {
		requireNoProjectSourceContains(t, root, needle)
	}

	for _, needle := range []string{
		"facts/",
		".jsonl",
		"CSVImportOptions",
	} {
		requireNoProjectSourceContains(t, root, needle)
	}
	requireFileNotContains(t, filepath.Join(repoRoot, "docs", "superpowers", "plans", "2026-06-17-storage-module-implementation.md"), []string{
		"pkg/quantstore",
		"quantstore.Store",
		"go test ./pkg/quantstore",
	})

	for _, proto := range []string{"common.proto", "data.proto", "query.proto", "primary.proto"} {
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
			"attrs",
			" entity_id =",
		})
	}

	requireProtocolFileContains(t, filepath.Join(root, "proto", "data.proto"), []string{
		"message DataScope",
		"message DataKey",
		"message DataRow",
		"参与逻辑定位",
		"不是普通过滤条件",
		"DataKey key",
		"rpc WriteRows",
		"rpc ReadRows",
	})
	requireProtocolFileNotContains(t, filepath.Join(root, "proto", "data.proto"), []string{
		"message DataSlice",
	})
	requireProtocolFileNotContains(t, filepath.Join(root, "proto", "query.proto"), []string{
		"SearchText",
		"TextSearch",
		"expression =",
		" source_type =",
		" source_id =",
	})
	requireProtocolFileContains(t, filepath.Join(root, "proto", "query.proto"), []string{
		"rpc QueryView",
		"rpc SearchRows",
		"message SearchRowsReq",
		"origin_type",
		"origin_id",
		"text_query",
		"repeated common.FilterExpr filters",
		"repeated common.SortSpec sorts",
		"repeated string column_names",
	})
	requireProtocolFileContains(t, filepath.Join(root, "proto", "primary.proto"), []string{
		"message PrimaryTarget",
		"node_id",
		"device_table",
		"rpc WritePrimaryRows",
		"rpc ReadPrimaryRows",
	})
	requireProtocolFileContains(t, filepath.Join(root, "proto", "metadata.proto"), []string{
		"message DataSetColumn",
		"space_id",
		"origin_type",
		"origin_id",
		"text_indexed",
	})
	requireProtocolFileNotContains(t, filepath.Join(root, "proto", "metadata.proto"), []string{
		"Workspace",
		"workspace_id",
		"MarketInfo",
		"Exchange",
		"Instrument",
		"InstrumentAlias",
		"DataView",
		"data_view",
		"FactorDef",
		"FactorInstance",
		"factor_instance",
		"CollectorDataSetBinding",
		"StorageEntity",
		"StorageDevice",
		"storage_entity",
		"message SpaceView",
		"BindSpaceView",
		"ListSpaceViews",
		"interface_name",
		" entity_id =",
		" source_type =",
		" source_id =",
		" role =",
		"device_id 是目标 Pebble 设备 ID",
	})
	requireProtocolFileContains(t, filepath.Join(root, "proto", "metadata.proto"), []string{
		"message Space",
		"message View",
		"message ViewColumn",
		"message DataSource",
		"message Subject",
		"message SubjectSymbol",
		"message DataSetSubject",
		"message Field",
		"message Factor",
		"message StorageNode",
		"message Device",
		"message StorageRoute",
		"message ArchiveFile",
		"rpc CreateSpace",
		"rpc CreateView",
		"rpc ListViews",
		"rpc CreateDataSource",
		"rpc UpsertSubject",
		"rpc UpsertSubjectSymbol",
		"rpc BindDataSetSubject",
		"rpc UpsertDataSetColumn",
		"rpc CreateFactor",
		"rpc CreateStorageNode",
		"rpc ListStorageNodes",
		"rpc CreateDevice",
		"rpc ListArchiveFiles",
	})
	requireProtocolFileNotContains(t, filepath.Join(root, "proto", "metadata.proto"), []string{
		"SubjectAlias",
		"UpsertSubjectAlias",
		"ListSubjectAliases",
		"active_table",
		"DuckDB 宽表名称",
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
	requireFileNotContains(t, path, needles)
}

func requireFileNotContains(t *testing.T, path string, needles []string) {
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
