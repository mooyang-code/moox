package bootstrap

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	"github.com/mooyang-code/moox/modules/collector/internal/tradingstatus"
	"github.com/stretchr/testify/require"
)

// 按各市场 market.yaml 组装交易状态任务：股票三个市场走腾讯；crypto 的现货、永续走币安，只在开关打开时装。
func TestTradingStatusJobsFollowMarketConfigs(t *testing.T) {
	listers := subjectsync.Listers{}
	runner, err := newTradingStatusRunner(nil, listers, nil, false)
	require.NoError(t, err)
	jobs := map[string]tradingstatus.Job{}
	for _, job := range runner.Jobs {
		jobs[job.SpaceID+"/"+job.MarketType] = job
	}
	require.Len(t, jobs, 3, "crypto 关掉时只有股票")
	require.Equal(t, "5 9,12 * * 1-5", jobs["stockcn/equity"].Cron)
	require.Equal(t, "Asia/Shanghai", jobs["stockcn/equity"].Timezone)
	require.Equal(t, tradingstatus.TencentSource{Client: jobs["stockcn/equity"].Source.(tradingstatus.TencentSource).Client, Market: tradingstatus.MarketCN}, jobs["stockcn/equity"].Source)
	require.Equal(t, tradingstatus.MarketHK, jobs["stockhk/equity"].Source.(tradingstatus.TencentSource).Market)
	require.Equal(t, "America/New_York", jobs["stockus/equity"].Timezone)

	_, err = newTradingStatusRunner(nil, listers, nil, true)
	require.ErrorContains(t, err, "没有币安", "开着 crypto 却没有币安 lister 要报错")
}
