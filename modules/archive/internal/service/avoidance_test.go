package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
)

type fakeCollector struct {
	mu    sync.Mutex
	freqs map[string][]string
	fail  map[string]bool
	calls []string
	// block 为真时阻塞到 ctx 结束（模拟读取超时）。
	block bool
}

func (f *fakeCollector) SpaceFrequencies(ctx context.Context, space string) ([]string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, space)
	block, fail, freqs := f.block, f.fail[space], f.freqs[space]
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if fail {
		return nil, errors.New("collector 不可用")
	}
	return freqs, nil
}

func (f *fakeCollector) takeCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	sort.Strings(out)
	return out
}

func TestAvoidanceRefreshReadsOnlySpacesWithTasks(t *testing.T) {
	h := newHarness(t)
	collector := &fakeCollector{freqs: map[string][]string{"crypto": {"1m", "1h"}, "stockcn": {"5m"}, "stockhk": {"1m"}}, fail: map[string]bool{}}
	h.svc.collector = collector
	ctx := context.Background()

	// 还没有任务：一个空间都不读，规则是“没有归档任务，不避让”。
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if calls := collector.takeCalls(); len(calls) != 0 {
		t.Fatalf("没有任务时不应读取: %v", calls)
	}
	if a := h.svc.avoidance(); a.EveryMinutes != 0 || a.HourQuiet != nil || !strings.Contains(a.Basis, "没有归档任务") {
		t.Fatalf("avoidance = %+v", a)
	}

	// 新建了任务（有任务的空间变了）：不等 10 分钟，下一次维护就重算。
	requireOK(t, h.createOnly("ds_1m", nil).GetRetInfo())
	h.now = h.now.Add(time.Minute)
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if calls := collector.takeCalls(); strings.Join(calls, ",") != "crypto" {
		t.Fatalf("新建首个任务后应立即读取: %v", calls)
	}
	requireOK(t, h.createOnly("cn_1m", func(r *archivepb.CreateArchiveTaskReq) { r.SpaceId = "stockcn" }).GetRetInfo())
	h.now = h.now.Add(time.Minute)
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	// 只读有任务的空间（stockhk 在配置里但没有任务）。
	if calls := collector.takeCalls(); strings.Join(calls, ",") != "crypto,stockcn" {
		t.Fatalf("calls = %v", calls)
	}
	// 空间不变且上次成功不到 10 分钟：不刷新。
	h.now = h.now.Add(9 * time.Minute)
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if calls := collector.takeCalls(); len(calls) != 0 {
		t.Fatalf("不到 10 分钟不应刷新: %v", calls)
	}
	h.now = h.now.Add(-9 * time.Minute)
	a := h.svc.avoidance()
	if a.EveryMinutes != 1 || a.HourQuiet == nil || !a.ComputedAt.Equal(h.now) || !strings.Contains(a.Basis, "crypto：1m、1h；stockcn：5m") {
		t.Fatalf("avoidance = %+v", a)
	}

	// 心跳与总览都带上当前规则。
	hb := h.heartbeat("w1", "i1")
	if got := hb.GetAvoidance(); got.GetEveryMinutes() != 1 || got.GetBeforeSeconds() != 5 || got.GetAfterSeconds() != 25 ||
		got.GetHourQuietFrom() != "58:00" || got.GetHourQuietUntil() != "04:00" || got.GetComputedAt() != fmtRFC(h.now) {
		t.Fatalf("heartbeat avoidance = %v", got)
	}
	overview, _ := h.mgr.GetArchiveOverview(ctx, &archivepb.GetArchiveOverviewReq{})
	if overview.GetAvoidance().GetBasis() != a.Basis {
		t.Fatalf("overview avoidance = %v", overview.GetAvoidance())
	}

	// stockcn 读取失败：沿用上次结果并注明；维护本身不算失败（避让有沿用与默认值兜底）。
	collector.mu.Lock()
	collector.fail["stockcn"] = true
	collector.freqs["crypto"] = []string{"1m"}
	collector.mu.Unlock()
	h.now = h.now.Add(10 * time.Minute)
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatalf("避让读取失败不应让维护失败: %v", err)
	}
	if err := h.svc.refreshAvoidance(ctx, []string{"crypto", "stockcn"}, h.now); err == nil || !strings.Contains(err.Error(), "stockcn") {
		t.Fatalf("部分失败时刷新本身应返回错误: %v", err)
	}
	a = h.svc.avoidance()
	if a.EveryMinutes != 1 || a.HourQuiet != nil || !strings.Contains(a.Basis, "stockcn：5m（读取失败，沿用") {
		t.Fatalf("avoidance = %+v", a)
	}
	collector.takeCalls()
	// 失败后 3 分钟内不再读（Collector 不可用时不让每分钟的维护都卡满读取上限），满 3 分钟重试。
	h.now = h.now.Add(2 * time.Minute)
	_ = h.svc.Maintain(ctx)
	if calls := collector.takeCalls(); len(calls) != 0 {
		t.Fatalf("失败后 3 分钟内不应重试: %v", calls)
	}
	h.now = h.now.Add(time.Minute)
	_ = h.svc.Maintain(ctx)
	if calls := collector.takeCalls(); len(calls) != 2 {
		t.Fatalf("失败满 3 分钟应重试: %v", calls)
	}
	// 恢复后成功一次就清掉失败时间：之后按 10 分钟刷新。
	collector.mu.Lock()
	collector.fail["stockcn"] = false
	collector.mu.Unlock()
	h.now = h.now.Add(3 * time.Minute)
	_ = h.svc.Maintain(ctx)
	if calls := collector.takeCalls(); len(calls) != 2 || strings.Contains(h.svc.avoidance().Basis, "读取失败") {
		t.Fatalf("恢复后应重读并去掉失败说明: %v %s", calls, h.svc.avoidance().Basis)
	}
}

func TestAvoidanceDefaultsUntilFirstSuccessAndRespectsTheBudget(t *testing.T) {
	h := newHarness(t)
	// 没有采集频率来源：一直用默认值。
	if err := h.svc.Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a := h.svc.avoidance(); a.EveryMinutes != 1 || a.HourQuiet == nil || a.Basis != "未取到采集频率，使用默认值" || !a.ComputedAt.IsZero() {
		t.Fatalf("avoidance = %+v", a)
	}
	requireOK(t, h.createOnly("ds_1m", nil).GetRetInfo())
	collector := &fakeCollector{block: true}
	h.svc.collector = collector
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatalf("读取超时不应让维护失败: %v", err)
	}
	if a := h.svc.avoidance(); a.EveryMinutes != 1 || a.HourQuiet == nil || !strings.Contains(a.Basis, "未取到采集频率，使用默认值") {
		t.Fatalf("从没成功过应使用默认值: %+v", a)
	}
}

// 刷新后的规则进入任务的下一次拉取时刻：只有 1h 以下频率时整点不再顺延到 :04:25。
func TestAvoidanceFeedsNextPullAt(t *testing.T) {
	h := newHarness(t)
	h.now = time.Date(2026, 10, 17, 1, 57, 30, 0, time.UTC)
	requireOK(t, h.createOnly("ds_1m", nil).GetRetInfo())
	next := func() string {
		rsp, _ := h.mgr.ListArchiveTasks(context.Background(), &archivepb.ListArchiveTasksReq{SpaceId: "crypto"})
		return rsp.GetTasks()[0].GetNextPullAt()
	}
	if got := next(); got != "2026-10-17T02:04:25Z" {
		t.Fatalf("默认规则 next_pull_at = %s", got)
	}
	h.svc.collector = &fakeCollector{freqs: map[string][]string{"crypto": {"1m"}}}
	if err := h.svc.Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := next(); got != "2026-10-17T01:58:25Z" {
		t.Fatalf("无整点窗口 next_pull_at = %s", got)
	}
	preview, _ := h.mgr.PreviewArchiveSchedule(context.Background(), &archivepb.PreviewArchiveScheduleReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, preview.GetRetInfo())
	if runs := preview.GetNextRuns(); len(runs) == 0 || runs[0] != "2026-10-17T01:58:25Z" {
		t.Fatalf("preview next_runs = %v", runs)
	}
}
