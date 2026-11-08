package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/store"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

// 4.6 节：Prepare 给出的最早值过一会儿再提交，差距不到 1 小时就夹到新的下界；差 1 小时以上照旧拒绝。
func TestBackfillStartsAreClampedToTheLowerBoundWithinAnHour(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	prep, _ := h.mgr.PrepareArchiveTask(ctx, &archivepb.PrepareArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, prep.GetRetInfo())
	h.now = h.now.Add(10 * time.Minute)
	created := h.createOnly("ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.BackfillFrom = prep.GetEarliestBackfillFrom() })
	requireOK(t, created.GetRetInfo())
	want := fmtRFC(h.now.Add(-168 * time.Hour).Add(24 * time.Hour))
	if got := created.GetTask().GetBackfillFrom(); got != want || got == prep.GetEarliestBackfillFrom() {
		t.Fatalf("保存的回补起点 = %s，want 新的下界 %s", got, want)
	}
	stored, _ := h.store.GetTask(ctx, "crypto", "ds_1m")
	if fmtRFC(stored.BackfillFrom) != want {
		t.Fatalf("库里的回补起点 = %s", fmtRFC(stored.BackfillFrom))
	}
	// 差 2 小时：拒绝，文案不变。
	cnPrep, _ := h.mgr.PrepareArchiveTask(ctx, &archivepb.PrepareArchiveTaskReq{SpaceId: "stockcn", DatasetId: "cn_1m"})
	early, _ := time.Parse(time.RFC3339, cnPrep.GetEarliestBackfillFrom())
	rejected := h.createOnly("cn_1m", func(r *archivepb.CreateArchiveTaskReq) {
		r.SpaceId, r.BackfillFrom = "stockcn", fmtRFC(early.Add(-2*time.Hour))
	})
	requireCode(t, rejected.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "不能早于")
	// 修改任务的回补起点同理。
	h.now = h.now.Add(10 * time.Minute)
	updated, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m", BackfillFrom: want})
	requireOK(t, updated.GetRetInfo())
	if got := updated.GetTask().GetBackfillFrom(); got != fmtRFC(h.now.Add(-144*time.Hour)) {
		t.Fatalf("修改后的回补起点 = %s", got)
	}

	// 补拉区间：早 10 分钟夹到下界，下发的就是夹取后的区间；早 2 小时拒绝。
	lower := h.now.Add(-144 * time.Hour)
	ok, _ := h.mgr.BackfillArchiveRange(ctx, &archivepb.BackfillArchiveRangeReq{SpaceId: "crypto", DatasetId: "ds_1m",
		RangeStart: fmtRFC(lower.Add(-10 * time.Minute)), RangeEnd: fmtRFC(lower.Add(2 * time.Hour))})
	requireOK(t, ok.GetRetInfo())
	if ok.GetRun().GetRangeStart() != fmtRFC(lower) {
		t.Fatalf("补拉起点 = %s，want %s", ok.GetRun().GetRangeStart(), fmtRFC(lower))
	}
	if _, err := h.store.CancelRun(ctx, ok.GetRun().GetRunId(), "", h.now); err != nil {
		t.Fatal(err)
	}
	far, _ := h.mgr.BackfillArchiveRange(ctx, &archivepb.BackfillArchiveRangeReq{SpaceId: "crypto", DatasetId: "ds_1m",
		RangeStart: fmtRFC(lower.Add(-2 * time.Hour)), RangeEnd: fmtRFC(lower.Add(2 * time.Hour))})
	requireCode(t, far.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "线上保留期 + 1 天")
	empty, _ := h.mgr.BackfillArchiveRange(ctx, &archivepb.BackfillArchiveRangeReq{SpaceId: "crypto", DatasetId: "ds_1m",
		RangeStart: fmtRFC(lower.Add(-20 * time.Minute)), RangeEnd: fmtRFC(lower.Add(-10 * time.Minute))})
	requireCode(t, empty.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "为空")
}

func TestRemoteStateCoversTheFiveValues(t *testing.T) {
	verifiedAt := time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC)
	file := store.Partition{FilePath: "a.csv.gz", FileSHA256: "sha-new"}
	for _, tc := range []struct {
		name    string
		p       store.Partition
		enabled bool
		want    string
	}{
		{"未启用", file, false, RemoteOff},
		{"没有文件", store.Partition{}, true, RemoteNone},
		{"已校验", withRemote(file, store.Remote{Key: "k", SHA256: "sha-new", VerifiedAt: verifiedAt}), true, RemoteVerified},
		{"只有旧版本且失败", withRemote(file, store.Remote{Key: "k", SHA256: "sha-old", VerifiedAt: verifiedAt, Error: "403"}), true, RemoteFailed},
		{"只有旧版本", withRemote(file, store.Remote{Key: "k", SHA256: "sha-old", VerifiedAt: verifiedAt}), true, RemotePending},
		{"已上传未校验", withRemote(file, store.Remote{Key: "k", SHA256: "sha-new"}), true, RemotePending},
	} {
		if got := remoteState(tc.p, tc.enabled); got != tc.want {
			t.Fatalf("%s: remote_state = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func withRemote(p store.Partition, r store.Remote) store.Partition {
	p.Remote = r
	return p
}

func TestRemoteHealthRuleAppliesOnlyWhenEnabled(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	base := store.Task{SpaceID: "crypto", DatasetID: "ds", Freq: "1m", PartitionUnit: "day", Retention: "168h", Status: store.StatusEnabled,
		IntervalSeconds: 3600, CreatedAt: now.Add(-240 * time.Hour), LastSuccessAt: now.Add(-10 * time.Minute)}
	eval := func(enabled bool, age time.Duration, lastErr string) (string, string) {
		return evaluateHealth(healthInput{Now: now, Task: base, WorkerActive: true, Lookback: 15 * time.Minute, SpaceTZ: "UTC", RemoteEnabled: enabled,
			Remote: store.RemoteBacklog{Unreplicated: 2, OldestExportedAt: now.Add(-age), OldestKey: "2026-10-13", LastError: lastErr}})
	}
	if level, _ := eval(false, 100*time.Hour, ""); level != HealthOK {
		t.Fatalf("未启用时没有远端规则: %s", level)
	}
	if level, _ := eval(true, 23*time.Hour, ""); level != HealthOK {
		t.Fatalf("24 小时以内正常: %s", level)
	}
	if level, reason := eval(true, 25*time.Hour, "上传 x: 访问被拒绝"); level != HealthWarning ||
		!strings.Contains(reason, "共 2 个分区还没有已校验的远端副本，最早的 2026-10-13 导出已超过 24 小时；最近错误：上传 x: 访问被拒绝") {
		t.Fatalf("24 小时 = %s %s", level, reason)
	}
	if level, reason := eval(true, 73*time.Hour, ""); level != HealthError || !strings.Contains(reason, "导出已超过 3 天") {
		t.Fatalf("72 小时 = %s %s", level, reason)
	}
}

// 心跳带对象存储摘要；任务列表按“已复制”口径给出远端计数与健康等级；重导出后从新的导出时间算；分区带 remote_state。
func TestRemoteReplicaFlowsThroughHeartbeatTasksAndPartitions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	beat := func() {
		rsp, err := h.worker.ArchiveWorkerHeartbeat(ctx, &archivepb.ArchiveWorkerHeartbeatReq{WorkerId: "w1", InstanceId: "i1", RootDir: "/data/archive",
			ObjectStore: &archivepb.ObjectStoreInfo{Endpoint: "cos.ap-nanjing.myqcloud.com", Bucket: "moox-archive-1250000000", Prefix: "moox-archive/", StorageClass: "STANDARD_IA", UrlStyle: "virtual"}})
		if err != nil {
			t.Fatal(err)
		}
		requireOK(t, rsp.GetRetInfo())
	}
	beat()
	overview, _ := h.mgr.GetArchiveOverview(ctx, &archivepb.GetArchiveOverviewReq{})
	if os := overview.GetWorker().GetObjectStore(); os.GetEndpoint() != "cos.ap-nanjing.myqcloud.com" || os.GetStorageClass() != "STANDARD_IA" || os.GetUrlStyle() != "virtual" {
		t.Fatalf("总览里的对象存储 = %v", os)
	}
	run := h.pull("w1", "i1", 1)[0]
	report := func(key, sha, exportedAt, remoteSHA, remoteErr string) *archivepb.PartitionReport {
		r := &archivepb.PartitionReport{PartitionKey: key, Status: "exported", RangeStart: key + "T00:00:00Z", RangeEnd: "2026-10-17T00:00:00Z", RowCount: 10, SubjectCount: 1,
			FilePath: "crypto/ds_1m/2026/" + key + ".csv.gz", FileBytes: 100, FileSha256: sha, ExportCount: 1, ExportedAt: exportedAt, RemoteError: remoteErr}
		if remoteSHA != "" {
			r.RemoteKey, r.RemoteSha256, r.RemoteEtag, r.RemoteUploadedAt, r.RemoteVerifiedAt = "crypto/ds_1m/2026/"+key+"."+remoteSHA[:3]+".csv.gz", remoteSHA, "md5", exportedAt, exportedAt
		}
		return r
	}
	done, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{WorkerId: "w1", RunId: run.GetRunId(), LeaseToken: run.GetLeaseToken(), Status: "succeeded", PartitionsUploaded: 1,
		Partitions: []*archivepb.PartitionReport{
			report("2026-10-16", "aaa111", "2026-10-17T00:30:00Z", "aaa111", ""),
			report("2026-10-15", "bbb222", "2026-10-15T00:30:00Z", "", "上传 x: 访问被拒绝"),
		}})
	requireOK(t, done.GetRetInfo())
	got, _ := h.mgr.GetArchiveRun(ctx, &archivepb.GetArchiveRunReq{RunId: run.GetRunId()})
	if got.GetRun().GetPartitionsUploaded() != 1 {
		t.Fatalf("partitions_uploaded = %d", got.GetRun().GetPartitionsUploaded())
	}
	parts, _ := h.mgr.ListArchivePartitions(ctx, &archivepb.ListArchivePartitionsReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	states := map[string]string{}
	for _, p := range parts.GetPartitions() {
		states[p.GetPartitionKey()] = p.GetRemoteState()
	}
	if states["2026-10-16"] != RemoteVerified || states["2026-10-15"] != RemoteFailed {
		t.Fatalf("remote_state = %v", states)
	}
	tasks, _ := h.mgr.ListArchiveTasks(ctx, &archivepb.ListArchiveTasksReq{})
	task := tasks.GetTasks()[0]
	if task.GetRemoteVerifiedCount() != 1 || task.GetRemotePendingCount() != 1 {
		t.Fatalf("远端计数 = %d/%d", task.GetRemoteVerifiedCount(), task.GetRemotePendingCount())
	}
	// 2026-10-15 导出于 2 天前（距 h.now 约 48.5 小时）：需关注，带最近错误。
	if task.GetHealth() != HealthWarning || !strings.Contains(task.GetHealthReason(), "共 1 个分区还没有已校验的远端副本，最早的 2026-10-15 导出已超过 24 小时；最近错误：上传 x: 访问被拒绝") {
		t.Fatalf("health = %s %s", task.GetHealth(), task.GetHealthReason())
	}
	// 再过一天多：异常。
	h.now = h.now.Add(26 * time.Hour)
	beat()
	tasks, _ = h.mgr.ListArchiveTasks(ctx, &archivepb.ListArchiveTasksReq{})
	if task := tasks.GetTasks()[0]; task.GetHealth() != HealthError || !strings.Contains(task.GetHealthReason(), "导出已超过 3 天") {
		t.Fatalf("health = %s %s", task.GetHealth(), task.GetHealthReason())
	}
	// worker 不再报告对象存储（最近一次心跳的记录）：远端规则不生效，计数为 0，分区 remote_state 为 off。
	rsp, _ := h.worker.ArchiveWorkerHeartbeat(ctx, &archivepb.ArchiveWorkerHeartbeatReq{WorkerId: "w1", InstanceId: "i1"})
	requireOK(t, rsp.GetRetInfo())
	tasks, _ = h.mgr.ListArchiveTasks(ctx, &archivepb.ListArchiveTasksReq{})
	if task := tasks.GetTasks()[0]; strings.Contains(task.GetHealthReason(), "远端副本") || task.GetRemotePendingCount() != 0 || task.GetRemoteVerifiedCount() != 0 {
		t.Fatalf("未启用时 = %s %s %d", task.GetHealth(), task.GetHealthReason(), task.GetRemotePendingCount())
	}
	one, _ := h.mgr.GetArchivePartition(ctx, &archivepb.GetArchivePartitionReq{SpaceId: "crypto", DatasetId: "ds_1m", PartitionKey: "2026-10-16"})
	if one.GetPartition().GetRemoteState() != RemoteOff || one.GetPartition().GetRemoteSha256() != "aaa111" {
		t.Fatalf("partition = %v", one.GetPartition())
	}
}

// 重导出后计时从新的导出时间算：新版本刚导出，旧版本远端副本不算数，但还没到 24 小时。
func TestRemoteHealthRestartsFromTheNewExport(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	in := healthInput{Now: now, Task: store.Task{Retention: "168h", Status: store.StatusEnabled, IntervalSeconds: 3600, LastSuccessAt: now}, WorkerActive: true,
		RemoteEnabled: true, Remote: store.RemoteBacklog{Unreplicated: 1, OldestExportedAt: now.Add(-time.Hour), OldestKey: "2026-10-01"}}
	if level, reason := evaluateHealth(in); level != HealthOK {
		t.Fatalf("重导出 1 小时内 = %s %s", level, reason)
	}
}
