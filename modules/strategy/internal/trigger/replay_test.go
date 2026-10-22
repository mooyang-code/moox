package trigger

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
)

// replayClient 是回放测试用的 Storage/Factor 替身：一个现货数据集 + 一个带因子列 m 的 View。
type replayClient struct{}

func (replayClient) GetView(context.Context, string, string) (input.ViewInfo, error) {
	return input.ViewInfo{ViewID: "view_a", DatasetID: "ds", Frequency: "1h", Status: "active", ActiveIndexID: "i1", Columns: []input.ViewColumn{
		{Name: "close", Attributes: map[string]string{}},
		{Name: "m", Attributes: map[string]string{"origin_factor_id": "f", "factor_output": "m"}},
	}}, nil
}
func (replayClient) ViewGeneration(context.Context, string, string) (string, error) { return "g", nil }
func (replayClient) ViewCoverage(context.Context, string, input.ViewInfo, bool) (input.Coverage, error) {
	return input.Coverage{}, nil
}
func (replayClient) GetDataset(context.Context, string, string) (input.DatasetInfo, error) {
	return input.DatasetInfo{DatasetID: "ds", Frequency: "1h", Retention: "forever", Attributes: map[string]string{"market_type": "spot"}}, nil
}
func (replayClient) ListDatasetSubjects(context.Context, string, string) ([]input.Subject, error) {
	return []input.Subject{{SubjectID: "A", Active: true}, {SubjectID: "B", Active: true}, {SubjectID: "C", Active: true}}, nil
}
func (replayClient) ListTagMembers(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (replayClient) GetTag(context.Context, string, string) (input.TagInfo, error) {
	return input.TagInfo{}, nil
}
func (replayClient) QueryRows(context.Context, string, input.Query) ([]input.Row, uint64, error) {
	return nil, 0, fmt.Errorf("回放不读行")
}
func (replayClient) GetFactor(context.Context, string) (input.FactorInfo, error) {
	return input.FactorInfo{FactorID: "f", DefinitionHash: "h1"}, nil
}

func newReplayEvaluator(t *testing.T) *ReplayEvaluator {
	t.Helper()
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	hash := dsl.Hash([]byte(rankDSL))
	if err := repo.CreateDefinition(ctx, store.Definition{StrategyID: "s1", Name: "demo", DSLYaml: rankDSL, DSLHash: hash, CreatedAt: bar0}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateInstance(ctx, store.Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", ViewID: "view_a", CreatedAt: bar0}); err != nil {
		t.Fatal(err)
	}
	return &ReplayEvaluator{Store: repo, Client: replayClient{}}
}

func replayRows(values map[string]float64) map[string]ReplayRow {
	rows := map[string]ReplayRow{}
	for subject, m := range values {
		rows[subject] = ReplayRow{Source: map[string]float64{"close": 10}, Factors: map[string]map[string]float64{"f": {"m": m}}, AgeBars: 100}
	}
	return rows
}

// 回放求值与实时同一条引擎：按因子列选出 top 2，规则状态由调用方带回；就绪判定失败时本期跳过且状态不变。
func TestReplayEvaluateRanksAndCarriesState(t *testing.T) {
	e := newReplayEvaluator(t)
	ctx := context.Background()
	binding, err := e.Bind(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	if binding.Factors["f"] != "h1" || binding.Bar != "1h" || !binding.Spot || binding.PreviousDepth != 0 {
		t.Fatalf("绑定不符：%+v", binding)
	}
	period := barStart(1).Format(time.RFC3339)
	complete := map[string]ReplayFactorState{"f": {Status: "complete", DefinitionHash: "h1"}}

	first, err := e.Evaluate(ctx, ReplayEvaluateRequest{ReplayID: "r1", InstanceID: "i1", DSLHash: binding.DSLHash, Period: period, Rows: replayRows(map[string]float64{"A": 3, "B": 2, "C": 1}), FactorStates: complete})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "ok" || len(first.Targets) != 2 {
		t.Fatalf("首期应选出 2 个标的：%+v", first)
	}
	got := map[string]string{}
	for _, target := range first.Targets {
		got[target.InstrumentID] = target.TargetWeight
	}
	if got["A"] != "0.5" || got["B"] != "0.5" {
		t.Fatalf("目标权重不符：%v", got)
	}
	var state map[string]any
	if err := json.Unmarshal(first.State, &state); err != nil || state["rules"] == nil {
		t.Fatalf("应返回下一期的规则状态：%s", first.State)
	}

	// 下一期：C 排名第一，但 buffer 保留上期持有的 A、B（与实盘同一条规则）；状态来自上一期。
	second, err := e.Evaluate(ctx, ReplayEvaluateRequest{ReplayID: "r1", InstanceID: "i1", DSLHash: binding.DSLHash, Period: barStart(2).Format(time.RFC3339), Rows: replayRows(map[string]float64{"A": 2, "B": 2.5, "C": 3}), FactorStates: complete, State: first.State})
	if err != nil || second.Status != "ok" {
		t.Fatalf("第二期应正常求值：%+v err=%v", second, err)
	}
	held := map[string]bool{}
	for _, target := range second.Targets {
		held[target.InstrumentID] = true
	}
	if !held["A"] || !held["B"] || held["C"] {
		t.Fatalf("buffer 应保留上期持有的 A、B：%v", held)
	}

	// 因子被跳过：本期跳过，规则状态原样带回。
	skipped, err := e.Evaluate(ctx, ReplayEvaluateRequest{ReplayID: "r1", InstanceID: "i1", DSLHash: binding.DSLHash, Period: barStart(3).Format(time.RFC3339), Rows: replayRows(map[string]float64{"A": 2}), FactorStates: map[string]ReplayFactorState{"f": {Status: "skipped", DefinitionHash: "h1"}}, State: second.State})
	if err != nil || skipped.Status != "skipped" || skipped.SkipReason != "factor_skipped" || len(skipped.Targets) != 0 {
		t.Fatalf("因子跳过应使本期跳过：%+v err=%v", skipped, err)
	}
	if string(skipped.State) != string(second.State) {
		t.Fatalf("跳过的周期不改变规则状态：\n%s\n%s", skipped.State, second.State)
	}

	// 因子指纹与绑定时固定的不同：回放要求重新发起。
	changed, err := e.Evaluate(ctx, ReplayEvaluateRequest{ReplayID: "r1", InstanceID: "i1", DSLHash: binding.DSLHash, Period: barStart(4).Format(time.RFC3339), Rows: replayRows(map[string]float64{"A": 2}), FactorStates: map[string]ReplayFactorState{"f": {Status: "complete", DefinitionHash: "h2"}}, State: second.State})
	if err != nil || changed.SkipReason != "factor_changed" {
		t.Fatalf("因子定义变了应跳过为 factor_changed：%+v err=%v", changed, err)
	}

	if _, err := e.Evaluate(ctx, ReplayEvaluateRequest{ReplayID: "r1", InstanceID: "i1", DSLHash: "other", Period: period}); err == nil {
		t.Fatal("没有绑定的版本不能求值")
	}
}
