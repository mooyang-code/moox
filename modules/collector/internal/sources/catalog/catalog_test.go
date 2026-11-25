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

// 字段的中文名与分组只在字段规格里登记：目录里每个附加字段都要带显示名与已定的一级分组，同一字段在各来源下名字一致。
func TestEveryFieldSpecHasDisplayNameAndGroup(t *testing.T) {
	groups := map[string]bool{
		marketdata.GroupTrading: true, marketdata.GroupFinance: true, marketdata.GroupIndustry: true,
		marketdata.GroupAdjust: true, marketdata.GroupDerivatives: true,
	}
	offers := FieldOffers()
	require.NotEmpty(t, offers)
	names := map[string]string{}
	for _, offer := range offers {
		spec := offer.Spec
		require.NotEmpty(t, spec.Name, "%s/%s", offer.MarketID, spec.ID)
		require.True(t, groups[spec.Group], "%s/%s 的分组 %q 不在已定的分组里", offer.MarketID, spec.ID, spec.Group)
		key := string(offer.MarketID) + "/" + spec.ID
		if name, ok := names[key]; ok {
			require.Equal(t, name, spec.Name, key)
		}
		names[key] = spec.Name
	}
}
