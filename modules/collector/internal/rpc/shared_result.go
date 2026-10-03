package rpc

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
)

func builtinSharedResultIDsForTask(task domain.CollectionTask, params *domain.CollectParams) (taskresult.IDs, bool, error) {
	if !taskresult.IsBuiltinSharedResultTask(task.SpaceID, task.TaskID) {
		return taskresult.IDs{}, false, nil
	}
	if params == nil {
		return taskresult.IDs{}, false, fmt.Errorf("%w: shared result task parameters are missing", taskresult.ErrResultContract)
	}
	ids, allowed := taskresult.BuiltinSharedResultIDs(task.SpaceID, task.TaskID, task.DataType, params.Frequency, task.TagIDs)
	if !allowed || task.ResultDatasetID != ids.DatasetID || task.ResultViewID != ids.ViewID || params.TargetDatasetID != ids.DatasetID {
		return taskresult.IDs{}, false, fmt.Errorf("%w: task %s/%s has an invalid shared result identity", taskresult.ErrResultContract, task.SpaceID, task.TaskID)
	}
	return ids, true, nil
}

func inspectCollectionTaskResult(ctx context.Context, manager *taskresult.Manager, task domain.CollectionTask, ids taskresult.IDs) (taskresult.Inspection, error) {
	if !taskresult.IsBuiltinSharedResultTask(task.SpaceID, task.TaskID) {
		return manager.InspectIDs(ctx, task.SpaceID, task.TaskID, ids)
	}
	params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
	if err != nil {
		return taskresult.Inspection{}, fmt.Errorf("%w: parse shared result task parameters: %v", taskresult.ErrResultContract, err)
	}
	sharedIDs, shared, err := builtinSharedResultIDsForTask(task, params)
	if err != nil {
		return taskresult.Inspection{}, err
	}
	if !shared || ids != sharedIDs {
		return taskresult.Inspection{}, fmt.Errorf("%w: task %s/%s requested a noncanonical shared result identity", taskresult.ErrResultContract, task.SpaceID, task.TaskID)
	}
	return manager.InspectBuiltinSharedIDs(ctx, task.SpaceID, task.TaskID, task.DataType, params.Frequency, task.TagIDs, ids)
}
