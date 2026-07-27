package jobdef

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestJobDefinition_Matches_MatchingSupport_ShouldReturnTrue(t *testing.T) {
	def := JobDefinition{
		JobType: "collect.kline",
		Supports: []Support{
			{Exchange: "binance", Market: "spot", DataType: "kline"},
		},
	}
	params := &domain.CollectParams{
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
		JobType: "collect.kline",
		Supports: []Support{
			{Exchange: "binance", Market: "spot", DataType: "kline"},
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
		JobType: "collect.kline",
		Supports: []Support{
			{Exchange: "binance", Market: "spot", DataType: "kline"},
		},
	}
	assert.False(t, def.Matches(nil))
}
