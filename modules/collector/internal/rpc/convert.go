package rpc

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"google.golang.org/protobuf/types/known/structpb"
)

func toPBTask(task domain.CollectionTask) *pb.CollectionTask {
	enabled := task.Enabled
	resultStatus := "active"
	switch task.PrepareState {
	case domain.PrepareStatePending, domain.PrepareStateWaitingView:
		resultStatus = "pending"
	case domain.PrepareStateError:
		resultStatus = "error"
	}
	return &pb.CollectionTask{
		SpaceId:       task.SpaceID,
		TaskId:        task.TaskID,
		TaskName:      task.TaskName,
		Description:   task.Description,
		DataType:      task.DataType,
		Provider:      task.Provider,
		MarketType:    task.MarketType,
		CollectParams: structFromJSONString(redactTaskResultDatasetID(task.CollectParams)),
		Enabled:       &enabled,
		Creator:       task.Creator,
		CreateTime:    formatTime(task.CreateTime),
		ModifyTime:    formatTime(task.ModifyTime),
		PrepareState:  string(task.PrepareState),
		LastError:     task.LastError,
		Result:        &pb.TaskResult{ResultName: resultName(task.TaskName), ViewId: task.ResultViewID, Status: resultStatus, DataKind: taskResultDataKind(task.DataType)},
	}
}

func redactTaskResultDatasetID(raw string) string {
	values := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return raw
	}
	for _, key := range []string{"target_dataset_id", "result_dataset_id", "symbol_dataset_id", "source_dataset_id"} {
		delete(values, key)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return raw
	}
	return string(encoded)
}

func fromPBTask(task *pb.CollectionTask) domain.CollectionTask {
	if task == nil {
		return domain.CollectionTask{}
	}
	return domain.CollectionTask{
		SpaceID:       task.GetSpaceId(),
		TaskID:        task.GetTaskId(),
		TaskName:      task.GetTaskName(),
		Description:   task.GetDescription(),
		DataType:      task.GetDataType(),
		Provider:      task.GetProvider(),
		MarketType:    task.GetMarketType(),
		CollectParams: jsonStringFromStruct(task.GetCollectParams()),
		Enabled:       taskEnabled(task),
		Creator:       task.GetCreator(),
		PrepareState:  domain.CollectionTaskPrepareState(task.GetPrepareState()),
		LastError:     task.GetLastError(),
	}
}

func taskEnabled(task *pb.CollectionTask) bool {
	if task == nil || task.Enabled == nil {
		return true
	}
	return task.GetEnabled()
}

func resultName(taskName string) string {
	name := strings.TrimSpace(taskName)
	if name == "" {
		return "采集结果"
	}
	return name + " 结果"
}

func taskResultDataKind(dataType string) string {
	if strings.EqualFold(strings.TrimSpace(dataType), "instrument") || strings.EqualFold(strings.TrimSpace(dataType), "symbol") {
		return "record"
	}
	return "time_series"
}

func toPBInstance(instance domain.TaskInstance) *pb.TaskInstance {
	return &pb.TaskInstance{
		SpaceId:        instance.SpaceID,
		TaskId:         instance.CollectionTaskID,
		InstanceId:     instance.InstanceID,
		Provider:       instance.Provider,
		MarketType:     instance.MarketType,
		DataType:       instance.DataType,
		DatasetId:      instance.DatasetID,
		SubjectId:      instance.SubjectID,
		Frequency:      instance.Frequency,
		SourceId:       instance.SourceID,
		FunctionName:   instance.FunctionName,
		TaskParams:     structFromJSONString(instance.TaskParams),
		LastExecStatus: toPBStatus(instance.LastExecStatus),
		LastExecTime:   formatPtrTime(instance.LastExecTime),
		Result:         structFromJSONString(instance.Result),
		IsDeleted:      instance.IsDeleted,
		CreateTime:     formatTime(instance.CreateTime),
		ModifyTime:     formatTime(instance.ModifyTime),
	}
}

func toPBStatus(status int) pb.TaskInstanceStatus {
	switch status {
	case domain.InstanceStatusPending:
		return pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_PENDING
	case domain.InstanceStatusSuccess:
		return pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_SUCCESS
	case domain.InstanceStatusFailed:
		return pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_FAILED
	default:
		return pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_UNSPECIFIED
	}
}

func fromPBStatus(status pb.TaskInstanceStatus) int {
	switch status {
	case pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_PENDING:
		return domain.InstanceStatusPending
	case pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_SUCCESS:
		return domain.InstanceStatusSuccess
	case pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_FAILED:
		return domain.InstanceStatusFailed
	default:
		return 0
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func formatPtrTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return formatTime(*t)
}

func structFromJSONString(raw string) *structpb.Struct {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &structpb.Struct{Fields: map[string]*structpb.Value{}}
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		out, _ := structpb.NewStruct(map[string]any{"raw": raw})
		return out
	}
	if fields, ok := decoded.(map[string]any); ok {
		out, err := structpb.NewStruct(fields)
		if err == nil {
			return out
		}
	}
	out, _ := structpb.NewStruct(map[string]any{"value": decoded})
	return out
}

func jsonStringFromStruct(value *structpb.Struct) string {
	if value == nil {
		return "{}"
	}
	raw, err := json.Marshal(value.AsMap())
	if err != nil {
		return "{}"
	}
	return string(raw)
}
