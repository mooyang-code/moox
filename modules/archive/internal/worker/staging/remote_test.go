package staging

import (
	"context"
	"testing"
	"time"
)

func TestUploadRetryDelayDoublesUpToAnHour(t *testing.T) {
	for attempts, want := range map[int]time.Duration{1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute, 6: 32 * time.Minute, 7: time.Hour, 30: time.Hour} {
		if got := UploadRetryDelay(attempts); got != want {
			t.Fatalf("UploadRetryDelay(%d) = %s, want %s", attempts, got, want)
		}
	}
}

// exported 准备一个已导出的分区（文件路径 rel、sha256 sha），并清掉待上报标记。
func exported(t *testing.T, s *Store, task Task, at time.Time, rel, sha string) PartitionState {
	t.Helper()
	ctx := context.Background()
	if err := s.EnsureTable(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stage(ctx, task, []Row{row("BTC", at, "", 1.0, 1.0, nil, nil)}, utcDay, now0); err != nil {
		t.Fatal(err)
	}
	key := utcDay(at).Key
	if err := s.MarkExported(ctx, task.SpaceID, task.DatasetID, key, now0, rel, sha); err != nil {
		t.Fatal(err)
	}
	_ = s.MarkReported(ctx, task.SpaceID, task.DatasetID, []string{key})
	p, _, _ := s.Partition(ctx, task.SpaceID, task.DatasetID, key)
	return p
}

func TestUploadStateFollowsThePendingRule(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	p := exported(t, s, task, day0.Add(time.Hour), "crypto/ds_1m/2026/20261015.csv.gz", "sha-1")
	if pending, _ := s.PendingUploads(ctx, "crypto", "ds_1m", now0); len(pending) != 1 || pending[0].Key != p.Key {
		t.Fatalf("导出后应待上传: %+v", pending)
	}
	// 失败：次数加 1、写错误、退避 1 分钟、待上报；退避期间不再列出。
	if err := s.MarkUploadFailed(ctx, "crypto", "ds_1m", p.Key, "sha-1", "上传失败", now0); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Partition(ctx, "crypto", "ds_1m", p.Key)
	if got.Remote.Attempts != 1 || got.Remote.Error != "上传失败" || !got.Remote.RetryAt.Equal(now0.Add(time.Minute)) || !got.ReportPending {
		t.Fatalf("失败后 = %+v", got.Remote)
	}
	if pending, _ := s.PendingUploads(ctx, "crypto", "ds_1m", now0.Add(30*time.Second)); len(pending) != 0 {
		t.Fatal("退避期间不应列出")
	}
	_ = s.MarkUploadFailed(ctx, "crypto", "ds_1m", p.Key, "sha-1", "又失败", now0)
	if got, _, _ := s.Partition(ctx, "crypto", "ds_1m", p.Key); got.Remote.Attempts != 2 || !got.Remote.RetryAt.Equal(now0.Add(2*time.Minute)) {
		t.Fatalf("第二次失败退避 2 分钟: %+v", got.Remote)
	}
	// 上传期间被重导出（sha256 变了）：不记成已上传，也不记失败。
	if ok, err := s.MarkUploaded(ctx, "crypto", "ds_1m", p.Key, "sha-old", Remote{Key: "k", ETag: "e", UploadedAt: now0, VerifiedAt: now0}); err != nil || ok {
		t.Fatalf("sha256 不符不应记成已上传: %v %v", ok, err)
	}
	// 成功：清掉错误与退避，已复制，不再待上传。
	_ = s.MarkReported(ctx, "crypto", "ds_1m", []string{p.Key})
	if ok, err := s.MarkUploaded(ctx, "crypto", "ds_1m", p.Key, "sha-1", Remote{Key: "k1", ETag: "e1", UploadedAt: now0, VerifiedAt: now0}); err != nil || !ok {
		t.Fatalf("MarkUploaded: %v %v", ok, err)
	}
	got, _, _ = s.Partition(ctx, "crypto", "ds_1m", p.Key)
	if !got.Replicated() || got.Remote.Error != "" || got.Remote.Attempts != 0 || !got.Remote.RetryAt.IsZero() || got.Remote.SHA256 != "sha-1" || !got.ReportPending {
		t.Fatalf("成功后 = %+v", got)
	}
	if pending, _ := s.PendingUploads(ctx, "crypto", "ds_1m", now0.Add(time.Hour)); len(pending) != 0 {
		t.Fatal("已复制的不再待上传")
	}
	if refs, _ := s.ReplicatedPartitions(ctx, 10); len(refs) != 1 || refs[0].SpaceID != "crypto" || refs[0].Partition.Key != p.Key {
		t.Fatalf("已复制的分区 = %+v", refs)
	}
	// 重导出：sha256 变了自动成为待上传，失败次数等被清零，远端字段保留（远端只有旧版本）。
	_ = s.MarkUploadFailed(ctx, "crypto", "ds_1m", p.Key, "sha-1", "x", now0)
	if err := s.MarkExported(ctx, "crypto", "ds_1m", p.Key, now0.Add(time.Hour), p.FilePath, "sha-2"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Partition(ctx, "crypto", "ds_1m", p.Key)
	if got.Replicated() || got.Remote.Attempts != 0 || !got.Remote.RetryAt.IsZero() || got.Remote.Error != "" || got.Remote.Key != "k1" || got.RemoteIsCurrent() {
		t.Fatalf("重导出后 = %+v", got.Remote)
	}
	if pending, _ := s.PendingUploads(ctx, "crypto", "ds_1m", now0); len(pending) != 1 {
		t.Fatal("重导出后应立刻待上传，不继承退避")
	}
	// 抽查通过、远端丢失。
	_, _ = s.MarkUploaded(ctx, "crypto", "ds_1m", p.Key, "sha-2", Remote{Key: "k2", ETag: "e2", UploadedAt: now0, VerifiedAt: now0})
	if err := s.MarkRemoteVerified(ctx, "crypto", "ds_1m", p.Key, now0.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Partition(ctx, "crypto", "ds_1m", p.Key); !got.Remote.VerifiedAt.Equal(now0.Add(24 * time.Hour)) {
		t.Fatalf("抽查时间 = %s", got.Remote.VerifiedAt)
	}
	_ = s.MarkReported(ctx, "crypto", "ds_1m", []string{p.Key})
	if err := s.ResetRemote(ctx, "crypto", "ds_1m", p.Key, "远端副本丢失"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Partition(ctx, "crypto", "ds_1m", p.Key)
	if got.Remote.Key != "" || got.Remote.SHA256 != "" || !got.Remote.VerifiedAt.IsZero() || got.Remote.Error != "远端副本丢失" || !got.ReportPending {
		t.Fatalf("重置后 = %+v", got)
	}
	if pending, _ := s.PendingUploads(ctx, "crypto", "ds_1m", now0); len(pending) != 1 {
		t.Fatal("重置后回到待上传")
	}
}

func TestMetaRemoteSHAIsStoredInTaskState(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	if sha, err := s.MetaRemoteSHA(ctx, "crypto", "ds"); err != nil || sha != "" {
		t.Fatalf("没有任务状态时为空: %q %v", sha, err)
	}
	if err := s.SetMetaRemoteSHA(ctx, "crypto", "ds", "x"); err == nil {
		t.Fatal("没有任务状态时应报错")
	}
	if err := s.BindTask(ctx, Task{SpaceID: "crypto", DatasetID: "ds", Purpose: "collect", DirName: "ds"}, "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMetaRemoteSHA(ctx, "crypto", "ds", "abc"); err != nil {
		t.Fatal(err)
	}
	if sha, _ := s.MetaRemoteSHA(ctx, "crypto", "ds"); sha != "abc" {
		t.Fatalf("sha = %q", sha)
	}
}

func TestRestorePartitionsKeepsTheRemoteFields(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	exportedAt := now0.Add(-time.Hour)
	regs := []PartitionState{
		{Key: "2026-10-14", Start: day0.AddDate(0, 0, -1), End: day0, Status: StatusFinal, FilePath: "crypto/ds_1m/2026/20261014.csv.gz", SHA256: "a",
			ExportedAt: exportedAt, FinalizedAt: exportedAt, Remote: Remote{Key: "crypto/ds_1m/2026/20261014.aaaa.csv.gz", SHA256: "a", ETag: "md5", UploadedAt: exportedAt, VerifiedAt: exportedAt}},
		{Key: "2026-10-15", Start: day0, End: day1, Status: StatusExported, FilePath: "crypto/ds_1m/2026/20261015.csv.gz", SHA256: "b",
			ExportedAt: exportedAt, Remote: Remote{Key: "old", SHA256: "older", VerifiedAt: exportedAt, Error: "403"}},
	}
	if _, err := s.RestorePartitions(ctx, "crypto", "ds_1m", regs, now0); err != nil {
		t.Fatal(err)
	}
	p14, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-14")
	if !p14.Replicated() || !p14.RemoteIsCurrent() || p14.Remote.ETag != "md5" {
		t.Fatalf("远端是当前版本的恢复为已上传: %+v", p14.Remote)
	}
	p15, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	if p15.Replicated() || p15.RemoteIsCurrent() || p15.Remote.Error != "403" {
		t.Fatalf("远端不是当前版本的恢复为待上传: %+v", p15.Remote)
	}
	if pending, _ := s.PendingUploads(ctx, "crypto", "ds_1m", now0); len(pending) != 1 || pending[0].Key != "2026-10-15" {
		t.Fatalf("待上传 = %+v", pending)
	}
	bad := []PartitionState{{Key: "2026-10-01", Status: StatusFinal, FilePath: "x", SHA256: "s", ExportedAt: exportedAt, Remote: Remote{Key: "k"}}}
	if _, err := s.RestorePartitions(ctx, "crypto", "ds_1m", bad, now0); err == nil {
		t.Fatal("有对象键却没有 sha256 的登记应报错")
	}
}
