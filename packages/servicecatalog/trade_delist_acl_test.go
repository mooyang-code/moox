package servicecatalog

import "testing"

// 下架名单与下架记录的调用方边界：下架处理在 Trade 进程内完成，这几个方法都没有机器调用方；
// 管理台能解除禁止和预览持有者，moox-cli 只读；名单、持有者与下架记录的查询是只读方法。
func TestTradeDelistMethodsCallerBoundary(t *testing.T) {
	c := mustCatalog(t)
	const service = "trpc.moox.trade.TradeConsoleService"
	allowed := map[string]map[string]bool{
		"UnblockInstrument":      {"console": true},
		"ListBlockedInstruments": {"console": true, "moox-cli": true},
		"ListInstrumentHolders":  {"console": true, "moox-cli": true},
		"ListDelists":            {"console": true, "moox-cli": true},
		"GetOperatorAction":      {"console": true, "moox-cli": true},
		"SummarizeAccountPnl":    {"console": true, "moox-cli": true},
		"ListOperatorActions":    {"console": true, "moox-cli": true},
	}
	for method, callers := range allowed {
		for _, caller := range []string{"console", "moox-cli", "strategy", "collector", "trade", "monitor"} {
			if got := c.Allowed(caller, service, method); got != callers[caller] {
				t.Errorf("%s 调用 %s：allowed=%v，期望 %v", caller, method, got, callers[caller])
			}
		}
	}
	for _, method := range []string{"ListBlockedInstruments", "ListInstrumentHolders", "ListDelists", "SummarizeAccountPnl", "ListOperatorActions"} {
		if !c.ReadOnly(service, method) {
			t.Errorf("%s 应当是只读方法", method)
		}
	}
	if c.ReadOnly(service, "UnblockInstrument") {
		t.Error("UnblockInstrument 不是只读方法")
	}
	for _, method := range []string{"BlockInstrument", "FlattenInstrument"} {
		if c.Allowed("console", service, method) || c.Allowed("moox-cli", service, method) {
			t.Errorf("%s 已删除，不应再放行", method)
		}
	}
}
