package marketdata

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRateLimitPolicyValidation(t *testing.T) {
	valid := RateLimitPolicy{
		RequestsPerSecond: 5,
		Burst:             2,
		MaxConcurrent:     1,
		Cooldown:          time.Minute,
		RequestTimeout:    2 * time.Second,
	}
	require.NoError(t, valid.Validate())

	tests := []struct {
		name   string
		mutate func(*RateLimitPolicy)
	}{
		{name: "zero rate", mutate: func(p *RateLimitPolicy) { p.RequestsPerSecond = 0 }},
		{name: "nan rate", mutate: func(p *RateLimitPolicy) { p.RequestsPerSecond = math.NaN() }},
		{name: "zero burst", mutate: func(p *RateLimitPolicy) { p.Burst = 0 }},
		{name: "zero concurrency", mutate: func(p *RateLimitPolicy) { p.MaxConcurrent = 0 }},
		{name: "zero cooldown", mutate: func(p *RateLimitPolicy) { p.Cooldown = 0 }},
		{name: "zero timeout", mutate: func(p *RateLimitPolicy) { p.RequestTimeout = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := valid
			tt.mutate(&got)
			assert.Error(t, got.Validate())
		})
	}
}

func TestKlineSpecAllowsProviderFrequenciesBeyondOneMinute(t *testing.T) {
	spec := KlineSpec{
		Markets: []string{"spot"}, Exchanges: []string{"binance"}, Frequencies: []string{"1m", "5m", "1h"},
		CompleteOHLCV: true, HasAmount: true, MaxBarsPerRequest: 1000,
		TimestampMode: TimestampModeOpen, RateLimit: validTestRateLimitPolicy(),
	}
	require.NoError(t, spec.Validate())
}

func TestInstrumentSpecRejectsPartialSnapshotContract(t *testing.T) {
	spec := InstrumentSpec{
		Markets: []string{"spot"}, Exchanges: []string{"binance"}, FullSnapshot: false,
		PageSize: 1000, RateLimit: validTestRateLimitPolicy(),
	}
	assert.Error(t, spec.Validate())
}

func validTestRateLimitPolicy() RateLimitPolicy {
	return RateLimitPolicy{RequestsPerSecond: 1, Burst: 1, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: time.Second}
}
