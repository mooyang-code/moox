package servicecatalog

import "testing"

// 删除策略实例（A 股执行计划 4.2）的调用方边界：管理台和 moox-cli 只调策略服务的 DeleteStrategyInstance；
// 随实例删除因子任务只给策略服务；策略服务不再访问 archive-mgr。
func TestStrategyInstanceDeleteCallerBoundary(t *testing.T) {
	c := mustCatalog(t)
	cases := []struct {
		service, method string
		callers         map[string]bool
		readOnly        bool
	}{
		{"trpc.moox.strategy.StrategyMgr", "DeleteStrategyInstance", map[string]bool{"console": true, "moox-cli": true}, false},
		{"trpc.moox.factor.FactorMgr", "DeleteBoundTask", map[string]bool{"strategy": true}, false},
	}
	for _, tc := range cases {
		for _, caller := range []string{"console", "moox-cli", "strategy", "monitor", "collector", "trade"} {
			if got := c.Allowed(caller, tc.service, tc.method); got != tc.callers[caller] {
				t.Errorf("%s 调用 %s：allowed=%v，期望 %v", caller, tc.method, got, tc.callers[caller])
			}
		}
		if c.ReadOnly(tc.service, tc.method) != tc.readOnly {
			t.Errorf("%s 的只读标记应为 %v", tc.method, tc.readOnly)
		}
	}
	if c.Allowed("strategy", "trpc.moox.archive.ArchiveMgr", "ListArchiveTasks") {
		t.Fatal("strategy 不再允许调用 ArchiveMgr.ListArchiveTasks")
	}
	strategy, ok := c.Service("trpc.moox.strategy.StrategyMgr")
	if !ok || strategy.TimeoutFor("DeleteStrategyInstance") != 30000 {
		t.Fatalf("DeleteStrategyInstance 的路由超时应为 30 秒")
	}
}
