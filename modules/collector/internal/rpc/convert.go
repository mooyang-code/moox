package rpc

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"google.golang.org/protobuf/types/known/structpb"
)

func toPBRule(rule domain.TaskRule) *pb.TaskRule {
	enabled := rule.Enabled
	return &pb.TaskRule{
		SpaceId:       rule.SpaceID,
		RuleId:        rule.RuleID,
		DataType:      rule.DataType,
		Provider:      rule.Provider,
		MarketType:    rule.MarketType,
		CollectParams: structFromJSONString(rule.CollectParams),
		Enabled:       &enabled,
		Creator:       rule.Creator,
		CreateTime:    formatTime(rule.CreateTime),
		ModifyTime:    formatTime(rule.ModifyTime),
		PrepareState:  string(rule.PrepareState),
		LastError:     rule.LastError,
	}
}

func fromPBRule(rule *pb.TaskRule) domain.TaskRule {
	if rule == nil {
		return domain.TaskRule{}
	}
	return domain.TaskRule{
		SpaceID:       rule.GetSpaceId(),
		RuleID:        rule.GetRuleId(),
		DataType:      rule.GetDataType(),
		Provider:      rule.GetProvider(),
		MarketType:    rule.GetMarketType(),
		CollectParams: jsonStringFromStruct(rule.GetCollectParams()),
		Enabled:       taskRuleEnabled(rule),
		Creator:       rule.GetCreator(),
		PrepareState:  domain.TaskRulePrepareState(rule.GetPrepareState()),
		LastError:     rule.GetLastError(),
	}
}

func taskRuleEnabled(rule *pb.TaskRule) bool {
	if rule.Enabled == nil {
		return true
	}
	return rule.GetEnabled()
}

func toPBInstance(instance domain.TaskInstance) *pb.TaskInstance {
	return &pb.TaskInstance{
		SpaceId:        instance.SpaceID,
		TaskId:         instance.TaskID,
		RuleId:         instance.RuleID,
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
