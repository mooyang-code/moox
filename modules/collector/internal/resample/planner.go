package resample

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
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

type resampleTaskSubjectSource interface {
	ListResampleSubjectsForTask(context.Context, string, string, string, string) ([]domain.DatasetSubject, error)
}

// PlanTask expands a ready task into one durable TaskInstance per active source
// subject. It is idempotent and never deletes target data for removed subjects.
func PlanTask(ctx context.Context, source subjectSource, instances *store.TaskInstanceRepository, task domain.CollectionTask, now time.Time) error {
	if source == nil || instances == nil {
		return fmt.Errorf("resample planner dependencies are required")
	}
	params, err := domain.ParseCollectParams(task.CollectParams, task.Provider, task.MarketType, task.DataType)
	if err != nil {
		return err
	}
	params.TargetDatasetID = taskresult.ResultIDs(task.SpaceID, task.TaskID).DatasetID
	if err := ValidateTaskParams(params); err != nil {
		return err
	}
	taskParams := task.CollectParams
	if canonical, canonicalErr := params.CanonicalJSON(); canonicalErr != nil {
		return canonicalErr
	} else {
		taskParams = canonical
	}
	info, err := source.GetDataset(ctx, task.SpaceID, params.SourceDatasetID)
	if err != nil {
		return fmt.Errorf("get resample source Dataset: %w", err)
	}
	var subjects []domain.DatasetSubject
	if sourceWithTaskSet, ok := source.(resampleTaskSubjectSource); ok {
		subjects, err = sourceWithTaskSet.ListResampleSubjectsForTask(ctx, task.SpaceID, params.SourceDatasetID, task.Provider, params.SourceSeriesTag)
	} else if sourceWithNativeSet, ok := source.(resampleSubjectSource); ok {
		subjects, err = sourceWithNativeSet.ListResampleSubjects(ctx, task.SpaceID, params.SourceDatasetID)
	} else {
		subjects, err = source.ListSubjects(ctx, task.SpaceID, params.SourceDatasetID, info.DataSourceID)
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
		spec := domain.TaskSpec{Provider: task.Provider, MarketType: task.MarketType, DataType: "kline_resample", DatasetID: params.TargetDatasetID, SubjectID: subject.SubjectID, Frequency: target.Storage}
		instanceID := domain.StableResampleTaskID(task.SpaceID, task.TaskID, spec, params.SourceSeriesTag)
		result := domain.NewResampleTaskResult(start)
		encoded, marshalErr := result.Marshal()
		if marshalErr != nil {
			return marshalErr
		}
		instancesToWrite = append(instancesToWrite, domain.TaskInstance{SpaceID: task.SpaceID, InstanceID: instanceID, CollectionTaskID: task.TaskID, Provider: task.Provider, MarketType: task.MarketType, DataType: "kline_resample", DatasetID: params.TargetDatasetID, SubjectID: subject.SubjectID, Frequency: target.Storage, FunctionName: localResampleFunction, LastExecStatus: domain.InstanceStatusPending, TaskParams: taskParams, Result: encoded})
		activeIDs = append(activeIDs, instanceID)
	}
	if err := instances.UpsertMany(ctx, instancesToWrite); err != nil {
		return fmt.Errorf("upsert resample task instances: %w", err)
	}
	return instances.DeactivateMissingResampleTaskInstances(ctx, task.SpaceID, task.TaskID, activeIDs)
}
