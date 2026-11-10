package servicecatalog

import "testing"

// 下架名单与下架清仓（交易计划 4.7）的调用方边界：写入和清仓只有事件中心，管理台能解除禁止和预览持有者，
// moox-cli 只读；名单和持有者查询是只读方法。
func TestTradeDelistMethodsCallerBoundary(t *testing.T) {
	c := mustCatalog(t)
	const service = "trpc.moox.trade.TradeConsoleService"
	allowed := map[string]map[string]bool{
		"BlockInstrument":        {"callback-dispatcher": true},
		"UnblockInstrument":      {"callback-dispatcher": true, "console": true},
		"ListBlockedInstruments": {"callback-dispatcher": true, "console": true, "moox-cli": true},
		"ListInstrumentHolders":  {"callback-dispatcher": true, "console": true, "moox-cli": true},
		"FlattenInstrument":      {"callback-dispatcher": true},
		"GetOperatorAction":      {"callback-dispatcher": true, "console": true, "moox-cli": true},
		"SummarizeAccountPnl":    {"console": true, "moox-cli": true},
		"ListOperatorActions":    {"console": true, "moox-cli": true},
	}
	for method, callers := range allowed {
		for _, caller := range []string{"callback-dispatcher", "console", "moox-cli", "strategy", "collector"} {
			if got := c.Allowed(caller, service, method); got != callers[caller] {
				t.Errorf("%s 调用 %s：allowed=%v，期望 %v", caller, method, got, callers[caller])
			}
		}
	}
	for _, method := range []string{"ListBlockedInstruments", "ListInstrumentHolders", "SummarizeAccountPnl", "ListOperatorActions"} {
		if !c.ReadOnly(service, method) {
			t.Errorf("%s 应当是只读方法", method)
		}
	}
	for _, method := range []string{"BlockInstrument", "UnblockInstrument", "FlattenInstrument"} {
		if c.ReadOnly(service, method) {
			t.Errorf("%s 不是只读方法", method)
		}
	}
}
