package deploy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteInstallCompleteAtomicAndSurvivesDeploy(t *testing.T) {
	root := t.TempDir()
	client := &localClient{t: t, home: t.TempDir(), path: os.Getenv("PATH")}
	layout := Layout{Root: root}

	marker := InstallCompleteMarker{
		CompletedAt: "2026-11-28T00:00:00Z",
		SpaceID:     "crypto",
		Version:     "v0.1.0",
		Commit:      "abcdef1",
	}
	require.NoError(t, WriteInstallComplete(context.Background(), client, layout, marker))

	dst := filepath.Join(root, InstallCompleteFile)
	raw, err := os.ReadFile(dst)
	require.NoError(t, err)
	var decoded InstallCompleteMarker
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, marker, decoded)
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dst)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}

	// 不残留临时文件。
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, InstallCompleteFile, entries[0].Name())
}
