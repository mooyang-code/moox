package view

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
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
	failFinish   bool
	notOddRev    int
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
	if e.svc.runtimeOf(marketRef).revision.Load()&1 == 0 {
		e.notOddRev++
	}
	if e.failDeleteAt > 0 && e.deletes == e.failDeleteAt {
		return 0, errors.New("模拟删除失败")
	}
	if len(e.svc.runtimeOf(marketRef).gate.token) != 0 {
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
	if e.svc.runtimeOf(marketRef).revision.Load()&1 == 0 {
		e.notOddRev++
	}
	if len(e.svc.runtimeOf(marketRef).gate.token) != 0 {
		e.unlocked++
	}
	if e.failFinish {
		return "", errors.New("模拟收尾失败")
	}
	return "2026-10-30T00:05:00.000000000Z", nil
}

func deleteOldestTestService(backlog uint64) (*Service, *oldestDeleterEngine) {
	engine := &oldestDeleterEngine{queryEngine: &queryEngine{}, backlog: backlog}
	svc := queryTestService(engine, true)
	engine.svc = svc
	svc.seriesBars = 10000
	return svc, engine
}

func TestDeleteOldestRoundDeletesInBatchesUnderWriteLockUpToRoundCap(t *testing.T) {
	svc, engine := deleteOldestTestService(1_000_000)
	beforeRevision := svc.runtimeOf(marketRef).revision.Load()
	svc.deleteOldestRound(context.Background(), 3)

	// 每批 3 行、每轮上限 3 × deleteOldestRoundRowsFactor 行。
	if engine.deletes != deleteOldestRoundRowsFactor || engine.backlog != 1_000_000-3*deleteOldestRoundRowsFactor {
		t.Fatalf("一轮删了 %d 批、剩 %d 行，应在 %d 行的上限处停下", engine.deletes, engine.backlog, 3*deleteOldestRoundRowsFactor)
	}
	// 每批删除和收尾采用 Seqlock 前后各加 1（合计 +2），结束时恢复偶数，分页读取才能在任意交错下发现变更。
	afterRevision := svc.runtimeOf(marketRef).revision.Load()
	if afterRevision&1 != 0 {
		t.Fatalf("删除结束后修订号应恢复为偶数，实际=%d", afterRevision)
	}
	if got, want := afterRevision-beforeRevision, 2*uint64(engine.deletes+1); got != want {
		t.Fatalf("修订号推进了 %d，应为 %d（%d 批删除加 1 次收尾，每次 +2）", got, want, engine.deletes+1)
	}
	if engine.notOddRev != 0 {
		t.Fatalf("有 %d 次引擎修改期间修订号未置为奇数", engine.notOddRev)
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
	if len(svc.runtimeOf(marketRef).gate.token) != 1 {
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

	// 记录型 View 不处理。
	svc.runtimeOf(marketRef).setSpec(recordSpec("space", "market"))
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
		runtime: svc.runtimeOf(marketRef), indexID: marketIndexID, deleter: engine,
	}, 10000, 3)
	if err == nil || deleted != 6 {
		t.Fatalf("第 3 批失败时应返回已删的 6 行与错误，实际 %d 行，错误 %v", deleted, err)
	}
	if engine.refreshes != 1 {
		t.Fatalf("已经删了行就要收尾，实际收尾 %d 次", engine.refreshes)
	}
	if len(svc.runtimeOf(marketRef).gate.token) != 1 {
		t.Fatal("失败后写锁没有释放")
	}
	if rev := svc.runtimeOf(marketRef).revision.Load(); rev&1 != 0 {
		t.Fatalf("删除批次失败后修订号应恢复偶数，实际=%d", rev)
	}
	if engine.notOddRev != 0 {
		t.Fatalf("失败场景下有 %d 次引擎修改期间修订号未置为奇数", engine.notOddRev)
	}

	// 收尾失败同样不能让 revision 卡在奇数状态。
	svc2, engine2 := deleteOldestTestService(10)
	engine2.failFinish = true
	if _, err := svc2.deleteOldestFromIndex(context.Background(), deleteOldestTarget{
		runtime: svc2.runtimeOf(marketRef), indexID: marketIndexID, deleter: engine2,
	}, 10000, 3); err == nil {
		t.Fatal("期望收尾返回模拟错误")
	}
	if rev := svc2.runtimeOf(marketRef).revision.Load(); rev&1 != 0 {
		t.Fatalf("收尾失败后修订号应恢复偶数，实际=%d", rev)
	}
	if engine2.notOddRev != 0 {
		t.Fatalf("收尾失败场景下有 %d 次引擎修改期间修订号未置为奇数", engine2.notOddRev)
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
