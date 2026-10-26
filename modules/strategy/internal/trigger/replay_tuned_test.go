package trigger

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
)

const tunedReplayDSL = `name: tuned_replay
factor_params:
  f__var:
    window:
      default: "m * 10"
rules:
  - id: r
    type: rank
    score: "m"
    filter: 'params("f__var", "window") > 0'
    select: {top: 2}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

// tunedReplayClient 是关联任务的结果 View：close、可变成员 f__var 的输出 m 与系统列 __params。
type tunedReplayClient struct{ replayClient }

func (tunedReplayClient) GetView(context.Context, string, string) (input.ViewInfo, error) {
	return input.ViewInfo{ViewID: "view_factor_a", DatasetID: "ds", Frequency: "1h", Status: "active", ActiveIndexID: "i1", Columns: []input.ViewColumn{
		{Name: "close", Attributes: map[string]string{}},
		{Name: "m", Attributes: map[string]string{"column_kind": "output", "member_key": "f__var", "factor_id": "f", "member_kind": "var", "output_name": "m"}},
		{Name: "__params", Attributes: map[string]string{"column_kind": "params"}},
	}}, nil
}

func (tunedReplayClient) GetFactor(context.Context, string) (input.FactorInfo, error) {
	return input.FactorInfo{FactorID: "f", DefinitionHash: "h1", Params: []input.ParamDecl{{Name: "window", Type: "int", Min: 5, Max: 60, Default: 20}}}, nil
}

func newTunedReplayEvaluator(t *testing.T) *ReplayEvaluator {
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
	if err := repo.CreateInstance(ctx, store.Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", FactorTaskID: "ftask_a", CreatedAt: bar0}); err != nil {
		t.Fatal(err)
	}
	return &ReplayEvaluator{Store: repo, Client: tunedReplayClient{}}
}

func tunedRows(values map[string]float64) map[string]ReplayRow {
	rows := map[string]ReplayRow{}
	for subject, m := range values {
		rows[subject] = ReplayRow{Source: map[string]float64{"close": 10}, Factors: map[string]map[string]float64{"f__var": {"m": m}},
			Params: map[string]map[string]float64{"f__var": {"window": 20}}, AgeBars: 100}
	}
	return rows
}

// 带 factor_params 的实例回放：按实例关联的因子任务绑定结果 View，读成员输出，params() 读行里的参数；
// ok 的周期输出下一期的可变成员参数（整型取整、限幅），skipped 的周期不输出。
func TestReplayTunedStrategyEmitsNextParams(t *testing.T) {
	e := newTunedReplayEvaluator(t)
	ctx := context.Background()
	binding, err := e.Bind(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	if binding.FactorTaskID != "ftask_a" || binding.ViewID != "view_factor_a" || !binding.Tuned || binding.Factors["f__var"] != "h1|var" {
		t.Fatalf("绑定不符：%+v", binding)
	}
	if got := binding.Columns["m"]; got.Member != "f__var" || got.MemberKind != "var" {
		t.Fatalf("输出列应绑定到可变成员：%+v", got)
	}
	complete := map[string]ReplayFactorState{"f__var": {Status: "complete", DefinitionHash: "h1", Kind: "var"}}
	decision, err := e.Evaluate(ctx, ReplayEvaluateRequest{ReplayID: "r1", InstanceID: "i1", DSLHash: binding.DSLHash, Period: barStart(1).Format(time.RFC3339), Rows: tunedRows(map[string]float64{"A": 3, "B": 2, "C": 1}), FactorStates: complete})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != "ok" || len(decision.Targets) != 2 {
		t.Fatalf("应选出 2 个标的：%+v", decision)
	}
	// default = m * 10 在 A、B、C 上是 30、20、10，中位数 20。
	if window := decision.NextParams["f__var"]["window"]; window.Default != 20 || len(window.Overrides) != 0 {
		t.Fatalf("下一期参数不符：%+v", decision.NextParams)
	}

	// 因子被跳过：本期跳过，不输出参数（因子沿用）。
	skipped, err := e.Evaluate(ctx, ReplayEvaluateRequest{ReplayID: "r1", InstanceID: "i1", DSLHash: binding.DSLHash, Period: barStart(2).Format(time.RFC3339), Rows: tunedRows(map[string]float64{"A": 3}), FactorStates: map[string]ReplayFactorState{"f__var": {Status: "skipped", DefinitionHash: "h1", Kind: "var"}}, State: decision.State})
	if err != nil || skipped.Status != "skipped" || len(skipped.NextParams) != 0 {
		t.Fatalf("skipped 不应输出参数：%+v err=%v", skipped, err)
	}
}
