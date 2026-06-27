package service_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestControlProtocolUsesCanonicalSurface(t *testing.T) {
	root := moduleRoot(t)
	versionTag := "v" + "2"

	for _, name := range []string{"moox_common.proto", "admin_service.proto", "ops_service.proto", "collect_service.proto", "infra_service.proto"} {
		require.FileExists(t, filepath.Join(root, "proto", name))
	}

	for _, path := range []string{
		"proto/gen" + versionTag,
		"proto/legacy",
		"internal/service/control" + versionTag,
	} {
		require.NoDirExists(t, filepath.Join(root, filepath.FromSlash(path)))
	}

	for _, needle := range []string{
		"proto/gen" + versionTag,
		"internal/service/control" + versionTag,
		"control" + versionTag,
		"pb" + versionTag,
		"gen" + versionTag,
		versionTag + ".0",
		"新 " + versionTag,
		" " + versionTag + " ",
	} {
		requireNoProjectSourceContains(t, root, needle)
	}
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
