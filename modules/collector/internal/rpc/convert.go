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
		SpaceId:        task.SpaceID,
		TaskId:         task.TaskID,
		TaskName:       task.TaskName,
		Description:    task.Description,
		DataType:       task.DataType,
		Ingest:         task.Ingest,
		TagIds:         append([]string(nil), task.TagIDs...),
		DefinitionHash: task.DefinitionHash,
		CollectParams:  structFromJSONString(task.CollectParams),
		Enabled:        &enabled,
		Creator:        task.Creator,
		CreatedAt:      unixMillis(task.CreateTime),
		UpdatedAt:      unixMillis(task.ModifyTime),
		PrepareState:   string(task.PrepareState),
		LastError:      task.LastError,
		Result:         &pb.TaskResult{ResultName: resultName(task.TaskName), DatasetId: task.ResultDatasetID, Status: resultStatus, DataKind: taskResultDataKind(task.DataType)},
	}
}

func fromPBTask(task *pb.CollectionTask) domain.CollectionTask {
	if task == nil {
		return domain.CollectionTask{}
	}
	return domain.CollectionTask{
		SpaceID:        task.GetSpaceId(),
		TaskID:         task.GetTaskId(),
		TaskName:       task.GetTaskName(),
		Description:    task.GetDescription(),
		DataType:       task.GetDataType(),
		Ingest:         strings.ToLower(strings.TrimSpace(task.GetIngest())),
		TagIDs:         append([]string(nil), task.GetTagIds()...),
		DefinitionHash: task.GetDefinitionHash(),
		CollectParams:  jsonStringFromStruct(task.GetCollectParams()),
		Enabled:        taskEnabled(task),
		Creator:        task.GetCreator(),
		PrepareState:   domain.CollectionTaskPrepareState(task.GetPrepareState()),
		LastError:      task.GetLastError(),
	}
}

func stringSliceJSON(values []string) string {
	b, _ := json.Marshal(values)
	return string(b)
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
	_ = dataType
	return "time_series"
}

// unixMillis 把墙钟时刻换成接口上的 Unix 毫秒；零值时间得 0（表示没有）。
func unixMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
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
