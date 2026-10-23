package trigger

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
)

// SetLocator resolves an enabled set and its current exact event filters.
// SetLocator 找到要计算的集合。同一个源数据集可以有多个集合（各策略实例的私有集合），周期完成事件分给其中每一个。
type SetLocator interface {
	// EnabledSetsByDataset 返回源数据集上启用的全部集合（含成员与结果数据集是否就绪）。
	EnabledSetsByDataset(ctx context.Context, spaceID, datasetID, freq string) ([]domain.EngineSet, error)
	// EnabledSet 按集合 ID 取启用的集合；没有时 found 为 false。
	EnabledSet(ctx context.Context, setID string) (set domain.EngineSet, found bool, err error)
	FilterSubjects(context.Context) ([]string, error)
}

// ParamFreezer 在开算一期之前冻结集合各成员的参数（别名 → 冻结记录）。
type ParamFreezer interface {
	Freeze(ctx context.Context, set domain.FactorSet, members []domain.MemberDef, period time.Time) (map[string]domain.ParamFreeze, error)
}

type PeriodStore interface {
	ComputedExists(context.Context, string, string, string, int64) (bool, error)
	DatasetColumns(context.Context, string, string) ([]string, error)
}

type PipelineRunner interface {
	Run(context.Context, pipeline.Plan) (pipeline.Outcome, error)
}

type SetLocks interface {
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
