package resample

import (
	"context"
	"encoding/json"
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
	ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error)
}

// PlanTask expands a ready task into one durable TaskInstance per active source
// subject. It is idempotent and never deletes target data for removed subjects.
func PlanTask(ctx context.Context, source subjectSource, instances *store.TaskInstanceRepository, task domain.CollectionTask, now time.Time) error {
	if source == nil || instances == nil {
		return fmt.Errorf("resample planner dependencies are required")
	}
	params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
	if err != nil {
		return err
	}
	resultIDs := taskresult.PersistedResultIDs(task.SpaceID, task.TaskID, task.ResultDatasetID, task.ResultViewID)
	params.TargetDatasetID = resultIDs.DatasetID
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
	subjects, err := source.ResolveSubjects(ctx, task.SpaceID, info.SubjectTags)
	if err != nil {
		return fmt.Errorf("list resample source subjects: %w", err)
	}
	target, _ := ParseFixedFrequency(params.TargetFrequency)
	start, _ := BucketAt(now.UTC().Add(-params.SettleDelay()), time.Unix(0, 0).UTC(), target)
	instancesToWrite := make([]domain.TaskInstance, 0, len(subjects))
	targetsToWrite := make([]domain.WriteTarget, 0, len(subjects))
	activeIDs := make([]string, 0, len(subjects))
	outputFields, _ := json.Marshal(params.OutputFields)
	for _, subject := range subjects {
		if strings.TrimSpace(subject.SubjectID) == "" {
			continue
		}
		spec := domain.TaskSpec{Provider: params.Provider, MarketType: params.MarketType, DataType: "kline_resample", SubjectID: subject.SubjectID, Frequency: target.Storage}
		instanceID := domain.StableResampleTaskID(task.SpaceID, task.TaskID, spec, params.SourceSeriesTag)
		result := domain.NewResampleTaskResult(start)
		encoded, marshalErr := result.Marshal()
		if marshalErr != nil {
			return marshalErr
		}
		instancesToWrite = append(instancesToWrite, domain.TaskInstance{SpaceID: task.SpaceID, InstanceID: instanceID, Provider: params.Provider, MarketType: params.MarketType, DataType: "kline_resample", SubjectID: subject.SubjectID, Frequency: target.Storage, SeriesTag: params.SourceSeriesTag, FunctionName: localResampleFunction, LastExecStatus: domain.InstanceStatusPending, TaskParams: taskParams, Result: encoded})
		targetsToWrite = append(targetsToWrite, domain.WriteTarget{
			ID: "wt_" + instanceID, SpaceID: task.SpaceID, InstanceID: instanceID, TaskID: task.TaskID,
			DatasetID: params.TargetDatasetID, ViewID: resultIDs.ViewID, OutputFields: string(outputFields), Status: "pending",
		})
		activeIDs = append(activeIDs, instanceID)
	}
	if err := instances.UpsertMany(ctx, instancesToWrite); err != nil {
		return fmt.Errorf("upsert resample task instances: %w", err)
	}
	if err := instances.UpsertWriteTargets(ctx, targetsToWrite); err != nil {
		return fmt.Errorf("upsert resample write targets: %w", err)
	}
	return instances.DeactivateMissingResampleTaskInstances(ctx, task.SpaceID, task.TaskID, activeIDs)
}
