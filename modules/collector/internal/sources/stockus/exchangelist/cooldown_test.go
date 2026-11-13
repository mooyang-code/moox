package exchangelist

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/packages/cnstock"
	"github.com/stretchr/testify/require"
)

// 东方财富主机被限流后：对照交易所的请求直接失败，错误经过各层包装仍能 errors.Is 到 cnstock.ErrCoolingDown，
// 并且冷却期间两个主机都是零请求（走进程内的限流器，与线上一致）。
func TestLookupStopsWhileHostsAreCooling(t *testing.T) {
	cfg := cnstock.DefaultRequestLimitConfig()
	cfg.CooldownFailures = 1
	require.NoError(t, cnstock.ConfigureRequestLimit(cfg, nil))
	t.Cleanup(looseRequestLimit)

	lists, _ := sampleLists(t)
	fake := newFakeClist(lists)
	fake.fail = func(string, int, int) int { return http.StatusTooManyRequests }
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	router := newHostRouter(server, server)
	routed := testConfig("")
	routed.HTTPClient = &http.Client{Transport: router}

	// 第一次：push2 返回 429，立即进入冷却（这次请求自己得到原来的限流错误）。
	_, err := Lookup(context.Background(), routed, []string{"AAPL"})
	require.ErrorIs(t, err, cnstock.ErrRateLimited)
	require.Equal(t, 1, router.total("push2.eastmoney.com"))

	// 第二次：push2 在冷却，改用 push2test；它同样 429 后也进入冷却。
	_, err = Lookup(context.Background(), routed, []string{"AAPL"})
	require.ErrorIs(t, err, cnstock.ErrRateLimited)
	require.Equal(t, 1, router.total("push2.eastmoney.com"), "push2 冷却期间零请求")
	require.Equal(t, 1, router.total("push2test.eastmoney.com"))

	// 第三次：两个主机都在冷却，整次失败，不发任何请求。
	_, err = Lookup(context.Background(), routed, []string{"AAPL"})
	require.ErrorIs(t, err, cnstock.ErrCoolingDown)
	require.Equal(t, 1, router.total("push2.eastmoney.com"))
	require.Equal(t, 1, router.total("push2test.eastmoney.com"))
}
