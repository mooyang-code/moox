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
	require.ErrorContains(t, runIngestModeSet(context.Background(), &out, ingestModeFlags{SpaceID: "crypto", Ingest: "pull"}), "--ingest")
	require.ErrorContains(t, runIngestModeSet(context.Background(), &out, ingestModeFlags{Ingest: "stream"}), "--space-id")
}
