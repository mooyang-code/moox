package sources

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectResultContractIsMinimal(t *testing.T) {
	result := CollectResult{
		RowsWritten:     2,
		OutputWatermark: "2026-07-28T01:02:03Z",
		SnapshotVersion: "snapshot-v1",
	}
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"rows_written": 2,
		"output_watermark": "2026-07-28T01:02:03Z",
		"snapshot_version": "snapshot-v1"
	}`, string(raw))
	assert.LessOrEqual(t, len(raw), 1024)
}
