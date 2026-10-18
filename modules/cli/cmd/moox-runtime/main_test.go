package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimeCommandReportsVersionWithoutOperatorConfiguration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	require.NoError(t, run(context.Background(), []string{"version"}, &stdout, &stderr))
	var metadata map[string]string
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &metadata))
	require.Equal(t, Version, metadata["version"])
	require.Len(t, metadata["catalog_sha256"], 71)
	require.Empty(t, stderr.String())
}

func TestRuntimeCommandRejectsMissingFlagsAndUnknownOperations(t *testing.T) {
	for _, operation := range []string{"activate", "rollback", "recover"} {
		for _, args := range [][]string{{operation}, {operation, "--unknown"}, {operation, "unexpected"}} {
			var stdout, stderr bytes.Buffer
			require.Error(t, run(context.Background(), args, &stdout, &stderr))
			require.Empty(t, stdout.String())
		}
	}
	for _, args := range [][]string{{"inspect-release"}, {"inspect-release", "--unknown"}, {"inspect-release", "--directory", "missing", "unexpected"}, {"inspect-release", "--directory", "missing"}} {
		var stdout, stderr bytes.Buffer
		require.Error(t, run(context.Background(), args, &stdout, &stderr))
		require.Empty(t, stdout.String())
	}
	for _, operation := range []string{"prepare", "seal-state", "bootstrap"} {
		for _, args := range [][]string{{operation}, {operation, "--unknown"}, {operation, "--request", "missing", "unexpected"}, {operation, "--request", "missing"}} {
			var stdout, stderr bytes.Buffer
			require.Error(t, run(context.Background(), args, &stdout, &stderr))
			require.Empty(t, stdout.String())
		}
	}
	for _, args := range [][]string{nil, {"start"}, {"start", "--plan", "missing", "unexpected"}, {"start", "--unknown"}, {"unknown", "--plan", "missing"}, {"extract"}, {"extract", "--unknown"}, {"extract", "unexpected"}, {"extract", "--archive", "missing", "--profile", "access", "--destination", "/tmp/unused", "--sha256", "invalid"}} {
		var stdout, stderr bytes.Buffer
		require.Error(t, run(context.Background(), args, &stdout, &stderr))
		require.Empty(t, stdout.String())
	}
}

func TestRuntimeCommandWatchRequiresRootAndReportsEmptyHosts(t *testing.T) {
	for _, args := range [][]string{{"watch"}, {"watch", "--unknown"}, {"watch", "--root", "/data/moox", "unexpected"}} {
		var stdout, stderr bytes.Buffer
		require.Error(t, run(context.Background(), args, &stdout, &stderr))
		require.Empty(t, stdout.String())
	}
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"watch", "--root", t.TempDir()}, &stdout, &stderr)
	require.ErrorContains(t, err, "no installed units")
	var report map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &report), "出错时也输出一行汇总，cron 日志里能看到")
}
