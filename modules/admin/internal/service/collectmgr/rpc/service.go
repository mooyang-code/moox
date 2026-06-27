// Package rpc 提供 collectmgr 对外的 trpc 普通 RPC 服务实现，
// 由统一 HTTP 转发层（/api/admin/collectmgr/{method}）调度。
package rpc

import (
	"context"

	"github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

	"trpc.group/trpc-go/trpc-go/log"
)

// Service 实现 pb.CollectMgrService，承载 collectmgr 业务逻辑。
// 内部组合 4 个子 service，space_id 由网关从 X-Space-Id 注入到 ctx，业务层从 ctx 读取。
//
// 设计说明：service 层已直接使用 PB 类型，rpc 层仅为薄包装——
// 调用 service 方法并补充 common.RetInfo，不再做 DTO↔PB 翻译。
type Service struct {
	pb.UnimplementedCollectMgr
	taskRuleSvc     collectmgr.TaskRuleService
	taskInstanceSvc collectmgr.TaskInstanceService
	dataTypeCfgSvc  collectmgr.DataTypeConfigService
	taskPlannerSvc  collectmgr.TaskPlannerService
}

// NewService 创建 CollectMgr RPC 实现。
func NewService(
	taskRuleSvc collectmgr.TaskRuleService,
	taskInstanceSvc collectmgr.TaskInstanceService,
	dataTypeCfgSvc collectmgr.DataTypeConfigService,
	taskPlannerSvc collectmgr.TaskPlannerService,
) *Service {
	return &Service{
		taskRuleSvc:     taskRuleSvc,
		taskInstanceSvc: taskInstanceSvc,
		dataTypeCfgSvc:  dataTypeCfgSvc,
		taskPlannerSvc:  taskPlannerSvc,
	}
}

// ========== 任务规则 ==========

// GetTaskRuleList 获取任务规则列表。
func (s *Service) GetTaskRuleList(ctx context.Context, req *pb.GetTaskRuleListReq) (*pb.GetTaskRuleListRsp, error) {
	rules, err := s.taskRuleSvc.GetTaskRuleList(ctx, req.GetBizType(), req.GetDataType(), req.GetDataSource(), req.GetEnabled())
	if err != nil {
		log.ErrorContextf(ctx, "[CollectMgr] GetTaskRuleList failed: %v", err)
		return &pb.GetTaskRuleListRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "查询失败")}, nil
	}
	return &pb.GetTaskRuleListRsp{
		RetInfo: retOK(),
		Rules:   rules,
		Total:   int64(len(rules)),
	}, nil
}

// GetTaskRuleDetail 获取任务规则详情。
func (s *Service) GetTaskRuleDetail(ctx context.Context, req *pb.GetTaskRuleDetailReq) (*pb.GetTaskRuleDetailRsp, error) {
	if req.GetRuleId() == "" {
		return &pb.GetTaskRuleDetailRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "rule_id 不能为空")}, nil
	}
	rule, err := s.taskRuleSvc.GetTaskRule(ctx, req.GetRuleId())
	if err != nil {
		return &pb.GetTaskRuleDetailRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "任务配置不存在")}, nil
	}
	return &pb.GetTaskRuleDetailRsp{RetInfo: retOK(), Rule: rule}, nil
}

// CreateTaskRule 创建任务规则。
func (s *Service) CreateTaskRule(ctx context.Context, req *pb.CreateTaskRuleReq) (*pb.CreateTaskRuleRsp, error) {
	rulePB := req.GetRule()
	if rulePB == nil {
		return &pb.CreateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "rule 不能为空")}, nil
	}
	ruleID, err := s.taskRuleSvc.CreateTaskRule(ctx, rulePB)
	if err != nil {
		log.ErrorContextf(ctx, "[CollectMgr] CreateTaskRule failed: %v", err)
		return &pb.CreateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "创建失败")}, nil
	}
	return &pb.CreateTaskRuleRsp{RetInfo: retOK(), RuleId: ruleID}, nil
}

// UpdateTaskRule 更新任务规则。
func (s *Service) UpdateTaskRule(ctx context.Context, req *pb.UpdateTaskRuleReq) (*pb.UpdateTaskRuleRsp, error) {
	if req.GetRuleId() == "" {
		return &pb.UpdateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "rule_id 不能为空")}, nil
	}
	rulePB := req.GetRule()
	if rulePB == nil {
		return &pb.UpdateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "rule 不能为空")}, nil
	}
	rulePB.RuleId = req.GetRuleId()
	if err := s.taskRuleSvc.UpdateTaskRule(ctx, rulePB); err != nil {
		log.ErrorContextf(ctx, "[CollectMgr] UpdateTaskRule failed: %v", err)
		return &pb.UpdateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "更新失败")}, nil
	}
	return &pb.UpdateTaskRuleRsp{RetInfo: retOK(), Rule: rulePB}, nil
}

// DisableTaskRule 关闭任务规则（设置为禁用）。
func (s *Service) DisableTaskRule(ctx context.Context, req *pb.DisableTaskRuleReq) (*pb.DisableTaskRuleRsp, error) {
	if req.GetRuleId() == "" {
		return &pb.DisableTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "rule_id 不能为空")}, nil
	}
	if err := s.taskRuleSvc.DisableTaskRule(ctx, req.GetRuleId()); err != nil {
		log.ErrorContextf(ctx, "[CollectMgr] DisableTaskRule failed: %v", err)
		return &pb.DisableTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "关闭任务规则失败")}, nil
	}
	return &pb.DisableTaskRuleRsp{RetInfo: retOK()}, nil
}

// ========== 任务实例 ==========

// GetTaskInstanceList 获取任务实例列表（分页 + 筛选）。
func (s *Service) GetTaskInstanceList(ctx context.Context, req *pb.GetTaskInstanceListReq) (*pb.GetTaskInstanceListRsp, error) {
	filter := req.GetFilter()
	if filter == nil {
		filter = &pb.TaskInstanceFilter{}
	}
	instances, total, err := s.taskInstanceSvc.ListTaskInstancesWithFilter(ctx, filter)
	if err != nil {
		log.ErrorContextf(ctx, "[CollectMgr] GetTaskInstanceList failed: %v", err)
		return &pb.GetTaskInstanceListRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "查询失败")}, nil
	}
	return &pb.GetTaskInstanceListRsp{
		RetInfo:   retOK(),
		Instances: instances,
		Total:     total,
	}, nil
}

// GetTaskInstanceListCache 带缓存的任务实例列表查询。
func (s *Service) GetTaskInstanceListCache(ctx context.Context, req *pb.GetTaskInstanceListCacheReq) (*pb.GetTaskInstanceListCacheRsp, error) {
	filter := req.GetFilter()
	if filter == nil {
		filter = &pb.TaskInstanceFilter{}
	}
	// cache 模式仅支持精确条件、有效数据，filter.IsDeleted 强制置空（不过滤）
	filter.IsDeleted = ""
	instances, total, err := s.taskInstanceSvc.GetTaskInstanceListCache(ctx, filter)
	if err != nil {
		log.ErrorContextf(ctx, "[CollectMgr] GetTaskInstanceListCache failed: %v", err)
		return &pb.GetTaskInstanceListCacheRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "查询失败")}, nil
	}
	return &pb.GetTaskInstanceListCacheRsp{
		RetInfo:   retOK(),
		Instances: instances,
		Total:     total,
	}, nil
}

// GetTaskInstanceDetail 获取任务实例详情。
func (s *Service) GetTaskInstanceDetail(ctx context.Context, req *pb.GetTaskInstanceDetailReq) (*pb.GetTaskInstanceDetailRsp, error) {
	if req.GetInstanceId() == "" {
		return &pb.GetTaskInstanceDetailRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "instance_id 不能为空")}, nil
	}
	inst, err := s.taskInstanceSvc.GetTaskInstance(ctx, req.GetInstanceId())
	if err != nil {
		return &pb.GetTaskInstanceDetailRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "任务实例不存在")}, nil
	}
	return &pb.GetTaskInstanceDetailRsp{RetInfo: retOK(), Instance: inst}, nil
}

// CreateTaskInstance 创建任务实例。
func (s *Service) CreateTaskInstance(ctx context.Context, req *pb.CreateTaskInstanceReq) (*pb.CreateTaskInstanceRsp, error) {
	instPB := req.GetInstance()
	if instPB == nil {
		return &pb.CreateTaskInstanceRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "instance 不能为空")}, nil
	}
	if err := s.taskInstanceSvc.CreateTaskInstance(ctx, instPB); err != nil {
		log.ErrorContextf(ctx, "[CollectMgr] CreateTaskInstance failed: %v", err)
		return &pb.CreateTaskInstanceRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "创建失败")}, nil
	}
	return &pb.CreateTaskInstanceRsp{RetInfo: retOK(), Instance: instPB}, nil
}

// UpdateTaskInstance 更新任务实例。
func (s *Service) UpdateTaskInstance(ctx context.Context, req *pb.UpdateTaskInstanceReq) (*pb.UpdateTaskInstanceRsp, error) {
	if req.GetInstanceId() == "" {
		return &pb.UpdateTaskInstanceRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "instance_id 不能为空")}, nil
	}
	instPB := req.GetInstance()
	if instPB == nil {
		return &pb.UpdateTaskInstanceRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "instance 不能为空")}, nil
	}
	if err := s.taskInstanceSvc.UpdateTaskInstance(ctx, req.GetInstanceId(), instPB); err != nil {
		log.ErrorContextf(ctx, "[CollectMgr] UpdateTaskInstance failed: %v", err)
		return &pb.UpdateTaskInstanceRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "更新失败")}, nil
	}
	return &pb.UpdateTaskInstanceRsp{RetInfo: retOK(), Instance: instPB}, nil
}

// DeleteTaskInstance 删除任务实例。
func (s *Service) DeleteTaskInstance(ctx context.Context, req *pb.DeleteTaskInstanceReq) (*pb.DeleteTaskInstanceRsp, error) {
	if req.GetInstanceId() == "" {
		return &pb.DeleteTaskInstanceRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "instance_id 不能为空")}, nil
	}
	if err := s.taskInstanceSvc.RemoveTaskInstance(ctx, req.GetInstanceId()); err != nil {
		log.ErrorContextf(ctx, "[CollectMgr] DeleteTaskInstance failed: %v", err)
		return &pb.DeleteTaskInstanceRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "删除失败")}, nil
	}
	return &pb.DeleteTaskInstanceRsp{RetInfo: retOK()}, nil
}

// StartTaskInstance 启动任务实例。
func (s *Service) StartTaskInstance(ctx context.Context, req *pb.StartTaskInstanceReq) (*pb.StartTaskInstanceRsp, error) {
	if req.GetInstanceId() == "" {
		return &pb.StartTaskInstanceRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "instance_id 不能为空")}, nil
	}
	if err := s.taskInstanceSvc.StartInstance(ctx, req.GetInstanceId()); err != nil {
		log.ErrorContextf(ctx, "[CollectMgr] StartTaskInstance failed: %v", err)
		return &pb.StartTaskInstanceRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "启动失败")}, nil
	}
	return &pb.StartTaskInstanceRsp{RetInfo: retOK()}, nil
}

// StopTaskInstance 停止任务实例（手动停止）。
func (s *Service) StopTaskInstance(ctx context.Context, req *pb.StopTaskInstanceReq) (*pb.StopTaskInstanceRsp, error) {
	if req.GetInstanceId() == "" {
		return &pb.StopTaskInstanceRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "instance_id 不能为空")}, nil
	}
	if err := s.taskInstanceSvc.CompleteInstance(ctx, req.GetInstanceId(), false, "手动停止"); err != nil {
		log.ErrorContextf(ctx, "[CollectMgr] StopTaskInstance failed: %v", err)
		return &pb.StopTaskInstanceRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "停止失败")}, nil
	}
	return &pb.StopTaskInstanceRsp{RetInfo: retOK()}, nil
}

// ReportTaskStatus 上报任务状态（客户端上报）。
func (s *Service) ReportTaskStatus(ctx context.Context, req *pb.ReportInstanceStatusReq) (*pb.ReportInstanceStatusRsp, error) {
	if req.GetInstanceId() == "" {
		return &pb.ReportInstanceStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "instance_id 不能为空")}, nil
	}
	if req.GetNodeId() == "" {
		return &pb.ReportInstanceStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_id 不能为空")}, nil
	}
	if err := s.taskInstanceSvc.ReportTaskStatus(ctx, req.GetInstanceId(), req.GetNodeId(), int(req.GetStatus()), req.GetResult()); err != nil {
		log.ErrorContextf(ctx, "[CollectMgr] ReportTaskStatus failed: %v", err)
		return &pb.ReportInstanceStatusRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "状态上报失败")}, nil
	}
	return &pb.ReportInstanceStatusRsp{RetInfo: retOK()}, nil
}

// InvalidateTaskInstance 作废任务实例。
func (s *Service) InvalidateTaskInstance(ctx context.Context, req *pb.InvalidateTaskInstanceReq) (*pb.InvalidateTaskInstanceRsp, error) {
	if req.GetTaskId() == "" {
		return &pb.InvalidateTaskInstanceRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task_id 不能为空")}, nil
	}
	if err := s.taskInstanceSvc.InvalidateTaskInstance(ctx, req.GetTaskId()); err != nil {
		log.ErrorContextf(ctx, "[CollectMgr] InvalidateTaskInstance failed: %v", err)
		return &pb.InvalidateTaskInstanceRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "作废任务失败")}, nil
	}
	return &pb.InvalidateTaskInstanceRsp{RetInfo: retOK()}, nil
}

// ========== 数据类型配置 ==========

// GetDataTypeConfigs 获取所有数据类型配置。
func (s *Service) GetDataTypeConfigs(ctx context.Context, req *pb.GetDataTypeConfigsReq) (*pb.GetDataTypeConfigsRsp, error) {
	configs, err := s.dataTypeCfgSvc.GetDataTypeConfigs(ctx)
	if err != nil {
		log.ErrorContextf(ctx, "[CollectMgr] GetDataTypeConfigs failed: %v", err)
		return &pb.GetDataTypeConfigsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "获取数据类型配置失败")}, nil
	}
	return &pb.GetDataTypeConfigsRsp{RetInfo: retOK(), Configs: configs}, nil
}

// GetDataTypeConfigWithFields 获取数据类型配置及字段信息。
func (s *Service) GetDataTypeConfigWithFields(ctx context.Context, req *pb.GetDataTypeConfigWithFieldsReq) (*pb.GetDataTypeConfigWithFieldsRsp, error) {
	if req.GetDataType() == "" {
		return &pb.GetDataTypeConfigWithFieldsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "data_type 不能为空")}, nil
	}
	detail, err := s.dataTypeCfgSvc.GetDataTypeConfigWithFields(ctx, req.GetDataType())
	if err != nil {
		log.ErrorContextf(ctx, "[CollectMgr] GetDataTypeConfigWithFields failed: %v", err)
		return &pb.GetDataTypeConfigWithFieldsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "获取数据类型配置失败")}, nil
	}
	return &pb.GetDataTypeConfigWithFieldsRsp{RetInfo: retOK(), Detail: detail}, nil
}

// ========== 任务规划器 ==========

// RecalculateAllTaskInstances 手动触发全量重算。
func (s *Service) RecalculateAllTaskInstances(ctx context.Context, req *pb.RecalculateAllTaskInstancesReq) (*pb.RecalculateAllTaskInstancesRsp, error) {
	log.InfoContext(ctx, "[CollectMgr] Manual recalculation triggered for all task instances")
	if err := s.taskPlannerSvc.RecalculateAllTaskInstances(ctx); err != nil {
		log.ErrorContextf(ctx, "[CollectMgr] RecalculateAllTaskInstances failed: %v", err)
		return &pb.RecalculateAllTaskInstancesRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "全量重算失败")}, nil
	}
	return &pb.RecalculateAllTaskInstancesRsp{RetInfo: retOK()}, nil
}

// ========== 通用 RetInfo 构造 ==========

// retOK 构造成功 RetInfo。
func retOK() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}
}

// retErr 构造错误 RetInfo。
func retErr(code pb.ErrorCode, msg string) *pb.RetInfo {
	return &pb.RetInfo{Code: code, Msg: msg}
}
