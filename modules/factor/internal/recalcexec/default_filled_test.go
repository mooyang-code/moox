package recalcexec

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/stretchr/testify/require"
)

// 按默认参数新补的期次只算确实写出了值、且该标的原来没有这个成员参数的：计算失败的标的、原来就有参数的标的都不算。
func TestFilterDefaultFilledKeepsOnlyNewlyFilledPeriods(t *testing.T) {
	t0 := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	t2 := t1.Add(time.Minute)
	selection := Selection{Subjects: []string{"BTC", "ETH"}}
	base := paramRows{byPeriod: map[int64]map[string]map[string]map[string]float64{
		// t0：BTC 原来没有参数（会被默认参数补出），ETH 原来有
		t0.Unix(): {"ETH": {"ema__var": {"span": 9}}},
		// t1：BTC 缺参数但计算失败，ETH 原来有参数且成功——没有任何新补的值
		t1.Unix(): {"ETH": {"ema__var": {"span": 9}}},
	}}
	filled := map[string][]time.Time{"ema__var": {t0, t1, t2}}
	items := map[pipeline.ItemKey]pipeline.ItemStatus{
		{Member: "ema__var", Subject: "BTC", Period: t0}: pipeline.ItemWritten,
		{Member: "ema__var", Subject: "ETH", Period: t0}: pipeline.ItemWritten,
		{Member: "ema__var", Subject: "BTC", Period: t1}: pipeline.ItemFailed,
		{Member: "ema__var", Subject: "ETH", Period: t1}: pipeline.ItemWritten,
		{Member: "ema__var", Subject: "BTC", Period: t2}: pipeline.ItemFailed,
	}
	require.Equal(t, map[string][]time.Time{"ema__var": {t0}}, filterDefaultFilled(selection, base, filled, items))
	require.Equal(t, filled, filterDefaultFilled(selection, base, filled, nil), "没有逐项结果时原样返回")
}
