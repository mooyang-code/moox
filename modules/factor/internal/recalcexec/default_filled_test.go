package recalcexec

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/stretchr/testify/require"
)

// 按默认参数新补的期次只算实际写出了值的：整期都失败的期次没有被补出。
func TestFilterDefaultFilledKeepsOnlyWrittenPeriods(t *testing.T) {
	t0 := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	filled := map[string][]time.Time{"ema__var": {t0, t1}}
	items := map[pipeline.ItemKey]pipeline.ItemStatus{
		{Member: "ema__var", Subject: "BTC", Period: t0}: pipeline.ItemWritten,
		{Member: "ema__var", Subject: "BTC", Period: t1}: pipeline.ItemFailed,
	}
	require.Equal(t, map[string][]time.Time{"ema__var": {t0}}, filterDefaultFilled(filled, items))
	require.Equal(t, filled, filterDefaultFilled(filled, nil), "没有逐项结果时原样返回")
}
