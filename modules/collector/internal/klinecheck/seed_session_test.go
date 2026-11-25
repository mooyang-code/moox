package klinecheck

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

// aShareCalendar 是 A 股交易时段的 5m 日历：周一到周五北京时间 09:30～11:30、13:00～15:00（按 UTC 写死，与进程时区无关），一天 48 根。
func aShareCalendar(_, _ string, at time.Time) (bool, error) {
	at = at.UTC()
	if at.Weekday() == time.Saturday || at.Weekday() == time.Sunday {
		return false, nil
	}
	minute := at.Hour()*60 + at.Minute()
	return (minute >= 90 && minute < 210) || (minute >= 300 && minute < 420), nil
}

// staticTimeline 是固定的时间线行。
type staticTimeline []store.SeriesTimelineEntry

func (s staticTimeline) ListBySpace(context.Context, string) ([]store.SeriesTimelineEntry, error) {
	return s, nil
}

// sessionRows 是按日历给出行的 Storage 夹具：empty 里的标的一根都没有，其余标的交易时段的每一根都在。
// 计数按日历直接算，不逐根生成行（几百个标的 × 一万根）；读行只会读到根数对不上的标的，也就是 empty 里的。
type sessionRows struct {
	mu       sync.Mutex
	calendar func(string, string, time.Time) (bool, error)
	empty    map[string]bool
	err      error
	counts   int
}

func (s *sessionRows) CountTimeSeriesRows(_ context.Context, req *storagepb.CountTimeSeriesRowsReq, _ ...client.Option) (*storagepb.CountTimeSeriesRowsRsp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts++
	if s.err != nil {
		return nil, s.err
	}
	start, _ := time.Parse(time.RFC3339, req.GetTimeRange().GetStartTime())
	end, _ := time.Parse(time.RFC3339, req.GetTimeRange().GetEndTime())
	bars := 0
	for t := start; t.Before(end); t = t.Add(5 * time.Minute) {
		if trading, _ := s.calendar("", "5m", t); trading {
			bars++
		}
	}
	rsp := &storagepb.CountTimeSeriesRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}
	for _, selector := range req.GetSelectors() {
		count := uint32(bars)
		if s.empty[selector.GetSubjectId()] {
			count = 0
		}
		rsp.Counts = append(rsp.Counts, &storagepb.SubjectRowCount{SubjectId: selector.GetSubjectId(), Rows: count})
	}
	return rsp, nil
}

func (s *sessionRows) ReadTimeSeriesRows(_ context.Context, req *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	for _, selector := range req.GetSelectors() {
		if !s.empty[selector.GetSubjectId()] {
			panic("根数齐全的标的不该读行：" + selector.GetSubjectId())
		}
	}
	return &storagepb.ReadTimeSeriesRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, PageResult: &storagepb.PageResult{}}, nil
}

// aShareSeedFixture 是 A 股 5m 的历史补齐夹具：count 个标的，生效点 2026-11-25 06:50Z（北京时间 14:50），lateness 90 秒，
// 补齐深度 300 天，跨休市合并分块；empty 里的标的历史一根都没有，其余齐全。
func aShareSeedFixture(t *testing.T, count int, empty ...string) (*Backfiller, *sessionRows, time.Time) {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })

	effective := time.Date(2026, 11, 25, 6, 50, 0, 0, time.UTC)
	series := make([]seriesset.Series, 0, count)
	for i := 0; i < count; i++ {
		series = append(series, seriesset.Series{SubjectID: fmt.Sprintf("S%03d.XSHG", i), SeriesTag: "t"})
	}
	timeline := staticTimeline{{SpaceID: "stockcn", DatasetID: "ds", Frequency: "5m", EffectiveFrom: effective.Unix(), Series: series, LatenessS: 90}}
	rows := &sessionRows{calendar: aShareCalendar, empty: map[string]bool{}}
	for _, subject := range empty {
		rows.empty[subject] = true
	}
	now := effective.Add(5 * time.Minute)
	clock := func() time.Time { return now }
	checker := &Checker{Timeline: timeline, Rows: rows, Absences: db.Backfill(), Calendar: aShareCalendar, Now: clock}
	b := &Backfiller{
		Checker: checker, Store: db.Backfill(), Routes: fakeRoutes{}, BaseCtx: context.Background(), Now: clock,
		Functions: func(string) FunctionProvider { return fakeFunctions{} },
		Seed: SeedOptions{
			Enabled: true, MergeSessions: true,
			Depth: func(context.Context, string, string, string) (time.Duration, error) { return 300 * 24 * time.Hour, nil },
		},
	}
	return b, rows, effective
}

// A 股按交易时段（含午休）数根数：批大小与检查的预检同一套数法，按全天 24 小时数会超上限的批次也能过预检。
func TestSeedBatchesPassTheCheckLimitOnTradingSessions(t *testing.T) {
	b, _, effective := aShareSeedFixture(t, 400, "S000.XSHG", "S300.XSHG")
	ctx := context.Background()
	frequency := mustFrequency(t, "5m")
	from := alignUp(frequency, effective.Add(-300*24*time.Hour))
	bars, _, err := b.Checker.tradingBars("stockcn", frequency, "5m", from, effective)
	require.NoError(t, err)
	batch := seedBatchSize(500, bars, bars/MaxChunkBars+2)
	require.Less(t, batch, 400, "夹具要分两批")
	require.Greater(t, int64(barCount(frequency, from, effective))*int64(batch), int64(MaxCheckPoints), "按全天 24 小时数，一批会超过检查上限")
	require.LessOrEqual(t, int64(bars)*int64(batch), int64(MaxCheckPoints))

	require.NoError(t, b.SeedTick(ctx, "stockcn"))
	b.Wait()
	seed, ok, err := b.Store.HistorySeedOf(ctx, "stockcn", "ds", "5m")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, store.SeedRunning, seed.Status, seed.Note)
	require.Len(t, seed.JobIDs, 2, "两批各有一个缺历史的标的")
	for _, jobID := range seed.JobIDs {
		_, chunks, err := b.Store.GetJob(ctx, jobID)
		require.NoError(t, err)
		require.Len(t, chunks, (bars+MaxChunkBars-1)/MaxChunkBars, "整段缺口按每块至多 1000 根交易时段 K 线拆")
		require.Equal(t, effective, chunks[len(chunks)-1].To.UTC())
	}
}

// 预检总是拒绝（一个标的的根数就超过检查上限）：拼作业连续失败到次数后记 failed、放行下游，原因写进说明。
func TestSeedGivesUpAfterRepeatedPrepareFailures(t *testing.T) {
	b, invoker, _, _ := seedFixture(t, 2*time.Hour, 5)
	b.Seed.Depth = func(context.Context, string, string, string) (time.Duration, error) {
		return time.Duration(MaxCheckPoints+60) * time.Minute, nil
	}
	ctx := context.Background()
	for attempt := 1; attempt < seedMaxPrepareFailures; attempt++ {
		require.NoError(t, b.SeedTick(ctx, "crypto"))
		b.Wait()
		seed := seedOf(t, b)
		require.Equal(t, store.SeedPreparing, seed.Status, "第 %d 次失败后仍在补齐", attempt)
		require.Contains(t, seed.Note, fmt.Sprintf("连续第 %d 次", attempt))
	}
	require.NoError(t, b.SeedTick(ctx, "crypto"))
	b.Wait()
	seed := seedOf(t, b)
	require.Equal(t, store.SeedFailed, seed.Status)
	require.False(t, seed.Active(), "放弃后下游放行")
	require.NotNil(t, seed.FinishedAt)
	require.Contains(t, seed.Note, "above the limit")
	require.Empty(t, invoker.events)

	active, err := b.Store.HistorySeeds(ctx, "crypto", true)
	require.NoError(t, err)
	require.Empty(t, active, "ListHistorySeeds 只取补齐中的：已不在其中")
	views, err := b.SeedViews(ctx, "crypto", false)
	require.NoError(t, err)
	require.Len(t, views, 1)
	require.Equal(t, store.SeedFailed, views[0].Status)

	// 终态之后不再重拼。
	require.NoError(t, b.SeedTick(ctx, "crypto"))
	b.Wait()
	require.Equal(t, store.SeedFailed, seedOf(t, b).Status)
}

// 读 Storage 一直失败同样计入连续失败次数；中途成功一次就从零重新算。
func TestSeedPrepareFailuresResetAfterSuccess(t *testing.T) {
	b, rows, _ := aShareSeedFixture(t, 3, "S001.XSHG")
	rows.err = errors.New("storage unavailable")
	ctx := context.Background()
	for attempt := 1; attempt < seedMaxPrepareFailures; attempt++ {
		require.NoError(t, b.SeedTick(ctx, "stockcn"))
		b.Wait()
	}
	seed, _, err := b.Store.HistorySeedOf(ctx, "stockcn", "ds", "5m")
	require.NoError(t, err)
	require.Equal(t, store.SeedPreparing, seed.Status)
	require.Contains(t, seed.Note, "storage unavailable")

	rows.mu.Lock()
	rows.err = nil
	rows.mu.Unlock()
	require.NoError(t, b.SeedTick(ctx, "stockcn"))
	b.Wait()
	seed, _, err = b.Store.HistorySeedOf(ctx, "stockcn", "ds", "5m")
	require.NoError(t, err)
	require.Equal(t, store.SeedRunning, seed.Status)
	b.mu.Lock()
	defer b.mu.Unlock()
	require.Empty(t, b.seedFailures, "成功后清掉失败次数")
}
