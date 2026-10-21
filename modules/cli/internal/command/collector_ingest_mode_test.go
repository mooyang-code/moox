package command

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIngestModeCommandsValidateArguments(t *testing.T) {
	var out bytes.Buffer
	require.ErrorContains(t, runIngestModeGet(context.Background(), &out, ingestModeFlags{}), "--space-id")
	require.ErrorContains(t, runIngestModeSet(context.Background(), &out, ingestModeFlags{SpaceID: "crypto", Mode: "ws"}), "--mode")
	require.ErrorContains(t, runIngestModeSet(context.Background(), &out, ingestModeFlags{Mode: "stream"}), "--space-id")
}
