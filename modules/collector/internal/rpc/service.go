// Package rpc implements the independent Collector management RPC service.
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs"
	"github.com/mooyang-code/moox/modules/collector/internal/planner"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/internal/taskpublisher"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go/log"
)

// Dependencies contains external service endpoints used by CollectMgr.
type Dependencies struct {
	AdminGatewayURL         string
	ServiceAuth             taskpublisher.AuthConfig
	StorageRPCGatewayTarget string
	// PlannerStorageRPCGatewayTarget is the control-plane's local metadata target.
	// It overrides the runtime Storage target when both are available.
	PlannerStorageRPCGatewayTarget string
}

// Service implements the independent CollectMgr RPC service.
type Service struct {
	pb.UnimplementedCollectMgr
	ruleRepo     *store.TaskRuleRepository
	instanceRepo *store.TaskInstanceRepository
	builder      *planner.TaskBuilder
	datasetSrc   *storagesource.DatasetSource
	cloudJobs    *taskpublisher.Client
	now          func() time.Time
}

// New creates a collector management service.
func New(persistence *store.Store, deps Dependencies) *Service {
	plannerMetadataTarget := deps.PlannerStorageRPCGatewayTarget
	if strings.TrimSpace(plannerMetadataTarget) == "" {
		plannerMetadataTarget = deps.StorageRPCGatewayTarget
	}
	return &Service{
		ruleRepo:     persistence.TaskRules(),
		instanceRepo: persistence.TaskInstances(),
		builder:      planner.NewTaskBuilder(),
		datasetSrc:   storagesource.NewDatasetSource(plannerMetadataTarget),
		cloudJobs: taskpublisher.New(taskpublisher.Config{
			ServiceGatewayTarget: deps.AdminGatewayURL,
			Auth:                 deps.ServiceAuth,
		}),
		now: time.Now,
	}
}

func retOK() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "ok"}
}

func retErr(code pb.ErrorCode, msg string) *pb.RetInfo {
	return &pb.RetInfo{Code: code, Msg: msg}
}

func pageParams(page *pb.Page) (int, int) {
	if page == nil {
		return 1, 50
	}
	return normalizePageParams(int(page.GetPage()), int(page.GetSize()))
}

func normalizePageParams(page int, size int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 50
	}
	if size > 1000 {
		size = 1000
	}
	return page, size
}

func pageResult(page int, size int, total int64) *pb.PageResult {
	return &pb.PageResult{
		Page:    uint32(page),
		Size:    uint32(size),
		Total:   uint32Total(total),
		HasMore: int64(page*size) < total,
	}
}

func uint32Total(total int64) uint32 {
	if total <= 0 {
		return 0
	}
	max := int64(^uint32(0))
	if total > max {
		return ^uint32(0)
	}
	return uint32(total)
}

// GetTaskRuleList returns rule data from the new collector DB.
func (s *Service) GetTaskRuleList(ctx context.Context, req *pb.GetTaskRuleListReq) (*pb.GetTaskRuleListRsp, error) {
	spaceID := strings.TrimSpace(req.GetSpaceId())
	if spaceID == "" {
		return &pb.GetTaskRuleListRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	page, size := pageParams(req.GetPage())
	rules, total, err := s.ruleRepo.List(ctx, store.TaskRuleFilter{
		SpaceID:  spaceID,
		DataType: req.GetDataType(),
		Exchange: req.GetExchange(),
		Enabled:  req.Enabled,
		RuleID:   req.GetRuleId(),
		Page:     page,
		PageSize: size,
	})
	if err != nil {
		log.ErrorContextf(ctx, "[Collector] list task rules failed: %v", err)
		return &pb.GetTaskRuleListRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	out := make([]*pb.TaskRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, toPBRule(rule))
	}
	return &pb.GetTaskRuleListRsp{RetInfo: retOK(), Rules: out, Page: pageResult(page, size, total)}, nil
}

// GetTaskRuleDetail returns a single collector rule.
func (s *Service) GetTaskRuleDetail(ctx context.Context, req *pb.GetTaskRuleDetailReq) (*pb.GetTaskRuleDetailRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.GetTaskRuleDetailRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if strings.TrimSpace(req.GetRuleId()) == "" {
		return &pb.GetTaskRuleDetailRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "rule_id is required")}, nil
	}
	rule, err := s.ruleRepo.GetByRuleID(ctx, req.GetSpaceId(), req.GetRuleId())
	if err != nil {
		return &pb.GetTaskRuleDetailRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, err.Error())}, nil
	}
	return &pb.GetTaskRuleDetailRsp{RetInfo: retOK(), Rule: toPBRule(*rule)}, nil
}

// CreateTaskRule creates a collector rule through the independent collector service.
func (s *Service) CreateTaskRule(ctx context.Context, req *pb.CreateTaskRuleReq) (*pb.CreateTaskRuleRsp, error) {
	rule := normalizeTaskRule(fromPBRule(req.GetRule()))
	if err := validateTaskRule(rule); err != nil {
		return &pb.CreateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if err := s.ruleRepo.Create(ctx, rule); err != nil {
		log.ErrorContextf(ctx, "[Collector] create task rule failed: %v", err)
		return &pb.CreateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.CreateTaskRuleRsp{RetInfo: retOK(), RuleId: rule.RuleID}, nil
}

// UpdateTaskRule updates a collector rule through the independent collector service.
func (s *Service) UpdateTaskRule(ctx context.Context, req *pb.UpdateTaskRuleReq) (*pb.UpdateTaskRuleRsp, error) {
	spaceID := strings.TrimSpace(req.GetSpaceId())
	ruleID := strings.TrimSpace(req.GetRuleId())
	rule := normalizeTaskRule(fromPBRule(req.GetRule()))
	if spaceID == "" {
		spaceID = strings.TrimSpace(rule.SpaceID)
	}
	if spaceID == "" {
		return &pb.UpdateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if ruleID == "" {
		ruleID = strings.TrimSpace(rule.RuleID)
	}
	if ruleID == "" {
		return &pb.UpdateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "rule_id is required")}, nil
	}
	if rule.RuleID == "" {
		rule.RuleID = ruleID
	}
	rule.SpaceID = spaceID
	if err := validateTaskRule(rule); err != nil {
		return &pb.UpdateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	updated, err := s.ruleRepo.UpdateByRuleID(ctx, spaceID, ruleID, rule)
	if err != nil {
		log.ErrorContextf(ctx, "[Collector] update task rule failed: %v", err)
		return &pb.UpdateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.UpdateTaskRuleRsp{RetInfo: retOK(), Rule: toPBRule(*updated)}, nil
}

// DisableTaskRule disables a collector rule without deleting runtime history.
func (s *Service) DisableTaskRule(ctx context.Context, req *pb.DisableTaskRuleReq) (*pb.DisableTaskRuleRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.DisableTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if strings.TrimSpace(req.GetRuleId()) == "" {
		return &pb.DisableTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "rule_id is required")}, nil
	}
	if err := s.ruleRepo.SetEnabled(ctx, req.GetSpaceId(), req.GetRuleId(), false); err != nil {
		log.ErrorContextf(ctx, "[Collector] disable task rule failed: %v", err)
		return &pb.DisableTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.DisableTaskRuleRsp{RetInfo: retOK()}, nil
}

// GetTaskInstanceList returns task instances from the new collector DB.
func (s *Service) GetTaskInstanceList(ctx context.Context, req *pb.GetTaskInstanceListReq) (*pb.GetTaskInstanceListRsp, error) {
	filter := req.GetFilter()
	if filter == nil || strings.TrimSpace(filter.GetSpaceId()) == "" {
		return &pb.GetTaskInstanceListRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	spaceID := strings.TrimSpace(filter.GetSpaceId())
	repoFilter := store.TaskInstanceFilter{Page: 1, PageSize: 50}
	page, size := 1, 50
	if filter != nil {
		page, size = pageParams(filter.GetPage())
		repoFilter.SpaceID = spaceID
		repoFilter.TaskID = filter.GetTaskId()
		repoFilter.RuleID = filter.GetRuleId()
		repoFilter.Exchange = filter.GetExchange()
		repoFilter.Market = filter.GetMarket()
		repoFilter.DataType = filter.GetDataType()
		repoFilter.DatasetID = filter.GetDatasetId()
		repoFilter.SubjectID = filter.GetSubjectId()
		repoFilter.Interval = filter.GetInterval()
		repoFilter.LastExecNode = filter.GetLastExecNode()
		repoFilter.Symbol = filter.GetSymbol()
		repoFilter.IncludeDeleted = filter.GetIncludeDeleted()
		repoFilter.Page = page
		repoFilter.PageSize = size
		if filter.GetLastExecStatus() != pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_UNSPECIFIED {
			status := fromPBStatus(filter.GetLastExecStatus())
			repoFilter.LastExecStatus = &status
		}
	}
	instances, total, err := s.instanceRepo.List(ctx, repoFilter)
	if err != nil {
		log.ErrorContextf(ctx, "[Collector] list task instances failed: %v", err)
		return &pb.GetTaskInstanceListRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	out := make([]*pb.TaskInstance, 0, len(instances))
	for _, instance := range instances {
		out = append(out, toPBInstance(instance))
	}
	return &pb.GetTaskInstanceListRsp{RetInfo: retOK(), Instances: out, Page: pageResult(page, size, total)}, nil
}

// ReportTaskStatus accepts SCF status reports during the service split.
func (s *Service) ReportTaskStatus(ctx context.Context, req *pb.ReportInstanceStatusReq) (*pb.ReportInstanceStatusRsp, error) {
	spaceID := strings.TrimSpace(req.GetSpaceId())
	taskID := strings.TrimSpace(req.GetTaskId())
	jobItemID := strings.TrimSpace(req.GetJobItemId())
	nodeID := strings.TrimSpace(req.GetNodeId())
	if spaceID == "" {
		return &pb.ReportInstanceStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if taskID == "" {
		return &pb.ReportInstanceStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task_id is required")}, nil
	}
	if jobItemID == "" {
		return &pb.ReportInstanceStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "job_item_id is required")}, nil
	}
	status := fromPBStatus(req.GetStatus())
	if status == 0 {
		return &pb.ReportInstanceStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "status is required")}, nil
	}
	result := jsonStringFromStruct(req.GetResult())
	updated, err := s.instanceRepo.UpdateStatus(ctx, spaceID, taskID, jobItemID, nodeID, status, result)
	if err != nil {
		log.ErrorContextf(ctx, "[Collector] update task status failed: %v", err)
		return &pb.ReportInstanceStatusRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if !updated {
		log.InfoContextf(
			ctx,
			"[Collector] ignored stale task status space_id=%s task_id=%s job_item_id=%s node_id=%s status=%d",
			spaceID,
			taskID,
			jobItemID,
			nodeID,
			status,
		)
		return &pb.ReportInstanceStatusRsp{RetInfo: retOK()}, nil
	}
	log.InfoContextf(ctx, "[Collector] task status space_id=%s task_id=%s job_item_id=%s node_id=%s status=%d",
		spaceID, taskID, jobItemID, nodeID, status)
	return &pb.ReportInstanceStatusRsp{RetInfo: retOK()}, nil
}

// ScheduleTasks plans and submits the next execution of every enabled task in one space.
func (s *Service) ScheduleTasks(ctx context.Context, req *pb.ScheduleTasksReq) (*pb.ScheduleTasksRsp, error) {
	spaceID := strings.TrimSpace(req.GetSpaceId())
	if spaceID == "" {
		return &pb.ScheduleTasksRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	rules, err := s.ruleRepo.ListEnabled(ctx, spaceID)
	if err != nil {
		log.ErrorContextf(ctx, "[Collector] list enabled rules failed: %v", err)
		return &pb.ScheduleTasksRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	total := 0
	var scheduleErr error
	for i := range rules {
		created, err := s.scheduleRule(ctx, &rules[i], now)
		if err != nil {
			log.ErrorContextf(ctx, "[Collector] schedule rule %s failed: %v", rules[i].RuleID, err)
			scheduleErr = errors.Join(scheduleErr, fmt.Errorf("rule %s: %w", rules[i].RuleID, err))
			continue
		}
		total += created
	}
	log.InfoContextf(ctx, "[Collector] scheduled tasks space_id=%s rules=%d instances=%d", spaceID, len(rules), total)
	if scheduleErr != nil {
		return &pb.ScheduleTasksRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, scheduleErr.Error())}, nil
	}
	return &pb.ScheduleTasksRsp{RetInfo: retOK()}, nil
}

func (s *Service) scheduleRule(ctx context.Context, rule *domain.TaskRule, now time.Time) (int, error) {
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Exchange, rule.DataType)
	if err != nil {
		return 0, fmt.Errorf("parse rule %s params: %w", rule.RuleID, err)
	}
	var subjects []domain.DatasetSubject
	if params.Source.Kind == "dataset_subjects" {
		subjects, err = s.datasetSrc.ListSubjects(ctx, rule.SpaceID, params.Source.DatasetID, rule.Exchange)
		if err != nil {
			return 0, fmt.Errorf("load dataset subjects for %s: %w", params.Source.DatasetID, err)
		}
	}
	instances, err := s.builder.BuildInstancesWithParams(ctx, rule, params, subjects)
	if err != nil {
		return 0, fmt.Errorf("build instances for rule %s: %w", rule.RuleID, err)
	}
	instances, err = taskpublisher.PrepareScheduledInstances(instances, now)
	if err != nil {
		return 0, fmt.Errorf("prepare instances for rule %s: %w", rule.RuleID, err)
	}
	reserved, err := s.instanceRepo.ReserveMany(ctx, instances)
	if err != nil {
		return 0, fmt.Errorf("reserve instances for rule %s: %w", rule.RuleID, err)
	}
	if len(reserved) == 0 {
		return 0, nil
	}
	_, submitErr := s.cloudJobs.SubmitCollectorJobItems(ctx, reserved)
	if submitErr != nil {
		return 0, fmt.Errorf("submit cloud job items for rule %s: %w", rule.RuleID, submitErr)
	}
	return len(reserved), nil
}

// GetDataTypeConfigs returns the currently supported collector rule data types.
func (s *Service) GetDataTypeConfigs(ctx context.Context, req *pb.GetDataTypeConfigsReq) (*pb.GetDataTypeConfigsRsp, error) {
	jobDefinitions := jobs.ListJobDefinitions()
	configs := make([]*pb.DataTypeConfig, 0, len(jobDefinitions))
	for _, definition := range jobDefinitions {
		configs = append(configs, dataTypeConfigFromDefinition(definition))
	}
	return &pb.GetDataTypeConfigsRsp{
		RetInfo: retOK(),
		Configs: configs,
	}, nil
}

// GetDataTypeConfigWithFields returns field metadata for the rule form.
func (s *Service) GetDataTypeConfigWithFields(ctx context.Context, req *pb.GetDataTypeConfigWithFieldsReq) (*pb.GetDataTypeConfigWithFieldsRsp, error) {
	definition, ok := jobs.JobDefinitionByDataType(req.GetDataType())
	if !ok {
		return &pb.GetDataTypeConfigWithFieldsRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "unsupported data_type")}, nil
	}
	return &pb.GetDataTypeConfigWithFieldsRsp{
		RetInfo: retOK(),
		Detail: &pb.DataTypeConfigDetail{
			Config: dataTypeConfigFromDefinition(definition),
			Fields: dataTypeFieldsFromDefinition(definition),
		},
	}, nil
}

func normalizeTaskRule(rule domain.TaskRule) domain.TaskRule {
	rule.RuleID = strings.TrimSpace(rule.RuleID)
	if rule.RuleID == "" {
		rule.RuleID = fmt.Sprintf("rule-%d", time.Now().UnixNano())
	}
	if strings.TrimSpace(rule.CollectParams) == "" {
		rule.CollectParams = "{}"
	}
	return rule
}

func validateTaskRule(rule domain.TaskRule) error {
	if strings.TrimSpace(rule.RuleID) == "" {
		return fmt.Errorf("rule_id is required")
	}
	if strings.TrimSpace(rule.SpaceID) == "" {
		return fmt.Errorf("space_id is required")
	}
	if strings.TrimSpace(rule.DataType) == "" {
		return fmt.Errorf("data_type is required")
	}
	if strings.TrimSpace(rule.Exchange) == "" {
		return fmt.Errorf("exchange is required")
	}
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Exchange, rule.DataType)
	if err != nil {
		return fmt.Errorf("invalid collect_params: %w", err)
	}
	if err := params.Validate(); err != nil {
		return fmt.Errorf("invalid collect_params: %w", err)
	}
	definition, ok := jobs.JobDefinitionByDataType(params.Collector.DataType)
	_, routeOK := jobs.JobRouteFor(params.Collector.Exchange, params.Collector.DataType)
	if !ok || !routeOK || !definition.Matches(params) {
		return fmt.Errorf(
			"unsupported collector: exchange=%s market=%s data_type=%s source_kind=%s",
			params.Collector.Exchange,
			params.Collector.Market,
			params.Collector.DataType,
			params.Source.Kind,
		)
	}
	if !strings.EqualFold(rule.Exchange, params.Collector.Exchange) ||
		!strings.EqualFold(rule.DataType, params.Collector.DataType) {
		return fmt.Errorf("rule identity does not match collect_params collector")
	}
	return nil
}

func dataTypeConfigFromDefinition(definition jobs.JobDefinition) *pb.DataTypeConfig {
	return &pb.DataTypeConfig{
		Id:                definition.ID,
		DataType:          definition.DataType,
		TypeName:          definition.TypeName,
		TypeDesc:          definition.TypeDesc,
		DataSourceOptions: structFromAny(definition.DataSourceOptions),
		SortOrder:         definition.SortOrder,
		Version:           definition.Version,
	}
}

func dataTypeFieldsFromDefinition(definition jobs.JobDefinition) []*pb.DataTypeFieldConfig {
	fields := make([]*pb.DataTypeFieldConfig, 0, len(definition.Fields))
	for _, field := range definition.Fields {
		fields = append(fields, &pb.DataTypeFieldConfig{
			Id:                field.ID,
			DataType:          field.DataType,
			FieldKey:          field.FieldKey,
			FieldName:         field.FieldName,
			FieldType:         field.FieldType,
			IsRequired:        field.IsRequired,
			DefaultValue:      valueFromAny(field.DefaultValue),
			FieldOptions:      structFromJSONString(field.FieldOptionsJSON),
			DataSourceOptions: structFromAny(field.DataSourceOptions),
			SortOrder:         field.SortOrder,
		})
	}
	return fields
}

func structFromAny(value any) *structpb.Struct {
	raw, err := json.Marshal(value)
	if err != nil {
		return &structpb.Struct{Fields: map[string]*structpb.Value{}}
	}
	return structFromJSONString(string(raw))
}

func valueFromAny(value any) *structpb.Value {
	out, err := structpb.NewValue(value)
	if err != nil {
		return structpb.NewStringValue("")
	}
	return out
}
