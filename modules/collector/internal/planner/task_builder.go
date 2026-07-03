// Package planner turns Collector rules and data objects into task instances.
package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
)

// TaskBuilder builds complete task instances from adapter task specs.
type TaskBuilder struct{}

// NewTaskBuilder creates a builder.
func NewTaskBuilder() *TaskBuilder {
	return &TaskBuilder{}
}

// BuildDatasetDrivenInstances builds idempotent task instances for dataset subjects.
func (b *TaskBuilder) BuildDatasetDrivenInstances(ctx context.Context, rule *domain.TaskRule, subjects []domain.DatasetSubject) ([]domain.TaskInstance, error) {
	if rule == nil {
		return nil, fmt.Errorf("rule is required")
	}
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Exchange, rule.DataType)
	if err != nil {
		return nil, err
	}
	if params.Source.Kind != "dataset_subjects" {
		return nil, fmt.Errorf("unsupported collect source kind: %s", params.Source.Kind)
	}
	return b.BuildInstancesWithParams(ctx, rule, params, subjects)
}

// BuildInstances builds idempotent task instances for a rule.
func (b *TaskBuilder) BuildInstances(ctx context.Context, rule *domain.TaskRule, subjects []domain.DatasetSubject) ([]domain.TaskInstance, error) {
	if rule == nil {
		return nil, fmt.Errorf("rule is required")
	}
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Exchange, rule.DataType)
	if err != nil {
		return nil, err
	}
	return b.BuildInstancesWithParams(ctx, rule, params, subjects)
}

// BuildInstancesWithParams builds idempotent task instances from already-normalized params.
func (b *TaskBuilder) BuildInstancesWithParams(ctx context.Context, rule *domain.TaskRule, params *domain.CollectParams, subjects []domain.DatasetSubject) ([]domain.TaskInstance, error) {
	if rule == nil {
		return nil, fmt.Errorf("rule is required")
	}
	if params == nil {
		return nil, fmt.Errorf("collect params are required")
	}
	if params.Source.Kind == "dataset_subjects" && len(subjects) == 0 {
		return []domain.TaskInstance{}, nil
	}
	sortSubjects(subjects)
	specs, err := buildTaskSpecs(ctx, rule, params, subjects)
	if err != nil {
		return nil, err
	}
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].SubjectID != specs[j].SubjectID {
			return specs[i].SubjectID < specs[j].SubjectID
		}
		return specs[i].Interval < specs[j].Interval
	})
	now := time.Now().UTC()
	instances := make([]domain.TaskInstance, 0, len(specs))
	for _, spec := range specs {
		paramsJSON, err := json.Marshal(spec.Params)
		if err != nil {
			return nil, fmt.Errorf("marshal task params: %w", err)
		}
		instances = append(instances, domain.TaskInstance{
			SpaceID:        rule.SpaceID,
			TaskID:         domain.StableTaskID(rule.SpaceID, rule.RuleID, spec),
			RuleID:         rule.RuleID,
			Exchange:       spec.Exchange,
			Market:         spec.Market,
			DataType:       spec.DataType,
			DatasetID:      spec.DatasetID,
			SubjectID:      spec.SubjectID,
			Symbol:         spec.Symbol,
			Interval:       spec.Interval,
			LastExecStatus: domain.InstanceStatusPending,
			TaskParams:     string(paramsJSON),
			Result:         "{}",
			IsDeleted:      false,
			CreateTime:     now,
			ModifyTime:     now,
		})
	}
	return instances, nil
}

func sortSubjects(subjects []domain.DatasetSubject) {
	sort.Slice(subjects, func(i, j int) bool {
		return subjects[i].SubjectID < subjects[j].SubjectID
	})
}
