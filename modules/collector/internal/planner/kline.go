package planner

import (
	"context"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs"
)

func buildTaskSpecs(ctx context.Context, rule *domain.TaskRule, params *domain.CollectParams, subjects []domain.DatasetSubject) ([]domain.TaskSpec, error) {
	return jobs.BuildTaskSpecs(ctx, rule, params, subjects)
}
