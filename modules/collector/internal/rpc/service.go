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
	"github.com/mooyang-code/moox/modules/collector/internal/planner"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/repository"
	"github.com/mooyang-code/moox/modules/collector/internal/taskpublisher"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// Dependencies contains external service endpoints used by CollectMgr.
type Dependencies struct {
	AdminGatewayURL       string
	ServiceGatewayTarget  string
	ServiceAuth           taskpublisher.AuthConfig
	StorageMetadataTarget string
	StorageAccessTarget   string
}

// Service implements the independent CollectMgr RPC service.
type Service struct {
	pb.UnimplementedCollectMgr
	ruleRepo     *repository.TaskRuleRepository
	instanceRepo *repository.TaskInstanceRepository
	builder      *planner.TaskBuilder
	datasetSrc   *storagesource.DatasetSource
	cloudJobs    *taskpublisher.Client
}

// New creates a collector management service.
func New(db *gorm.DB, deps Dependencies) *Service {
	return &Service{
		ruleRepo:     repository.NewTaskRuleRepository(db),
		instanceRepo: repository.NewTaskInstanceRepository(db),
		builder:      planner.NewTaskBuilder(),
		datasetSrc:   storagesource.NewDatasetSource(deps.StorageMetadataTarget),
		cloudJobs: taskpublisher.New(taskpublisher.Config{
			ServiceGatewayTarget:  deps.ServiceGatewayTarget,
			GatewayURL:            deps.AdminGatewayURL,
			StorageMetadataTarget: deps.StorageMetadataTarget,
			StorageAccessTarget:   deps.StorageAccessTarget,
			Auth:                  deps.ServiceAuth,
		}),
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
	rules, total, err := s.ruleRepo.List(ctx, repository.TaskRuleFilter{
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
	repoFilter := repository.TaskInstanceFilter{Page: 1, PageSize: 50}
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
		repoFilter.PlannedExecNode = filter.GetPlannedExecNode()
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
	nodeID := strings.TrimSpace(req.GetNodeId())
	if spaceID == "" {
		return &pb.ReportInstanceStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if taskID == "" {
		return &pb.ReportInstanceStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task_id is required")}, nil
	}
	status := fromPBStatus(req.GetStatus())
	if status == 0 {
		return &pb.ReportInstanceStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "status is required")}, nil
	}
	result := jsonStringFromStruct(req.GetResult())
	if err := s.instanceRepo.UpdateStatus(ctx, spaceID, taskID, nodeID, status, result); err != nil {
		log.ErrorContextf(ctx, "[Collector] update task status failed: %v", err)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &pb.ReportInstanceStatusRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "task instance not found")}, nil
		}
		return &pb.ReportInstanceStatusRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	duration := time.Duration(req.GetDurationMs()) * time.Millisecond
	if err := s.instanceRepo.AddExecutionLog(ctx, spaceID, taskID, nodeID, status, result, req.GetErrorMessage(), duration); err != nil {
		log.WarnContextf(ctx, "[Collector] add execution log failed: %v", err)
	}
	log.InfoContextf(ctx, "[Collector] task status space_id=%s task_id=%s node_id=%s status=%d",
		spaceID, taskID, nodeID, status)
	return &pb.ReportInstanceStatusRsp{RetInfo: retOK()}, nil
}

// RecalculateAllTaskInstances rebuilds task instances for all enabled rules in one space.
func (s *Service) RecalculateAllTaskInstances(ctx context.Context, req *pb.RecalculateAllTaskInstancesReq) (*pb.RecalculateAllTaskInstancesRsp, error) {
	spaceID := strings.TrimSpace(req.GetSpaceId())
	if spaceID == "" {
		return &pb.RecalculateAllTaskInstancesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	rules, err := s.ruleRepo.ListEnabled(ctx, spaceID)
	if err != nil {
		log.ErrorContextf(ctx, "[Collector] list enabled rules failed: %v", err)
		return &pb.RecalculateAllTaskInstancesRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	total := 0
	for i := range rules {
		created, err := s.recalculateRule(ctx, &rules[i])
		if err != nil {
			log.ErrorContextf(ctx, "[Collector] recalculate rule %s failed: %v", rules[i].RuleID, err)
			return &pb.RecalculateAllTaskInstancesRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
		total += created
	}
	log.InfoContextf(ctx, "[Collector] recalculated task instances space_id=%s rules=%d instances=%d", spaceID, len(rules), total)
	return &pb.RecalculateAllTaskInstancesRsp{RetInfo: retOK()}, nil
}

func (s *Service) recalculateRule(ctx context.Context, rule *domain.TaskRule) (int, error) {
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
	if err := s.instanceRepo.UpsertMany(ctx, instances); err != nil {
		return 0, fmt.Errorf("upsert instances for rule %s: %w", rule.RuleID, err)
	}
	jobItemIDs, err := s.cloudJobs.SubmitCollectorJobItems(ctx, instances)
	if err != nil {
		return 0, fmt.Errorf("submit cloud job items for rule %s: %w", rule.RuleID, err)
	}
	if err := s.instanceRepo.UpdateCloudJobItemIDs(ctx, rule.SpaceID, jobItemIDs); err != nil {
		return 0, fmt.Errorf("record cloud job items for rule %s: %w", rule.RuleID, err)
	}
	if len(jobItemIDs) > 0 {
		woken, err := s.cloudJobs.WakeCollectorNodes(ctx, taskpublisher.WakeOptions{
			SpaceID:  rule.SpaceID,
			JobTypes: jobTypesFromInstances(instances),
		})
		if err != nil {
			log.WarnContextf(ctx, "[Collector] wake collector nodes failed rule_id=%s: %v", rule.RuleID, err)
		} else if woken == 0 {
			log.WarnContextf(ctx, "[Collector] no collector nodes woken rule_id=%s", rule.RuleID)
		} else {
			log.InfoContextf(ctx, "[Collector] woken collector nodes rule_id=%s count=%d", rule.RuleID, woken)
		}
	}
	return len(instances), nil
}

func jobTypesFromInstances(instances []domain.TaskInstance) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(instances))
	for _, instance := range instances {
		payload := map[string]any{}
		_ = json.Unmarshal([]byte(instance.TaskParams), &payload)
		jobType, _ := payload["job_type"].(string)
		jobType = strings.TrimSpace(jobType)
		if jobType == "" {
			dataType, _ := payload["data_type"].(string)
			jobType = "collect." + strings.TrimSpace(dataType)
		}
		if jobType == "collect." || jobType == "" {
			continue
		}
		if _, ok := seen[jobType]; ok {
			continue
		}
		seen[jobType] = struct{}{}
		out = append(out, jobType)
	}
	return out
}

// GetDataTypeConfigs returns the currently supported collector rule data types.
func (s *Service) GetDataTypeConfigs(ctx context.Context, req *pb.GetDataTypeConfigsReq) (*pb.GetDataTypeConfigsRsp, error) {
	return &pb.GetDataTypeConfigsRsp{
		RetInfo: retOK(),
		Configs: []*pb.DataTypeConfig{
			klineDataTypeConfig(),
			symbolDataTypeConfig(),
		},
	}, nil
}

// GetDataTypeConfigWithFields returns field metadata for the rule form.
func (s *Service) GetDataTypeConfigWithFields(ctx context.Context, req *pb.GetDataTypeConfigWithFieldsReq) (*pb.GetDataTypeConfigWithFieldsRsp, error) {
	switch req.GetDataType() {
	case "kline":
		return &pb.GetDataTypeConfigWithFieldsRsp{
			RetInfo: retOK(),
			Detail: &pb.DataTypeConfigDetail{
				Config: klineDataTypeConfig(),
				Fields: []*pb.DataTypeFieldConfig{
					{
						Id:                1,
						DataType:          "kline",
						FieldKey:          "inst_type",
						FieldName:         "产品类型",
						FieldType:         "select",
						IsRequired:        true,
						DefaultValue:      valueFromAny("SPOT"),
						FieldOptions:      structFromJSONString(`{"options":[{"value":"SPOT","label":"现货"},{"value":"SWAP","label":"永续合约"}]}`),
						DataSourceOptions: supportedDataSourceOptions(),
						SortOrder:         1,
					},
					{
						Id:                2,
						DataType:          "kline",
						FieldKey:          "objects",
						FieldName:         "交易标的",
						FieldType:         "multi_input",
						IsRequired:        true,
						DefaultValue:      valueFromAny([]any{"*"}),
						FieldOptions:      structFromJSONString(`{"placeholder":"输入交易标的，例如 BTCUSDT；选择全部时使用 *"}`),
						DataSourceOptions: supportedDataSourceOptions(),
						SortOrder:         2,
					},
					{
						Id:                3,
						DataType:          "kline",
						FieldKey:          "intervals",
						FieldName:         "K线周期",
						FieldType:         "multi_select",
						IsRequired:        true,
						DefaultValue:      valueFromAny([]any{"1m"}),
						FieldOptions:      structFromJSONString(`{"options":[{"value":"1m","label":"1分钟"},{"value":"3m","label":"3分钟"},{"value":"5m","label":"5分钟"},{"value":"15m","label":"15分钟"},{"value":"30m","label":"30分钟"},{"value":"1h","label":"1小时"},{"value":"2h","label":"2小时"},{"value":"4h","label":"4小时"},{"value":"6h","label":"6小时"},{"value":"12h","label":"12小时"},{"value":"1d","label":"1天"},{"value":"1w","label":"1周"},{"value":"1M","label":"1月"}]}`),
						DataSourceOptions: supportedDataSourceOptions(),
						SortOrder:         3,
					},
				},
			},
		}, nil
	case "symbol":
		return &pb.GetDataTypeConfigWithFieldsRsp{
			RetInfo: retOK(),
			Detail: &pb.DataTypeConfigDetail{
				Config: symbolDataTypeConfig(),
				Fields: []*pb.DataTypeFieldConfig{
					{
						Id:                4,
						DataType:          "symbol",
						FieldKey:          "inst_type",
						FieldName:         "产品类型",
						FieldType:         "select",
						IsRequired:        true,
						DefaultValue:      valueFromAny("SPOT"),
						FieldOptions:      structFromJSONString(`{"options":[{"value":"SPOT","label":"现货"},{"value":"SWAP","label":"永续合约"}]}`),
						DataSourceOptions: supportedDataSourceOptions(),
						SortOrder:         1,
					},
				},
			},
		}, nil
	default:
		return &pb.GetDataTypeConfigWithFieldsRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "unsupported data_type")}, nil
	}
}

func normalizeTaskRule(rule domain.TaskRule) domain.TaskRule {
	rule.RuleID = strings.TrimSpace(rule.RuleID)
	if rule.RuleID == "" {
		rule.RuleID = fmt.Sprintf("rule-%d", time.Now().UnixNano())
	}
	if strings.TrimSpace(rule.AssignmentType) == "" {
		rule.AssignmentType = "auto"
	}
	if strings.TrimSpace(rule.AssignedNodes) == "" {
		rule.AssignedNodes = "[]"
	}
	if strings.TrimSpace(rule.NodeTags) == "" {
		rule.NodeTags = "[]"
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
	if _, err := domain.ParseCollectParams(rule.CollectParams, rule.Exchange, rule.DataType); err != nil {
		return fmt.Errorf("invalid collect_params: %w", err)
	}
	return nil
}

func klineDataTypeConfig() *pb.DataTypeConfig {
	return &pb.DataTypeConfig{
		Id:                1,
		DataType:          "kline",
		TypeName:          "K线",
		TypeDesc:          "交易所K线行情采集",
		DataSourceOptions: supportedDataSourceOptions(),
		SortOrder:         1,
		Version:           1,
	}
}

func symbolDataTypeConfig() *pb.DataTypeConfig {
	return &pb.DataTypeConfig{
		Id:                2,
		DataType:          "symbol",
		TypeName:          "标的",
		TypeDesc:          "交易所标的元数据同步",
		DataSourceOptions: supportedDataSourceOptions(),
		SortOrder:         2,
		Version:           1,
	}
}

func supportedDataSourceOptions() *structpb.Struct {
	return structFromJSONString(`{"options":[{"value":"binance","label":"币安"}]}`)
}

func valueFromAny(value any) *structpb.Value {
	out, err := structpb.NewValue(value)
	if err != nil {
		return structpb.NewStringValue("")
	}
	return out
}
