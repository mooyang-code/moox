package localdata

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/memadmit"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

// stockBackend 把 fakeStorage 补成完整的 Storage 客户端（storageio.Backend），用真实时间保存 A 股 5m 行，
// 由 storageio.Axis 包装后交给数据层与管线，和引擎的装配一致。
type stockBackend struct {
	storageio.Backend
	*fakeStorage
}

func (b stockBackend) GetDataset(ctx context.Context, spaceID, datasetID string) (storageio.DatasetInfo, error) {
	info, err := b.fakeStorage.GetDataset(ctx, spaceID, datasetID)
	info.SpaceID, info.DatasetID, info.Freq = spaceID, datasetID, "5m"
	return info, err
}

func (b stockBackend) ListColumns(ctx context.Context, spaceID, datasetID string) ([]storageio.ColumnInfo, error) {
	return b.fakeStorage.ListColumns(ctx, spaceID, datasetID)
}

func (b stockBackend) DatasetColumns(ctx context.Context, spaceID, datasetID string) ([]string, error) {
	return b.fakeStorage.DatasetColumns(ctx, spaceID, datasetID)
}

func (b stockBackend) ReadWindow(ctx context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	return b.fakeStorage.ReadWindow(ctx, req)
}

func (b stockBackend) ReadDatasetChanges(ctx context.Context, spaceID, datasetID string, cursor *storageio.ChangeCursor, fromHead bool, limit int) (storageio.Changes, error) {
	return b.fakeStorage.ReadDatasetChanges(ctx, spaceID, datasetID, cursor, fromHead, limit)
}

func (b stockBackend) EarliestPeriod(ctx context.Context, spaceID, datasetID, freq string, subjects, columns []string) (time.Time, bool, error) {
	return b.fakeStorage.EarliestPeriod(ctx, spaceID, datasetID, freq, subjects, columns)
}

func (b stockBackend) WriteRows(ctx context.Context, spaceID, datasetID, commitID string, rows []storageio.ResultRow) error {
	return b.fakeStorage.WriteRows(ctx, spaceID, datasetID, commitID, rows)
}

func (b stockBackend) ReportComputed(ctx context.Context, marker storageio.PeriodMarker) error {
	return b.fakeStorage.ReportComputed(ctx, marker)
}

func (b stockBackend) ComputedExists(ctx context.Context, spaceID, datasetID, triggerEventID string, periodTime int64) (bool, error) {
	return b.fakeStorage.ComputedExists(ctx, spaceID, datasetID, triggerEventID, periodTime)
}

func cstTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02 15:04", value, time.FixedZone("CST", 8*3600))
	require.NoError(t, err)
	return parsed.UTC()
}

// A 股 5m 端到端：Storage 里是跨周末、跨午休的真实交易 K 线，同步进本地存储后在轴上连续，没有填充行；
// ma 窗口 10 的结果是最近 10 根交易 K 线的均值；写回 Storage 的 data_time 与周期完成标记的 period_time 是真实时间。
func TestStockCN5mAcrossBreaks(t *testing.T) {
	clock, err := periodclock.ForSpace("stockcn")
	require.NoError(t, err)
	axis, err := periodclock.AxisFor("stockcn", "5m")
	require.NoError(t, err)
	// 2026-10-09（周五）下午到 2026-10-12（周一）13:05 的 40 根交易 K 线：周五 14 根，周一上午 24 根，下午 2 根。
	bars, err := clock.Window(cstTime(t, "2026-10-12 13:05"), "5m", 40)
	require.NoError(t, err)
	require.Equal(t, cstTime(t, "2026-10-09 13:50"), bars[0])

	for _, c := range []struct {
		name, period, now string
		want              float64 // 最近 10 根 close（100 + 序号）的均值
	}{
		{"跨午休", "2026-10-12 13:05", "2026-10-12 13:10", 100 + 34.5},
		{"跨周末", "2026-10-12 09:45", "2026-10-12 09:50", 100 + 12.5},
	} {
		t.Run(c.name, func(t *testing.T) {
			store, err := seriesstore.Open(seriesstore.Options{Dir: t.TempDir(), BlockCacheBytes: 8 << 20, DisableSync: true})
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			raw := newFakeStorage()
			for i, at := range bars {
				raw.rows["600000.SH"] = append(raw.rows["600000.SH"], bar{at: at, close: 100 + float64(i), volume: 10})
			}
			storage := storageio.NewAxis(stockBackend{fakeStorage: raw})
			now := cstTime(t, c.now)
			data := New(Config{Store: store, Storage: storage, Admit: memadmit.New(0), Now: func() time.Time { return now }})
			runner := pipeline.NewRunner(storage, data, periodclock.Continuous{}, pipeline.Config{ComputeWorkers: 1})
			set := domain.EngineTask{
				Set: domain.FactorTask{TaskID: "stock5m", SpaceID: "stockcn", SourceDatasetID: "stock_bars", Freq: "5m",
					ResultDatasetID: "factor_stock", Status: domain.TaskStatusEnabled},
				Members: domain.AsMembers(
					domain.MustDefinition("ma", "ma", "output: ts_mean(close, 10)\n"),
					domain.MustDefinition("filled", "filled", "output: ts_sum(is_filled + 0, 10)\n"),
				),
			}
			subjects := []string{"600000.SH"}
			require.NoError(t, data.Prepare(context.Background(), set, subjects))

			// 本地存储在轴上连续：最近 10 根相邻相差一个周期，没有填充行，换回真实时间正好是最近 10 根交易 K 线。
			period, err := axis.ToAxis(cstTime(t, c.period))
			require.NoError(t, err)
			src := seriesstore.Source{SpaceID: "stockcn", DatasetID: "stock_bars", Freq: "5m"}
			local, err := store.ReadRange(context.Background(), src, subjects, period.Add(-9*5*time.Minute), period.Add(5*time.Minute))
			require.NoError(t, err)
			cols := local["600000.SH"]
			require.Equal(t, 10, cols.Len())
			want, err := clock.Window(cstTime(t, c.period), "5m", 10)
			require.NoError(t, err)
			for i := range cols.Times {
				require.False(t, cols.Filled[i], "窗口内没有填充行")
				back, err := axis.FromAxis(time.Unix(cols.Times[i], 0))
				require.NoError(t, err)
				require.Equal(t, want[i], back)
			}

			plan, err := pipeline.BuildLivePlan(periodclock.Continuous{}, pipeline.LiveInput{
				Set: set.Set, Members: set.Members, PeriodTime: period, Universe: subjects, CarryColumns: []string{"close", "volume"}, TriggerEventID: "evt",
			})
			require.NoError(t, err)
			outcome, err := runner.Run(context.Background(), plan)
			require.NoError(t, err)
			data.Drain()
			require.Equal(t, "complete", outcome.Status)

			raw.mu.Lock()
			defer raw.mu.Unlock()
			require.Len(t, raw.results, 1)
			row, ok := raw.results["600000.SH"+cstTime(t, c.period).Format(time.RFC3339)]
			require.True(t, ok, "写回 Storage 的 data_time 是真实时间")
			require.InDelta(t, c.want, row.Fields["ma"], 1e-9)
			require.InDelta(t, 0, row.Fields["filled"], 1e-9)
			require.Len(t, raw.markers, 1)
			require.Equal(t, cstTime(t, c.period).Unix(), raw.markers[0].PeriodTime, "周期完成标记的 period_time 是真实时间")
		})
	}
}

// 新建的 A 股数据集：本地只有开盘第一根，也就是 Storage 的首根。N 不超过 7200h 保留期内的交易根数（约 205 个交易日 × 48）时
// 按新上市豁免通过覆盖检查；N=5 万远超保留期容量，首根可能是被截断的，仍然列为不足。
func TestStockCNCoverageExemptsNewDatasetWithinRetention(t *testing.T) {
	for _, c := range []struct {
		lookback int
		short    bool
	}{{5000, false}, {50000, true}} {
		store, err := seriesstore.Open(seriesstore.Options{Dir: t.TempDir(), BlockCacheBytes: 8 << 20, DisableSync: true})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		raw := newFakeStorage()
		raw.retention = "7200h"
		raw.rows["600519.XSHG"] = []bar{{at: cstTime(t, "2026-11-05 09:30"), close: 1465, volume: 10}}
		storage := storageio.NewAxis(stockBackend{fakeStorage: raw})
		now := cstTime(t, "2026-11-05 09:36")
		lookback := c.lookback
		data := New(Config{Store: store, Storage: storage, Admit: memadmit.New(0), Now: func() time.Time { return now },
			Lookback: func(seriesstore.Source) int { return lookback }})
		set := domain.EngineTask{
			Set: domain.FactorTask{TaskID: "cn5m", SpaceID: "stockcn", SourceDatasetID: "stock_bars", Freq: "5m",
				ResultDatasetID: "factor_stock", Status: domain.TaskStatusEnabled},
			Members: domain.AsMembers(domain.MustDefinition("ma", "ma", "output: ts_mean(close, 10)\n")),
		}
		require.NoError(t, data.Prepare(context.Background(), set, []string{"600519.XSHG"}))
		short, err := data.Coverage(context.Background(), seriesstore.Source{SpaceID: "stockcn", DatasetID: "stock_bars", Freq: "5m"})
		require.NoError(t, err)
		if c.short {
			require.Len(t, short, 1, "N=%d", c.lookback)
			require.Equal(t, 1, short[0].Have)
		} else {
			require.Empty(t, short, "N=%d", c.lookback)
		}
	}
}
