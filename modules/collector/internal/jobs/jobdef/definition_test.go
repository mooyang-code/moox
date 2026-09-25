package jobdef

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestJobDefinition_Matches_MatchingSupport_ShouldReturnTrue(t *testing.T) {
	def := JobDefinition{
		Supports: []Support{
			{Exchange: "binance", Market: "spot", DataType: "kline", SourceKind: "dataset"},
		},
	}
	params := &domain.CollectParams{
		Source: domain.CollectSource{Kind: "dataset"},
		Collector: domain.CollectorSpec{
			Exchange: " Binance ",
			Market:   "SPOT",
			DataType: "kline",
		},
	}
	assert.True(t, def.Matches(params))
}

func TestJobDefinition_Matches_NonMatchingExchange_ShouldReturnFalse(t *testing.T) {
	def := JobDefinition{
		Supports: []Support{
			{Exchange: "binance", Market: "spot", DataType: "kline", SourceKind: "dataset"},
		},
	}
	params := &domain.CollectParams{
		Collector: domain.CollectorSpec{
			Exchange: "okx",
			Market:   "spot",
			DataType: "kline",
		},
	}
	assert.False(t, def.Matches(params))
}

func TestJobDefinition_Matches_NilParams_ShouldReturnFalse(t *testing.T) {
	def := JobDefinition{
		Supports: []Support{
			{Exchange: "binance", Market: "spot", DataType: "kline", SourceKind: "dataset"},
		},
	}
	assert.False(t, def.Matches(nil))
}

func TestJobDefinition_Matches_NonMatchingSourceKind_ShouldReturnFalse(t *testing.T) {
	def := JobDefinition{Supports: []Support{{
		Exchange: "binance", Market: "spot", DataType: "kline", SourceKind: "dataset",
	}}}
	params := &domain.CollectParams{
		Source: domain.CollectSource{Kind: "none"},
		Collector: domain.CollectorSpec{
			Exchange: "binance", Market: "spot", DataType: "kline",
		},
	}
	assert.False(t, def.Matches(params))
}

func TestExecutionModeValidation(t *testing.T) {
	assert.True(t, ExecutionModeCloudInvoke.Valid())
	assert.True(t, ExecutionModeCollectorLocal.Valid())
	assert.False(t, ExecutionMode("unknown").Valid())
}
