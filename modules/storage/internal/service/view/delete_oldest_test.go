package view

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// oldestDeleterEngine 在查询替身上加删除最旧数据的接口：每次计划给出一个序列、rowsPerPlan 行，
// 共有 backlog 行可删；记录每次删除时是否持有该索引的写锁。
type oldestDeleterEngine struct {
	*queryEngine
	svc         *Service
	backlog     uint64
	plans       int
	deletes     int
	refreshes   int
	unlocked    int
	gotKeep     uint64
	gotMaxRows  []int
	gotMaxSerie int
}

func (e *oldestDeleterEngine) PlanOldestRows(_ context.Context, _ string, keep uint64, maxSeries, maxRows int) ([]viewindex.OldestRowsCut, error) {
	e.plans++
	e.gotKeep, e.gotMaxSerie = keep, maxSeries
	e.gotMaxRows = append(e.gotMaxRows, maxRows)
	if e.backlog == 0 {
		return nil, nil
	}
	rows := min(e.backlog, uint64(maxRows))
	return []viewindex.OldestRowsCut{{SubjectID: "BTC-USDT", Freq: "1m", SeriesTag: "venue:x", Before: time.Now(), Rows: rows}}, nil
}

func (e *oldestDeleterEngine) DeleteOldestRows(_ context.Context, indexID string, cuts []viewindex.OldestRowsCut) (uint64, error) {
	e.deletes++
	if len(e.svc.indexWriteGate(indexID).token) != 0 {
		e.unlocked++
	}
	var total uint64
	for _, cut := range cuts {
		total += cut.Rows
	}
	e.backlog -= total
	return total, nil
}

func (e *oldestDeleterEngine) RefreshCoverageFrom(_ context.Context, indexID string) (string, error) {
	e.refreshes++
	if len(e.svc.indexWriteGate(indexID).token) != 0 {
		e.unlocked++
	}
	return "2026-10-30T00:05:00.000000000Z", nil
}

func deleteOldestTestService(backlog uint64) (*Service, *oldestDeleterEngine) {
	engine := &oldestDeleterEngine{queryEngine: &queryEngine{}, backlog: backlog}
	svc, _ := queryTestService(engine, true)
	engine.svc = svc
	svc.catalogViews = map[viewRef]*pb.View{{spaceID: "space", viewID: "prices"}: {SpaceId: "space", ViewId: "prices", Freq: "1m"}}
	svc.maintenanceReady = true
	svc.seriesBars = 10000
	return svc, engine
}

func TestDeleteOldestRoundDeletesInBatchesUnderWriteLockUpToRoundCap(t *testing.T) {
	svc, engine := deleteOldestTestService(1_000_000)
	svc.deleteOldestRound(context.Background(), 3)

	if engine.deletes != deleteOldestBatchesPerRound {
		t.Fatalf("一轮删了 %d 批，应在上限 %d 批处停下", engine.deletes, deleteOldestBatchesPerRound)
	}
	if engine.unlocked != 0 {
		t.Fatalf("有 %d 次删除或更新覆盖起点时没有持有写锁", engine.unlocked)
	}
	if engine.refreshes != 1 {
		t.Fatalf("一轮删完应更新一次覆盖起点，实际 %d 次", engine.refreshes)
	}
	if engine.gotKeep != 10000 || engine.gotMaxSerie != deleteOldestSeriesPerBatch {
		t.Fatalf("计划参数 keep=%d 序列数=%d，应为 10000 与 %d", engine.gotKeep, engine.gotMaxSerie, deleteOldestSeriesPerBatch)
	}
	for i, maxRows := range engine.gotMaxRows {
		if maxRows != 3 {
			t.Fatalf("第 %d 批的行数额度=%d，应为配置的 3", i, maxRows)
		}
	}
	if len(svc.indexWriteGate("prices-index").token) != 1 {
		t.Fatal("一轮结束后写锁没有释放")
	}
}

func TestDeleteOldestRoundStopsWhenNothingLeftAndSkipsUnsafeViews(t *testing.T) {
	svc, engine := deleteOldestTestService(5)
	svc.deleteOldestRound(context.Background(), 3)
	if engine.deletes != 2 || engine.backlog != 0 || engine.refreshes != 1 {
		t.Fatalf("5 行积压按每批 3 行应删 2 批后停下：批数=%d 剩余=%d 覆盖更新=%d", engine.deletes, engine.backlog, engine.refreshes)
	}

	// 没有可删的：不更新覆盖起点。
	svc.deleteOldestRound(context.Background(), 3)
	if engine.refreshes != 1 {
		t.Fatalf("没有删除时不应更新覆盖起点，实际 %d 次", engine.refreshes)
	}

	// 正在做 A/B 构建的 View 跳过。
	engine.backlog = 5
	plans := engine.plans
	svc.views[viewRef{spaceID: "space", viewID: "prices"}].next = "prices-next"
	svc.deleteOldestRound(context.Background(), 3)
	if engine.plans != plans {
		t.Fatal("正在做 A/B 构建的 View 不应删除最旧数据")
	}

	// 维护尚未就绪时整轮跳过。
	svc.views[viewRef{spaceID: "space", viewID: "prices"}].next = ""
	svc.maintenanceReady = false
	svc.deleteOldestRound(context.Background(), 3)
	if engine.plans != plans {
		t.Fatal("维护尚未就绪时不应删除最旧数据")
	}

	// 记录型 View（没有频率）不处理。
	svc.maintenanceReady = true
	svc.catalogViews[viewRef{spaceID: "space", viewID: "prices"}] = &pb.View{SpaceId: "space", ViewId: "prices"}
	svc.deleteOldestRound(context.Background(), 3)
	if engine.plans != plans {
		t.Fatal("记录型 View 不应删除最旧数据")
	}
}

func TestDeleteOldestScheduleRunsAtSecondTenOfEvenMinutes(t *testing.T) {
	base := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	cases := []struct {
		now, want time.Time
	}{
		{base, base.Add(10 * time.Second)},
		{base.Add(9 * time.Second), base.Add(10 * time.Second)},
		{base.Add(10 * time.Second), base.Add(70 * time.Second)},
		{base.Add(59*time.Second + 999*time.Millisecond), base.Add(70 * time.Second)},
	}
	for _, c := range cases {
		if got := nextDeleteOldestTick(c.now); !got.Equal(c.want) {
			t.Fatalf("nextDeleteOldestTick(%s)=%s，应为 %s", c.now.Format(time.RFC3339Nano), got.Format(time.RFC3339Nano), c.want.Format(time.RFC3339Nano))
		}
	}
	if !deleteOldestRoundDue(base.Add(10*time.Second + 3*time.Millisecond)) {
		t.Fatal("偶数分钟应执行删除")
	}
	if deleteOldestRoundDue(base.Add(70 * time.Second)) {
		t.Fatal("奇数分钟应跳过删除")
	}
}
