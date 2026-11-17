package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 采集方式不在任务参数里：旧的 ingest、executor 两项（含“推送加云函数补采”这个组合）写不进去。
func TestCollectParamsRejectIngestAndExecutor(t *testing.T) {
	base := `{"provider":"binance","market_type":"spot","subject_tags":["t"],"target_dataset_id":"ds","frequency":"1m"`
	_, err := ParseCollectParams(base+`}`, "", "", "kline")
	require.NoError(t, err)
	_, err = ParseCollectParams(base+`,"ingest":"stream"}`, "", "", "kline")
	require.ErrorContains(t, err, "ingest")
	_, err = ParseCollectParams(base+`,"ingest":"stream","executor":"scf"}`, "", "", "kline")
	require.Error(t, err)
}

func TestValidateIngestAcceptsOnlyTheThreeValues(t *testing.T) {
	for _, ingest := range []string{IngestStreaming, IngestSCF, IngestLocal} {
		require.NoError(t, ValidateIngest(ingest))
	}
	for _, ingest := range []string{"", "pull", "ws", "STREAM"} {
		require.Error(t, ValidateIngest(ingest), ingest)
	}
}
