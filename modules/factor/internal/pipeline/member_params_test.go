package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/factorname"
)

const paramMeanSpec = "params:\n  window: {type: int, min: 2, max: 10, default: 3}\noutput: ts_mean(close, window)\n"

func paramMeanMembers(t *testing.T) []domain.MemberDef {
	t.Helper()
	def := domain.MustDefinition("pmean", "pmean", paramMeanSpec)
	build := func(kind string, params map[string]float64) domain.MemberDef {
		full, err := domain.ResolveMemberParams(def, params)
		require.NoError(t, err)
		key, err := domain.DeriveMemberKey(def, kind, full)
		require.NoError(t, err)
		return domain.MemberDef{Key: key, Factor: def, Kind: kind, Params: full}
	}
	return []domain.MemberDef{build(domain.MemberKindFixed, nil), build(domain.MemberKindFixed, map[string]float64{"window": 5})}
}

// 同一个定义带不同固定参数，在一个任务里并行计算：各自的结果与直接写死窗口的表达式一致，__params 如实记录每个成员的参数。
func TestFixedParamMembersComputeIndependently(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	subjects := []string{"BTC", "ETH"}
	store := newMemoryStorage(subjects, target, 10)
	runner := NewRunner(store, store.data(), periodclock.Continuous{}, Config{ComputeWorkers: 2})
	input := e2eLiveInput(target, subjects, nil)
	input.Members = paramMeanMembers(t)
	plan, err := BuildLivePlan(periodclock.Continuous{}, input)
	require.NoError(t, err)

	outcome, err := runner.Run(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, "complete", outcome.Status)
	require.Equal(t, 2, outcome.RowsWritten)
	require.Len(t, store.markers[0].Factors, 2)

	for index, row := range sortedRows(store) {
		base := float64(100 * (index + 1))
		require.InDelta(t, base+9, row.Fields["pmean"], 1e-9, "窗口 3：最近三根的均值")
		require.InDelta(t, base+8, row.Fields["pmean__5"], 1e-9, "窗口 5：最近五根的均值")
		require.Equal(t, storageio.JSONText(`{"pmean":{"window":3},"pmean__5":{"window":5}}`), row.Fields[factorname.ParamsColumn])
	}
	kinds := map[string]string{}
	for _, state := range store.markers[0].Factors {
		kinds[state.MemberKey] = state.MemberKind
	}
	require.Equal(t, map[string]string{"pmean": "fixed", "pmean__5": "fixed"}, kinds)
}

// 可变成员没有提交值时取默认参数，照样写进 __params。
func TestVarMemberWithoutSubmissionWritesDefaultsToParams(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	subjects := []string{"BTC"}
	def := domain.MustDefinition("pmean", "pmean", paramMeanSpec)
	store := newMemoryStorage(subjects, target, 10)
	runner := NewRunner(store, store.data(), periodclock.Continuous{}, Config{ComputeWorkers: 1})
	input := e2eLiveInput(target, subjects, nil)
	input.Members = []domain.MemberDef{{Key: "pmean__var", Factor: def, Kind: domain.MemberKindVar, Params: map[string]float64{"window": 3}}}
	plan, err := BuildLivePlan(periodclock.Continuous{}, input)
	require.NoError(t, err)

	_, err = runner.Run(context.Background(), plan)
	require.NoError(t, err)
	rows := sortedRows(store)
	require.Len(t, rows, 1)
	require.Equal(t, storageio.JSONText(`{"pmean__var":{"window":3}}`), rows[0].Fields[factorname.ParamsColumn])
	require.Contains(t, rows[0].Fields, "pmean__var")
}
