package rpc

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/packages/commonpb"
	trpc "trpc.group/trpc-go/trpc-go"
)

// 读取回放的返回码：跨空间访问是请求的问题（参数无效）；查询阶段的存储错误是内部错误，不能笼统地报参数无效。
func TestReplayLookupMapsSpaceAndStoreErrors(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	started, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_a"}, StartTime: "2026-09-02", EndTime: "2026-09-03"})
	requireOK(t, int32(started.GetRetInfo().GetCode()), started.GetRetInfo().GetMsg())
	id := started.GetReplay().GetReplayId()

	other := trpc.BackgroundContext()
	trpc.SetMetaData(other, "x-space-id", []byte("stock"))
	for name, code := range map[string]commonpb.ErrorCode{
		"GetReplay":      mustCode(h.service.GetReplay(other, &strategypb.GetReplayReq{ReplayId: id})),
		"ListReplayBars": mustCode(h.service.ListReplayBars(other, &strategypb.ListReplayBarsReq{ReplayId: id})),
		"CancelReplay":   mustCode(h.service.CancelReplay(other, &strategypb.CancelReplayReq{ReplayId: id})),
	} {
		if code != commonpb.ErrorCode_INVALID_PARAM {
			t.Fatalf("%s 跨空间访问应返回参数无效：%v", name, code)
		}
	}

	if err := h.service.Store.Close(); err != nil {
		t.Fatal(err)
	}
	for name, code := range map[string]commonpb.ErrorCode{
		"GetReplay":      mustCode(h.service.GetReplay(h.ctx, &strategypb.GetReplayReq{ReplayId: id})),
		"ListReplayBars": mustCode(h.service.ListReplayBars(h.ctx, &strategypb.ListReplayBarsReq{ReplayId: id})),
		"CancelReplay":   mustCode(h.service.CancelReplay(h.ctx, &strategypb.CancelReplayReq{ReplayId: id})),
	} {
		if code != commonpb.ErrorCode_INNER_ERR {
			t.Fatalf("%s 查询阶段的存储错误应返回内部错误：%v", name, code)
		}
	}
}

// retInfoGetter 是带 RetInfo 的响应。
type retInfoGetter interface{ GetRetInfo() *commonpb.RetInfo }

func mustCode[T retInfoGetter](rsp T, err error) commonpb.ErrorCode {
	if err != nil {
		return commonpb.ErrorCode_INNER_ERR
	}
	return rsp.GetRetInfo().GetCode()
}

// A 股回放记录带上日历，页面据此按上海日期显示区间。
func TestStartReplayRecordsCalendar(t *testing.T) {
	h := newHarness(t)
	h.resolver.calendar = "cn_stock"
	shanghai := time.FixedZone("CST", 8*3600)
	h.resolver.view = input.ViewInfo{ViewID: "view_a", ActiveIndexID: "idx", Generation: "idx@b1", IndexedFrom: time.Date(2025, 1, 2, 0, 0, 0, 0, shanghai), IndexedTo: time.Date(2026, 10, 9, 0, 0, 0, 0, shanghai)}
	h.createStrategy("s1", demoDSL)
	rsp, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_a"}, StartTime: "2026-09-01", EndTime: "2026-10-01"})
	requireOK(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
	if rsp.GetReplay().GetCalendar() != "cn_stock" {
		t.Fatalf("回放记录应带上日历：%q", rsp.GetReplay().GetCalendar())
	}
	got, _ := h.service.GetReplay(h.ctx, &strategypb.GetReplayReq{ReplayId: rsp.GetReplay().GetReplayId()})
	if got.GetReplay().GetCalendar() != "cn_stock" {
		t.Fatalf("读取回放也应带上日历：%q", got.GetReplay().GetCalendar())
	}
}

// 从实例发起的研究回放：省略 factor_tasks 时读实例关联的任务；给了但与实例不一致被拒绝；绑定两个任务的实例也能回放。
func TestInstanceReplayUsesInstanceTasks(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	h.createInstance("i1", "s1", "ftask_i1", "", true)
	window := &strategypb.StartReplayReq{InstanceId: "i1", StartTime: "2026-09-02", EndTime: "2026-09-03"}

	omitted, _ := h.service.StartReplay(h.ctx, window)
	requireOK(t, int32(omitted.GetRetInfo().GetCode()), omitted.GetRetInfo().GetMsg())
	if task := omitted.GetReplay().GetTasks()["spot"]; task.GetFactorTaskId() != "ftask_i1" || len(omitted.GetReplay().GetTasks()) != 1 {
		t.Fatalf("省略 factor_tasks 应读实例关联的任务：%+v", omitted.GetReplay().GetTasks())
	}

	mismatch := &strategypb.StartReplayReq{InstanceId: "i1", StartTime: "2026-09-02", EndTime: "2026-09-03", FactorTasks: map[string]string{"spot": "ftask_a"}}
	rejected, _ := h.service.StartReplay(h.ctx, mismatch)
	requireFail(t, int32(rejected.GetRetInfo().GetCode()), rejected.GetRetInfo().GetMsg(), "必须读取实例关联的因子任务")

	same := &strategypb.StartReplayReq{InstanceId: "i1", StartTime: "2026-09-02", EndTime: "2026-09-03", FactorTasks: map[string]string{"spot": "ftask_i1"}}
	accepted, _ := h.service.StartReplay(h.ctx, same)
	requireOK(t, int32(accepted.GetRetInfo().GetCode()), accepted.GetRetInfo().GetMsg())

	// 绑定现货与永续两个任务的实例：研究回放按两个任务读取（不再拒绝多任务实例）。
	dual, err := h.service.CreateStrategyInstance(h.ctx, &strategypb.CreateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{InstanceId: "i2", StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_d1", "swap": "ftask_d2swap"}, Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	requireOK(t, int32(dual.GetRetInfo().GetCode()), dual.GetRetInfo().GetMsg())
	both, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{InstanceId: "i2", StartTime: "2026-09-02", EndTime: "2026-09-03", SwapFeeBps: 5})
	requireOK(t, int32(both.GetRetInfo().GetCode()), both.GetRetInfo().GetMsg())
	if tasks := both.GetReplay().GetTasks(); len(tasks) != 2 || tasks["spot"].GetFactorTaskId() != "ftask_d1" || tasks["swap"].GetFactorTaskId() != "ftask_d2swap" || both.GetReplay().GetInstanceId() != "i2" {
		t.Fatalf("双任务实例的回放应按两个任务读取：%+v", both.GetReplay())
	}
}
