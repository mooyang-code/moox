package recalcexec

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/stretchr/testify/require"
)

// 统一数据集同一标的两个 tag：现货行记下了成员参数，永续当期失败写的是 {}。按标的整理时不能被 {} 盖掉，结果与遍历顺序无关。
func TestParamRowsMergeTagsOfTheSameSubject(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for range 200 {
		rows := map[pipeline.RowKey]map[string]map[string]float64{
			{Subject: "BTC", At: at, Tag: "spot"}: {"ma__var": {"window": 20}},
			{Subject: "BTC", At: at, Tag: "swap"}: {},
			{Subject: "ETH", At: at, Tag: "spot"}: {},
			{Subject: "ETH", At: at, Tag: "swap"}: {"ma__var": {"window": 30}},
		}
		byPeriod := newParamRows(rows).byPeriod[at.Unix()]
		require.Equal(t, map[string]map[string]float64{"ma__var": {"window": 20}}, byPeriod["BTC"])
		require.Equal(t, map[string]map[string]float64{"ma__var": {"window": 30}}, byPeriod["ETH"])
	}
}
