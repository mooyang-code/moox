package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCollectParamsValidateIngestMode(t *testing.T) {
	base := `{"provider":"binance","market_type":"spot","subject_tags":["t"],"target_dataset_id":"ds","frequency":"1m"`
	params, err := ParseCollectParams(base+`,"ingest":" Stream ","executor":"LOCAL"}`, "", "", "kline")
	require.NoError(t, err)
	require.Equal(t, "stream", params.Ingest)
	require.Equal(t, "local", params.Executor)
	_, err = ParseCollectParams(base+`,"ingest":"ws"}`, "", "", "kline")
	require.ErrorContains(t, err, "ingest")
	_, err = ParseCollectParams(base+`,"executor":"lambda"}`, "", "", "kline")
	require.ErrorContains(t, err, "executor")
}
