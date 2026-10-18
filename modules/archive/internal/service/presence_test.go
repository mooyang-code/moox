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
