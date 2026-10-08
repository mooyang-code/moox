package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
)

var origin = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

// fakeClient 生成 1h 现货 View：A、B、C 三个标的，close 按标的与小时确定。
type fakeClient struct {
	marketType  string
	indexedFrom time.Time
	missing     map[string]map[int]bool // 标的 → 小时 → 无行
	queries     int
	onQuery     func(query input.Query)
}

func price(id string, hour int) float64 {
	switch id {
	case "A":
		return 100 + float64(hour)
	case "B":
		return 50 + float64(hour%5)*2
	default:
		return 10 + float64(hour%3)
	}
}

func (f *fakeClient) GetView(context.Context, string, string) (input.ViewInfo, error) {
	return input.ViewInfo{ViewID: "view_a", DatasetID: "ds", Frequency: "1h", Status: "active", ActiveIndexID: "idx", IndexedFrom: f.indexedFrom, Columns: []input.ViewColumn{{Name: "close", Attributes: map[string]string{}}, {Name: "mom", Attributes: map[string]string{"origin_factor_id": "mom", "factor_output": "mom"}}}}, nil
}

func (f *fakeClient) GetDataset(_ context.Context, _, datasetID string) (input.DatasetInfo, error) {
	return input.DatasetInfo{DatasetID: datasetID, Status: "active", Frequency: "1h", Retention: "forever", Attributes: map[string]string{}, SubjectTags: []string{"binance_" + f.marketType}}, nil
}

func (f *fakeClient) GetTag(_ context.Context, _, tagID string) (input.TagInfo, error) {
	return input.TagInfo{TagID: tagID, MarketType: strings.TrimPrefix(tagID, "binance_")}, nil
}

func (f *fakeClient) ListDatasetSubjects(context.Context, string, string) ([]input.Subject, error) {
	return []input.Subject{{SubjectID: "A", InstrumentID: "A", Active: true}, {SubjectID: "B", InstrumentID: "B", Active: true}, {SubjectID: "C", InstrumentID: "C", Active: false}}, nil
}

func (f *fakeClient) ListTagMembers(context.Context, string, string) ([]string, error) {
	return nil, nil
}

func (f *fakeClient) QueryRows(_ context.Context, _ string, query input.Query) ([]input.Row, uint64, error) {
	f.queries++
	if f.onQuery != nil {
		f.onQuery(query)
	}
	rows := make([]input.Row, 0)
	for at := query.Start.Truncate(time.Hour); at.Before(query.End); at = at.Add(time.Hour) {
		if at.Before(query.Start) {
			continue
		}
		hour := int(at.Sub(origin) / time.Hour)
		for _, subject := range query.Subjects {
			if f.missing[subject.SubjectID][hour] || hour < 0 {
				continue
			}
			value := price(subject.SubjectID, hour)
			rows = append(rows, input.Row{SubjectID: subject.SubjectID, DataTime: at, Values: map[string]float64{"close": value, "mom": value - price(subject.SubjectID, hour-1)}})
		}
	}
	return rows, 1, nil
}

func (f *fakeClient) GetFactor(context.Context, string) (input.FactorInfo, error) {
	return input.FactorInfo{FactorID: "mom", DefinitionHash: "sha256:mom", Outputs: []string{"mom"}}, nil
}

const replayDSL = `name: replay_demo
rules:
  - id: r
    type: rank
    score: "mom"
    select: {top: 1}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

const previousDSL = `name: replay_prev
rules:
  - id: r
    type: rank
    score: "close - bars[-1].close"
    select: {top: 1}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

func openStore(t *testing.T) *store.Store {
	t.Helper()
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	return repo
}

func startReplay(t *testing.T, repo *store.Store, id, dslYaml string, hours int) store.Replay {
	t.Helper()
	job := store.Replay{ReplayID: id, DSLYaml: dslYaml, SpaceID: "crypto", ViewID: "view_a", StartTime: origin.Add(2 * time.Hour), EndTime: origin.Add(time.Duration(2+hours) * time.Hour), FeeBps: 10, CreatedAt: origin}
	if err := repo.CreateReplay(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	claimed, found, err := repo.ClaimNextReplay(context.Background(), origin)
	if err != nil || !found || claimed.ReplayID != id {
		t.Fatalf("认领回放失败：%+v found=%v err=%v", claimed, found, err)
	}
	return claimed
}

func barsOf(t *testing.T, repo *store.Store, id string) []store.ReplayBar {
	t.Helper()
	bars, _, err := repo.ListReplayBars(context.Background(), id, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return bars
}

func TestReplayRunsEndToEnd(t *testing.T) {
	repo := openStore(t)
	client := &fakeClient{marketType: "spot"}
	runner := &Runner{Store: repo, Client: client, ChunkBars: 4, InitialEquity: 100}
	job := startReplay(t, repo, "p1", replayDSL, 10)
	runner.Execute(context.Background(), job)
	done, err := repo.GetReplay(context.Background(), "p1")
	if err != nil || done.Status != store.ReplayDone || done.ProgressTime == nil {
		t.Fatalf("回放应完成：%+v err=%v", done, err)
	}
	var metrics Metrics
	if err := json.Unmarshal(done.MetricsJSON, &metrics); err != nil {
		t.Fatal(err)
	}
	if metrics.Bars != 10 || metrics.OKBars != 10 || metrics.InitialEquity != 100 || metrics.Factors["mom"] != "sha256:mom" || len(metrics.Limitations) < 4 || metrics.TotalFee <= 0 {
		t.Fatalf("绩效不符：%+v", metrics)
	}
	bars := barsOf(t, repo, "p1")
	if len(bars) != 10 || !bars[0].BarEndTime.Equal(origin.Add(3*time.Hour)) {
		t.Fatalf("回放周期不符：%d 个，首个 %v", len(bars), bars[0].BarEndTime)
	}
	// A 的动量恒为 +1，B 在 0、2、2、2、-8 之间循环：两者交替领先。
	var targets []map[string]string
	for _, bar := range bars {
		var items []map[string]string
		if err := json.Unmarshal(bar.TargetsJSON, &items); err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 {
			t.Fatalf("每期应只选一个标的：%s", bar.TargetsJSON)
		}
		targets = append(targets, items[0])
		if bar.Equity <= 0 {
			t.Fatalf("权益应为正：%+v", bar)
		}
	}
	if !strings.Contains(string(bars[0].PositionsJSON), "cash") {
		t.Fatalf("持仓账本应包含现金：%s", bars[0].PositionsJSON)
	}
	near(t, "最终权益与绩效一致", metrics.FinalEquity, bars[len(bars)-1].Equity)
}

// S24：回放进行中进程退出：任务保持 running，重启后变为 failed(interrupted)，已写入的 bars 保留，可重新发起。
func TestS24InterruptedReplayKeepsBars(t *testing.T) {
	repo := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	client := &fakeClient{marketType: "spot"}
	calls := 0
	client.onQuery = func(input.Query) {
		calls++
		if calls == 2 {
			cancel()
		}
	}
	runner := &Runner{Store: repo, Client: client, ChunkBars: 3}
	job := startReplay(t, repo, "p1", replayDSL, 9)
	runner.Execute(ctx, job)
	running, _ := repo.GetReplay(context.Background(), "p1")
	if running.Status != store.ReplayRunning {
		t.Fatalf("进程退出时任务应保持 running：%+v", running)
	}
	written := len(barsOf(t, repo, "p1"))
	if written == 0 || written >= 9 {
		t.Fatalf("应只写入部分周期：%d", written)
	}
	if affected, err := repo.MarkRunningReplaysInterrupted(context.Background(), origin); err != nil || affected != 1 {
		t.Fatalf("重启应标记中断：affected=%d err=%v", affected, err)
	}
	interrupted, _ := repo.GetReplay(context.Background(), "p1")
	if interrupted.Status != store.ReplayFailed || interrupted.Error != "interrupted" || len(barsOf(t, repo, "p1")) != written {
		t.Fatalf("中断后的状态或 bars 不符：%+v", interrupted)
	}
	again := startReplay(t, repo, "p2", replayDSL, 9)
	(&Runner{Store: repo, Client: &fakeClient{marketType: "spot"}, ChunkBars: 3}).Execute(context.Background(), again)
	if done, _ := repo.GetReplay(context.Background(), "p2"); done.Status != store.ReplayDone {
		t.Fatalf("可以重新发起：%+v", done)
	}
}

func TestReplayCancellationKeepsWrittenBars(t *testing.T) {
	repo := openStore(t)
	client := &fakeClient{marketType: "spot"}
	calls := 0
	client.onQuery = func(input.Query) {
		calls++
		if calls == 2 {
			if err := repo.CancelReplay(context.Background(), "p1", origin); err != nil {
				t.Fatal(err)
			}
		}
	}
	job := startReplay(t, repo, "p1", replayDSL, 9)
	(&Runner{Store: repo, Client: client, ChunkBars: 3}).Execute(context.Background(), job)
	cancelled, _ := repo.GetReplay(context.Background(), "p1")
	if cancelled.Status != store.ReplayCancelled {
		t.Fatalf("取消后状态应为 cancelled：%+v", cancelled)
	}
	if written := len(barsOf(t, repo, "p1")); written != 3 {
		t.Fatalf("已写入的 bars 应保留：%d", written)
	}
}

// 分段边界处 bars[-1] 正确：不同的 chunk_bars 产生逐期相同的结果。
func TestReplayChunkBoundaryPreviousBar(t *testing.T) {
	results := make([][]store.ReplayBar, 0, 2)
	for i, chunk := range []int{2, 100} {
		repo := openStore(t)
		job := startReplay(t, repo, fmt.Sprintf("p%d", i), previousDSL, 8)
		(&Runner{Store: repo, Client: &fakeClient{marketType: "spot"}, ChunkBars: chunk}).Execute(context.Background(), job)
		if done, _ := repo.GetReplay(context.Background(), job.ReplayID); done.Status != store.ReplayDone {
			t.Fatalf("回放应完成：%+v", done)
		}
		results = append(results, barsOf(t, repo, job.ReplayID))
	}
	for i := range results[0] {
		a, b := results[0][i], results[1][i]
		if a.Status != "ok" || string(a.TargetsJSON) != string(b.TargetsJSON) || a.Equity != b.Equity {
			t.Fatalf("第 %d 期在不同分段下结果不同：%s / %s", i, a.TargetsJSON, b.TargetsJSON)
		}
	}
}

// 清算与缺价只依赖截至当期的数据：改变结束时间不改变前面各期的结果。
func TestReplayPrefixIsIndependentOfEndTime(t *testing.T) {
	missing := map[string]map[int]bool{"A": {5: true, 6: true, 7: true, 8: true}}
	var short, long []store.ReplayBar
	for i, hours := range []int{6, 10} {
		repo := openStore(t)
		job := startReplay(t, repo, fmt.Sprintf("p%d", i), replayDSL, hours)
		(&Runner{Store: repo, Client: &fakeClient{marketType: "spot", missing: missing}, ChunkBars: 3, MissingPriceLiquidateBars: 2}).Execute(context.Background(), job)
		bars := barsOf(t, repo, job.ReplayID)
		if i == 0 {
			short = bars
		} else {
			long = bars
		}
	}
	for i := range short {
		if string(short[i].PositionsJSON) != string(long[i].PositionsJSON) || short[i].Equity != long[i].Equity {
			t.Fatalf("第 %d 期结果随结束时间变化：%s / %s", i, short[i].PositionsJSON, long[i].PositionsJSON)
		}
	}
}

func TestReplayRejectsSwapAndEarlyStart(t *testing.T) {
	repo := openStore(t)
	job := startReplay(t, repo, "p1", replayDSL, 4)
	(&Runner{Store: repo, Client: &fakeClient{marketType: "swap"}}).Execute(context.Background(), job)
	failed, _ := repo.GetReplay(context.Background(), "p1")
	if failed.Status != store.ReplayFailed || !strings.Contains(failed.Error, "只支持现货") {
		t.Fatalf("合约 View 的回放应失败：%+v", failed)
	}
	early := startReplay(t, repo, "p2", previousDSL, 4)
	(&Runner{Store: repo, Client: &fakeClient{marketType: "spot", indexedFrom: origin.Add(2 * time.Hour)}}).Execute(context.Background(), early)
	rejected, _ := repo.GetReplay(context.Background(), "p2")
	if rejected.Status != store.ReplayFailed || !strings.Contains(rejected.Error, "可用起点为 2026-09-01T03:00:00Z") {
		t.Fatalf("起点早于保留起点应给出可用起点：%+v", rejected)
	}
}

func TestRunnerDrainsQueueAndWakes(t *testing.T) {
	repo := openStore(t)
	for _, id := range []string{"p1", "p2"} {
		if err := repo.CreateReplay(context.Background(), store.Replay{ReplayID: id, DSLYaml: replayDSL, SpaceID: "crypto", ViewID: "view_a", StartTime: origin.Add(2 * time.Hour), EndTime: origin.Add(5 * time.Hour), CreatedAt: origin}); err != nil {
			t.Fatal(err)
		}
	}
	runner := &Runner{Store: repo, Client: &fakeClient{marketType: "spot"}}
	runner.Wake()
	runner.Wake()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go runner.Run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		first, _ := repo.GetReplay(context.Background(), "p1")
		second, _ := repo.GetReplay(context.Background(), "p2")
		if first.Status == store.ReplayDone && second.Status == store.ReplayDone {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("执行器应依次完成排队的回放")
}
