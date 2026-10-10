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
	for _, args := range [][]string{nil, {"start"}, {"start", "--plan", "missing", "unexpected"}, {"start", "--unknown"}, {"unknown", "--plan", "missing"}} {
		var stdout, stderr bytes.Buffer
		require.Error(t, run(context.Background(), args, &stdout, &stderr))
		require.Empty(t, stdout.String())
	}
}
