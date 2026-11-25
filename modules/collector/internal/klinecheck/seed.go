package klinecheck

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"golang.org/x/time/rate"
	"trpc.group/trpc-go/trpc-go/log"
)

// 历史补齐（首次接入时把历史补到保留期与行情源都允许的深度）。
//
// 流程：时间线里一个数据集刚出现（最早生效点在最近 seedRecentWindow 内）→ 建补齐记录（此刻起下游不生效）→
// 按标的分批拼缺口、建补采作业 → 作业全部结束（失败分块自动重试一次）→ 记 done，下游放行。
// 已有数据的数据集（最早生效点更早）不补，所以功能上线不会让线上已有的数据集进入不生效状态。

const (
	// seedRecentWindow 是“新数据集”的判定窗口：最早生效点在这个时间内才补。
	seedRecentWindow = 24 * time.Hour
	// seedDefaultSubjectsPerJob 是一个补齐作业最多放的标的数。
	seedDefaultSubjectsPerJob = 500
	// seedSampleWindow 是估计剩余时间用的采样窗口。
	seedSampleWindow = 10 * time.Minute
)

// SeedOptions 是历史补齐的参数与回调。
type SeedOptions struct {
	Enabled bool
	// SubjectsPerJob 是一个补齐作业最多放的标的数，不大于 0 用默认值。
	SubjectsPerJob int
	// MergeSessions 让日内分块跨休市区间合并（一个标的连续多个交易日一块），默认关闭由调用方打开：
	// 它要求行情源按“区间 + 根数”一次取回跨交易日的数据。
	MergeSessions bool
	// Depth 返回数据集的补齐深度，不大于 0 表示不补（保留期太短、行情源不支持更早的数据）。
	Depth func(ctx context.Context, spaceID, datasetID, frequency string) (time.Duration, error)
	// Slots 返回空间补齐分块的并发上限（地域数 × 单个补采函数的预留并发）；为空或返回值不大于 0 时用空间的补采并发上限。
	Slots func(ctx context.Context, spaceID string) int
	// RowsPerSecond、LocalRowsPerSecond 是补齐写 Storage 的全局速率上限与本地执行端的速率上限，不大于 0 表示不限。
	RowsPerSecond      float64
	LocalRowsPerSecond float64
}

type seedSample struct {
	at   time.Time
	done int
}

// SeedView 是补齐记录加上分块进度，给状态接口与下游用。
type SeedView struct {
	store.HistorySeed
	TotalChunks  int
	DoneChunks   int
	FailedChunks int
	// ETA 按最近 10 分钟的速率估计剩余时间，不能估计时为 0。
	ETA time.Duration
}

func seedKey(spaceID, datasetID, frequency string) string {
	return spaceID + "|" + datasetID + "|" + frequency
}

func (b *Backfiller) seedSubjectsPerJob() int {
	if b.Seed.SubjectsPerJob > 0 {
		return b.Seed.SubjectsPerJob
	}
	return seedDefaultSubjectsPerJob
}

// seedJobIDs 返回空间里补齐中的作业 ID（Tick 据此把它们单独排队、单独分名额）。
func (b *Backfiller) seedJobIDs(ctx context.Context, spaceID string) (map[string]bool, error) {
	out := map[string]bool{}
	if !b.Seed.Enabled {
		return out, nil
	}
	seeds, err := b.Store.HistorySeeds(ctx, spaceID, true)
	if err != nil {
		return nil, err
	}
	for _, seed := range seeds {
		for _, id := range seed.JobIDs {
			out[id] = true
		}
	}
	return out, nil
}

// seedSlots 是补齐分块在空间里的并发上限。
func (b *Backfiller) seedSlots(ctx context.Context, spaceID string) int {
	if b.Seed.Slots != nil {
		if n := b.Seed.Slots(ctx, spaceID); n > 0 {
			return n
		}
	}
	return b.maxInFlight(ctx, spaceID)
}

// waitSeedRate 在补齐分块调用执行端前按根数扣令牌：全局上限给实时写入留余量；本地执行端的数据要经海外机，另有更严的上限。
func (b *Backfiller) waitSeedRate(ctx context.Context, local bool, bars int) error {
	b.mu.Lock()
	if b.seedLimiter == nil && b.Seed.RowsPerSecond > 0 {
		b.seedLimiter = rate.NewLimiter(rate.Limit(b.Seed.RowsPerSecond), max(int(b.Seed.RowsPerSecond), MaxChunkBars))
	}
	if b.seedLocalLimiter == nil && b.Seed.LocalRowsPerSecond > 0 {
		b.seedLocalLimiter = rate.NewLimiter(rate.Limit(b.Seed.LocalRowsPerSecond), max(int(b.Seed.LocalRowsPerSecond), MaxChunkBars))
	}
	limiters := []*rate.Limiter{b.seedLimiter}
	if local {
		limiters = append(limiters, b.seedLocalLimiter)
	}
	b.mu.Unlock()
	for _, limiter := range limiters {
		if limiter == nil {
			continue
		}
		if err := limiter.WaitN(ctx, min(max(bars, 1), limiter.Burst())); err != nil {
			return err
		}
	}
	return nil
}

// SeedTick 是历史补齐的一轮推进（每个空间每分钟一次）：为新数据集建补齐，并推进补齐中的记录。
func (b *Backfiller) SeedTick(ctx context.Context, spaceID string) error {
	if !b.Seed.Enabled || b == nil || b.Checker == nil || b.Store == nil {
		return nil
	}
	if b.Paused != nil {
		if paused, err := b.Paused(ctx, spaceID); err != nil || paused {
			return err
		}
	}
	if err := b.startSeeds(ctx, spaceID); err != nil {
		log.WarnContextf(ctx, "collector history seed start failed space=%s: %v", spaceID, err)
	}
	return b.advanceSeeds(ctx, spaceID)
}

// startSeeds 为时间线里刚出现的数据集建补齐记录并在后台拼作业。
func (b *Backfiller) startSeeds(ctx context.Context, spaceID string) error {
	timeline, err := loadTimeline(ctx, b.Checker.Timeline, spaceID)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(timeline))
	for key := range timeline {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	now := b.now()
	for _, key := range keys {
		segments := timeline[key]
		if len(segments) == 0 || !timeline.Active(key) {
			continue
		}
		effective := time.Unix(segments[0].From, 0).UTC()
		if now.Sub(effective) > seedRecentWindow {
			continue
		}
		datasetID, frequency, _ := strings.Cut(key, "|")
		if _, exists, err := b.Store.HistorySeedOf(ctx, spaceID, datasetID, frequency); err != nil {
			return err
		} else if exists {
			continue
		}
		if err := b.beginSeed(ctx, spaceID, datasetID, frequency, effective, segments[0]); err != nil {
			log.WarnContextf(ctx, "collector history seed begin failed space=%s dataset=%s|%s: %v", spaceID, datasetID, frequency, err)
		}
	}
	return nil
}

// beginSeed 建补齐记录；深度为 0 的直接记 done。作业在后台拼，期间记录是 preparing。
func (b *Backfiller) beginSeed(ctx context.Context, spaceID, datasetID, frequencyName string, effective time.Time, first Segment) error {
	frequency, err := marketdata.ParseFrequency(frequencyName)
	if err != nil {
		return err
	}
	var depth time.Duration
	if b.Seed.Depth != nil {
		if depth, err = b.Seed.Depth(ctx, spaceID, datasetID, frequencyName); err != nil {
			return err
		}
	}
	now := b.now()
	seed := store.HistorySeed{SpaceID: spaceID, DatasetID: datasetID, Frequency: frequencyName, Status: store.SeedPreparing, EffectiveFrom: effective, StartedAt: now}
	seed.TargetFrom = alignUp(frequency, effective.Add(-depth))
	if depth <= 0 || !seed.TargetFrom.Before(effective) {
		seed.Status, seed.Note, seed.TargetFrom = store.SeedDone, "保留期或行情源不允许补更早的历史", effective
		seed.FinishedAt = &now
	}
	created, err := b.Store.CreateHistorySeed(ctx, seed)
	if err != nil || !created || seed.Status == store.SeedDone {
		return err
	}
	b.prepareAsync(spaceID, seed, first)
	return nil
}

// prepareAsync 在后台拼补齐作业；同一个补齐在本进程里只拼一份。
func (b *Backfiller) prepareAsync(spaceID string, seed store.HistorySeed, first Segment) {
	key := seedKey(spaceID, seed.DatasetID, seed.Frequency)
	b.mu.Lock()
	if b.seedPreparing == nil {
		b.seedPreparing = map[string]bool{}
	}
	if b.seedPreparing[key] {
		b.mu.Unlock()
		return
	}
	b.seedPreparing[key] = true
	b.mu.Unlock()
	b.workers.Add(1)
	go func() {
		defer b.workers.Done()
		defer func() {
			b.mu.Lock()
			delete(b.seedPreparing, key)
			b.mu.Unlock()
		}()
		ctx := b.baseContext()
		if err := b.prepareSeed(ctx, spaceID, seed, first); err != nil {
			log.WarnContextf(ctx, "collector history seed prepare failed space=%s dataset=%s|%s: %v", spaceID, seed.DatasetID, seed.Frequency, err)
			// 拼作业失败：留在 preparing，下一轮推进时没有本进程的拼作业协程会重新来过。
			seed.Note = "拼补采作业失败：" + err.Error()
			if updateErr := b.Store.UpdateHistorySeed(ctx, seed); updateErr != nil {
				log.WarnContextf(ctx, "collector history seed note write failed: %v", updateErr)
			}
		}
	}()
}

// seedBatchSize 估计一次检查放多少个标的：检查的（标的 × K 线）总数、返回的缺口数都有上限。
func seedBatchSize(perJob, bars, groups int) int {
	points := max(bars*max(groups, 1), 1)
	batch := min(perJob, max(MaxCheckPoints/points, 1))
	gapsPerSubject := (bars/MaxChunkBars + 2) * max(groups, 1)
	return min(batch, max(MaxGaps*3/4/gapsPerSubject, 1))
}

// prepareSeed 按标的分批检查缺口、建补采作业，每建一个作业就记进补齐记录；全部拼完记 running。
func (b *Backfiller) prepareSeed(ctx context.Context, spaceID string, seed store.HistorySeed, first Segment) error {
	frequency, err := marketdata.ParseFrequency(seed.Frequency)
	if err != nil {
		return err
	}
	// 先把上一次没拼完留下的作业取消（拼作业的进程退出后重新来过）。
	for _, id := range seed.JobIDs {
		if err := b.Store.CancelJob(ctx, id); err != nil {
			return err
		}
	}
	seed.JobIDs, seed.Note = nil, ""
	to := seed.EffectiveFrom
	if limit := alignDown(frequency, b.now()); limit.Before(to) {
		to = limit
	}
	subjects := append([]string(nil), first.Subjects...)
	if len(subjects) == 0 {
		for _, group := range first.groups() {
			subjects = append(subjects, group.Subjects...)
		}
	}
	subjects = uniqueSorted(subjects)
	expected, err := b.Checker.expectedBars(spaceID, frequency, seed.Frequency, seed.TargetFrom, to, 0)
	if err != nil {
		return err
	}
	batchSize := seedBatchSize(b.seedSubjectsPerJob(), len(expected), len(first.groups()))
	merge := b.Seed.MergeSessions
	jobs := 0
	for from := 0; from < len(subjects); from += batchSize {
		// 用户手动放行或重新登记之后记录不再是 preparing，别再往里加作业。
		current, ok, err := b.Store.HistorySeedOf(ctx, spaceID, seed.DatasetID, seed.Frequency)
		if err != nil {
			return err
		}
		if !ok || current.Status != store.SeedPreparing {
			return b.cancelJobs(ctx, seed.JobIDs)
		}
		batch := subjects[from:min(from+batchSize, len(subjects))]
		id, err := b.submitSeedBatch(ctx, spaceID, seed, batch, to, merge)
		if err != nil {
			return err
		}
		if id == "" {
			continue
		}
		jobs++
		seed.JobIDs = append(seed.JobIDs, id)
		if err := b.Store.UpdateHistorySeed(ctx, seed); err != nil {
			return err
		}
	}
	current, ok, err := b.Store.HistorySeedOf(ctx, spaceID, seed.DatasetID, seed.Frequency)
	if err != nil || !ok || current.Status != store.SeedPreparing {
		return err
	}
	if jobs == 0 {
		now := b.now()
		seed.Status, seed.Note, seed.FinishedAt = store.SeedDone, "没有缺口需要补", &now
	} else {
		seed.Status = store.SeedRunning
	}
	return b.Store.UpdateHistorySeed(ctx, seed)
}

func (b *Backfiller) cancelJobs(ctx context.Context, ids []string) error {
	for _, id := range ids {
		if err := b.Store.CancelJob(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// submitSeedBatch 检查一批标的的缺口并建一个补采作业；检查结果被截断时把批对半拆开重来。没有缺口返回空作业 ID。
func (b *Backfiller) submitSeedBatch(ctx context.Context, spaceID string, seed store.HistorySeed, batch []string, to time.Time, merge bool) (string, error) {
	request := Request{SpaceID: spaceID, DatasetID: seed.DatasetID, Subjects: batch, From: seed.TargetFrom, To: to, BeforeFirstPlan: true}
	if merge {
		request.MergeSessions, request.MaxGapBars = true, MaxChunkBars
	}
	result, err := b.Checker.Check(ctx, request)
	if err != nil {
		return "", err
	}
	if result.Truncated {
		if len(batch) < 2 {
			return "", fmt.Errorf("标的 %s 的缺口数超过 %d", batch[0], MaxGaps)
		}
		return "", fmt.Errorf("一批 %d 个标的的缺口数超过 %d，调小 history_seed.subjects_per_job", len(batch), MaxGaps)
	}
	var gaps []Gap
	for _, gap := range result.Gaps {
		if gap.Frequency == seed.Frequency {
			gaps = append(gaps, gap)
		}
	}
	if len(gaps) == 0 {
		return "", nil
	}
	job := store.BackfillJob{SpaceID: spaceID, Source: store.BackfillSourceManual, Kind: store.BackfillKindGap, DatasetID: seed.DatasetID, Frequency: seed.Frequency, From: seed.TargetFrom, To: to}
	id, _, err := b.createJobWith(ctx, job, gaps, MaxChunksPerJob, merge)
	return id, err
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

// advanceSeeds 推进补齐中的记录：作业全部结束后，有失败分块先重试一次，之后记 done。
func (b *Backfiller) advanceSeeds(ctx context.Context, spaceID string) error {
	seeds, err := b.Store.HistorySeeds(ctx, spaceID, true)
	if err != nil {
		return err
	}
	now := b.now()
	for _, seed := range seeds {
		key := seedKey(spaceID, seed.DatasetID, seed.Frequency)
		switch seed.Status {
		case store.SeedPreparing:
			// 本进程没有在拼它（进程重启过）：重新拼。
			b.mu.Lock()
			busy := b.seedPreparing[key]
			b.mu.Unlock()
			if !busy {
				first, ok, err := b.firstSegment(ctx, spaceID, seed)
				if err != nil {
					return err
				}
				if ok {
					b.prepareAsync(spaceID, seed, first)
				}
			}
		case store.SeedRunning:
			counts, err := b.Store.ChunkStatusCountsOf(ctx, seed.JobIDs)
			if err != nil {
				return err
			}
			total := sumCounts(counts)
			b.recordSample(key, now, total[store.BackfillSucceeded])
			if total[store.BackfillPending] > 0 || total[store.BackfillRunning] > 0 {
				continue
			}
			if total[store.BackfillFailed] > 0 && !seed.Retried {
				retryable := true
				for id, perJob := range counts {
					if perJob[store.BackfillFailed] == 0 {
						continue
					}
					// 分块都结束了但作业状态还没被 Tick 改成 failed：等下一轮再重试。
					if _, err := b.Store.RetryJob(ctx, id); errors.Is(err, store.ErrJobNotRetryable) {
						retryable = false
					} else if err != nil {
						return err
					}
				}
				if !retryable {
					continue
				}
				seed.Retried = true
				if err := b.Store.UpdateHistorySeed(ctx, seed); err != nil {
					return err
				}
				continue
			}
			seed.Status, seed.FinishedAt = store.SeedDone, &now
			if failed := total[store.BackfillFailed]; failed > 0 {
				seed.Note = fmt.Sprintf("%d 个分块失败（已重试），用 collector kline check 查缺口再手动补采", failed)
			}
			if err := b.Store.UpdateHistorySeed(ctx, seed); err != nil {
				return err
			}
			b.dropSamples(key)
			b.flushNotices(ctx, "seed:"+seed.DatasetID)
			log.InfoContextf(ctx, "collector history seed finished space=%s dataset=%s|%s failed=%d", spaceID, seed.DatasetID, seed.Frequency, total[store.BackfillFailed])
		}
	}
	return nil
}

func (b *Backfiller) firstSegment(ctx context.Context, spaceID string, seed store.HistorySeed) (Segment, bool, error) {
	timeline, err := loadTimeline(ctx, b.Checker.Timeline, spaceID)
	if err != nil {
		return Segment{}, false, err
	}
	segments := timeline[DatasetKey(seed.DatasetID, seed.Frequency)]
	if len(segments) == 0 {
		return Segment{}, false, nil
	}
	return segments[0], true, nil
}

func sumCounts(counts map[string]map[string]int) map[string]int {
	total := map[string]int{}
	for _, perJob := range counts {
		for status, n := range perJob {
			total[status] += n
		}
	}
	return total
}

func (b *Backfiller) recordSample(key string, at time.Time, done int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.seedSamples == nil {
		b.seedSamples = map[string][]seedSample{}
	}
	samples := append(b.seedSamples[key], seedSample{at: at, done: done})
	for len(samples) > 1 && at.Sub(samples[0].at) > seedSampleWindow {
		samples = samples[1:]
	}
	b.seedSamples[key] = samples
}

func (b *Backfiller) dropSamples(key string) {
	b.mu.Lock()
	delete(b.seedSamples, key)
	b.mu.Unlock()
}

// SeedViews 返回补齐记录与分块进度（补齐中的排前）；onlyActive 只返回补齐中的。spaceID 为空返回全部空间。
func (b *Backfiller) SeedViews(ctx context.Context, spaceID string, onlyActive bool) ([]SeedView, error) {
	seeds, err := b.Store.HistorySeeds(ctx, spaceID, onlyActive)
	if err != nil {
		return nil, err
	}
	views := make([]SeedView, 0, len(seeds))
	for _, seed := range seeds {
		view := SeedView{HistorySeed: seed}
		if len(seed.JobIDs) > 0 {
			counts, err := b.Store.ChunkStatusCountsOf(ctx, seed.JobIDs)
			if err != nil {
				return nil, err
			}
			total := sumCounts(counts)
			view.DoneChunks, view.FailedChunks = total[store.BackfillSucceeded], total[store.BackfillFailed]
			for _, n := range total {
				view.TotalChunks += n
			}
			if seed.Status == store.SeedRunning {
				view.ETA = b.seedETA(seedKey(seed.SpaceID, seed.DatasetID, seed.Frequency), view.TotalChunks-view.DoneChunks-view.FailedChunks)
			}
		}
		views = append(views, view)
	}
	return views, nil
}

// seedETA 按采样窗口里的完成速率估计剩余时间；样本不足或没有进展时返回 0。
func (b *Backfiller) seedETA(key string, remaining int) time.Duration {
	b.mu.Lock()
	samples := b.seedSamples[key]
	b.mu.Unlock()
	if remaining <= 0 || len(samples) < 2 {
		return 0
	}
	first, last := samples[0], samples[len(samples)-1]
	elapsed, done := last.at.Sub(first.at), last.done-first.done
	if elapsed <= 0 || done <= 0 {
		return 0
	}
	return time.Duration(float64(remaining) / (float64(done) / elapsed.Seconds()) * float64(time.Second))
}

// SkipSeeds 手动放行：取消补齐中的作业、记 skipped，已经补进去的数据保留。datasetID 为空表示空间内全部补齐中的数据集，返回放行的个数。
func (b *Backfiller) SkipSeeds(ctx context.Context, spaceID, datasetID string) (int, error) {
	seeds, err := b.Store.HistorySeeds(ctx, spaceID, true)
	if err != nil {
		return 0, err
	}
	skipped := 0
	now := b.now()
	for _, seed := range seeds {
		if datasetID != "" && seed.DatasetID != datasetID {
			continue
		}
		seed.Status, seed.FinishedAt, seed.Note = store.SeedSkipped, &now, "用户手动放行"
		// 先改状态再取消作业：拼作业的协程每建一个作业前都会看状态，看到不是 preparing 就停下并取消它已建的作业。
		if err := b.Store.UpdateHistorySeed(ctx, seed); err != nil {
			return skipped, err
		}
		if err := b.cancelJobs(ctx, seed.JobIDs); err != nil {
			return skipped, err
		}
		b.dropSamples(seedKey(spaceID, seed.DatasetID, seed.Frequency))
		skipped++
	}
	if skipped > 0 {
		b.flushNotices(ctx, "seed-skip")
	}
	return skipped, nil
}
