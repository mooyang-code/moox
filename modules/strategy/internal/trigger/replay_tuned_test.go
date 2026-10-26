package trigger

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
)

const tunedReplayDSL = `name: tuned_replay
factors:
  fast: {factor: f}
factor_params:
  fast:
    window:
      default: "fast_m * 10"
rules:
  - id: r
    type: rank
    score: "fast_m"
    filter: "fast__window > 0"
    select: {top: 2}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

// tunedReplayClient 是私有因子任务的结果 View：close、成员 fast 的输出 fast_m 与参数列 fast__window。
type tunedReplayClient struct{ replayClient }

func (tunedReplayClient) GetView(context.Context, string, string) (input.ViewInfo, error) {
	return input.ViewInfo{ViewID: "view_factor_i1", DatasetID: "ds", Frequency: "1h", Status: "active", ActiveIndexID: "i1", Columns: []input.ViewColumn{
		{Name: "close", Attributes: map[string]string{}},
		{Name: "fast_m", Attributes: map[string]string{"origin_factor_id": "fast", "member_alias": "fast", "factor_id": "f", "factor_output": "fast_m", "column_kind": "output"}},
		{Name: "fast__window", Attributes: map[string]string{"origin_factor_id": "fast", "member_alias": "fast", "factor_id": "f", "factor_output": "fast__window", "column_kind": "param", "param_name": "window"}},
	}}, nil
}

func newTunedReplayEvaluator(t *testing.T, enabled bool) *ReplayEvaluator {
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
	hash := dsl.Hash([]byte(tunedReplayDSL))
	if err := repo.CreateDefinition(ctx, store.Definition{StrategyID: "s1", Name: "tuned", DSLYaml: tunedReplayDSL, DSLHash: hash, CreatedAt: bar0}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateInstance(ctx, store.Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", ViewID: "view_a", CreatedAt: bar0}); err != nil {
		t.Fatal(err)
	}
	if enabled {
		resolved, err := json.Marshal(input.Resolved{
			CompletionKind: "factor_period.computed", ViewID: "view_factor_i1", SourceViewID: "view_a", FactorTaskID: "ftask_i1", DatasetID: "ds", Bar: "1h", Calendar: input.DefaultCalendar,
			FactorParams: map[string][]dsl.ParamDecl{"fast": {{Name: "window", Type: "int", Min: 5, Max: 60, Default: 20}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		session := "session-1"
		if err := repo.OpenSession(ctx, store.Session{SessionID: session, InstanceID: "i1", DSLHash: hash, ResolvedJSON: string(resolved), CreatedAt: bar0}, tunedReplayDSL); err != nil {
			t.Fatal(err)
		}
		if err := repo.SetInstanceEnabled(ctx, "i1", false, &session, resolved, bar0); err != nil {
			t.Fatal(err)
		}
		if err := repo.SetInstanceEnabled(ctx, "i1", true, &session, resolved, bar0); err != nil {
			t.Fatal(err)
		}
	}
	return &ReplayEvaluator{Store: repo, Client: tunedReplayClient{}}
}

func tunedRows(values map[string]float64) map[string]ReplayRow {
	rows := map[string]ReplayRow{}
	for subject, m := range values {
		rows[subject] = ReplayRow{Source: map[string]float64{"close": 10}, Factors: map[string]map[string]float64{"fast": {"fast_m": m, "fast__window": 20}}, AgeBars: 100}
	}
	return rows
}

// 声明了 factors 的实例回放：绑定到实例私有因子任务的结果 View（用启用时固化的解析），读因子输出与参数列；
// ok 的周期输出下一期的因子参数（整型取整、限幅），skipped 的周期不输出。
func TestReplayTunedStrategyEmitsNextParams(t *testing.T) {
	e := newTunedReplayEvaluator(t, true)
	ctx := context.Background()
	binding, err := e.Bind(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	if binding.FactorTaskID != "ftask_i1" || !binding.Tuned || binding.Factors["fast"] != "h1" {
		t.Fatalf("绑定不符：%+v", binding)
	}
	if got := binding.Columns["fast__window"]; got.Member != "fast" || got.Param != "window" {
		t.Fatalf("参数列应绑定到成员：%+v", got)
	}
	complete := map[string]ReplayFactorState{"fast": {Status: "complete", DefinitionHash: "h1"}}
	decision, err := e.Evaluate(ctx, ReplayEvaluateRequest{ReplayID: "r1", InstanceID: "i1", DSLHash: binding.DSLHash, Period: barStart(1).Format(time.RFC3339), Rows: tunedRows(map[string]float64{"A": 3, "B": 2, "C": 1}), FactorStates: complete})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != "ok" || len(decision.Targets) != 2 {
		t.Fatalf("应选出 2 个标的：%+v", decision)
	}
	// default = fast_m * 10 在 A、B、C 上是 30、20、10，中位数 20。
	if window := decision.NextParams["fast"]["window"]; window.Default != 20 || len(window.Overrides) != 0 {
		t.Fatalf("下一期参数不符：%+v", decision.NextParams)
	}

	// 因子被跳过：本期跳过，不输出参数（因子沿用）。
	skipped, err := e.Evaluate(ctx, ReplayEvaluateRequest{ReplayID: "r1", InstanceID: "i1", DSLHash: binding.DSLHash, Period: barStart(2).Format(time.RFC3339), Rows: tunedRows(map[string]float64{"A": 3}), FactorStates: map[string]ReplayFactorState{"fast": {Status: "skipped", DefinitionHash: "h1"}}, State: decision.State})
	if err != nil || skipped.Status != "skipped" || len(skipped.NextParams) != 0 {
		t.Fatalf("skipped 不应输出参数：%+v err=%v", skipped, err)
	}
}

func TestReplayTunedStrategyNeedsEnabledInstance(t *testing.T) {
	e := newTunedReplayEvaluator(t, false)
	if _, err := e.Bind(context.Background(), "i1"); err == nil {
		t.Fatal("没有启用过的实例没有私有因子任务，不能回放")
	}
}
