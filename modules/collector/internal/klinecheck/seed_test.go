package klinecheck

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/stretchr/testify/require"
)

// seedFixture 是历史补齐的夹具：计划生效点在 2 小时前（算新数据集），补齐深度 30 分钟，
// BTC 在生效点前 30 分钟里按 gapMinutes 缺 K 线，ETH 齐全。
func seedFixture(t *testing.T, age time.Duration, gapMinutes ...int) (*Backfiller, *healingInvoker, *fakeRows, time.Time) {
	t.Helper()
	backfiller, invoker, rows, base := newBackfillFixtureAged(t, "", age)
	rows.mu.Lock()
	rows.missing["BTC-USDT"] = map[int64]bool{}
	rows.missing["ETH-USDT"] = map[int64]bool{}
	for _, minute := range gapMinutes {
		rows.missing["BTC-USDT"][base.Add(-time.Duration(minute)*time.Minute).Unix()] = true
	}
	rows.mu.Unlock()
	backfiller.Seed = SeedOptions{
		Enabled: true,
		Depth: func(context.Context, string, string, string) (time.Duration, error) {
			return 30 * time.Minute, nil
		},
	}
	return backfiller, invoker, rows, base
}

func seedOf(t *testing.T, b *Backfiller) store.HistorySeed {
	t.Helper()
	seed, ok, err := b.Store.HistorySeedOf(context.Background(), "crypto", "ds", "1m")
	require.NoError(t, err)
	require.True(t, ok, "应当有补齐记录")
	return seed
}

// 新数据集：建记录、拼作业、补齐分块、全部成功后记 done。
func TestSeedStartsForNewDatasetAndCompletes(t *testing.T) {
	b, invoker, rows, _ := seedFixture(t, 2*time.Hour, 5, 6, 20)
	ctx := context.Background()

	require.NoError(t, b.SeedTick(ctx, "crypto"))
	b.Wait()
	seed := seedOf(t, b)
	require.Equal(t, store.SeedRunning, seed.Status)
	require.True(t, seed.Active(), "补齐中下游不生效")
	require.Len(t, seed.JobIDs, 1, "一批标的一个作业")

	views, err := b.SeedViews(ctx, "crypto", true)
	require.NoError(t, err)
	require.Len(t, views, 1)
	require.Equal(t, 2, views[0].TotalChunks, "5～6 分钟一块、20 分钟一块")
	require.Zero(t, views[0].DoneChunks)

	require.NoError(t, b.Tick(ctx, "crypto"))
	b.Wait()
	require.NoError(t, b.Tick(ctx, "crypto"))
	require.NoError(t, b.SeedTick(ctx, "crypto"))
	seed = seedOf(t, b)
	require.Equal(t, store.SeedDone, seed.Status)
	require.False(t, seed.Active())
	require.NotNil(t, seed.FinishedAt)
	require.Len(t, invoker.events, 2)
	require.Empty(t, rows.missing["BTC-USDT"], "缺口已补上")

	// 记录已经存在，再来一轮不会重复补齐。
	require.NoError(t, b.SeedTick(ctx, "crypto"))
	b.Wait()
	require.Len(t, invoker.events, 2)
	active, err := b.Store.HistorySeeds(ctx, "crypto", true)
	require.NoError(t, err)
	require.Empty(t, active)
}

// 最早生效点早于 24 小时的数据集是线上已有的，不补，也不会进入不生效状态。
func TestSeedIgnoresExistingDatasets(t *testing.T) {
	b, invoker, _, _ := seedFixture(t, 48*time.Hour, 5)
	ctx := context.Background()
	require.NoError(t, b.SeedTick(ctx, "crypto"))
	b.Wait()
	seeds, err := b.Store.HistorySeeds(ctx, "crypto", false)
	require.NoError(t, err)
	require.Empty(t, seeds)
	require.Empty(t, invoker.events)
}

// 保留期或行情源不允许补更早的历史（深度为 0）：直接记 done，不建作业。
func TestSeedWithZeroDepthIsDoneImmediately(t *testing.T) {
	b, _, _, _ := seedFixture(t, 2*time.Hour, 5)
	b.Seed.Depth = func(context.Context, string, string, string) (time.Duration, error) { return 0, nil }
	require.NoError(t, b.SeedTick(context.Background(), "crypto"))
	b.Wait()
	seed := seedOf(t, b)
	require.Equal(t, store.SeedDone, seed.Status)
	require.Empty(t, seed.JobIDs)
	require.NotEmpty(t, seed.Note)
}

// 范围内没有缺口：作业一个都不建，记 done 并说明。
func TestSeedWithoutGapsIsDone(t *testing.T) {
	b, invoker, _, _ := seedFixture(t, 2*time.Hour)
	require.NoError(t, b.SeedTick(context.Background(), "crypto"))
	b.Wait()
	seed := seedOf(t, b)
	require.Equal(t, store.SeedDone, seed.Status)
	require.Empty(t, seed.JobIDs)
	require.Empty(t, invoker.events)
}

// 失败分块自动重试一次，还失败就带说明记 done，不卡住放行。
func TestSeedRetriesFailedChunksOnceThenFinishes(t *testing.T) {
	b, invoker, _, _ := seedFixture(t, 2*time.Hour, 5)
	invoker.fail = true
	ctx := context.Background()
	require.NoError(t, b.SeedTick(ctx, "crypto"))
	b.Wait()

	require.NoError(t, b.Tick(ctx, "crypto"))
	b.Wait()
	require.NoError(t, b.Tick(ctx, "crypto"))
	require.NoError(t, b.SeedTick(ctx, "crypto"))
	seed := seedOf(t, b)
	require.Equal(t, store.SeedRunning, seed.Status, "失败分块重试期间仍在补齐")
	require.True(t, seed.Retried)

	require.NoError(t, b.Tick(ctx, "crypto"))
	b.Wait()
	require.NoError(t, b.Tick(ctx, "crypto"))
	require.NoError(t, b.SeedTick(ctx, "crypto"))
	seed = seedOf(t, b)
	require.Equal(t, store.SeedDone, seed.Status)
	require.Contains(t, seed.Note, "失败")
	views, err := b.SeedViews(ctx, "crypto", false)
	require.NoError(t, err)
	require.Equal(t, 1, views[0].FailedChunks)
}

// 手动放行：取消剩余分块、记 skipped，下游放行。
func TestSkipSeedsCancelsJobsAndReleases(t *testing.T) {
	b, invoker, _, _ := seedFixture(t, 2*time.Hour, 5)
	ctx := context.Background()
	require.NoError(t, b.SeedTick(ctx, "crypto"))
	b.Wait()
	seed := seedOf(t, b)
	require.True(t, seed.Active())

	skipped, err := b.SkipSeeds(ctx, "crypto", "")
	require.NoError(t, err)
	require.Equal(t, 1, skipped)
	seed = seedOf(t, b)
	require.Equal(t, store.SeedSkipped, seed.Status)
	require.False(t, seed.Active())

	require.NoError(t, b.Tick(ctx, "crypto"))
	b.Wait()
	require.Empty(t, invoker.events, "作业已取消，没有分块再被认领")
	status, err := b.Status(ctx, seed.JobIDs[0])
	require.NoError(t, err)
	require.Equal(t, store.BackfillCanceled, status.Job.Status)

	again, err := b.SkipSeeds(ctx, "crypto", "")
	require.NoError(t, err)
	require.Zero(t, again)
}

// 补齐分块单独分名额：补齐的名额用完时，补缺口的作业不受影响。
func TestSeedChunksUseTheirOwnSlots(t *testing.T) {
	b, invoker, rows, base := seedFixture(t, 2*time.Hour, 5, 12, 20)
	b.MaxInFlight = 5
	b.Seed.Slots = func(context.Context, string) int { return 1 }
	ctx := context.Background()
	require.NoError(t, b.SeedTick(ctx, "crypto"))
	b.Wait()

	// 同一空间再有一个补缺口的手动作业（ETH 生效点之后缺 2 根）。
	rows.mu.Lock()
	rows.missing["ETH-USDT"][base.Add(30*time.Minute).Unix()] = true
	rows.missing["ETH-USDT"][base.Add(40*time.Minute).Unix()] = true
	rows.mu.Unlock()
	_, chunks, err := b.Submit(ctx, JobRequest{SpaceID: "crypto", SubjectID: "ETH-USDT", From: base, To: base.Add(time.Hour)}, "manual")
	require.NoError(t, err)
	require.Equal(t, 2, chunks)

	b.Refill = false
	require.NoError(t, b.Tick(ctx, "crypto"))
	b.Wait()
	// 补缺口的 2 块一次认领；补齐的 3 块只认领 1 块。
	require.Len(t, invoker.events, 3)
}

// 日内分块跨休市区间：交易时段内的缺口连成一段，每段至多 MaxGapBars 根；不合并时按交易时段拆。
func TestCheckMergesGapsAcrossClosedSessions(t *testing.T) {
	b, _, rows, base := seedFixture(t, 2*time.Hour)
	// 每小时只有前 30 分钟是交易时间。
	b.Checker.Calendar = func(_, _ string, at time.Time) (bool, error) { return at.Minute() < 30, nil }
	start := base.Add(-3 * time.Hour).Truncate(time.Hour)
	rows.mu.Lock()
	for minute := 0; minute < 180; minute++ {
		rows.missing["BTC-USDT"][start.Add(time.Duration(minute)*time.Minute).Unix()] = true
	}
	rows.mu.Unlock()
	request := Request{SpaceID: "crypto", DatasetID: "ds", Subjects: []string{"BTC-USDT"}, From: start, To: start.Add(3 * time.Hour), BeforeFirstPlan: true}

	plain, err := b.Checker.Check(context.Background(), request)
	require.NoError(t, err)
	require.Len(t, plain.Gaps, 3, "不合并：每个交易时段一段")
	for _, gap := range plain.Gaps {
		require.Equal(t, 30, gap.Missing)
	}

	request.MergeSessions, request.MaxGapBars = true, 40
	merged, err := b.Checker.Check(context.Background(), request)
	require.NoError(t, err)
	require.Len(t, merged.Gaps, 3, "合并：90 根按每段 40 根拆成 40、40、10")
	require.Equal(t, []int{40, 40, 10}, []int{merged.Gaps[0].Missing, merged.Gaps[1].Missing, merged.Gaps[2].Missing})
	require.True(t, merged.Gaps[0].To.Sub(merged.Gaps[0].From) > 40*time.Minute, "第一段跨过了休市区间")

	// 分块的根数按日历数，不按墙上时间。
	chunk := store.BackfillChunk{Frequency: "1m", From: merged.Gaps[0].From, To: merged.Gaps[0].To}
	require.Equal(t, 40, b.chunkBars("crypto", chunk))
	require.Greater(t, barCount(mustFrequency(t, "1m"), chunk.From, chunk.To), 40)
}

// Subjects 只检查指定的标的。
func TestCheckRestrictsToSubjects(t *testing.T) {
	b, _, rows, base := seedFixture(t, 2*time.Hour)
	rows.mu.Lock()
	rows.missing["BTC-USDT"][base.Add(-10*time.Minute).Unix()] = true
	rows.missing["ETH-USDT"][base.Add(-10*time.Minute).Unix()] = true
	rows.mu.Unlock()
	request := Request{SpaceID: "crypto", DatasetID: "ds", From: base.Add(-30 * time.Minute), To: base, BeforeFirstPlan: true}
	all, err := b.Checker.Check(context.Background(), request)
	require.NoError(t, err)
	require.Len(t, all.Gaps, 2)
	request.Subjects = []string{"ETH-USDT"}
	one, err := b.Checker.Check(context.Background(), request)
	require.NoError(t, err)
	require.Len(t, one.Gaps, 1)
	require.Equal(t, "ETH-USDT", one.Gaps[0].SubjectID)
}

func TestSeedBatchSizeKeepsChecksBounded(t *testing.T) {
	// A 股 5m 约 1 万根：受 MaxCheckPoints 限制，一批约 300 个标的。
	require.Equal(t, MaxCheckPoints/9840, seedBatchSize(500, 9840, 1, 11))
	// crypto 1m 约 4.9 万根：一批约 61 个标的。
	require.Equal(t, MaxCheckPoints/48960, seedBatchSize(500, 48960, 1, 50))
	// 标的少时不超过 perJob。
	require.Equal(t, 10, seedBatchSize(10, 100, 1, 2))
	// 统一数据集两个 tag，检查的点数翻倍。
	require.Equal(t, MaxCheckPoints/(48960*2), seedBatchSize(500, 48960, 2, 50))
	require.GreaterOrEqual(t, seedBatchSize(500, MaxCheckPoints*2, 1, 5000), 1)
	// 不合并时每个交易时段一段缺口，缺口数限制批大小：A 股 205 个交易日 × 2 段 = 410 段，一批不超过 3750/410 个标的。
	require.Equal(t, MaxGaps*3/4/410, seedBatchSize(500, 9840, 1, 410))
}

// 一个地域的补采函数建不出来时换下一个地域，不让分块无限退回。
func TestSeedChunksFailOverToTheNextRegion(t *testing.T) {
	b, _, _, _ := seedFixture(t, 2*time.Hour, 5)
	provider := &regionFunctions{regions: []string{"ap-singapore", "ap-tokyo"}, broken: map[string]bool{"ap-singapore": true, "ap-tokyo": true}}
	b.Functions = func(string) FunctionProvider { return provider }
	b.Refill = false
	ctx := context.Background()
	require.NoError(t, b.SeedTick(ctx, "crypto"))
	b.Wait()

	require.NoError(t, b.Tick(ctx, "crypto"))
	b.Wait()
	require.Len(t, provider.regionsUsed, 2, "两个地域都试过")
	require.Equal(t, store.BackfillPending, onlyChunkStatus(t, b, seedOf(t, b).JobIDs[0]), "都建不出来：分块退回，不算失败")

	provider.mu.Lock()
	provider.broken["ap-singapore"] = false
	provider.mu.Unlock()
	require.NoError(t, b.Tick(ctx, "crypto"))
	b.Wait()
	require.NoError(t, b.Tick(ctx, "crypto"))
	require.Equal(t, store.BackfillSucceeded, onlyChunkStatus(t, b, seedOf(t, b).JobIDs[0]))
}

func onlyChunkStatus(t *testing.T, b *Backfiller, jobID string) string {
	t.Helper()
	status, err := b.Status(context.Background(), jobID)
	require.NoError(t, err)
	for name, count := range status.Counts {
		if count == 1 {
			return name
		}
	}
	t.Fatalf("作业 %s 没有恰好一个分块：%v", jobID, status.Counts)
	return ""
}

// 手动放行不会被拼作业的协程改回去，已建的作业也会取消。
func TestSkipDuringPreparationWins(t *testing.T) {
	b, invoker, _, _ := seedFixture(t, 2*time.Hour, 5)
	ctx := context.Background()
	now := time.Now().UTC()
	seed := store.HistorySeed{SpaceID: "crypto", DatasetID: "ds", Frequency: "1m", Status: store.SeedPreparing, TargetFrom: now.Add(-time.Hour), EffectiveFrom: now, StartedAt: now}
	created, err := b.Store.CreateHistorySeed(ctx, seed)
	require.NoError(t, err)
	require.True(t, created)

	skipped, err := b.SkipSeeds(ctx, "crypto", "")
	require.NoError(t, err)
	require.Equal(t, 1, skipped)

	// 拼作业的协程之后才来写：条件写不会生效。
	seed.Status, seed.JobIDs = store.SeedRunning, []string{"late-job"}
	updated, err := b.Store.UpdateHistorySeed(ctx, seed, store.SeedPreparing)
	require.NoError(t, err)
	require.False(t, updated)
	require.Equal(t, store.SeedSkipped, seedOf(t, b).Status)
	require.Empty(t, invoker.events)

	// 真正走一遍拼作业：记录已经是 skipped，拼出来的作业被取消。
	first, ok, err := b.firstSegment(ctx, "crypto", seed)
	require.NoError(t, err)
	require.True(t, ok)
	seed.JobIDs, seed.Status = nil, store.SeedPreparing
	seed.TargetFrom = first.fromTime().Add(-30 * time.Minute)
	seed.EffectiveFrom = first.fromTime()
	require.NoError(t, b.prepareSeed(ctx, "crypto", seed, first))
	jobs, err := b.Store.ActiveJobs(ctx, "crypto")
	require.NoError(t, err)
	require.Empty(t, jobs, "放行后拼出来的作业都已取消")
	require.Equal(t, store.SeedSkipped, seedOf(t, b).Status)
}

// 补齐的分块按地域轮流走补采函数；没有地域时走原来的执行端。
func TestSeedChunksRotateAcrossRegions(t *testing.T) {
	b, _, _, _ := seedFixture(t, 2*time.Hour, 5, 12, 20, 25)
	provider := &regionFunctions{regions: []string{"ap-singapore", "ap-tokyo"}}
	b.Functions = func(string) FunctionProvider { return provider }
	b.Refill = false
	ctx := context.Background()
	require.NoError(t, b.SeedTick(ctx, "crypto"))
	b.Wait()
	require.NoError(t, b.Tick(ctx, "crypto"))
	b.Wait()
	require.Len(t, provider.regionsUsed, 4)
	seen := map[string]int{}
	for _, region := range provider.regionsUsed {
		seen[region]++
	}
	require.Equal(t, map[string]int{"ap-singapore": 2, "ap-tokyo": 2}, seen, "两个地域各分到一半")
	require.Zero(t, provider.defaultCalls, "配了地域就不走默认地域")
}

type regionFunctions struct {
	mu           sync.Mutex
	broken       map[string]bool
	regions      []string
	regionsUsed  []string
	defaultCalls int
}

func (f *regionFunctions) BackfillRegions(string) []string { return f.regions }

func (f *regionFunctions) EnsureBackfillFunction(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.defaultCalls++
	return "moox-fetch-plan-backfill-crypto-spot-nanjing", nil
}

func (f *regionFunctions) EnsureBackfillFunctionIn(_ context.Context, _, region string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.regionsUsed = append(f.regionsUsed, region)
	if f.broken[region] {
		return "", errors.New("函数建不出来")
	}
	return "moox-fetch-plan-backfill-crypto-spot-" + region, nil
}

// 速率上限：令牌够就立刻放行，取消上下文时返回错误。
func TestWaitSeedRate(t *testing.T) {
	b := &Backfiller{Seed: SeedOptions{RowsPerSecond: 100000, LocalRowsPerSecond: 1}}
	require.NoError(t, b.waitSeedRate(context.Background(), false, 500))

	// 本地执行端上限 1 行/秒、突发 MaxChunkBars：第一块取走突发，第二块要等，上下文取消就返回。
	require.NoError(t, b.waitSeedRate(context.Background(), true, MaxChunkBars))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.Error(t, b.waitSeedRate(ctx, true, MaxChunkBars))

	// 没配上限不限速。
	require.NoError(t, (&Backfiller{}).waitSeedRate(context.Background(), true, MaxChunkBars))
}

// 补齐记录的增删改：重复创建不覆盖，Reset 一并清掉。
func TestHistorySeedRepository(t *testing.T) {
	b, _, _, _ := seedFixture(t, 2*time.Hour)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	seed := store.HistorySeed{SpaceID: "crypto", DatasetID: "d1", Frequency: "5m", Status: store.SeedRunning, TargetFrom: now.Add(-time.Hour), EffectiveFrom: now, JobIDs: []string{"j1"}, StartedAt: now}
	created, err := b.Store.CreateHistorySeed(ctx, seed)
	require.NoError(t, err)
	require.True(t, created)
	seed.Status = store.SeedDone
	created, err = b.Store.CreateHistorySeed(ctx, seed)
	require.NoError(t, err)
	require.False(t, created, "同一数据集同一频率已有记录，不覆盖")

	got, ok, err := b.Store.HistorySeedOf(ctx, "crypto", "d1", "5m")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, store.SeedRunning, got.Status)
	require.Equal(t, []string{"j1"}, got.JobIDs)

	got.Status, got.Retried = store.SeedDone, true
	updated, err := b.Store.UpdateHistorySeed(ctx, got)
	require.NoError(t, err)
	require.True(t, updated)
	got, _, _ = b.Store.HistorySeedOf(ctx, "crypto", "d1", "5m")
	require.Equal(t, store.SeedDone, got.Status)
	require.True(t, got.Retried)

	repo := b.Store.(*store.BackfillRepository)
	_, err = repo.Reset(ctx, "crypto")
	require.NoError(t, err)
	_, ok, err = b.Store.HistorySeedOf(ctx, "crypto", "d1", "5m")
	require.NoError(t, err)
	require.False(t, ok, "重新登记时补齐记录一并清掉")
}

func mustFrequency(t *testing.T, name string) marketdata.Frequency {
	t.Helper()
	frequency, err := marketdata.ParseFrequency(name)
	require.NoError(t, err)
	return frequency
}

func (s Segment) fromTime() time.Time { return time.Unix(s.From, 0).UTC() }
