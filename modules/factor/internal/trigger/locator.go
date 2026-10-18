package trigger

import (
	"context"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
)

// SetLocator resolves an enabled set and its current exact event filters.
type SetLocator interface {
	EnabledSetByDataset(context.Context, string, string, string) (domain.FactorSet, []domain.FactorDef, bool, error)
	FilterSubjects(context.Context) ([]string, error)
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
