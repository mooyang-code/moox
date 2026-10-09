package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// submitReplay 按 StartReplay 的做法登记并认领一个回放：记录 DSL 哈希与提交时校验区间所用的活动索引。
func submitReplay(t *testing.T, repo *store.Store, dslYaml, indexID string, start time.Time, hours int) store.Replay {
	t.Helper()
	job := store.Replay{ReplayID: "p1", DSLYaml: dslYaml, DSLHash: dsl.Hash([]byte(dslYaml)), ViewGeneration: indexID, SpaceID: "crypto", ViewID: "view_a", StartTime: start, EndTime: start.Add(time.Duration(hours) * time.Hour), Factors: pinnedFactors(t, dslYaml), CreatedAt: origin}
	if err := repo.CreateReplay(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	claimed, found, err := repo.ClaimNextReplay(context.Background(), origin)
	if err != nil || !found {
		t.Fatalf("认领回放失败：found=%v err=%v", found, err)
	}
	return claimed
}

// 从覆盖边缘起步的回放在排队期间 View 正常滚动了几根：活动索引没变时行不会被删除，沿用提交时校验过的区间，
// 不再读取覆盖统计，也不因覆盖起点前移而失败。
func TestReplayKeepsSubmittedWindowWhileIndexUnchanged(t *testing.T) {
	repo := openStore(t)
	aged := strings.Replace(replayDSL, "rules:", "universe:\n  min_age_bars: 3\nrules:", 1)
	job := submitReplay(t, repo, aged, "idx", origin.Add(2*time.Hour), 4)
	// 执行时覆盖统计显示起点已前移到 10:00，晚于提交的 02:00。
	client := &fakeClient{marketType: "spot", indexedFrom: origin.Add(10 * time.Hour)}
	(&Runner{Store: repo, Client: client, ChunkBars: 10}).Execute(context.Background(), job)
	done, _ := repo.GetReplay(context.Background(), "p1")
	if done.Status != store.ReplayDone || len(barsOf(t, repo, "p1")) != 4 {
		t.Fatalf("索引未变时应按提交的区间执行：%+v", done)
	}
	if client.coverageCalls != 0 {
		t.Fatalf("索引未变时不应读取覆盖统计：%d", client.coverageCalls)
	}
}

// 排队期间 View 重建了索引（新索引只保留最近的根数）：按新索引复查起点，越界时明确提示重建并要求重新发起。
func TestReplayRechecksWindowAfterIndexRebuild(t *testing.T) {
	repo := openStore(t)
	job := submitReplay(t, repo, replayDSL, "idx_old", origin.Add(2*time.Hour), 4)
	client := &fakeClient{marketType: "spot", indexedFrom: origin.Add(10 * time.Hour)}
	(&Runner{Store: repo, Client: client, ChunkBars: 10}).Execute(context.Background(), job)
	failed, _ := repo.GetReplay(context.Background(), "p1")
	if failed.Status != store.ReplayFailed || !strings.Contains(failed.Error, "重建了索引") || !strings.Contains(failed.Error, "重新发起") {
		t.Fatalf("索引重建后起点越界应失败并提示重建：%+v", failed)
	}
	if client.coverageCalls == 0 {
		t.Fatal("索引变化时应读取新索引的覆盖统计")
	}
}

// 设了 min_age_bars 的策略在执行时覆盖统计未知：代次没变时不读统计，按提交的区间执行（执行路径不做启用级的覆盖校验）；
// 代次变了而新索引的统计未知时无法确认区间，拒绝执行。
func TestReplayWithMinAgeAndUnknownCoverage(t *testing.T) {
	repo := openStore(t)
	aged := strings.Replace(replayDSL, "rules:", "universe:\n  min_age_bars: 3\nrules:", 1)
	job := submitReplay(t, repo, aged, "idx", origin.Add(4*time.Hour), 4)
	(&Runner{Store: repo, Client: &fakeClient{marketType: "spot", unknownCoverage: true}, ChunkBars: 10}).Execute(context.Background(), job)
	done, _ := repo.GetReplay(context.Background(), "p1")
	if done.Status != store.ReplayDone || len(barsOf(t, repo, "p1")) != 4 {
		t.Fatalf("代次没变时应按提交的区间执行：%+v", done)
	}

	other := openStore(t)
	changed := submitReplay(t, other, aged, "idx_old", origin.Add(4*time.Hour), 4)
	(&Runner{Store: other, Client: &fakeClient{marketType: "spot", unknownCoverage: true}, ChunkBars: 10}).Execute(context.Background(), changed)
	failed, _ := other.GetReplay(context.Background(), "p1")
	if failed.Status != store.ReplayFailed || !strings.Contains(failed.Error, "覆盖范围暂时未知") {
		t.Fatalf("代次变了且统计未知时应拒绝执行：%+v", failed)
	}
}

// A/B 槽位名在重建时交替复用：排队期间经历 A→B→A，槽位名相同、构建 ID 不同，必须按新一代复查区间。
func TestReplayDetectsSameSlotRebuild(t *testing.T) {
	repo := openStore(t)
	job := submitReplay(t, repo, replayDSL, "idx@b1", origin.Add(2*time.Hour), 4)
	client := &fakeClient{marketType: "spot", build: "b3", indexedFrom: origin.Add(10 * time.Hour)}
	(&Runner{Store: repo, Client: client, ChunkBars: 10}).Execute(context.Background(), job)
	failed, _ := repo.GetReplay(context.Background(), "p1")
	if failed.Status != store.ReplayFailed || !strings.Contains(failed.Error, "重建了索引") || client.coverageCalls == 0 {
		t.Fatalf("同一槽位换了一代也应复查并发现起点越界：%+v coverage=%d", failed, client.coverageCalls)
	}
}

// 回放过程中活动索引换了一代：读取器不静默改读新索引。新一代仍覆盖剩余区间时切换过去继续，不再覆盖时明确失败。
func TestReplayHandlesIndexSwitchDuringRun(t *testing.T) {
	repo := openStore(t)
	job := submitReplay(t, repo, replayDSL, "idx", origin.Add(2*time.Hour), 12)
	// 第 2 段（第 2 次读取）之前换代，新一代从 00:00 起仍有数据：复查通过，回放完成。
	covered := &fakeClient{marketType: "spot", switchAtQuery: 2, switchedFrom: origin}
	(&Runner{Store: repo, Client: covered, ChunkBars: 4}).Execute(context.Background(), job)
	done, _ := repo.GetReplay(context.Background(), "p1")
	if done.Status != store.ReplayDone || len(barsOf(t, repo, "p1")) != 12 {
		t.Fatalf("新一代仍覆盖剩余区间时应切换过去继续：%+v bars=%d", done, len(barsOf(t, repo, "p1")))
	}
	for _, bar := range barsOf(t, repo, "p1") {
		if bar.SkipReason == "no_data" {
			t.Fatalf("切换后不应出现缺数据的周期：%+v", bar)
		}
	}

	other := openStore(t)
	trimmed := submitReplay(t, other, replayDSL, "idx", origin.Add(2*time.Hour), 12)
	// 新一代从 20:00 起才有数据：第 2 段（06:00 起）已不再被覆盖。
	cut := &fakeClient{marketType: "spot", switchAtQuery: 2, switchedFrom: origin.Add(20 * time.Hour)}
	(&Runner{Store: other, Client: cut, ChunkBars: 4}).Execute(context.Background(), trimmed)
	failed, _ := other.GetReplay(context.Background(), "p1")
	if failed.Status != store.ReplayFailed || !strings.Contains(failed.Error, "在回放过程中重建了索引") {
		t.Fatalf("新一代不再覆盖剩余区间时应失败：%+v", failed)
	}
	if written := len(barsOf(t, other, "p1")); written != 4 {
		t.Fatalf("换代前写入的周期应保留：%d", written)
	}
}

// 失败原因不带英文原文：超时等错误换成中文概述。
func TestReplayFailureMessageIsFriendly(t *testing.T) {
	repo := openStore(t)
	job := submitReplay(t, repo, replayDSL, "idx", origin.Add(2*time.Hour), 4)
	client := &fakeClient{marketType: "spot", failQuery: func(input.Query) error {
		return fmt.Errorf("读取 View 行：%w", context.DeadlineExceeded)
	}}
	(&Runner{Store: repo, Client: client, ChunkBars: 10}).Execute(context.Background(), job)
	failed, _ := repo.GetReplay(context.Background(), "p1")
	if failed.Status != store.ReplayFailed || strings.Contains(failed.Error, "deadline") || !strings.Contains(failed.Error, "请求超时") {
		t.Fatalf("失败原因应是中文概述：%+v", failed)
	}
}

// 判定顺序与实时一致：当期歧义 → 年龄探针 → 上一根歧义。上一根有歧义、同时年龄窗口是历史缺口时记 history_insufficient。
func TestReplaySkipOrderMatchesLive(t *testing.T) {
	repo := openStore(t)
	// 第 5 小时的年龄窗口是第 1~3 小时，整个数据集在其中都没有行；上一根（第 4 小时）A 有两个序列。
	gap := map[int]bool{1: true, 2: true, 3: true}
	client := &fakeClient{marketType: "spot", ambiguous: map[int]bool{4: true}, missing: map[string]map[int]bool{"A": gap, "B": gap, "C": gap}}
	aged := strings.Replace(previousDSL, "rules:", "universe:\n  min_age_bars: 3\nrules:", 1)
	job := submitReplay(t, repo, aged, "idx", origin.Add(5*time.Hour), 1)
	(&Runner{Store: repo, Client: client, ChunkBars: 10}).Execute(context.Background(), job)
	bars := barsOf(t, repo, "p1")
	if len(bars) != 1 || bars[0].SkipReason != input.SkipHistoryInsufficient {
		t.Fatalf("上一根歧义与历史缺口同时出现时应先报历史不足：%+v", bars)
	}
}

// A 股日线：年龄窗口里落在非交易日的行（例如节假日误写的一行）跳过，不让整个回放失败。
func TestPresenceSkipsRowsOffCalendar(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	resolved := input.Resolved{Calendar: "cn_stock", Bar: "1d", MinAgeBars: 1}
	holiday := time.Date(2026, 10, 5, 0, 0, 0, 0, location).UTC()
	trading := time.Date(2026, 10, 9, 0, 0, 0, 0, location).UTC()
	rows := input.RangeRows{Bars: map[int64]map[string]input.Row{
		holiday.Unix(): {"600000.SH": {SubjectID: "600000.SH", DataTime: holiday}},
		trading.Unix(): {"600000.SH": {SubjectID: "600000.SH", DataTime: trading}},
	}}
	ages := newPresence()
	ages.add(resolved, rows)
	if len(ages.cache) != 1 || len(ages.indexes["600000.SH"]) != 1 {
		t.Fatalf("应只登记交易日的行：cache=%v indexes=%v", ages.cache, ages.indexes)
	}
	period, err := input.FromStorageStart("cn_stock", "1d", trading)
	if err != nil {
		t.Fatal(err)
	}
	satisfied, err := ages.probe(resolved, period)(context.Background(), []string{"600000.SH"})
	if err != nil || len(satisfied) != 1 {
		t.Fatalf("交易日的行应满足年龄：%v err=%v", satisfied, err)
	}
}

// 年化门槛按根数：A 股日线一整周是 5 个交易日，加密货币 1h 是 168 根。
func TestAnnualizedThresholdByCalendar(t *testing.T) {
	if bars := minAnnualizedBars("cn_stock", 24*time.Hour); bars != 5 {
		t.Fatalf("A 股一周应是 5 根：%d", bars)
	}
	if bars := minAnnualizedBars("crypto_24x7", time.Hour); bars != 168 {
		t.Fatalf("1h 一周应是 168 根：%d", bars)
	}
	acc := newAccumulator(100, periodsPerYear("cn_stock", 24*time.Hour), minAnnualizedBars("cn_stock", 24*time.Hour))
	for i := 0; i < 5; i++ {
		acc.add(origin.Add(time.Duration(i)*24*time.Hour), true, "", Outcome{EquityBefore: 100 + float64(i), EquityAfter: 100 + float64(i+1)}, 0)
	}
	if metrics := acc.finish(); metrics.AnnualizedReturn == nil {
		t.Fatalf("A 股一整周应给出年化：%+v", metrics)
	}
}

// 先写入周期再累计指标：写入失败时，保留的部分指标与已写入的周期一致。第 4 根的记录已存在（主键冲突）让写入失败。
func TestReplayPartialMetricsMatchWrittenBars(t *testing.T) {
	repo := openStore(t)
	job := submitReplay(t, repo, replayDSL, "idx", origin.Add(2*time.Hour), 6)
	if err := repo.AppendReplayBar(context.Background(), store.ReplayBar{ReplayID: "p1", BarEndTime: origin.Add(6 * time.Hour), Status: "ok", TargetsJSON: []byte(`[]`), PositionsJSON: []byte(`{}`), SummaryJSON: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	(&Runner{Store: repo, Client: &fakeClient{marketType: "spot"}, ChunkBars: 10}).Execute(context.Background(), job)
	failed, _ := repo.GetReplay(context.Background(), "p1")
	var metrics Metrics
	if err := json.Unmarshal(failed.MetricsJSON, &metrics); err != nil {
		t.Fatal(err)
	}
	if failed.Status != store.ReplayFailed || metrics.Bars != 3 {
		t.Fatalf("部分指标应只含已写入的 3 根：status=%s bars=%d error=%s", failed.Status, metrics.Bars, failed.Error)
	}
	if strings.Contains(failed.Error, "UNIQUE") || strings.Contains(failed.Error, "constraint") {
		t.Fatalf("失败原因不应带数据库驱动的英文原文：%s", failed.Error)
	}
}
