package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
)

const paramMeanSpec = "params:\n  window: {type: int, min: 2, max: 10, default: 3}\noutput: ts_mean(close, window)\n"

// 同一个定义带不同固定参数，在一个任务里并行计算：各自的结果与直接写死窗口的表达式一致，不写参数列。
func TestFixedParamMembersComputeIndependently(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	subjects := []string{"BTC", "ETH"}
	def := domain.MustDefinition("pmean", "pmean", paramMeanSpec)
	store := newMemoryStorage(subjects, target, 10)
	runner := NewRunner(store, store.data(), periodclock.Continuous{}, Config{ComputeWorkers: 2})
	input := e2eLiveInput(target, subjects, nil)
	input.Members = []domain.MemberDef{
		{Alias: "pmean", Factor: def, Params: map[string]float64{"window": 3}},
		{Alias: "pmean_5", Factor: def, Params: map[string]float64{"window": 5}},
	}
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
		require.InDelta(t, base+8, row.Fields["pmean_5_pmean"], 1e-9, "窗口 5：最近五根的均值")
		for name := range row.Fields {
			require.NotContains(t, name, "__", "带固定参数的成员不写参数列：%s", name)
		}
	}
	for _, state := range store.markers[0].Factors {
		require.NotEmpty(t, state.ParamsDigest, "参数摘要按成员的固定参数计算")
	}
	digests := map[string]string{}
	for _, state := range store.markers[0].Factors {
		digests[state.Member] = state.ParamsDigest
	}
	require.NotEqual(t, digests["pmean"], digests["pmean_5"], "不同的固定参数有不同的摘要")
}

// 不带固定参数的成员（策略实例专属任务）：参数取冻结记录或定义默认值，并写参数列。
func TestMembersWithoutFixedParamsStillWriteParamColumns(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	subjects := []string{"BTC"}
	def := domain.MustDefinition("pmean", "pmean", paramMeanSpec)
	store := newMemoryStorage(subjects, target, 10)
	runner := NewRunner(store, store.data(), periodclock.Continuous{}, Config{ComputeWorkers: 1})
	input := e2eLiveInput(target, subjects, nil)
	input.Members = []domain.MemberDef{{Alias: "fast", Factor: def}}
	plan, err := BuildLivePlan(periodclock.Continuous{}, input)
	require.NoError(t, err)

	_, err = runner.Run(context.Background(), plan)
	require.NoError(t, err)
	rows := sortedRows(store)
	require.Len(t, rows, 1)
	require.InDelta(t, 3.0, rows[0].Fields["fast__window"], 1e-9, "没有冻结记录时取定义的默认值，并写进参数列")
	require.Contains(t, rows[0].Fields, "fast_pmean")
}
