package replay

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// hkClient 生成港股 5m 的 View：行只落在港股交易时段的 bar 上（午休、收市后、休市日没有行，半日市只有上午），
// 收盘价按 bar 的交易序号确定。其余沿用 fakeClient。
type hkClient struct {
	*fakeClient
	from, to time.Time
}

func (c *hkClient) GetView(ctx context.Context, spaceID, viewID string) (input.ViewInfo, error) {
	view, err := c.fakeClient.GetView(ctx, spaceID, viewID)
	view.Frequency = "5m"
	return view, err
}

func (c *hkClient) ViewGeneration(ctx context.Context, spaceID, viewID string) (string, error) {
	view, err := c.GetView(ctx, spaceID, viewID)
	return view.Generation, err
}

func (c *hkClient) GetDataset(_ context.Context, _, datasetID string) (input.DatasetInfo, error) {
	return input.DatasetInfo{DatasetID: datasetID, Status: "active", Frequency: "5m", Retention: "forever", Alignment: "session", Attributes: map[string]string{"calendar": "hk_stock", "market_type": "equity"}}, nil
}

func (c *hkClient) ViewCoverage(context.Context, string, input.ViewInfo, bool) (input.Coverage, error) {
	return input.Coverage{IndexedFrom: c.from, IndexedTo: c.to}, nil
}

func (c *hkClient) QueryRows(_ context.Context, _ string, query input.Query) ([]input.Row, uint64, error) {
	c.queries++
	bars, err := input.ReplayBars("hk_stock", "5m", query.Start, query.End, 100000)
	if err != nil {
		return nil, 0, err
	}
	rows := make([]input.Row, 0, len(bars)*len(query.Subjects))
	for _, bar := range bars {
		if bar.StorageStart.Before(c.from) || bar.StorageStart.After(c.to) {
			continue
		}
		for _, subject := range query.Subjects {
			value := price(subject.SubjectID, int(bar.BarIndex%1000))
			rows = append(rows, input.Row{SubjectID: subject.SubjectID, DataTime: bar.StorageStart, SeriesTag: "default", Values: map[string]float64{"close": value, "mom": value - price(subject.SubjectID, int(bar.BarIndex%1000)-1)}})
		}
	}
	return rows, 1, nil
}

// 回放能在港股 5m 数据上跑完：区间 2026-12-23（完整交易日 66 根）到 12-24（半日市 30 根），跨午休、隔夜与半日市收市，
// 共 96 根，一根不缺；年化按股票市场的每日根数折算。
func TestReplayRunsOnHKIntradayData(t *testing.T) {
	hk, err := time.LoadLocation("Asia/Hong_Kong")
	if err != nil {
		t.Fatal(err)
	}
	repo := openStore(t)
	start := time.Date(2026, 12, 23, 0, 0, 0, 0, hk)
	end := time.Date(2026, 12, 25, 0, 0, 0, 0, hk)
	job := store.Replay{
		ReplayID: "p1", DSLYaml: replayDSL, DSLHash: dsl.Hash([]byte(replayDSL)), Calendar: "hk_stock", SpaceID: "stockhk",
		Tasks:     map[string]store.ReplayTask{"equity": {FactorTaskID: "ftask_hk_5m", ViewID: "view_factor_a", ViewGeneration: "idx"}},
		StartTime: start, EndTime: end, Factors: pinnedFactors(t, replayDSL), CreatedAt: origin,
	}
	if err := repo.CreateReplay(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	claimed, found, err := repo.ClaimNextReplay(context.Background(), origin)
	if err != nil || !found {
		t.Fatalf("认领回放失败：found=%v err=%v", found, err)
	}
	client := &hkClient{fakeClient: &fakeClient{marketType: "equity"}, from: time.Date(2026, 12, 1, 9, 30, 0, 0, hk), to: time.Date(2026, 12, 30, 10, 0, 0, 0, hk)}
	(&Runner{Store: repo, Client: client, ChunkBars: 40, Now: func() time.Time { return time.Date(2027, 1, 4, 10, 0, 0, 0, hk) }}).Execute(context.Background(), claimed)
	done, err := repo.GetReplay(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != store.ReplayDone {
		t.Fatalf("港股回放应跑完：status=%s error=%s", done.Status, done.Error)
	}
	metrics := metricsOf(t, done)
	if metrics.Bars != 66+30 || metrics.OKBars != 66+30 {
		t.Fatalf("12-23 与 12-24（半日市）共 96 根、每根都求值成功：%+v", metrics)
	}
	if got := periodsPerYear("hk_stock", "5m", 5*time.Minute); got != float64(input.StockTradingDaysPerYear*66) {
		t.Fatalf("港股 5m 的年化根数按每日 66 根折算：%v", got)
	}
}
