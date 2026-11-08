package rpc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

// fakeFactorTasks 是 Factor 里的任务：任务 ID → 关联的实例。deleteErr 让下一次删除失败一次。
type fakeFactorTasks struct {
	bound     map[string]string
	deleted   []string
	deleteErr error
	listErr   error
}

func (f *fakeFactorTasks) GetFactorTask(_ context.Context, taskID string) (input.FactorTaskRecord, error) {
	instanceID, ok := f.bound[taskID]
	if !ok {
		return input.FactorTaskRecord{}, fmt.Errorf("%w：%s", input.ErrFactorTaskNotFound, taskID)
	}
	return input.FactorTaskRecord{TaskID: taskID, ResultDatasetID: "dataset_of_" + taskID, BoundInstanceID: instanceID}, nil
}

func (f *fakeFactorTasks) ListFactorTaskIDs(context.Context) (map[string]bool, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	ids := map[string]bool{}
	for taskID := range f.bound {
		ids[taskID] = true
	}
	return ids, nil
}

func (f *fakeFactorTasks) DeleteBoundTask(_ context.Context, taskID, instanceID string) error {
	if f.deleteErr != nil {
		err := f.deleteErr
		f.deleteErr = nil
		return err
	}
	if f.bound[taskID] != instanceID {
		return errors.New("关联的不是这个实例")
	}
	delete(f.bound, taskID)
	f.deleted = append(f.deleted, taskID)
	return nil
}

type fakeArchives struct {
	datasets map[string]bool
	err      error
	calls    int
}

func (f *fakeArchives) ArchivedDatasets(context.Context, string) (map[string]bool, error) {
	f.calls++
	return f.datasets, f.err
}

// deleteHarness 建出定义 s1 和一个启用过又停用的实例 i1（现货、永续各一个任务，都已关联 i1）。
func deleteHarness(t *testing.T) (*harness, *fakeFactorTasks, *fakeArchives) {
	t.Helper()
	h := newHarness(t)
	// 任务创建出来时还没有关联实例，第一次启用时关联到 i1。
	tasks := &fakeFactorTasks{bound: map[string]string{"ftask_i1": ""}}
	archives := &fakeArchives{}
	h.service.Tasks, h.service.Archives = tasks, archives
	h.createStrategy("s1", demoDSL)
	created := h.createInstance("i1", "s1", "ftask_i1", "", true)
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	tasks.bound["ftask_i1"] = "i1"
	disabled := h.setEnabled("i1", false)
	requireOK(t, int32(disabled.GetRetInfo().GetCode()), disabled.GetRetInfo().GetMsg())
	return h, tasks, archives
}

func (h *harness) deleteInstance(id string) *strategypb.DeleteStrategyInstanceRsp {
	h.t.Helper()
	rsp, err := h.service.DeleteStrategyInstance(h.ctx, &strategypb.DeleteStrategyInstanceReq{InstanceId: id})
	if err != nil {
		h.t.Fatal(err)
	}
	return rsp
}

func TestDeleteStrategyInstanceRemovesTasksAndRecords(t *testing.T) {
	h, tasks, archives := deleteHarness(t)
	rsp := h.deleteInstance("i1")
	requireOK(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
	if len(tasks.deleted) != 1 || tasks.deleted[0] != "ftask_i1" || archives.calls != 1 {
		t.Fatalf("应删除因子任务并先检查归档：deleted=%v archives=%d", tasks.deleted, archives.calls)
	}
	if _, err := h.service.Store.GetInstance(h.ctx, "i1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("实例应已删除：%v", err)
	}
	// 再删一次（例如上次的响应丢了）：实例不存在算成功。
	again := h.deleteInstance("i1")
	requireOK(t, int32(again.GetRetInfo().GetCode()), again.GetRetInfo().GetMsg())
	// 定义下已经没有实例，可以删除定义。
	deleted, _ := h.service.DeleteStrategy(h.ctx, &strategypb.DeleteStrategyReq{StrategyId: "s1"})
	requireOK(t, int32(deleted.GetRetInfo().GetCode()), deleted.GetRetInfo().GetMsg())
}

func TestDeleteStrategyInstanceRejectsEnabledInstance(t *testing.T) {
	h, tasks, _ := deleteHarness(t)
	enabled := h.setEnabled("i1", true)
	requireOK(t, int32(enabled.GetRetInfo().GetCode()), enabled.GetRetInfo().GetMsg())
	rsp := h.deleteInstance("i1")
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "先停用")
	if len(tasks.deleted) != 0 {
		t.Fatalf("启用中的实例不能删任务：%v", tasks.deleted)
	}
}

// 有归档任务、任务关联的不是本实例：拒绝，而且在删任何任务之前。
func TestDeleteStrategyInstanceRejectsBeforeDeletingAnything(t *testing.T) {
	h, tasks, archives := deleteHarness(t)
	archives.datasets = map[string]bool{"dataset_of_ftask_i1": true}
	rsp := h.deleteInstance("i1")
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "归档任务")
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_INVALID_PARAM || !strings.Contains(rsp.GetRetInfo().GetMsg(), "dataset_of_ftask_i1") {
		t.Fatalf("应按参数错误拒绝并列出数据集：%+v", rsp.GetRetInfo())
	}

	archives.datasets = nil
	archives.err = errors.New("no route")
	rsp = h.deleteInstance("i1")
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "读不到归档任务")

	archives.err = nil
	tasks.bound["ftask_i1"] = ""
	rsp = h.deleteInstance("i1")
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "还没有关联本实例")
	tasks.bound["ftask_i1"] = "other"
	rsp = h.deleteInstance("i1")
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "关联的是实例 other")
	if len(tasks.deleted) != 0 {
		t.Fatalf("拒绝时不能删任何任务：%v", tasks.deleted)
	}
	if _, err := h.service.Store.GetInstance(h.ctx, "i1"); err != nil {
		t.Fatal(err)
	}
}

// 删除因子任务后、删除实例记录前失败：实例停用着留下来，列表与详情标出删除未完成，启用给出明确报错，再删一次能做完。
func TestDeleteStrategyInstanceResumesAfterPartialFailure(t *testing.T) {
	h, tasks, _ := deleteHarness(t)
	tasks.deleteErr = errors.New("timeout")
	rsp := h.deleteInstance("i1")
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "可以重新执行删除")

	// 模拟第一次删除已经删掉了任务、响应丢失。
	delete(tasks.bound, "ftask_i1")
	listed, err := h.service.ListStrategyInstances(h.ctx, &strategypb.ListStrategyInstancesReq{})
	if err != nil {
		t.Fatal(err)
	}
	requireOK(t, int32(listed.GetRetInfo().GetCode()), listed.GetRetInfo().GetMsg())
	if got := listed.GetInstances()[0].GetMissingFactorTasks(); len(got) != 1 || got[0] != "ftask_i1" {
		t.Fatalf("列表应标出已删除的任务：%v", got)
	}
	detail, _ := h.service.GetStrategyInstance(h.ctx, &strategypb.GetStrategyInstanceReq{InstanceId: "i1"})
	requireOK(t, int32(detail.GetRetInfo().GetCode()), detail.GetRetInfo().GetMsg())
	if got := detail.GetInstance().GetMissingFactorTasks(); len(got) != 1 {
		t.Fatalf("详情应标出已删除的任务：%v", got)
	}
	resolves := h.resolver.calls
	enabled := h.setEnabled("i1", true)
	requireFail(t, int32(enabled.GetRetInfo().GetCode()), enabled.GetRetInfo().GetMsg(), errTaskDeleted)
	if h.resolver.calls != resolves {
		t.Fatal("任务已删除时不应进入解析")
	}
	current, _ := h.service.Store.GetInstance(h.ctx, "i1")
	if current.Enabled || current.SessionID != nil {
		t.Fatalf("启用失败不能留下会话：%+v", current)
	}

	rsp = h.deleteInstance("i1")
	requireOK(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
	if _, err := h.service.Store.GetInstance(h.ctx, "i1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("重试后实例应已删除：%v", err)
	}
}

// Factor 读不到时列表照常返回，只是不标删除未完成。
func TestListInstancesIgnoresFactorFailure(t *testing.T) {
	h, tasks, _ := deleteHarness(t)
	tasks.listErr = errors.New("unavailable")
	listed, _ := h.service.ListStrategyInstances(h.ctx, &strategypb.ListStrategyInstancesReq{})
	requireOK(t, int32(listed.GetRetInfo().GetCode()), listed.GetRetInfo().GetMsg())
	if len(listed.GetInstances()) != 1 || len(listed.GetInstances()[0].GetMissingFactorTasks()) != 0 {
		t.Fatalf("Factor 读不到时只返回实例：%+v", listed.GetInstances())
	}
}

// 挂着组合账户的停用实例也能删，账户绑定随实例一起清掉。
func TestDeleteStrategyInstanceWithLogicalAccount(t *testing.T) {
	h := newHarness(t)
	tasks := &fakeFactorTasks{bound: map[string]string{"ftask_acct": ""}}
	h.service.Tasks, h.service.Archives = tasks, &fakeArchives{}
	h.createStrategy("s1", demoDSL)
	created := h.createInstance("ia", "s1", "ftask_acct", "acct-1", true)
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	tasks.bound["ftask_acct"] = "ia"
	disabled := h.setEnabled("ia", false)
	requireOK(t, int32(disabled.GetRetInfo().GetCode()), disabled.GetRetInfo().GetMsg())
	rsp := h.deleteInstance("ia")
	requireOK(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
	sort.Strings(tasks.deleted)
	if len(tasks.deleted) != 1 {
		t.Fatalf("应删除关联的任务：%v", tasks.deleted)
	}
}
