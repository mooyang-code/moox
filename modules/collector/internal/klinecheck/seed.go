package klinecheck

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"golang.org/x/time/rate"
	"trpc.group/trpc-go/trpc-go/log"
)

// 历史补齐（首次接入时把历史补到保留期与行情源都允许的深度）。
//
// 流程：采集计划的决策表为一个数据集写下第一行时间线（新数据集的“加一份”，或重新登记之后的第一份）→
// 时间线里有这个数据集、却还没有补齐记录 → 建补齐记录（此刻起下游不生效）→ 按标的分批拼缺口、建补采作业 →
// 作业全部结束（失败分块自动重试一次）→ 记 done，下游放行。拼作业连续失败 seedMaxPrepareFailures 次记 failed，同样放行。
// “首次接入”只看补齐记录在不在：时间线只由决策表写，重新登记（Reset）把时间线与补齐记录一起清掉；
// 记录按（空间, 数据集, 频率）唯一，停掉后又加回来的数据集已有记录，不再补。

const (
	// seedDefaultSubjectsPerJob 是一个补齐作业最多放的标的数。
	seedDefaultSubjectsPerJob = 500
	// seedSampleWindow 是估计剩余时间用的采样窗口。
	seedSampleWindow = 10 * time.Minute
	// seedMaxPrepareFailures 是拼作业连续失败多少次后放弃（记 failed、放行下游）；拼作业每分钟重试一次。
	seedMaxPrepareFailures = 5
	// seedLookbackMargin 是补齐深度比行情源回看上限少留的余量：上限按函数执行时刻往回算，补齐要跑一阵子
	// （失败分块还要在全部结束后重试一次），目标起点正好压在上限上，每个标的最早的一块到执行时都已越界。
	seedLookbackMargin = 24 * time.Hour
	// seedWindowMargin 是补齐分块执行前按回看上限截掉起点时多留的时间：覆盖限速排队、调用与函数内重试。
	seedWindowMargin = 5 * time.Minute
)

// SeedOptions 是历史补齐的参数与回调。
type SeedOptions struct {
	Enabled bool
	// SubjectsPerJob 是一个补齐作业最多放的标的数，不大于 0 用默认值。
	SubjectsPerJob int
	// MergeSessions 让日内分块跨休市区间合并（一个标的连续多个交易日一块），默认关闭由调用方打开：
	// 它要求行情源按“区间 + 根数”一次取回跨交易日的数据。
	MergeSessions bool
	// Depth 返回数据集的补齐深度（保留期、配置上限），不大于 0 表示不补。行情源的回看上限另由 Lookback 给出。
	Depth func(ctx context.Context, spaceID, datasetID, frequency string) (time.Duration, error)
	// Lookback 返回补采来源这个频率最多能往回取多久（按调用时刻算）；bounded 为假表示不限。为空表示不限。
	// 补齐深度不超过它减 seedLookbackMargin；分块执行时早于“此刻 − 回看上限”的部分截掉不补，不算失败。
	Lookback func(market marketfetch.PlanMarket, frequency string) (lookback time.Duration, bounded bool, err error)
	// Regions 返回市场补齐分块可用的云函数地域：必须是函数写入直达存储机的地域（入口主机就是存储机），
	// 经海外机转发的地域（带宽很小，会挤掉实时采集）不能算。为空时用补采函数提供方的全部地域。
	Regions func(spaceID, marketType string) []string
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

// seedRegions 返回市场补齐分块可用的云函数地域；没有时分块按任务的采集方式选执行端。
func (b *Backfiller) seedRegions(provider FunctionProvider, spaceID, marketType string) []string {
	if b.Seed.Regions != nil {
		return b.Seed.Regions(spaceID, marketType)
	}
	return provider.BackfillRegions(marketType)
}

// seedWindowStart 返回补齐分块此刻还能取到的最早 K 线开始时间（此刻 − 行情源回看上限 + seedWindowMargin，向上对齐）；
// 来源不限或查不到回看能力时 ok 为假。
func (b *Backfiller) seedWindowStart(ctx context.Context, market marketfetch.PlanMarket, frequencyName string) (time.Time, bool) {
	if b.Seed.Lookback == nil {
		return time.Time{}, false
	}
	frequency, err := marketdata.ParseFrequency(frequencyName)
	if err != nil {
		return time.Time{}, false
	}
	lookback, bounded, err := b.Seed.Lookback(market, frequencyName)
	if err != nil {
		log.WarnContextf(ctx, "collector history seed lookback unavailable market=%s frequency=%s: %v", market.MarketID, frequencyName, err)
		return time.Time{}, false
	}
	if !bounded {
		return time.Time{}, false
	}
	return alignUp(frequency, b.now().Add(-lookback+seedWindowMargin)), true
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

// startSeeds 为时间线里还没有补齐记录的数据集建记录并在后台拼作业。
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
	for _, key := range keys {
		segments := timeline[key]
		if len(segments) == 0 || !timeline.Active(key) {
			continue
		}
		effective := time.Unix(segments[0].From, 0).UTC()
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
	if b.Seed.Lookback != nil && b.Routes != nil {
		plan, found, err := b.Routes.BackfillRoute(ctx, spaceID, datasetID, frequencyName)
		if err != nil {
			return err
		}
		if found {
			lookback, bounded, err := b.Seed.Lookback(plan.Market, frequencyName)
			if err != nil {
				return err
			}
			if bounded {
				depth = min(depth, lookback-seedLookbackMargin)
			}
		}
	}
	// 新记录（例如重新登记之后）的连续失败次数从零算，不沿用旧记录在本进程里留下的。
	b.clearPrepareFailures(seedKey(spaceID, datasetID, frequencyName))
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
	// 第一行时间线在 Storage 确认登记时写，生效点还在未来（提前量几分钟）：记录先建好、下游先不生效，
	// 作业等到可以拼的时刻再拼（见 seedReadyAt，没到时由 advanceSeeds 接着做）。
	if !now.Before(seedReadyAt(frequency, effective, first)) {
		b.prepareAsync(spaceID, seed, first)
	}
	return nil
}

// seedReadyAt 是可以拼补齐作业的时刻：生效点之前最后一根 K 线过了截止时间（收盘 + 计划段的 lateness）。
// 补齐范围是 [目标起点, 生效点)，实时采集从生效点起；早于这个时刻拼，检查按截止时间把最后一根当作还没到期排除，
// 补齐止于它之前，这一根两边都不管、巡检也不看（2026-11-25 港股 lateness 90 秒，06:50 生效、06:50:10 拼作业，06:45 那根全缺）。
func seedReadyAt(frequency marketdata.Frequency, effective time.Time, first Segment) time.Time {
	return alignUp(frequency, effective).Add(time.Duration(first.LatenessS) * time.Second)
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
		err := b.prepareSeed(ctx, spaceID, seed, first)
		if err == nil {
			b.clearPrepareFailures(key)
			return
		}
		log.WarnContextf(ctx, "collector history seed prepare failed space=%s dataset=%s|%s: %v", spaceID, seed.DatasetID, seed.Frequency, err)
		b.prepareFailed(ctx, spaceID, seed, err)
	}()
}

// prepareFailed 记下一次拼作业失败。不到 seedMaxPrepareFailures 次时留在 preparing，下一轮推进时重新来过；
// 连续失败到次数（例如批次总被检查上限拒绝）记 failed、取消已建的作业并放行下游，原因写进说明，不让记录一直挡着下游。
// 说明写在库里最新的记录上（已建作业的 ID 已记在库里，不能用拼作业前的旧副本覆盖它们）。
func (b *Backfiller) prepareFailed(ctx context.Context, spaceID string, seed store.HistorySeed, cause error) {
	key := seedKey(spaceID, seed.DatasetID, seed.Frequency)
	b.mu.Lock()
	if b.seedFailures == nil {
		b.seedFailures = map[string]int{}
	}
	b.seedFailures[key]++
	failures := b.seedFailures[key]
	b.mu.Unlock()
	latest, ok, err := b.Store.HistorySeedOf(ctx, spaceID, seed.DatasetID, seed.Frequency)
	if err != nil || !ok {
		return
	}
	if failures < seedMaxPrepareFailures {
		latest.Note = fmt.Sprintf("拼补采作业失败（连续第 %d 次，到 %d 次放弃）：%v", failures, seedMaxPrepareFailures, cause)
		if _, err := b.Store.UpdateHistorySeed(ctx, latest, store.SeedPreparing); err != nil {
			log.WarnContextf(ctx, "collector history seed note write failed: %v", err)
		}
		return
	}
	now := b.now()
	latest.Status, latest.FinishedAt = store.SeedFailed, &now
	latest.Note = fmt.Sprintf("拼补采作业连续 %d 次失败，已放弃并放行下游：%v", failures, cause)
	// 先改状态再取消作业，与手动放行同一顺序：条件写没生效（已被手动放行）时作业已由放行取消。
	updated, err := b.Store.UpdateHistorySeed(ctx, latest, store.SeedPreparing)
	if err != nil {
		log.WarnContextf(ctx, "collector history seed failure write failed: %v", err)
		return
	}
	b.clearPrepareFailures(key)
	if !updated {
		return
	}
	if err := b.cancelJobs(ctx, latest.JobIDs); err != nil {
		log.WarnContextf(ctx, "collector history seed jobs cancel failed space=%s dataset=%s|%s: %v", spaceID, seed.DatasetID, seed.Frequency, err)
	}
	b.flushNotices(ctx, "seed:"+seed.DatasetID)
	log.WarnContextf(ctx, "collector history seed gave up space=%s dataset=%s|%s after %d prepare failures: %v", spaceID, seed.DatasetID, seed.Frequency, failures, cause)
}

// clearPrepareFailures 清掉补齐连续拼作业失败的次数。
func (b *Backfiller) clearPrepareFailures(key string) {
	b.mu.Lock()
	delete(b.seedFailures, key)
	b.mu.Unlock()
}

// seedBatchSize 估计一次检查放多少个标的：检查的（标的 × K 线）总数、返回的缺口数都有上限。
// bars 是每个标的应有的 K 线数（按交易日历，见 tradingBars）；gaps 是每个标的预计的缺口数：
// 合并时约 K 线数 ÷ MaxChunkBars，不合并时每个交易时段一段。
func seedBatchSize(perJob, bars, gaps int) int {
	batch := min(perJob, max(MaxCheckPoints/max(bars, 1), 1))
	return min(batch, max(MaxGaps*3/4/max(gaps, 1), 1))
}

// prepareSeed 按标的分批检查缺口、建补采作业，每建一个作业就记进补齐记录；全部拼完记 running。
// 记录被手动放行（不再是 preparing）时，已建的作业全部取消。
func (b *Backfiller) prepareSeed(ctx context.Context, spaceID string, seed store.HistorySeed, first Segment) error {
	frequency, err := marketdata.ParseFrequency(seed.Frequency)
	if err != nil {
		return err
	}
	// 先把上一次没拼完留下的作业取消（拼作业的进程退出后重新来过）。
	if err := b.cancelJobs(ctx, seed.JobIDs); err != nil {
		return err
	}
	seed.JobIDs, seed.Note = nil, ""
	// 补到生效点为止（含生效点前最后一根）：只在 seedReadyAt 之后拼，这一根已过截止时间，检查不会把它排除。
	to := seed.EffectiveFrom
	subjects := uniqueSorted(first.Subjects)
	// 根数按交易日历数，与检查的预检同一套数法（tradingBars），批次才过得了预检。
	bars, sessions, err := b.Checker.tradingBars(spaceID, frequency, seed.Frequency, seed.TargetFrom, to)
	if err != nil {
		return err
	}
	merge := b.Seed.MergeSessions
	gapsPerSubject := bars/MaxChunkBars + 2
	if !merge {
		gapsPerSubject += sessions
	}
	batchSize := seedBatchSize(b.seedSubjectsPerJob(), bars, gapsPerSubject)
	jobs := 0
	for from := 0; from < len(subjects); from += batchSize {
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
		// 条件写：记录已被手动放行就不再往里加，并取消已建的作业（含刚建的这个）。
		updated, err := b.Store.UpdateHistorySeed(ctx, seed, store.SeedPreparing)
		if err != nil {
			return err
		}
		if !updated {
			return b.cancelJobs(ctx, seed.JobIDs)
		}
	}
	if jobs == 0 {
		now := b.now()
		seed.Status, seed.Note, seed.FinishedAt = store.SeedDone, "没有缺口需要补", &now
	} else {
		seed.Status = store.SeedRunning
	}
	updated, err := b.Store.UpdateHistorySeed(ctx, seed, store.SeedPreparing)
	if err != nil {
		return err
	}
	if !updated {
		return b.cancelJobs(ctx, seed.JobIDs)
	}
	return nil
}

func (b *Backfiller) cancelJobs(ctx context.Context, ids []string) error {
	for _, id := range ids {
		if err := b.Store.CancelJob(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// submitSeedBatch 检查一批标的的缺口并建一个补采作业；没有缺口返回空作业 ID。
// 检查结果被截断（缺口数超过 MaxGaps）时直接返回错误，不拆批重来：advanceSeeds 每分钟重拼同一批、再次截断，
// 连续 seedMaxPrepareFailures 次后记 failed 放行（见 prepareFailed）。批大小按全新数据集的缺口数估计（seedBatchSize），
// 只有补齐区间里已有零碎数据、缺口数远超估计时才会截断（已知差别，见采集.md 第 3b 节）。
func (b *Backfiller) submitSeedBatch(ctx context.Context, spaceID string, seed store.HistorySeed, batch []string, to time.Time, merge bool) (string, error) {
	request := Request{SpaceID: spaceID, DatasetID: seed.DatasetID, Subjects: batch, From: seed.TargetFrom, To: to, FullHistory: true}
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
			// 本进程没有在拼它（进程重启过，或到了可以拼的时刻）：重新拼。
			b.mu.Lock()
			busy := b.seedPreparing[key]
			b.mu.Unlock()
			if busy || now.Before(seed.EffectiveFrom) {
				continue
			}
			first, ok, err := b.firstSegment(ctx, spaceID, seed)
			if err != nil {
				return err
			}
			if !ok {
				// 时间线里已经没有这个数据集（生效点之前又停止了采集）：没有可补的标的，记 done 放行，不让记录一直挡着下游。
				if err := b.cancelJobs(ctx, seed.JobIDs); err != nil {
					return err
				}
				seed.Status, seed.FinishedAt, seed.Note = store.SeedDone, &now, "生效前已停止采集，不补"
				if _, err := b.Store.UpdateHistorySeed(ctx, seed, store.SeedPreparing); err != nil {
					return err
				}
				continue
			}
			frequency, err := marketdata.ParseFrequency(seed.Frequency)
			if err != nil {
				return err
			}
			if !now.Before(seedReadyAt(frequency, seed.EffectiveFrom, first)) {
				b.prepareAsync(spaceID, seed, first)
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
				if _, err := b.Store.UpdateHistorySeed(ctx, seed, store.SeedRunning); err != nil {
					return err
				}
				continue
			}
			seed.Status, seed.FinishedAt = store.SeedDone, &now
			if failed := total[store.BackfillFailed]; failed > 0 {
				seed.Note = fmt.Sprintf("%d 个分块失败（已重试），用 collector kline check 查缺口再手动补采", failed)
			}
			if _, err := b.Store.UpdateHistorySeed(ctx, seed, store.SeedRunning); err != nil {
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
		if _, err := b.Store.UpdateHistorySeed(ctx, seed, store.SeedPreparing, store.SeedRunning); err != nil {
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
