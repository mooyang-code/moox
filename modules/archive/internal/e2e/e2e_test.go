// Package e2e wires the real manager service (SQLite) to the real worker app (DuckDB) in one process,
// replacing only Storage and the network, to check the mgr/worker contract end to end.
package e2e

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/config"
	"github.com/mooyang-code/moox/modules/archive/internal/service"
	"github.com/mooyang-code/moox/modules/archive/internal/store"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/app"
	workerconfig "github.com/mooyang-code/moox/modules/archive/internal/worker/config"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/export"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/manager"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/runner"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/source"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

// inProcess 让 worker 的 manager.Client 直接调用 mgr 的 ArchiveWorker 实现，行为与网关客户端一致（冲突映射成 ErrConflict）。
type inProcess struct{ svc *service.WorkerService }

func check(info *commonpb.RetInfo) error {
	switch info.GetCode() {
	case commonpb.ErrorCode_SUCCESS:
		return nil
	case commonpb.ErrorCode_CONFLICT:
		return fmt.Errorf("%w: %s", manager.ErrConflict, info.GetMsg())
	default:
		return fmt.Errorf("%s", info.GetMsg())
	}
}

func (p inProcess) Heartbeat(ctx context.Context, req *archivepb.ArchiveWorkerHeartbeatReq) (*archivepb.ArchiveWorkerHeartbeatRsp, error) {
	rsp, _ := p.svc.ArchiveWorkerHeartbeat(ctx, req)
	return rsp, check(rsp.GetRetInfo())
}
func (p inProcess) Pull(ctx context.Context, req *archivepb.PullArchiveRunReq) (*archivepb.PullArchiveRunRsp, error) {
	rsp, _ := p.svc.PullArchiveRun(ctx, req)
	return rsp, check(rsp.GetRetInfo())
}
func (p inProcess) Progress(ctx context.Context, req *archivepb.ReportArchiveRunProgressReq) (*archivepb.ReportArchiveRunProgressRsp, error) {
	rsp, _ := p.svc.ReportArchiveRunProgress(ctx, req)
	return rsp, check(rsp.GetRetInfo())
}
func (p inProcess) Complete(ctx context.Context, req *archivepb.CompleteArchiveRunReq) (*archivepb.CompleteArchiveRunRsp, error) {
	rsp, _ := p.svc.CompleteArchiveRun(ctx, req)
	return rsp, check(rsp.GetRetInfo())
}

type datasets struct{}

func (datasets) GetDataset(_ context.Context, space, dataset string) (service.Dataset, error) {
	if dataset != "ds_1m" {
		return service.Dataset{}, fmt.Errorf("%w: %s", service.ErrDatasetNotFound, dataset)
	}
	return service.Dataset{SpaceID: space, DatasetID: dataset, DataKind: "time_series", Freq: "1m", Status: "active", DataNodeID: "node-0", Retention: "forever"}, nil
}

type fakeStorage struct{ rows []staging.Row }

func (f *fakeStorage) Columns(context.Context, string, string) ([]staging.Column, error) {
	return []staging.Column{{Name: "close", Type: staging.TypeDouble}, {Name: "open", Type: staging.TypeDouble}}, nil
}

func (f *fakeStorage) ReadRange(_ context.Context, req source.Range) (source.Page, error) {
	var out []staging.Row
	for _, row := range f.rows {
		if !row.DataTime.Before(req.Start) && row.DataTime.Before(req.End) {
			out = append(out, row)
		}
	}
	return source.Page{Rows: out, Bytes: int64(80 * len(out))}, nil
}

type instant struct{}

func (instant) WaitTurn(context.Context) error             { return nil }
func (instant) Throttle(context.Context, int64) error      { return nil }
func (instant) Sleep(context.Context, time.Duration) error { return nil }
func (instant) Interval() time.Duration                    { return 0 }

func TestManagerAndWorkerCompleteAFullArchiveCycle(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 17, 0, 34, 43, 0, time.UTC) // 昨天的分区在这次运行结束时导出
	clock := func() time.Time { return now }

	db, err := store.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, err := service.New(db, config.Default(), datasets{}, service.WithClock(clock))
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	stage, err := staging.Open(filepath.Join(root, ".state", "staging.db"), staging.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	yesterday := time.Date(2026, 10, 16, 20, 0, 0, 0, time.UTC)
	var rows []staging.Row
	for i := 0; i < 4*60; i++ { // 昨天 20:00–23:59
		for _, subject := range []string{"BTC-USDT", "ETH-USDT"} {
			rows = append(rows, staging.Row{SubjectID: subject, DataTime: yesterday.Add(time.Duration(i) * time.Minute), Values: []any{float64(i), float64(i) + 0.5}})
		}
	}
	storage := &fakeStorage{rows: rows}
	wcfg := workerconfig.Default()
	wcfg.Worker.WorkerID, wcfg.Worker.RootDir = "archive-worker@e2e", root
	files := export.Files{Root: root}
	worker := app.New(app.Options{
		Config: wcfg, Manager: inProcess{svc.Worker()}, Staging: stage, Files: files, InstanceID: "i1", Version: "e2e", Now: clock,
		Runner:    &runner.Runner{Source: storage, Staging: stage, Files: files, Pace: instant{}, PageRows: 2000, Now: clock, ReadAttempts: 1},
		DiskUsage: func(string) (uint64, uint64, error) { return 1 << 40, 1 << 39, nil },
	})

	// 1. 创建任务（立即开始，回补起点在昨天 20:00）：每小时 :34:43 的计划。
	created, _ := svc.Mgr().CreateArchiveTask(ctx, &archivepb.CreateArchiveTaskReq{
		SpaceId: "crypto", DatasetId: "ds_1m", ScheduleCron: "43 34 * * * *", BackfillFrom: yesterday.Format(time.RFC3339),
	})
	if created.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("create = %v", created.GetRetInfo())
	}

	// 2. worker 心跳取得租约、领取、执行（同步等待结束）。
	runOnce := func() {
		t.Helper()
		worker.HeartbeatOnce(ctx)
		worker.PullOnce(ctx)
		deadline := time.Now().Add(20 * time.Second)
		for worker.Snapshot().Running > 0 {
			if time.Now().After(deadline) {
				t.Fatal("运行没有结束")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	runOnce()

	// 3. mgr 侧：运行成功，分区已登记；昨天的分区在 00:34:43 已过 收盘 + 15 分钟，应已导出。
	runs, _ := svc.Mgr().ListArchiveRuns(ctx, &archivepb.ListArchiveRunsReq{SpaceId: "crypto"})
	if len(runs.GetRuns()) != 1 || runs.GetRuns()[0].GetStatus() != "succeeded" || runs.GetRuns()[0].GetRowsRead() != 2*4*60 || runs.GetRuns()[0].GetPartitionsExported() != 1 {
		t.Fatalf("runs = %v", runs.GetRuns())
	}
	parts, _ := svc.Mgr().ListArchivePartitions(ctx, &archivepb.ListArchivePartitionsReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	if len(parts.GetPartitions()) != 1 {
		t.Fatalf("partitions = %v", parts.GetPartitions())
	}
	p := parts.GetPartitions()[0]
	if p.GetPartitionKey() != "2026-10-16" || p.GetStatus() != "exported" || p.GetRowCount() != 2*4*60 || p.GetSubjectCount() != 2 || p.GetWorkerId() != "archive-worker@e2e" ||
		p.GetFilePath() != "crypto/2026/ds_1m_20261016.csv" || p.GetFileSha256() == "" || p.GetExportCount() != 1 || !strings.HasPrefix(p.GetPreview(), "data_time,subject_id,series_tag,close,open\n") {
		t.Fatalf("partition = %v", p)
	}
	if sha, size, err := export.FileSHA256(files.Abs(p.GetFilePath())); err != nil || sha != p.GetFileSha256() || size != p.GetFileBytes() {
		t.Fatalf("登记的 sha256/大小要与磁盘文件一致: %s %d %v", sha, size, err)
	}
	list, _ := svc.Mgr().ListArchiveTasks(ctx, &archivepb.ListArchiveTasksReq{SpaceId: "crypto"})
	task := list.GetTasks()[0]
	if task.GetLastSuccessAt() != now.Format(time.RFC3339) || task.GetPartitionCount() != 1 || task.GetFileCount() != 1 || task.GetTotalRows() != 480 || task.GetHealth() != "ok" {
		t.Fatalf("task = %v", task)
	}

	// 4. 下一次计划时刻（一小时后）：触发器生成 scheduled 运行，worker 从水位往前 L 读取，没有新数据，不重写文件。
	now = now.Add(time.Hour)
	if err := svc.Trigger(ctx); err != nil {
		t.Fatal(err)
	}
	runOnce()
	runs, _ = svc.Mgr().ListArchiveRuns(ctx, &archivepb.ListArchiveRunsReq{SpaceId: "crypto", Status: "succeeded"})
	if len(runs.GetRuns()) != 2 {
		t.Fatalf("应有两次成功运行: %v", runs.GetRuns())
	}
	again, _ := svc.Mgr().GetArchivePartition(ctx, &archivepb.GetArchivePartitionReq{SpaceId: "crypto", DatasetId: "ds_1m", PartitionKey: "2026-10-16"})
	if again.GetPartition().GetExportCount() != 1 || again.GetPartition().GetFileSha256() != p.GetFileSha256() {
		t.Fatalf("没有变化不应重写: %v", again.GetPartition())
	}

	// 5. 三天之后：分区过了暂存保留期，被定稿，mgr 的定稿边界前进，文件仍在。
	now = time.Date(2026, 10, 20, 1, 34, 43, 0, time.UTC)
	if err := svc.Trigger(ctx); err != nil {
		t.Fatal(err)
	}
	runOnce()
	final, _ := svc.Mgr().GetArchivePartition(ctx, &archivepb.GetArchivePartitionReq{SpaceId: "crypto", DatasetId: "ds_1m", PartitionKey: "2026-10-16"})
	if final.GetPartition().GetStatus() != "final" || final.GetPartition().GetFinalizedAt() == "" || final.GetPartition().GetFileSha256() != p.GetFileSha256() || final.GetPartition().GetRowCount() != 480 {
		t.Fatalf("final = %v", final.GetPartition())
	}
	list, _ = svc.Mgr().ListArchiveTasks(ctx, &archivepb.ListArchiveTasksReq{SpaceId: "crypto"})
	if list.GetTasks()[0].GetFinalUntil() != "2026-10-17T00:00:00Z" {
		t.Fatalf("final_until = %s", list.GetTasks()[0].GetFinalUntil())
	}

	// 6. 补拉区间：读回已定稿的文件、写入晚到的行、重新导出，登记里的行数和导出次数同步更新。
	storage.rows = append(storage.rows, staging.Row{SubjectID: "SOL-USDT", DataTime: yesterday.Add(time.Hour), Values: []any{1.0, 2.0}})
	now = now.Add(time.Hour)
	backfill, _ := svc.Mgr().BackfillArchiveRange(ctx, &archivepb.BackfillArchiveRangeReq{SpaceId: "crypto", DatasetId: "ds_1m", RangeStart: "2026-10-16T20:00:00Z", RangeEnd: "2026-10-17T00:00:00Z"})
	if backfill.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("backfill = %v", backfill.GetRetInfo())
	}
	runOnce()
	reopened, _ := svc.Mgr().GetArchivePartition(ctx, &archivepb.GetArchivePartitionReq{SpaceId: "crypto", DatasetId: "ds_1m", PartitionKey: "2026-10-16"})
	rp := reopened.GetPartition()
	if rp.GetStatus() != "final" || rp.GetRowCount() != 481 || rp.GetSubjectCount() != 3 || rp.GetExportCount() != 2 || rp.GetFileSha256() == p.GetFileSha256() {
		t.Fatalf("补拉后: %v", rp)
	}
	if sha, _, err := export.FileSHA256(files.Abs(rp.GetFilePath())); err != nil || sha != rp.GetFileSha256() {
		t.Fatalf("重写后的登记要与磁盘文件一致: %s %v", sha, err)
	}
}
