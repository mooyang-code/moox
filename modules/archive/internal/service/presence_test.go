package service

import (
	"context"
	"testing"
	"time"

	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/report"
)

func (h *harness) presence() (int, string) {
	h.t.Helper()
	state, workerID, _, err := h.svc.Presence(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return state, workerID
}

// 没有启用的归档任务时不需要 worker；有任务时按最近一次心跳的 worker 判断，换机后的旧登记不影响；
// 删除全部任务后恢复为“不需要”。
func TestArchiveWorkerPresence(t *testing.T) {
	h := newHarness(t)
	if state, _ := h.presence(); state != report.PresenceNotRequired {
		t.Fatalf("no tasks: state = %d", state)
	}
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	if state, _ := h.presence(); state != report.PresenceOffline {
		t.Fatalf("task without any worker: state = %d", state)
	}
	requireOK(t, h.heartbeat("worker@old", "worker@old-1").GetRetInfo())
	h.now = h.now.Add(10 * time.Minute) // 旧 worker 停止，新机器接管
	requireOK(t, h.heartbeat("worker@new", "worker@new-1").GetRetInfo())
	h.now = h.now.Add(90 * time.Second)
	if state, workerID := h.presence(); state != report.PresenceOnline || workerID != "worker@new" {
		t.Fatalf("after move: state = %d worker = %s", state, workerID)
	}
	h.now = h.now.Add(time.Minute)
	if state, workerID := h.presence(); state != report.PresenceOffline || workerID != "worker@new" {
		t.Fatalf("stopped: state = %d worker = %s", state, workerID)
	}
	rsp, err := h.mgr.DeleteArchiveTask(context.Background(), &archivepb.DeleteArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	if err != nil {
		t.Fatal(err)
	}
	requireOK(t, rsp.GetRetInfo())
	if state, _ := h.presence(); state != report.PresenceNotRequired {
		t.Fatalf("tasks deleted: state = %d", state)
	}
}

// 退役只删除租约已过期的登记；仍活动时拒绝；不存在时返回 retired=false。
func TestRetireArchiveWorker(t *testing.T) {
	h := newHarness(t)
	requireOK(t, h.heartbeat("worker@old", "worker@old-1").GetRetInfo())
	retire := func(id string) *archivepb.RetireArchiveWorkerRsp {
		rsp, err := h.mgr.RetireArchiveWorker(context.Background(), &archivepb.RetireArchiveWorkerReq{WorkerId: id})
		if err != nil {
			t.Fatal(err)
		}
		return rsp
	}
	requireCode(t, retire("worker@old").GetRetInfo(), commonpb.ErrorCode_CONFLICT, "活动租约")
	h.now = h.now.Add(10 * time.Minute)
	rsp := retire("worker@old")
	requireOK(t, rsp.GetRetInfo())
	if !rsp.GetRetired() {
		t.Fatal("expected the stale worker to be retired")
	}
	workers, err := h.store.ListWorkers(context.Background())
	if err != nil || len(workers) != 0 {
		t.Fatalf("workers = %v, err = %v", workers, err)
	}
	if retire("worker@old").GetRetired() {
		t.Fatal("retiring a missing worker must report retired=false")
	}
	requireCode(t, retire(" ").GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "worker_id")
}

// 对象存储状态：探测结果经心跳落库、在总览带出；未配置对象存储或心跳不新鲜时不报；不可用满 5 分钟升为 2，恢复后回到 0。
func TestArchiveObjectStoreState(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	beat := func(info *archivepb.ObjectStoreInfo) {
		rsp, err := h.worker.ArchiveWorkerHeartbeat(ctx, &archivepb.ArchiveWorkerHeartbeatReq{WorkerId: "w1", InstanceId: "i1", RootDir: "/data/archive", ObjectStore: info})
		if err != nil {
			t.Fatal(err)
		}
		requireOK(t, rsp.GetRetInfo())
	}
	state := func() (string, int, string, bool) {
		workerID, state, probeError, ok, err := h.svc.ObjectStoreState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return workerID, state, probeError, ok
	}
	if _, _, _, ok := state(); ok {
		t.Fatal("没有 worker 时不应上报")
	}
	beat(nil)
	if _, _, _, ok := state(); ok {
		t.Fatal("没配置对象存储时不应上报")
	}
	since := h.now.Add(-2 * time.Minute).UTC().Format(time.RFC3339)
	down := &archivepb.ObjectStoreInfo{Endpoint: "127.0.0.1:11463", Bucket: "moox-archive", Prefix: "moox-archive/", UrlStyle: "path",
		ProbeError: "查询 _probe/absent: 访问被拒绝", UnavailableSince: since}
	beat(down)
	overview, _ := h.mgr.GetArchiveOverview(ctx, &archivepb.GetArchiveOverviewReq{})
	if got := overview.GetWorker().GetObjectStore(); got.GetProbeError() != down.GetProbeError() || got.GetUnavailableSince() != since {
		t.Fatalf("总览里的对象存储 = %v", got)
	}
	if workerID, s, probeError, ok := state(); !ok || workerID != "w1" || s != report.ObjectStoreUnavailable || probeError != down.GetProbeError() {
		t.Fatalf("不可用未满 5 分钟 = %s %d %s %v", workerID, s, probeError, ok)
	}
	h.now = h.now.Add(3 * time.Minute)
	beat(down)
	if _, s, _, _ := state(); s != report.ObjectStoreDown {
		t.Fatalf("不可用满 5 分钟 state = %d", s)
	}
	h.now = h.now.Add(3 * time.Minute)
	if _, _, _, ok := state(); ok {
		t.Fatal("心跳不新鲜时不应上报")
	}
	beat(&archivepb.ObjectStoreInfo{Endpoint: "127.0.0.1:11463", Bucket: "moox-archive", Prefix: "moox-archive/", UrlStyle: "path"})
	if _, s, probeError, ok := state(); !ok || s != report.ObjectStoreAvailable || probeError != "" {
		t.Fatalf("恢复后 = %d %q %v", s, probeError, ok)
	}
}
