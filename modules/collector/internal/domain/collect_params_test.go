package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCollectParamsUsesSingleDatasetDrivenContract(t *testing.T) {
	raw := `{
		"source":{"kind":"dataset_subjects","dataset_id":"source-kline"},
		"collector":{"exchange":" Binance ","market":" SPOT ","data_type":" KLINE ","intervals":["1m"],"live":false},
		"target":{"dataset_id":"target-kline"},
		"schedule":{"interval":"1h"}
	}`
	params, err := ParseCollectParams(raw, "", "")
	require.NoError(t, err)
	require.NoError(t, params.Validate())
	assert.Equal(t, "binance", params.Collector.Exchange)
	assert.Equal(t, "spot", params.Collector.Market)
	assert.Equal(t, "kline", params.Collector.DataType)
	assert.Equal(t, "source-kline", params.Source.DatasetID)
	assert.Equal(t, "target-kline", params.Target.DatasetID)
}

func TestParseCollectParamsAcceptsSymbolWithoutSourceDataset(t *testing.T) {
	raw := `{
		"source":{"kind":"none","dataset_id":""},
		"collector":{"exchange":"binance","market":"swap","data_type":"symbol","intervals":[]},
		"target":{"dataset_id":"symbols"},
		"schedule":{"interval":"30m"}
	}`
	params, err := ParseCollectParams(raw, "", "")
	require.NoError(t, err)
	require.NoError(t, params.Validate())
	assert.Equal(t, "none", params.Source.Kind)
	assert.Empty(t, params.Collector.Intervals)
}

func TestParseCollectParamsRejectsRemovedFields(t *testing.T) {
	for _, raw := range []string{
		`{"target":{"job_type":"collect.binance.kline"}}`,
		`{"schedule":{"timezone":"Asia/Shanghai"}}`,
		`{"schedule":{"intervals":["1m"]}}`,
		`{"objects":["BTCUSDT"]}`,
	} {
		_, err := ParseCollectParams(raw, "binance", "kline")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown field")
	}
}

func TestCollectParamsValidateRequiresExplicitDatasetsAndIntervals(t *testing.T) {
	params, err := ParseCollectParams(`{
		"source":{"kind":"dataset_subjects"},
		"collector":{"exchange":"binance","market":"spot","data_type":"kline"},
		"target":{},
		"schedule":{"interval":"bad"}
	}`, "", "")
	require.NoError(t, err)
	require.Error(t, params.Validate())
}

func TestParseCollectParamsInvalidJSONReturnsError(t *testing.T) {
	_, err := ParseCollectParams("{", "binance", "kline")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse collect params")
}
