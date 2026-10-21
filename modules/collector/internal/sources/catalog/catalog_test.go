package catalog

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

// 本地执行端按计划随请求给出的限流与超时覆盖进程环境的默认值。
func TestBinanceRateLimitComesFromDeps(t *testing.T) {
	provider, err := Default().Provider("crypto", marketdata.InstrumentSwap, "binance", "", marketdata.Deps{Settings: marketdata.SourceSettings{
		RateLimit: marketdata.RateLimitPolicy{RequestsPerSecond: 40, Burst: 40, MaxConcurrent: 16, RequestTimeout: 3 * time.Second},
	}})
	require.NoError(t, err)
	limit := provider.(interface{ KlineSpec() marketdata.KlineSpec }).KlineSpec().RateLimit
	require.Equal(t, 40.0, limit.RequestsPerSecond)
	require.Equal(t, 40, limit.Burst)
	require.Equal(t, 16, limit.MaxConcurrent)
	require.Equal(t, 3*time.Second, limit.RequestTimeout)

	provider, err = Default().Provider("crypto", marketdata.InstrumentSwap, "binance", "", marketdata.Deps{})
	require.NoError(t, err)
	require.Equal(t, 1, provider.(interface{ KlineSpec() marketdata.KlineSpec }).KlineSpec().RateLimit.MaxConcurrent, "没给时按环境与默认")
}
