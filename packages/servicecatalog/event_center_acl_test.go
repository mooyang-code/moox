package servicecatalog

import "testing"

// CreateEventRule 的返回里有验证密钥明文：只有管理台（console）能调用，moox-cli 和经 gateway call 的操作员都不行；
// 其余事件中心接口 console 与 moox-cli 都能调用。
func TestCreateEventRuleIsConsoleOnly(t *testing.T) {
	c := mustCatalog(t)
	const service = "trpc.moox.callbackdispatcher.CallbackDispatcherService"
	if !c.Allowed("console", service, "CreateEventRule") {
		t.Fatal("console 必须能新建事件规则")
	}
	for _, caller := range []string{"moox-cli", "collector", "trade", "admin", "callback-dispatcher"} {
		if c.Allowed(caller, service, "CreateEventRule") {
			t.Fatalf("%s 不应该能调用 CreateEventRule", caller)
		}
	}
	for _, method := range []string{"ListEventRules", "UpdateEventRule", "DeleteEventRule", "TestEventRule", "ListEventExecutions", "RetryEventExecution", "CancelEventExecution"} {
		if !c.Allowed("console", service, method) || !c.Allowed("moox-cli", service, method) {
			t.Fatalf("console 与 moox-cli 都应该能调用 %s", method)
		}
	}
}
