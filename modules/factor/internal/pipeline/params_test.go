package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/factorname"
	"github.com/stretchr/testify/require"
)

const pmeanSpec = "params:\n  window: {type: int, min: 2, max: 5, default: 2}\noutput: ts_mean(close, window)\n"

// pmeanMember 返回因子 pmean 的成员：可变成员取默认参数，固定成员取给定参数。
func pmeanMember(t *testing.T, kind string, params map[string]float64) domain.MemberDef {
	t.Helper()
	def := domain.MustDefinition("pmean", "pmean", pmeanSpec)
	full, err := domain.ResolveMemberParams(def, params)
	require.NoError(t, err)
	key, err := domain.DeriveMemberKey(def, kind, full)
	require.NoError(t, err)
	return domain.MemberDef{Key: key, Factor: def, Kind: kind, Params: full}
}

func rowFields(rows []storageio.ResultRow, target time.Time) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, row := range rows {
		if row.DataTime.Equal(target) {
			out[row.SubjectID] = row.Fields
		}
	}
	return out
}

// 提交的参数真正改变计算：默认值之外的标的级覆盖让两个标的用不同的窗口；可变成员与固定成员各用各的参数，
// 结果列按成员键命名，__params 写这个标的这一期各成员实际用的值，成员状态带成员键与类型。
func TestComputeAndAssembleUsePerSubjectParams(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	plan := computePlan(target, nil, []string{"BTC", "ETH"})
	variable := pmeanMember(t, domain.MemberKindVar, nil)
	fixed := pmeanMember(t, domain.MemberKindFixed, map[string]float64{"window": 5})
	plan.Members = []domain.MemberDef{variable, fixed}
	plan.Params = map[string]domain.ParamSnapshot{
		variable.Key: {"window": {Default: 2, Overrides: map[string]float64{"ETH": 4}}},
	}
	// BTC 的 close 是 100..104，ETH 是 200..204（目标时刻是最后一根）。
	loaded := withView(t, framesForBars(plan.Available, target, 5))
	runner := NewRunner(&outputStore{}, nil, periodclock.Continuous{}, Config{ComputeWorkers: 2})

	computation, err := runner.Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	require.Equal(t, 103.5, valueAt(t, computation.Results["pmean__var"]["BTC"], target), "BTC 用默认窗口 2")
	require.Equal(t, 202.5, valueAt(t, computation.Results["pmean__var"]["ETH"], target), "ETH 用覆盖窗口 4")
	require.Equal(t, 102.0, valueAt(t, computation.Results["pmean__5"]["BTC"], target), "固定成员用窗口 5")
	require.Equal(t, []string{"data_time", "series_tag", "pmean__var"}, computation.Results["pmean__var"]["BTC"].Columns)
	state := computation.FactorStates["pmean__var"]
	require.Equal(t, "pmean__var", state.MemberKey)
	require.Equal(t, domain.MemberKindVar, state.MemberKind)
	require.Equal(t, "pmean", state.FactorID)
	require.Equal(t, domain.MemberKindFixed, computation.FactorStates["pmean__5"].MemberKind)

	rows, err := runner.Assemble(plan, loaded, computation)
	require.NoError(t, err)
	fields := rowFields(rows, target)
	require.Equal(t, 202.5, fields["ETH"]["pmean__var"])
	require.Equal(t, `{"pmean__5":{"window":5},"pmean__var":{"window":4}}`, string(fields["ETH"][factorname.ParamsColumn].(storageio.JSONText)), "__params 写实际用的值")
	require.Equal(t, `{"pmean__5":{"window":5},"pmean__var":{"window":2}}`, string(fields["BTC"][factorname.ParamsColumn].(storageio.JSONText)))
	for name := range fields["BTC"] {
		require.NotContains(t, name, "__window", "不再有参数列：%s", name)
	}
}

// 没有提交值的可变成员按它自己的默认参数计算。
func TestComputeWithoutSubmissionUsesMemberParams(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	plan := computePlan(target, nil, []string{"ETH"})
	variable := pmeanMember(t, domain.MemberKindVar, map[string]float64{"window": 3})
	plan.Members = []domain.MemberDef{variable}
	loaded := withView(t, framesForBars([]string{"ETH"}, target, 5))
	computation, err := newComputeRunner(Config{ComputeWorkers: 1}).Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	require.Equal(t, 103.0, valueAt(t, computation.Results["pmean__var"]["ETH"], target), "单个标的是第一个标的：100..104，窗口 3")
	require.Equal(t, []string{"data_time", "series_tag", "pmean__var"}, computation.Results["pmean__var"]["ETH"].Columns)
}

// 补算只替换本次计算的成员：已有行的 __params 里其他成员的记录保留；已有行里没有的行按本次计算生成。
func TestAssembleMergesBaseParams(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	plan := computePlan(target, nil, []string{"BTC"})
	variable := pmeanMember(t, domain.MemberKindVar, nil)
	plan.Members = []domain.MemberDef{variable}
	plan.Params = map[string]domain.ParamSnapshot{variable.Key: {"window": {Default: 3}}}
	plan.BaseParams = map[RowKey]map[string]map[string]float64{
		{Subject: "BTC", At: target, Tag: "venue:binance"}: {"other__var": {"span": 9}, "pmean__var": {"window": 2}},
	}
	loaded := withView(t, framesForBars(plan.Available, target, 5))
	runner := NewRunner(&outputStore{}, nil, periodclock.Continuous{}, Config{ComputeWorkers: 1})
	computation, err := runner.Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	rows, err := runner.Assemble(plan, loaded, computation)
	require.NoError(t, err)
	got := string(rowFields(rows, target)["BTC"][factorname.ParamsColumn].(storageio.JSONText))
	require.Equal(t, `{"other__var":{"span":9},"pmean__var":{"window":3}}`, got)
}

// 补算时某个成员计算失败：输出列写 NULL，它在 __params 里的记录一并去掉（没有取得值，就没有“实际用的参数”），其他成员的记录不动。
func TestAssembleDropsFailedMemberParams(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	plan := computePlan(target, nil, []string{"BTC"})
	variable := pmeanMember(t, domain.MemberKindVar, nil)
	plan.Members = []domain.MemberDef{variable}
	plan.Params = map[string]domain.ParamSnapshot{variable.Key: {"window": {Default: 3}}}
	plan.BaseParams = map[RowKey]map[string]map[string]float64{
		{Subject: "BTC", At: target, Tag: "venue:binance"}: {"other__var": {"span": 9}, "pmean__var": {"window": 2}},
	}
	loaded := withView(t, framesForBars(plan.Available, target, 5))
	runner := NewRunner(&outputStore{}, nil, periodclock.Continuous{}, Config{ComputeWorkers: 1})
	computation, err := runner.Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	item := computation.Results["pmean__var"]["BTC"]
	item.Err = errors.New("boom")
	computation.Results["pmean__var"]["BTC"] = item
	rows, err := runner.Assemble(plan, loaded, computation)
	require.NoError(t, err)
	fields := rowFields(rows, target)["BTC"]
	require.Nil(t, fields["pmean__var"])
	require.Equal(t, `{"other__var":{"span":9}}`, string(fields[factorname.ParamsColumn].(storageio.JSONText)))
}

// 失败成员是行里唯一的参数键时，写出 {}：不写这一列的话 Storage 会保留已有行的旧 __params。
func TestAssembleWritesEmptyParamsWhenOnlyMemberFailed(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	plan := computePlan(target, nil, []string{"BTC"})
	variable := pmeanMember(t, domain.MemberKindVar, nil)
	plan.Members = []domain.MemberDef{variable}
	plan.Params = map[string]domain.ParamSnapshot{variable.Key: {"window": {Default: 3}}}
	plan.BaseParams = map[RowKey]map[string]map[string]float64{
		{Subject: "BTC", At: target, Tag: "venue:binance"}: {"pmean__var": {"window": 2}},
	}
	loaded := withView(t, framesForBars(plan.Available, target, 5))
	runner := NewRunner(&outputStore{}, nil, periodclock.Continuous{}, Config{ComputeWorkers: 1})
	computation, err := runner.Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	item := computation.Results["pmean__var"]["BTC"]
	item.Err = errors.New("boom")
	computation.Results["pmean__var"]["BTC"] = item
	rows, err := runner.Assemble(plan, loaded, computation)
	require.NoError(t, err)
	require.Equal(t, `{}`, string(rowFields(rows, target)["BTC"][factorname.ParamsColumn].(storageio.JSONText)))
}
