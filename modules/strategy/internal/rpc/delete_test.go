package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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
	// block 为真时读取一直等到 ctx 结束（模拟 Factor 很慢）。
	block bool
	// recalc 是任务正在进行的补算作业：Factor 的删除前检查据此拒绝。
	recalc map[string][]string
	// spaces 是任务所在的空间，没登记的在 crypto。
	spaces  map[string]string
	checked []string
}

func (f *fakeFactorTasks) CheckBoundTask(_ context.Context, taskID, instanceID string) error {
	f.checked = append(f.checked, taskID)
	if jobs := f.recalc[taskID]; len(jobs) > 0 {
		return &input.RejectedError{Operation: "检查因子任务能否删除", Msg: "因子任务 " + taskID + " 有补算正在进行（" + strings.Join(jobs, "、") + "），请先取消补算"}
	}
	if f.bound[taskID] != instanceID {
		return &input.RejectedError{Operation: "检查因子任务能否删除", Msg: "关联的不是这个实例"}
	}
	return nil
}

func (f *fakeFactorTasks) GetFactorTask(ctx context.Context, taskID string) (input.FactorTaskRecord, error) {
	if f.block {
		<-ctx.Done()
		return input.FactorTaskRecord{}, ctx.Err()
	}
	instanceID, ok := f.bound[taskID]
	if !ok {
		return input.FactorTaskRecord{}, fmt.Errorf("%w：%s", input.ErrFactorTaskNotFound, taskID)
	}
	space := f.spaces[taskID]
	if space == "" {
		space = "crypto"
	}
	return input.FactorTaskRecord{SpaceID: space, ResultDatasetID: "dataset_of_" + taskID, BoundInstanceID: instanceID}, nil
}

func (f *fakeFactorTasks) ListFactorTaskIDs(ctx context.Context) (map[string]bool, error) {
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
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
		return &input.RejectedError{Operation: "删除因子任务", Msg: "关联的不是这个实例"}
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

// 有归档任务、有回放或补算在进行：拒绝，而且在删任何任务之前。
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

	tasks.recalc = map[string][]string{"ftask_i1": {"job-1"}}
	rsp = h.deleteInstance("i1")
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "请先取消补算")
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_INVALID_PARAM || !strings.Contains(rsp.GetRetInfo().GetMsg(), "job-1") {
		t.Fatalf("补算在进行应按参数错误拒绝并列出作业：%+v", rsp.GetRetInfo())
	}
	tasks.recalc = nil

	instanceID, sessionID := "i1", "sess-old"
	if err := h.service.Store.CreateReplay(h.ctx, store.Replay{
		ReplayID: "rep-1", InstanceID: &instanceID, SessionID: &sessionID, DSLYaml: demoDSL, DSLHash: "sha256:x", Calendar: "crypto_24x7", SpaceID: "crypto",
		Tasks:     map[string]store.ReplayTask{"spot": {FactorTaskID: "ftask_i1", ViewID: "view_factor_i1", ViewGeneration: "idx@1"}},
		StartTime: h.now.Add(-48 * time.Hour), EndTime: h.now, CreatedAt: h.now,
	}); err != nil {
		t.Fatal(err)
	}
	rsp = h.deleteInstance("i1")
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "请先取消回放")
	if len(tasks.deleted) != 0 {
		t.Fatalf("拒绝时不能删任何任务：%v", tasks.deleted)
	}
	if _, err := h.service.Store.GetInstance(h.ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	cancelled, _ := h.service.CancelReplay(h.ctx, &strategypb.CancelReplayReq{ReplayId: "rep-1"})
	requireOK(t, int32(cancelled.GetRetInfo().GetCode()), cancelled.GetRetInfo().GetMsg())
	rsp = h.deleteInstance("i1")
	requireOK(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
}

// 从没启用过的实例：任务没有绑定到它，任务保留，只删实例；任务可以登记到新实例上。
func TestDeleteNeverEnabledInstanceKeepsTasks(t *testing.T) {
	h := newHarness(t)
	tasks := &fakeFactorTasks{bound: map[string]string{"ftask_spot_1m": ""}}
	h.service.Tasks, h.service.Archives = tasks, &fakeArchives{datasets: map[string]bool{"dataset_of_ftask_spot_1m": true}}
	h.createStrategy("s1", demoDSL)
	requireOK(t, int32(h.createInstance("i0", "s1", "ftask_spot_1m", "", false).GetRetInfo().GetCode()), "")
	rsp := h.deleteInstance("i0")
	requireOK(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
	if len(tasks.deleted) != 0 || tasks.bound["ftask_spot_1m"] != "" {
		t.Fatalf("没绑定的任务要保留（归档任务也不影响）：deleted=%v", tasks.deleted)
	}
	if _, err := h.service.Store.GetInstance(h.ctx, "i0"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("实例应已删除：%v", err)
	}
	again := h.createInstance("i0b", "s1", "ftask_spot_1m", "", false)
	requireOK(t, int32(again.GetRetInfo().GetCode()), again.GetRetInfo().GetMsg())
}

// 实例引用的任务绑定在别的实例上：任务保留，只删本实例。
func TestDeleteInstanceKeepsTaskBoundElsewhere(t *testing.T) {
	h, tasks, _ := deleteHarness(t)
	tasks.bound["ftask_i1"] = "other"
	rsp := h.deleteInstance("i1")
	requireOK(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
	if len(tasks.deleted) != 0 || tasks.bound["ftask_i1"] != "other" {
		t.Fatalf("绑定在别的实例上的任务要保留：deleted=%v", tasks.deleted)
	}
	if _, err := h.service.Store.GetInstance(h.ctx, "i1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("实例应已删除：%v", err)
	}
}

// 混合：启用到一半，现货任务已绑定到本实例、永续任务还没绑定——只删现货任务，永续任务保留；
// 补算与归档只看要删的任务。
func TestDeleteHalfEnabledInstanceDeletesOnlyBoundTask(t *testing.T) {
	h := newHarness(t)
	tasks := &fakeFactorTasks{bound: map[string]string{"ftask_mix_spot": "", "ftask_mix_swap": ""},
		recalc: map[string][]string{"ftask_mix_swap": {"job-keep"}}}
	archives := &fakeArchives{datasets: map[string]bool{"dataset_of_ftask_mix_swap": true}}
	h.service.Tasks, h.service.Archives = tasks, archives
	h.createStrategy("s1", demoDSL)
	created, err := h.service.CreateStrategyInstance(h.ctx, &strategypb.CreateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{
		InstanceId: "imix", StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_mix_spot", "swap": "ftask_mix_swap"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	tasks.bound["ftask_mix_spot"] = "imix"
	rsp := h.deleteInstance("imix")
	requireOK(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
	if len(tasks.deleted) != 1 || tasks.deleted[0] != "ftask_mix_spot" {
		t.Fatalf("只删绑定到本实例的任务：%v", tasks.deleted)
	}
	if _, ok := tasks.bound["ftask_mix_swap"]; !ok {
		t.Fatal("没绑定的永续任务要保留")
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
	tasks := &fakeFactorTasks{bound: map[string]string{"ftask_acct": "", "ftask_acct2": ""}}
	h.service.Tasks, h.service.Archives = tasks, &fakeArchives{}
	h.createStrategy("s1", demoDSL)
	created := h.createInstance("ia", "s1", "ftask_acct", "acct-1", true)
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	tasks.bound["ftask_acct"] = "ia"
	disabled := h.setEnabled("ia", false)
	requireOK(t, int32(disabled.GetRetInfo().GetCode()), disabled.GetRetInfo().GetMsg())
	releases := len(h.owner.releases)
	rsp := h.deleteInstance("ia")
	requireOK(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
	if len(tasks.deleted) != 1 || tasks.deleted[0] != "ftask_acct" {
		t.Fatalf("应删除关联的任务：%v", tasks.deleted)
	}
	// 停用时已经向 Trade 释放了会话；删除不再联系 Trade，账户绑定随实例一起清掉，账户可以挂到新实例上启用。
	if len(h.owner.releases) != releases || h.owner.owner["acct-1"] != "" {
		t.Fatalf("删除不应再联系 Trade，账户也不应还有所有者：releases=%v owner=%v", h.owner.releases, h.owner.owner)
	}
	next := h.createInstance("ib", "s1", "ftask_acct2", "acct-1", true)
	requireOK(t, int32(next.GetRetInfo().GetCode()), next.GetRetInfo().GetMsg())
	if !next.GetInstance().GetEnabled() || !strings.HasPrefix(h.owner.owner["acct-1"], "ib/") {
		t.Fatalf("账户应能挂到新实例上启用：%+v owner=%v", next.GetInstance(), h.owner.owner)
	}
}

// Factor 很慢时列表与详情在预算内照常返回，只是不标删除未完成。
func TestListAndGetInstanceDoNotWaitForSlowFactor(t *testing.T) {
	h, tasks, _ := deleteHarness(t)
	tasks.block = true
	started := time.Now()
	listed, _ := h.service.ListStrategyInstances(h.ctx, &strategypb.ListStrategyInstancesReq{})
	requireOK(t, int32(listed.GetRetInfo().GetCode()), listed.GetRetInfo().GetMsg())
	detail, _ := h.service.GetStrategyInstance(h.ctx, &strategypb.GetStrategyInstanceReq{InstanceId: "i1"})
	requireOK(t, int32(detail.GetRetInfo().GetCode()), detail.GetRetInfo().GetMsg())
	if elapsed := time.Since(started); elapsed > 2*missingTasksBudget+time.Second {
		t.Fatalf("列表与详情不应等 Factor 超过预算：%v", elapsed)
	}
	if len(listed.GetInstances()[0].GetMissingFactorTasks()) != 0 || len(detail.GetInstance().GetMissingFactorTasks()) != 0 {
		t.Fatal("读不到 Factor 时不标删除未完成")
	}
}

// 本空间里按定义或 DSL 发起、读取要删的任务的回放也挡住删除；别的空间的、只读保留任务的不挡。
func TestDeleteRejectsResearchReplayReadingDeletedTask(t *testing.T) {
	h, tasks, _ := deleteHarness(t)
	tasks.bound["ftask_kept"] = ""
	strategyID := "s1"
	create := func(id, space, taskID string) {
		t.Helper()
		if err := h.service.Store.CreateReplay(h.ctx, store.Replay{
			ReplayID: id, StrategyID: &strategyID, DSLYaml: demoDSL, DSLHash: "sha256:x", Calendar: "crypto_24x7", SpaceID: space,
			Tasks:     map[string]store.ReplayTask{"spot": {FactorTaskID: taskID, ViewID: "view_factor_x", ViewGeneration: "idx@1"}},
			StartTime: h.now.Add(-48 * time.Hour), EndTime: h.now, CreatedAt: h.now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	create("rep-other-space", "stockcn", "ftask_i1")
	create("rep-kept-task", "crypto", "ftask_kept")
	create("rep-research", "crypto", "ftask_i1")
	rsp := h.deleteInstance("i1")
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "请先取消回放")
	if !strings.Contains(rsp.GetRetInfo().GetMsg(), "rep-research") || strings.Contains(rsp.GetRetInfo().GetMsg(), "rep-other-space") || strings.Contains(rsp.GetRetInfo().GetMsg(), "rep-kept-task") {
		t.Fatalf("只应列出读取要删任务的本空间回放：%s", rsp.GetRetInfo().GetMsg())
	}
	if len(tasks.deleted) != 0 || len(tasks.checked) != 0 {
		t.Fatalf("回放挡住时不能检查或删除任务：checked=%v deleted=%v", tasks.checked, tasks.deleted)
	}
	cancelled, _ := h.service.CancelReplay(h.ctx, &strategypb.CancelReplayReq{ReplayId: "rep-research"})
	requireOK(t, int32(cancelled.GetRetInfo().GetCode()), cancelled.GetRetInfo().GetMsg())
	rsp = h.deleteInstance("i1")
	requireOK(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
}

// 要删的任务不在实例的空间：在动手前拒绝。
func TestDeleteRejectsTaskInAnotherSpace(t *testing.T) {
	h, tasks, _ := deleteHarness(t)
	tasks.spaces = map[string]string{"ftask_i1": "stockcn"}
	rsp := h.deleteInstance("i1")
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "不是实例所在的空间")
	if rsp.GetIncomplete() || len(tasks.deleted) != 0 {
		t.Fatalf("什么都没删：%+v deleted=%v", rsp, tasks.deleted)
	}
}

// 只有动手之后的失败才标 incomplete：读不到归档任务时什么都还没删；删任务时超时则是删除未完成。
func TestDeleteMarksIncompleteOnlyAfterStarting(t *testing.T) {
	h, tasks, archives := deleteHarness(t)
	archives.err = errors.New("no route")
	rsp := h.deleteInstance("i1")
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "读不到归档任务")
	if rsp.GetIncomplete() {
		t.Fatal("还没动手，不能标删除未完成")
	}
	archives.err = nil
	tasks.deleteErr = context.DeadlineExceeded
	rsp = h.deleteInstance("i1")
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "可以重新执行删除")
	if !rsp.GetIncomplete() || rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_INNER_ERR {
		t.Fatalf("删任务失败应标删除未完成：%+v", rsp)
	}
}

// 传给 DeleteInstance 的 View：清掉删掉的任务的年龄探针记录，保留的任务的记录不动。
func TestDeleteClearsAgeRecordsOnlyForDeletedTasks(t *testing.T) {
	h := newHarness(t)
	tasks := &fakeFactorTasks{bound: map[string]string{"ftask_age_spot": "", "ftask_age_swap": ""}}
	h.service.Tasks, h.service.Archives = tasks, &fakeArchives{}
	h.createStrategy("s1", demoDSL)
	created, err := h.service.CreateStrategyInstance(h.ctx, &strategypb.CreateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{
		InstanceId: "iage", StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_age_spot", "swap": "ftask_age_swap"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	tasks.bound["ftask_age_spot"] = "iage"
	views := map[string]string{}
	for _, taskID := range []string{"ftask_age_spot", "ftask_age_swap"} {
		viewID, err := input.ViewIDOfTask(taskID)
		if err != nil {
			t.Fatal(err)
		}
		views[taskID] = viewID
		if err := h.service.Store.SaveAgePassed(h.ctx, "crypto", viewID, "idx", 10, "spot", []string{"BTC-USDT"}, h.now); err != nil {
			t.Fatal(err)
		}
	}
	rsp := h.deleteInstance("iage")
	requireOK(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
	deleted, _ := h.service.Store.LoadAgePassed(h.ctx, "crypto", views["ftask_age_spot"], "idx", 10, "spot")
	kept, _ := h.service.Store.LoadAgePassed(h.ctx, "crypto", views["ftask_age_swap"], "idx", 10, "spot")
	if len(deleted) != 0 || len(kept) != 1 {
		t.Fatalf("删掉的任务的年龄记录应清掉、保留的任务的不动：deleted=%v kept=%v", deleted, kept)
	}
}

// 从没关联过的任务被另外删掉：标为不存在（absent），不是删除未完成；启用报“引用的因子任务已不存在”；删除照常完成。
func TestUnboundTaskDeletedElsewhereIsAbsentNotIncomplete(t *testing.T) {
	h := newHarness(t)
	tasks := &fakeFactorTasks{bound: map[string]string{"ftask_gone": ""}}
	h.service.Tasks, h.service.Archives = tasks, &fakeArchives{}
	h.createStrategy("s1", demoDSL)
	requireOK(t, int32(h.createInstance("i0", "s1", "ftask_gone", "", false).GetRetInfo().GetCode()), "")
	delete(tasks.bound, "ftask_gone")
	detail, _ := h.service.GetStrategyInstance(h.ctx, &strategypb.GetStrategyInstanceReq{InstanceId: "i0"})
	if got := detail.GetInstance(); len(got.GetMissingFactorTasks()) != 0 || len(got.GetAbsentFactorTasks()) != 1 {
		t.Fatalf("应标为不存在而不是删除未完成：%+v", got)
	}
	enabled := h.setEnabled("i0", true)
	requireFail(t, int32(enabled.GetRetInfo().GetCode()), enabled.GetRetInfo().GetMsg(), "引用的因子任务 ftask_gone 已不存在")
	rsp := h.deleteInstance("i0")
	requireOK(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
}
