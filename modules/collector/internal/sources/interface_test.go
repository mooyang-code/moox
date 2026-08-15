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

func TestCollectParamsDNSIPsNormalizesHostAndAddresses(t *testing.T) {
	params := &CollectParams{DNSRoutes: map[string]DNSResolution{
		"FAPI.BINANCE.COM.": {IPs: []string{" 203.0.113.2 ", "203.0.113.2", "bad"}},
	}}
	assert.Equal(t, []string{"203.0.113.2"}, params.DNSIPs("fapi.binance.com."))
}
