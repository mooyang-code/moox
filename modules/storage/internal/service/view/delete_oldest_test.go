package view

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// oldestDeleterEngine 在查询替身上加删除最旧数据的接口：每次计划给出一个序列、rowsPerPlan 行，
// 共有 backlog 行可删；记录每次删除时是否持有该索引的写锁。
type oldestDeleterEngine struct {
	*queryEngine
	svc          *Service
	backlog      uint64
	plans        int
	deletes      int
	refreshes    int
	unlocked     int
	gotKeep      uint64
	gotMaxRows   []int
	gotMaxSerie  int
	finishKeep   uint64
	failDeleteAt int
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
	if e.failDeleteAt > 0 && e.deletes == e.failDeleteAt {
		return 0, errors.New("模拟删除失败")
	}
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

func (e *oldestDeleterEngine) FinishOldestRowsDelete(_ context.Context, indexID string, keep uint64) (string, error) {
	e.refreshes++
	e.finishKeep = keep
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

	// 每批 3 行、每轮上限 3 × deleteOldestRoundRowsFactor 行。
	if engine.deletes != deleteOldestRoundRowsFactor || engine.backlog != 1_000_000-3*deleteOldestRoundRowsFactor {
		t.Fatalf("一轮删了 %d 批、剩 %d 行，应在 %d 行的上限处停下", engine.deletes, engine.backlog, 3*deleteOldestRoundRowsFactor)
	}
	// 每批删除和收尾都推进修订号，分页读取才能发现读取期间数据变了。
	if got := svc.indexRevisionOf("prices-index"); got != uint64(engine.deletes+1) {
		t.Fatalf("修订号推进了 %d 次，应为 %d 批删除加 1 次收尾", got, engine.deletes+1)
	}
	if engine.finishKeep != 10000 {
		t.Fatalf("收尾时的保留根数=%d，应为 10000", engine.finishKeep)
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

	engine.backlog = 5
	plans := engine.plans

	// 记录型 View（没有频率）不处理。
	svc.maintenanceReady = true
	svc.catalogViews[viewRef{spaceID: "space", viewID: "prices"}] = &pb.View{SpaceId: "space", ViewId: "prices"}
	svc.deleteOldestRound(context.Background(), 3)
	if engine.plans != plans {
		t.Fatal("记录型 View 不应删除最旧数据")
	}
}

// 一轮中途删除失败：已经删掉的部分照样收尾（前移覆盖起点、修正保留根数），不留着错误的覆盖起点等下一轮。
func TestDeleteOldestRoundFinishesDeletedRowsWhenALaterBatchFails(t *testing.T) {
	svc, engine := deleteOldestTestService(100)
	engine.failDeleteAt = 3
	deleted, err := svc.deleteOldestFromIndex(context.Background(), deleteOldestTarget{
		ref: viewRef{spaceID: "space", viewID: "prices"}, indexID: "prices-index", deleter: engine,
	}, 10000, 3)
	if err == nil || deleted != 6 {
		t.Fatalf("第 3 批失败时应返回已删的 6 行与错误，实际 %d 行，错误 %v", deleted, err)
	}
	if engine.refreshes != 1 {
		t.Fatalf("已经删了行就要收尾，实际收尾 %d 次", engine.refreshes)
	}
	if len(svc.indexWriteGate("prices-index").token) != 1 {
		t.Fatal("失败后写锁没有释放")
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
