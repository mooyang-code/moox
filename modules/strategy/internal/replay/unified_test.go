package replay

import (
	"context"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

const (
	uniSpotTag = "venue:binance|market:spot|source:spot_http"
	uniSwapTag = "venue:binance|market:swap|source:swap_http"
)

// unifiedClient 把 dualClient 的现货、永续两份数据放进统一数据集的一个 View：同一个 View 里同一个标的有两条序列（series_tag 不同），
// 其余元数据沿用现货的。读行时带不带 series_tag 的 selector 都按 Storage 的口径处理。
type unifiedClient struct {
	*dualClient
}

const (
	uniView    = "view_factor_u"
	uniDataset = "ds_u"
	uniSource  = "src_u"
)

func (c *unifiedClient) GetView(ctx context.Context, spaceID, viewID string) (input.ViewInfo, error) {
	view, err := c.spot.GetView(ctx, spaceID, viewID)
	view.ViewID, view.DatasetID = uniView, uniDataset
	return view, err
}

func (c *unifiedClient) GetDataset(ctx context.Context, spaceID, datasetID string) (input.DatasetInfo, error) {
	dataset, err := c.spot.GetDataset(ctx, spaceID, datasetID)
	switch datasetID {
	case uniDataset:
		dataset.Attributes = map[string]string{"source_dataset_id": uniSource}
	case uniSource:
		dataset.Attributes = map[string]string{"market_type": "multi"}
	}
	return dataset, err
}

func (c *unifiedClient) ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]input.Subject, error) {
	return c.spot.ListDatasetSubjects(ctx, spaceID, datasetID)
}

func (c *unifiedClient) QueryRows(ctx context.Context, spaceID string, query input.Query) ([]input.Row, uint64, error) {
	spotRows, revision, err := c.spot.QueryRows(ctx, spaceID, query)
	if err != nil {
		return nil, 0, err
	}
	swapRows, _, err := c.swap.QueryRows(ctx, spaceID, query)
	if err != nil {
		return nil, 0, err
	}
	var rows []input.Row
	for _, row := range spotRows {
		if row.SeriesTag == "venue:binance" {
			row.SeriesTag = uniSpotTag
		}
		rows = append(rows, row)
	}
	for _, row := range swapRows {
		if row.SeriesTag == "venue:binance" {
			row.SeriesTag = uniSwapTag
		}
		rows = append(rows, row)
	}
	return rows, revision, nil
}

var uniTask = store.ReplayTask{FactorTaskID: "ftask_u", ViewID: uniView, ViewGeneration: "idx"}

// 同样的行，方式 A（现货、永续两个任务）与方式 B（统一数据集上的一个任务）的研究回放逐期一致：目标、持仓、权益都相同。
func TestReplayOnUnifiedDatasetMatchesTwoTasks(t *testing.T) {
	for name, dslYaml := range map[string]string{"混合对冲": hedgeDSL, "年龄按市场判断": agedPreferSwapDSL, "纯永续": swapOnlyDSL} {
		t.Run(name, func(t *testing.T) {
			run := func(unified bool) []store.ReplayBar {
				repo := openStore(t)
				dual := newDualClient()
				dual.swap.missing = map[string]map[int]bool{"B": {0: true, 1: true, 2: true, 3: true, 4: true, 5: true, 6: true, 7: true}}
				var client input.Client = dual
				tasks := map[string]store.ReplayTask{"spot": spotTask, "swap": swapTask}
				if unified {
					client, tasks = &unifiedClient{dual}, map[string]store.ReplayTask{"multi": uniTask}
				} else if strings.Contains(dslYaml, "market: {select: swap}") {
					tasks = map[string]store.ReplayTask{"swap": swapTask}
				}
				job := startMarketReplay(t, repo, client, "p1", dslYaml, 14, tasks, 10, 20)
				(&Runner{Store: repo, Client: client, ChunkBars: 5, InitialEquity: 100}).Execute(context.Background(), job)
				done, err := repo.GetReplay(context.Background(), "p1")
				if err != nil || done.Status != store.ReplayDone {
					t.Fatalf("回放应完成（unified=%v）：%+v err=%v", unified, done, err)
				}
				return barsOf(t, repo, "p1")
			}
			a, b := run(false), run(true)
			if len(a) != len(b) || len(a) == 0 {
				t.Fatalf("期数不一致：%d / %d", len(a), len(b))
			}
			for i := range a {
				if a[i].Status != b[i].Status || string(a[i].TargetsJSON) != string(b[i].TargetsJSON) || string(a[i].PositionsJSON) != string(b[i].PositionsJSON) || a[i].Equity != b[i].Equity || a[i].Fee != b[i].Fee {
					t.Fatalf("第 %d 期不一致：\n A：%s %s %v\n B：%s %s %v", i, a[i].Status, a[i].TargetsJSON, a[i].Equity, b[i].Status, b[i].TargetsJSON, b[i].Equity)
				}
			}
		})
	}
}

// 统一数据集的回放里，同一个市场出现两个序列才是歧义：整期跳过；现货、永续两条序列并存不是歧义。
func TestReplayOnUnifiedDatasetSkipsSameMarketAmbiguity(t *testing.T) {
	repo := openStore(t)
	dual := newDualClient()
	dual.spot.ambiguous = map[int]bool{4: true} // 现货上的 A 在第 4 小时有两个序列
	client := &unifiedClient{dual}
	job := startMarketReplay(t, repo, client, "p1", hedgeDSL, 6, map[string]store.ReplayTask{"multi": uniTask}, 0, 0)
	(&Runner{Store: repo, Client: client, ChunkBars: 5, InitialEquity: 100}).Execute(context.Background(), job)
	if done, _ := repo.GetReplay(context.Background(), "p1"); done.Status != store.ReplayDone {
		t.Fatalf("回放应完成：%+v", done)
	}
	for _, bar := range barsOf(t, repo, "p1") {
		ambiguous := bar.SkipReason == input.SkipAmbiguousSeries
		if (hourOf(bar) == 4) != ambiguous {
			t.Fatalf("只有第 4 小时（现货上的 A 有两个序列）应因歧义跳过：小时 %d 状态 %s 原因 %q", hourOf(bar), bar.Status, bar.SkipReason)
		}
	}
}
