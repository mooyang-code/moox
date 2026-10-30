package view

import (
	"context"
	"log"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
)

// 定时删除最旧数据：时序 View 每个序列只保留最新的 bars 根，超出的部分由这个定时任务删掉，不再靠 A/B 重建裁剪。
//
// 任务每分钟第 10 秒触发（避开整点前后的周期提交与策略读取高峰），偶数分钟执行一轮。一轮里逐个活动索引处理：
// 先不持锁按快照算出每个序列的删除分界，再拿该索引的写锁在一个短事务里删掉分界之前的行并减计数，批与批之间放开写锁，
// 让实时写入插进来。2 分钟一轮的开销约为每分钟一轮的一半（成本主要在逐个序列查分界），序列最多临时多出 1 根。
const (
	deleteOldestTickSecond = 10 * time.Second
	// deleteOldestSeriesPerBatch 是一批最多处理的序列数，和每批行数一起控制单批持有写锁的时长。
	deleteOldestSeriesPerBatch = 64
	// deleteOldestBatchesPerRound 是一个索引一轮最多删几批，超出的部分留到下一轮。
	deleteOldestBatchesPerRound = 10
	// DefaultDeleteOldestBatchSize 是每批最多删除的最旧行数的默认值（2026-10-30 用线上 1m 因子索引实测，单批持锁约 30～50ms）。
	DefaultDeleteOldestBatchSize = 2000
	// MaxDeleteOldestBatchSize 是每批行数配置的上限。
	MaxDeleteOldestBatchSize = 20000
	// slowDeleteOldestBatch 是需要打日志的单批持锁时长。
	slowDeleteOldestBatch = 200 * time.Millisecond
)

// DeleteOldestOptions 是定时删除最旧数据的参数。
type DeleteOldestOptions struct {
	// BatchSize 是每批最多删除的最旧行数；为 0 时取默认值。
	BatchSize int
	// now 只供测试替换时钟。
	now func() time.Time
}

// StartDeleteOldest 启动定时删除最旧数据的任务，返回停止函数。维护尚未就绪（首次维护没跑完）时跳过本轮。
func (s *Service) StartDeleteOldest(ctx context.Context, opts DeleteOldestOptions) func() {
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultDeleteOldestBatchSize
	}
	now := opts.now
	if now == nil {
		now = time.Now
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			// 每次都按做完之后的时间重新对齐：上一轮超过一分钟时，错过的那次直接跳过，不会叠加。
			current := now()
			timer := time.NewTimer(nextDeleteOldestTick(current).Sub(current))
			select {
			case <-loopCtx.Done():
				timer.Stop()
				return
			case fired := <-timer.C:
				if !deleteOldestRoundDue(fired) {
					continue
				}
				s.deleteOldestRound(loopCtx, opts.BatchSize)
			}
		}
	}()
	return func() { cancel(); <-done }
}

// nextDeleteOldestTick 返回 now 之后最近的“某分钟第 10 秒”。
func nextDeleteOldestTick(now time.Time) time.Time {
	tick := now.Truncate(time.Minute).Add(deleteOldestTickSecond)
	if !tick.After(now) {
		tick = tick.Add(time.Minute)
	}
	return tick
}

// deleteOldestRoundDue 判断这次触发是否执行删除：只在偶数分钟执行。计时器可能稍晚触发几毫秒，按所在分钟判断。
func deleteOldestRoundDue(fired time.Time) bool {
	return fired.Minute()%2 == 0
}

// deleteOldestTarget 是一轮要处理的一个活动索引。
type deleteOldestTarget struct {
	ref     viewRef
	indexID string
	deleter viewindex.OldestRowsDeleter
}

// deleteOldestRound 删一轮：逐个时序 View 的活动索引删除超出保留根数的最旧数据。
func (s *Service) deleteOldestRound(ctx context.Context, batchSize int) {
	// 先在 s.mu 下抄出时序 View 的运行时，再逐个加 runtime.mu：别处是先持 runtime.mu 再取 s.mu，嵌套反过来会死锁。
	type candidate struct {
		ref     viewRef
		runtime *viewRuntime
	}
	s.mu.RLock()
	ready := s.maintenanceReady
	bars := s.seriesBars
	candidates := make([]candidate, 0, len(s.views))
	for ref, runtime := range s.views {
		if view := s.catalogViews[ref]; runtime != nil && view != nil && view.GetFreq() != "" {
			candidates = append(candidates, candidate{ref: ref, runtime: runtime})
		}
	}
	s.mu.RUnlock()
	if !ready || bars == 0 {
		return
	}
	targets := make([]deleteOldestTarget, 0, len(candidates))
	for _, item := range candidates {
		item.runtime.mu.Lock()
		activeID, nextID := item.runtime.active, item.runtime.next
		item.runtime.mu.Unlock()
		// 正在做 A/B 构建的 View 跳过：新索引按保留根数回填，切换后由下一轮接着处理。
		if activeID == "" || nextID != "" {
			continue
		}
		engine, err := s.engineFor(activeID)
		if err != nil {
			continue
		}
		deleter, ok := engine.(viewindex.OldestRowsDeleter)
		if !ok {
			continue
		}
		targets = append(targets, deleteOldestTarget{ref: item.ref, indexID: activeID, deleter: deleter})
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].ref.spaceID != targets[j].ref.spaceID {
			return targets[i].ref.spaceID < targets[j].ref.spaceID
		}
		return targets[i].ref.viewID < targets[j].ref.viewID
	})
	for _, target := range targets {
		if ctx.Err() != nil {
			return
		}
		if err := s.deleteOldestFromIndex(ctx, target, bars, batchSize); err != nil {
			log.Printf("storage view 删除最旧数据失败 space=%s view=%s index=%s：%v", target.ref.spaceID, target.ref.viewID, target.indexID, err)
		}
	}
}

// deleteOldestFromIndex 对一个索引删一轮，最多 batchSize × deleteOldestBatchesPerRound 行。
func (s *Service) deleteOldestFromIndex(ctx context.Context, target deleteOldestTarget, bars uint64, batchSize int) error {
	roundCap := batchSize * deleteOldestBatchesPerRound
	began := time.Now()
	var deleted uint64
	var maxHeld time.Duration
	for int(deleted) < roundCap {
		// 查分界只读，不持写锁；删除只碰比分界更旧的行，实时写入追加的新根不受影响。
		cuts, err := target.deleter.PlanOldestRows(ctx, target.indexID, bars, deleteOldestSeriesPerBatch, min(batchSize, roundCap-int(deleted)))
		if err != nil {
			return err
		}
		if len(cuts) == 0 {
			break
		}
		release, err := s.indexWriteGate(target.indexID).lock(ctx)
		if err != nil {
			return err
		}
		lockedAt := time.Now()
		n, err := target.deleter.DeleteOldestRows(ctx, target.indexID, cuts)
		held := time.Since(lockedAt)
		release()
		if err != nil {
			return err
		}
		maxHeld = max(maxHeld, held)
		s.metrics.ObserveViewOldestRowsDeleted(target.ref.spaceID, target.ref.viewID, n, held)
		if held >= slowDeleteOldestBatch {
			log.Printf("storage view 删除最旧数据单批持锁较久 space=%s view=%s 序列=%d 行=%d 持锁=%s", target.ref.spaceID, target.ref.viewID, len(cuts), n, held.Round(time.Millisecond))
		}
		if n == 0 {
			break
		}
		deleted += n
	}
	if deleted == 0 {
		return nil
	}
	release, err := s.indexWriteGate(target.indexID).lock(ctx)
	if err != nil {
		return err
	}
	from, err := target.deleter.RefreshCoverageFrom(ctx, target.indexID)
	release()
	if err != nil {
		return err
	}
	s.refreshCachedCoverageFrom(target, from)
	if int(deleted) >= roundCap {
		// 达到一轮上限说明还有积压（首次删到保留根数，或删除跟不上写入），剩余部分留到下一轮。
		log.Printf("storage view 删除最旧数据本轮达到上限 space=%s view=%s 行=%d 用时=%s 单批最长持锁=%s", target.ref.spaceID, target.ref.viewID, deleted, time.Since(began).Round(time.Millisecond), maxHeld.Round(time.Millisecond))
	}
	return nil
}

// refreshCachedCoverageFrom 把缓存的活动索引覆盖起点改成删除后的值，查询响应不用等下一次全量统计。
func (s *Service) refreshCachedCoverageFrom(target deleteOldestTarget, from string) {
	if from == "" {
		return
	}
	_, runtime := s.activeIndex(target.ref.spaceID, target.ref.viewID)
	stats, ok := cachedActiveIndexStats(runtime, target.indexID)
	if !ok {
		return
	}
	stats.IndexedFrom = from
	s.cacheActiveIndexStats(runtime, target.indexID, stats)
}
