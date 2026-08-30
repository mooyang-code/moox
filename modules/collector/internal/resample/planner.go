package resample

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
)

const localResampleFunction = "collector_local_resample"

type subjectSource interface {
	GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error)
	ListSubjects(context.Context, string, string, string) ([]domain.DatasetSubject, error)
}

type resampleSubjectSource interface {
	ListResampleSubjects(context.Context, string, string) ([]domain.DatasetSubject, error)
}

type resampleRuleSubjectSource interface {
	ListResampleSubjectsForRule(context.Context, string, string, string, string) ([]domain.DatasetSubject, error)
}

// PlanRule expands a ready rule into one durable TaskInstance per active source
// subject. It is idempotent and never deletes target data for removed subjects.
func PlanRule(ctx context.Context, source subjectSource, instances *store.TaskInstanceRepository, rule domain.TaskRule, now time.Time) error {
	if source == nil || instances == nil {
		return fmt.Errorf("resample planner dependencies are required")
	}
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
	if err != nil {
		return err
	}
	if err := params.Validate(); err != nil {
		return err
	}
	info, err := source.GetDataset(ctx, rule.SpaceID, params.SourceDatasetID)
	if err != nil {
		return fmt.Errorf("get resample source Dataset: %w", err)
	}
	var subjects []domain.DatasetSubject
	if sourceWithRuleSet, ok := source.(resampleRuleSubjectSource); ok {
		subjects, err = sourceWithRuleSet.ListResampleSubjectsForRule(ctx, rule.SpaceID, params.SourceDatasetID, rule.Provider, params.SourceSeriesTag)
	} else if sourceWithNativeSet, ok := source.(resampleSubjectSource); ok {
		subjects, err = sourceWithNativeSet.ListResampleSubjects(ctx, rule.SpaceID, params.SourceDatasetID)
	} else {
		subjects, err = source.ListSubjects(ctx, rule.SpaceID, params.SourceDatasetID, info.DataSourceID)
	}
	if err != nil {
		return fmt.Errorf("list resample source subjects: %w", err)
	}
	target, _ := ParseFixedFrequency(params.TargetFrequency)
	start, _ := BucketAt(now.UTC().Add(-params.SettleDelay()), time.Unix(0, 0).UTC(), target)
	instancesToWrite := make([]domain.TaskInstance, 0, len(subjects))
	activeIDs := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		if strings.TrimSpace(subject.SubjectID) == "" || (strings.TrimSpace(subject.Status) != "" && !strings.EqualFold(subject.Status, "active")) {
			continue
		}
		spec := domain.TaskSpec{Provider: rule.Provider, MarketType: rule.MarketType, DataType: "kline_resample", DatasetID: params.TargetDatasetID, SubjectID: subject.SubjectID, Frequency: target.Storage}
		taskID := domain.StableResampleTaskID(rule.SpaceID, rule.RuleID, spec, params.SourceSeriesTag)
		result := domain.NewResampleTaskResult(start)
		encoded, marshalErr := result.Marshal()
		if marshalErr != nil {
			return marshalErr
		}
		instancesToWrite = append(instancesToWrite, domain.TaskInstance{SpaceID: rule.SpaceID, TaskID: taskID, RuleID: rule.RuleID, Provider: rule.Provider, MarketType: rule.MarketType, DataType: "kline_resample", DatasetID: params.TargetDatasetID, SubjectID: subject.SubjectID, Frequency: target.Storage, FunctionName: localResampleFunction, LastExecStatus: domain.InstanceStatusPending, TaskParams: rule.CollectParams, Result: encoded})
		activeIDs = append(activeIDs, taskID)
	}
	if err := instances.UpsertMany(ctx, instancesToWrite); err != nil {
		return fmt.Errorf("upsert resample task instances: %w", err)
	}
	return instances.DeactivateMissingResampleRuleInstances(ctx, rule.SpaceID, rule.RuleID, activeIDs)
}
