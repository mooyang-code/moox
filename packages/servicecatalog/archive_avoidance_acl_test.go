package servicecatalog

import "testing"

// 归档避让（数据归档执行计划 3.3）：archive-mgr 在 Collector 上只能调用只读的 GetCollectorStatus。
func TestArchiveMgrOnlyReadsCollectorStatus(t *testing.T) {
	c := mustCatalog(t)
	const path = "trpc.moox.collector.CollectMgr"
	service, ok := c.Service(path)
	if !ok {
		t.Fatalf("目录里没有 %s", path)
	}
	for _, method := range service.Methods {
		want := method == "GetCollectorStatus"
		if got := c.Allowed("archive-mgr", path, method); got != want {
			t.Errorf("archive-mgr 调用 CollectMgr.%s：allowed=%v，期望 %v", method, got, want)
		}
	}
	if !c.ReadOnly(path, "GetCollectorStatus") {
		t.Fatal("GetCollectorStatus 必须是只读方法")
	}
	// 原有调用方不受影响。
	for _, caller := range []string{"console", "moox-cli"} {
		if !c.Allowed(caller, path, "GetCollectorStatus") {
			t.Errorf("%s 应仍能调用 GetCollectorStatus", caller)
		}
	}
	if c.Allowed("monitor", path, "GetCollectorStatus") {
		t.Error("monitor 不应获得 GetCollectorStatus")
	}
}
