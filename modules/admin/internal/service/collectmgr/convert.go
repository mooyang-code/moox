package collectmgr

import (
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

// ========== model ↔ PB 转换 ==========
//
// service 实现内部一次性转换，dao 层仍返回 model（带 gorm tag）。
// 时间字段在 PB 中以 RFC3339 字符串表达。

// ----- 任务规则 -----

// taskRuleModelToPB 将 model.CollectorTaskRules 转为 pb.TaskRule。
func taskRuleModelToPB(r *model.CollectorTaskRules) *pb.TaskRule {
	if r == nil {
		return nil
	}
	return &pb.TaskRule{
		Id:             int32(r.ID),
		SpaceId:        r.SpaceID,
		RuleId:         r.RuleID,
		BizType:        r.BizType,
		DataType:       r.DataType,
		DataSource:     r.DataSource,
		CollectParams:  r.CollectParams,
		AssignmentType: r.AssignmentType,
		AssignedNodes:  r.AssignedNodes,
		NodePattern:    r.NodePattern,
		NodeTags:       r.NodeTags,
		Enabled:        r.Enabled,
		Creator:        r.Creator,
		CreateTime:     formatTime(r.CreateTime),
		ModifyTime:     formatTime(r.ModifyTime),
	}
}

// taskRulePBToModel 将 pb.TaskRule 转为 model.CollectorTaskRules（用于写入 DB）。
func taskRulePBToModel(r *pb.TaskRule) *model.CollectorTaskRules {
	if r == nil {
		return nil
	}
	return &model.CollectorTaskRules{
		ID:             int(r.GetId()),
		SpaceID:        r.GetSpaceId(),
		RuleID:         r.GetRuleId(),
		BizType:        r.GetBizType(),
		DataType:       r.GetDataType(),
		DataSource:     r.GetDataSource(),
		CollectParams:  r.GetCollectParams(),
		AssignmentType: r.GetAssignmentType(),
		AssignedNodes:  r.GetAssignedNodes(),
		NodePattern:    r.GetNodePattern(),
		NodeTags:       r.GetNodeTags(),
		Enabled:        r.GetEnabled(),
		Creator:        r.GetCreator(),
	}
}

// ----- 任务实例 -----

// taskInstanceModelToPB 将 model.CollectorTaskInstance 转为 pb.TaskInstance。
func taskInstanceModelToPB(i *model.CollectorTaskInstance) *pb.TaskInstance {
	if i == nil {
		return nil
	}
	return &pb.TaskInstance{
		Id:              int32(i.ID),
		SpaceId:         i.SpaceID,
		TaskId:          i.TaskID,
		RuleId:          i.RuleID,
		BizType:         i.BizType,
		PlannedExecNode: i.PlannedExecNode,
		LastExecNode:    i.LastExecNode,
		LastExecStatus:  int32(i.LastExecStatus),
		Symbol:          i.Symbol,
		CollectDataType: i.CollectDataType,
		TaskParams:      i.TaskParams,
		LastExecTime:    formatTimePtr(i.LastExecTime),
		Result:          i.Result,
		IsDeleted:       i.IsDeleted,
		CreateTime:      formatTime(i.CreateTime),
		ModifyTime:      formatTime(i.ModifyTime),
	}
}

// taskInstancePBToModel 将 pb.TaskInstance 转为 model.CollectorTaskInstance（用于写入 DB）。
func taskInstancePBToModel(i *pb.TaskInstance) *model.CollectorTaskInstance {
	if i == nil {
		return nil
	}
	return &model.CollectorTaskInstance{
		ID:              int(i.GetId()),
		SpaceID:         i.GetSpaceId(),
		TaskID:          i.GetTaskId(),
		RuleID:          i.GetRuleId(),
		BizType:         i.GetBizType(),
		PlannedExecNode: i.GetPlannedExecNode(),
		LastExecNode:    i.GetLastExecNode(),
		LastExecStatus:  int(i.GetLastExecStatus()),
		Symbol:          i.GetSymbol(),
		CollectDataType: i.GetCollectDataType(),
		TaskParams:      i.GetTaskParams(),
		Result:          i.GetResult(),
	}
}

// ----- 数据类型配置 -----

// dataTypeConfigModelToPB 将 model.CollectorDataTypeConfig 转为 pb.DataTypeConfig。
func dataTypeConfigModelToPB(c *model.CollectorDataTypeConfig) *pb.DataTypeConfig {
	if c == nil {
		return nil
	}
	return &pb.DataTypeConfig{
		Id:                int32(c.ID),
		DataType:          c.DataType,
		TypeName:          c.TypeName,
		TypeDesc:          c.TypeDesc,
		DataSourceOptions: c.DataSourceOptions,
		SortOrder:         int32(c.SortOrder),
		Version:           int32(c.Version),
		CreateTime:        formatTime(c.CreateTime),
		ModifyTime:        formatTime(c.ModifyTime),
	}
}

// fieldConfigModelToPB 将 model.CollectorFieldConfig 转为 pb.DataTypeFieldConfig。
func fieldConfigModelToPB(f *model.CollectorFieldConfig) *pb.DataTypeFieldConfig {
	if f == nil {
		return nil
	}
	return &pb.DataTypeFieldConfig{
		Id:                int32(f.ID),
		DataType:          f.DataType,
		FieldKey:          f.FieldKey,
		FieldName:         f.FieldName,
		FieldType:         f.FieldType,
		IsRequired:        f.IsRequired,
		DefaultValue:      f.DefaultValue,
		FieldOptions:      f.FieldOptions,
		DataSourceOptions: f.DataSourceOptions,
		SortOrder:         int32(f.SortOrder),
		CreateTime:        formatTime(f.CreateTime),
		ModifyTime:        formatTime(f.ModifyTime),
	}
}

// ----- 时间格式化 -----

// formatTime 格式化 time.Time 为 RFC3339 字符串，零值返回空串。
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// formatTimePtr 格式化 *time.Time 为 RFC3339 字符串，nil/零值返回空串。
func formatTimePtr(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
