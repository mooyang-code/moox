package recalcexec

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/stretchr/testify/require"
)

type planRecorder struct{ plans []pipeline.Plan }

func (r *planRecorder) Run(_ context.Context, plan pipeline.Plan) (pipeline.Outcome, error) {
	r.plans = append(r.plans, plan)
	return pipeline.Outcome{Status: "complete"}, nil
}

type fixedHistory struct {
	subjects []string
	asked    []pipeline.Plan
}

func (h *fixedHistory) HistorySubjects(_ context.Context, plan pipeline.Plan) ([]string, error) {
	h.asked = append(h.asked, plan)
	return h.subjects, nil
}

// 有 observed 成员、预算刚好装下“只算本期标的”的分块时：选块把历史截面标的计入并收窄，计划带上同一份名单，
// 管线按同样的序列数预检不会超限；只有 current 成员时不问历史标的、分块不变。
func TestChunkCountsObservedHistorySubjects(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	subjects := make([]string, 10)
	history := &fixedHistory{}
	for i := range subjects {
		subjects[i] = fmt.Sprintf("S%02d", i)
		history.subjects = append(history.subjects, fmt.Sprintf("OLD%02d", i))
	}
	observed := domain.MustDefinition("obs", "obs", "cross_section_history: observed\noutput: ts_mean(cs_rank(close), 3)\n")
	observed.AllowPartialUniverse = true
	current := domain.MustDefinition("cur", "cur", "output: ts_mean(cs_rank(close), 3)\n")
	current.AllowPartialUniverse = true
	selection := func(def domain.FactorDef) Selection {
		return Selection{Set: domain.FactorTask{TaskID: "ftask", SpaceID: "crypto", SourceDatasetID: "bars", Freq: "1m", Status: domain.TaskStatusEnabled},
			Members: domain.AsMembers(def), Subjects: subjects, Columns: []string{"close"}}
	}
	budget, err := pipeline.EstimateChunkMemory(selection(observed).Members, len(subjects), 6, 1)
	require.NoError(t, err)
	window := Window{Start: start, End: start.Add(6 * time.Minute)}

	runner := &planRecorder{}
	executor := NewExecutor(runner, WithChunkPeriods(6), WithMemoryBudget(budget.Total()), WithHistory(history))
	var records []progressRecord
	require.NoError(t, executor.Run(context.Background(), window, fixedSource(selection(observed)), recordProgress(&records, 0)))
	require.Greater(t, len(runner.plans), 1, "计入历史标的后分块被收窄")
	first := runner.plans[0]
	require.Equal(t, history.subjects, first.HistorySubjects)
	periods := int(first.TargetEnd.Sub(first.TargetStart) / time.Minute)
	require.Less(t, periods, 6)
	require.NoError(t, pipeline.CheckChunkMemory(first.Members, len(subjects)+len(first.HistorySubjects), periods, 1, budget.Total()),
		"管线的内存预检与选块用同样的序列数，不会整块超限")

	plain := &planRecorder{}
	history.asked = nil
	executor = NewExecutor(plain, WithChunkPeriods(6), WithMemoryBudget(budget.Total()), WithHistory(history))
	records = nil
	require.NoError(t, executor.Run(context.Background(), window, fixedSource(selection(current)), recordProgress(&records, 0)))
	require.Len(t, plain.plans, 1)
	require.Nil(t, plain.plans[0].HistorySubjects)
	require.Empty(t, history.asked)
}
