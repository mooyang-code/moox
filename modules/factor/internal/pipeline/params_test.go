package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/stretchr/testify/require"
)

func paramMeanMember(alias string) domain.MemberDef {
	def := domain.MustDefinition("pmean", "pmean", "params:\n  window: {type: int, min: 2, max: 5, default: 2}\noutputs:\n  mean: ts_mean(close, window)\n")
	return domain.MemberDef{Alias: alias, Factor: def}
}

func paramFreeze(alias string, snapshot domain.ParamSnapshot) domain.ParamFreeze {
	return domain.ParamFreeze{MemberAlias: alias, Snapshot: snapshot, SnapshotHash: snapshot.Hash(), Source: domain.ParamsRequested}
}

// 冻结的参数真正改变计算：默认值之外的标的级覆盖让两个标的用不同的窗口；同一定义的两个别名各用各的参数，
// 结果列与参数列按别名命名，参数列写这个标的这一期实际用的值，成员状态带参数来源与摘要。
func TestComputeAndAssembleUsePerSubjectParams(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	plan := computePlan(target, nil, []string{"BTC", "ETH"})
	plan.Members = []domain.MemberDef{paramMeanMember("fast"), paramMeanMember("slow")}
	plan.Params = map[string]domain.ParamFreeze{
		"fast": paramFreeze("fast", domain.ParamSnapshot{"window": {Default: 2, Overrides: map[string]float64{"ETH": 4}}}),
		"slow": paramFreeze("slow", domain.ParamSnapshot{"window": {Default: 5}}),
	}
	// BTC 的 close 是 100..104，ETH 是 200..204（目标时刻是最后一根）。
	loaded := withView(t, framesForBars(plan.Available, target, 5))
	runner := NewRunner(&outputStore{}, nil, periodclock.Continuous{}, Config{ComputeWorkers: 2})

	computation, err := runner.Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	require.Equal(t, 103.5, valueAt(t, computation.Results["fast"]["BTC"], target), "BTC 用默认窗口 2")
	require.Equal(t, 202.5, valueAt(t, computation.Results["fast"]["ETH"], target), "ETH 用覆盖窗口 4")
	require.Equal(t, 102.0, valueAt(t, computation.Results["slow"]["BTC"], target), "另一个别名用窗口 5")
	require.Equal(t, []string{"data_time", "series_tag", "fast_mean"}, computation.Results["fast"]["BTC"].Columns)
	fast := computation.FactorStates["fast"]
	require.Equal(t, "fast", fast.Member)
	require.Equal(t, "pmean", fast.FactorID)
	require.Equal(t, domain.ParamsRequested, fast.ParamsSource)
	require.NotEmpty(t, fast.ParamsDigest)
	require.NotEqual(t, fast.ParamsDigest, computation.FactorStates["slow"].ParamsDigest)

	rows, err := runner.Assemble(plan, loaded, computation)
	require.NoError(t, err)
	fields := map[string]map[string]any{}
	for _, row := range rows {
		if row.DataTime.Equal(target) {
			fields[row.SubjectID] = row.Fields
		}
	}
	require.Equal(t, 202.5, fields["ETH"]["fast_mean"])
	require.Equal(t, 4.0, fields["ETH"]["fast__window"], "参数列写实际用的值")
	require.Equal(t, 2.0, fields["BTC"]["fast__window"])
	require.Equal(t, 5.0, fields["BTC"]["slow__window"])
}

// 没有冻结记录的成员按默认参数计算，来源记为 default。
func TestComputeWithoutParamsUsesDefaults(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	plan := computePlan(target, nil, []string{"ETH"})
	plan.Members = []domain.MemberDef{paramMeanMember("pmean")}
	loaded := withView(t, framesForBars([]string{"ETH"}, target, 5))
	computation, err := newComputeRunner(Config{ComputeWorkers: 1}).Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	require.Equal(t, 103.5, valueAt(t, computation.Results["pmean"]["ETH"], target))
	require.Equal(t, domain.ParamsDefault, computation.FactorStates["pmean"].ParamsSource)
	require.Equal(t, []string{"data_time", "series_tag", "mean"}, computation.Results["pmean"]["ETH"].Columns, "别名与定义 ID 相同时列名就是输出名")
}
