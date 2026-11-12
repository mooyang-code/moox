package klinecheck

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

// hkt 解析香港时间。
func hkt(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", value, time.FixedZone("HKT", 8*3600))
	require.NoError(t, err)
	return parsed.UTC()
}

// closeRefetchFixture 建一个港股空间：5m 数据集从 2026-11-10 开盘起采集两只标的，时钟、日历、重取时刻都用真的市场登记。
// now 是可改的当前时间。
func closeRefetchFixture(t *testing.T) (*Backfiller, *store.Store, *time.Time) {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })

	options := marketfetch.PlanOptions{SpaceID: "stockhk", Markets: map[string][]string{"equity": {"ap-nanjing"}}, DefaultLatenessS: 90,
		Fetch: marketfetch.PlanFetch{Concurrency: 5, NotBeforeMS: 5000}}
	desired, err := marketfetch.BuildDesiredState(options, []marketfetch.TaskGroup{{
		Provider: "eastmoney", SourceID: "stockhk_http", MarketType: "equity", MarketID: "stockhk", InstrumentType: "equity", DatasetID: "ds_hk_5m", Frequency: "5m",
		Subjects: []string{"00005.XHKG", "00700.XHKG"},
	}}, nil)
	require.NoError(t, err)
	effective := hkt(t, "2026-11-10 09:30:00")
	content, _, err := marketfetch.BuildPlanContent(nil, nil, desired, func(string) (int64, error) { return effective.Unix(), nil })
	require.NoError(t, err)
	raw, err := json.Marshal(content)
	require.NoError(t, err)
	record, err := db.FetchPlans().Create(context.Background(), "stockhk", string(raw), "{}")
	require.NoError(t, err)
	require.NoError(t, db.FetchPlans().Update(context.Background(), record.ID, store.FetchPlanApplied, "{}", ""))

	now := new(time.Time)
	clock := func() time.Time { return *now }
	checker := &Checker{Plans: db.FetchPlans(), Rows: &fakeRows{}, Absences: db.Backfill(), Now: clock, Calendar: markets.TradingBar}
	backfiller := &Backfiller{
		Checker: checker, Store: db.Backfill(), Plans: db.FetchPlans(), BaseCtx: context.Background(), Now: clock,
		CloseRefetch: markets.CloseRefetchDay,
	}
	return backfiller, db, now
}

func refetchJobs(t *testing.T, db *store.Store) []store.BackfillJob {
	t.Helper()
	jobs, _, err := db.Backfill().ListJobs(context.Background(), "stockhk", store.BackfillJobFilter{}, 1, 50)
	require.NoError(t, err)
	return jobs
}

// 收市后 15 分钟那一轮提交一次整天的强制补采；之前的轮、同一轮里再调、之后的轮都不提交。
func TestRefetchAfterCloseSubmitsOneForcedJobForTheDay(t *testing.T) {
	backfiller, db, now := closeRefetchFixture(t)
	ctx := context.Background()

	for _, at := range []string{"2026-11-12 16:00:10", "2026-11-12 16:05:10", "2026-11-12 16:10:10", "2026-11-12 16:14:10"} {
		*now = hkt(t, at)
		require.NoError(t, backfiller.RefetchAfterClose(ctx, "stockhk"))
		require.Empty(t, refetchJobs(t, db), at)
	}

	*now = hkt(t, "2026-11-12 16:15:10")
	require.NoError(t, backfiller.RefetchAfterClose(ctx, "stockhk"))
	jobs := refetchJobs(t, db)
	require.Len(t, jobs, 1)
	job := jobs[0]
	require.Equal(t, store.BackfillKindForce, job.Kind)
	require.Equal(t, store.BackfillSourceManual, job.Source)
	require.Equal(t, "ds_hk_5m", job.DatasetID)
	require.Equal(t, "5m", job.Frequency)
	require.True(t, job.From.Equal(time.Date(2026, 11, 11, 16, 0, 0, 0, time.UTC)), "范围从香港时间当天零点起：%s", job.From)
	require.True(t, job.To.Equal(time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC)), "到次日零点止：%s", job.To)

	// 分块：每只标的上午、下午各一块，覆盖 09:30～12:00、13:00～16:00 的全部 66 根，含已经有数据的根。
	_, chunks, err := db.Backfill().GetJob(ctx, job.JobID)
	require.NoError(t, err)
	require.Len(t, chunks, 4)
	spans := map[string][]string{}
	for _, chunk := range chunks {
		require.Equal(t, "5m", chunk.Frequency)
		hk := time.FixedZone("HKT", 8*3600)
		spans[chunk.SubjectID] = append(spans[chunk.SubjectID], chunk.From.In(hk).Format("15:04")+"-"+chunk.To.In(hk).Format("15:04"))
	}
	require.Equal(t, map[string][]string{
		"00005.XHKG": {"09:30-12:00", "13:00-16:00"},
		"00700.XHKG": {"09:30-12:00", "13:00-16:00"},
	}, spans)

	// 同一轮里后面几次对账、以及之后的轮都不再提交。
	for _, at := range []string{"2026-11-12 16:16:10", "2026-11-12 16:19:10", "2026-11-12 16:20:10", "2026-11-12 23:00:10"} {
		*now = hkt(t, at)
		require.NoError(t, backfiller.RefetchAfterClose(ctx, "stockhk"))
		require.Len(t, refetchJobs(t, db), 1, at)
	}

	// 下一个交易日照常再提交一次。
	*now = hkt(t, "2026-11-13 16:15:10")
	require.NoError(t, backfiller.RefetchAfterClose(ctx, "stockhk"))
	require.Len(t, refetchJobs(t, db), 2)
}

// 周末不是交易日：16:15 那一轮不提交。
func TestRefetchAfterCloseSkipsNonTradingDays(t *testing.T) {
	backfiller, db, now := closeRefetchFixture(t)
	*now = hkt(t, "2026-11-14 16:15:10")
	require.NoError(t, backfiller.RefetchAfterClose(context.Background(), "stockhk"))
	require.Empty(t, refetchJobs(t, db))
}

// 半日市 12:00 收市：12:15 那一轮提交，只有上午的 30 根；16:15 不再提交。
func TestRefetchAfterCloseFollowsTheEarlyClose(t *testing.T) {
	backfiller, db, now := closeRefetchFixture(t)
	ctx := context.Background()
	*now = hkt(t, "2026-12-24 12:15:10")
	require.NoError(t, backfiller.RefetchAfterClose(ctx, "stockhk"))
	jobs := refetchJobs(t, db)
	require.Len(t, jobs, 1)
	require.True(t, jobs[0].From.Equal(hkt(t, "2026-12-24 00:00:00")))
	require.True(t, jobs[0].To.Equal(hkt(t, "2026-12-25 00:00:00")))
	_, chunks, err := db.Backfill().GetJob(ctx, jobs[0].JobID)
	require.NoError(t, err)
	require.Len(t, chunks, 2)
	for _, chunk := range chunks {
		require.True(t, chunk.From.Equal(hkt(t, "2026-12-24 09:30:00")))
		require.True(t, chunk.To.Equal(hkt(t, "2026-12-24 12:00:00")))
	}

	*now = hkt(t, "2026-12-24 16:15:10")
	require.NoError(t, backfiller.RefetchAfterClose(ctx, "stockhk"))
	require.Len(t, refetchJobs(t, db), 1)
}

// 没配延迟的市场（A 股、crypto 的 CloseRefetch 永远不命中）、没有接入 CloseRefetch 的装配都不提交；
// 周期不在数据源声明范围内的数据集不提交。
func TestRefetchAfterCloseIsOffByDefault(t *testing.T) {
	backfiller, db, now := closeRefetchFixture(t)
	ctx := context.Background()
	*now = hkt(t, "2026-11-12 16:15:10")

	asked := 0
	backfiller.CloseRefetch = func(spaceID string, after, until time.Time) (time.Time, time.Time, bool, error) {
		asked++
		require.True(t, until.Equal(hkt(t, "2026-11-12 16:15:00")))
		require.True(t, after.Equal(hkt(t, "2026-11-12 16:10:00")))
		// 用 A 股的登记回答：它没配 refetch_after_close_minutes。
		return markets.CloseRefetchDay("stockcn", after, until)
	}
	require.NoError(t, backfiller.RefetchAfterClose(ctx, "stockhk"))
	require.Equal(t, 1, asked)
	require.Empty(t, refetchJobs(t, db))

	backfiller.CloseRefetch = nil
	require.NoError(t, backfiller.RefetchAfterClose(ctx, "stockhk"))
	require.Empty(t, refetchJobs(t, db))

	backfiller.CloseRefetch = markets.CloseRefetchDay
	backfiller.RefetchFrequency = func(_, frequency string) bool { return frequency != "5m" }
	require.NoError(t, backfiller.RefetchAfterClose(ctx, "stockhk"))
	require.Empty(t, refetchJobs(t, db))
}
