package kline

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKlinePlannerUsesSourceDatasetSubjectsAndTargetDatasetID(t *testing.T) {
	params := &domain.CollectParams{}
	params.Normalize("binance", "kline")
	params.Source.Kind = "dataset_subjects"
	params.Source.DatasetID = "symbols"
	params.Target.DatasetID = "kline_1m"
	params.Collector.Intervals = []string{"1m"}
	subjects := []domain.DatasetSubject{
		{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"},
		{SubjectID: "ETH-USDT", ExternalSymbol: "ETHUSDT", Status: "active"},
	}

	specs := BuildTaskSpecs(params, subjects)

	require.Len(t, specs, len(subjects))
	for i, spec := range specs {
		assert.Equal(t, "kline_1m", spec.DatasetID)
		assert.Equal(t, "kline_1m", spec.Params["dataset_id"])
		assert.Equal(t, subjects[i].SubjectID, spec.SubjectID)
		assert.Equal(t, subjects[i].ExternalSymbol, spec.Symbol)
		assert.Equal(t, "1m", spec.Interval)
	}
}

func TestKlinePlannerSkipsSubjectWithoutExternalSymbol(t *testing.T) {
	params := &domain.CollectParams{}
	params.Normalize("binance", "kline")
	params.Target.DatasetID = "kline_1m"
	params.Collector.Intervals = []string{"1m"}

	specs := BuildTaskSpecs(params, []domain.DatasetSubject{{
		SubjectID: "BTC-USDT",
		Status:    "active",
	}})

	assert.Empty(t, specs)
}
