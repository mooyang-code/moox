package trigger

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
)

// TaskLocator resolves an enabled set and its current exact event filters.
// TaskLocator 找到要计算的任务。同一个源数据集可以有多个任务（各策略实例的私有任务），周期完成事件分给其中每一个。
type TaskLocator interface {
	// EnabledSetsByDataset 返回源数据集上启用的全部任务（含成员与结果数据集是否就绪）。
	EnabledSetsByDataset(ctx context.Context, spaceID, datasetID, freq string) ([]domain.EngineTask, error)
	// EnabledTask 按任务 ID 取启用的任务；没有时 found 为 false。
	EnabledTask(ctx context.Context, taskID string) (set domain.EngineTask, found bool, err error)
	FilterSubjects(context.Context) ([]string, error)
}

// ParamSelector 在开算一期之前取得任务各可变成员用的参数（成员键 → 快照）；固定成员和没有提交值的可变成员不在结果里，按成员自己的参数计算。
type ParamSelector interface {
	Select(ctx context.Context, task domain.FactorTask, members []domain.MemberDef, period time.Time) (map[string]domain.ParamSnapshot, error)
}

type PeriodStore interface {
	ComputedExists(context.Context, string, string, string, int64) (bool, error)
	DatasetColumns(context.Context, string, string) ([]string, error)
}

type PipelineRunner interface {
	Run(context.Context, pipeline.Plan) (pipeline.Outcome, error)
}

type TaskLocks interface {
	LockContext(context.Context, string) (func(), error)
}

// NormalizeFilters trims, de-duplicates and sorts subject filters.
func NormalizeFilters(filters []string) []string {
	seen := make(map[string]struct{}, len(filters))
	out := make([]string, 0, len(filters))
	for _, filter := range filters {
		filter = strings.TrimSpace(filter)
		if filter == "" {
			continue
		}
		if _, ok := seen[filter]; ok {
			continue
		}
		seen[filter] = struct{}{}
		out = append(out, filter)
	}
	sort.Strings(out)
	return out
}
