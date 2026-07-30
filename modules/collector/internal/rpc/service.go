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
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/internal/taskpublisher"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	mooxreport "github.com/mooyang-code/moox/packages/report"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"
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
	DatasetMetrics                 DatasetRunObserver
	RealtimeInventory              RealtimeInventory
}

// DatasetRunObserver records accepted realtime Dataset results.
type DatasetRunObserver interface {
	ObserveRun(mooxreport.DatasetObservation) error
}

// RealtimeInventory reconciles the derived expected Dataset registry.
type RealtimeInventory interface {
	MarkDirty()
	Refresh(context.Context) error
}

// Service implements the independent CollectMgr RPC service.
type Service struct {
	pb.UnimplementedCollectMgr
	ruleRepo       *store.TaskRuleRepository
	instanceRepo   *store.TaskInstanceRepository
	builder        *planner.TaskBuilder
	datasetSrc     datasetSource
	cloudJobs      *taskpublisher.Client
	datasetMetrics DatasetRunObserver
	inventory      RealtimeInventory
	now            func() time.Time
}

type datasetSource interface {
	GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error)
	ListSubjects(context.Context, string, string, string) ([]domain.DatasetSubject, error)
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
		datasetMetrics: deps.DatasetMetrics,
		inventory:      deps.RealtimeInventory,
		now:            time.Now,
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
	if err := s.validateTaskRuleDatasets(ctx, rule); err != nil {
		return &pb.CreateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if err := s.ruleRepo.Create(ctx, rule); err != nil {
		log.ErrorContextf(ctx, "[Collector] create task rule failed: %v", err)
		return &pb.CreateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	s.refreshRealtimeInventory(ctx)
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
	if err := s.validateTaskRuleDatasets(ctx, rule); err != nil {
		return &pb.UpdateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	updated, err := s.ruleRepo.UpdateByRuleID(ctx, spaceID, ruleID, rule)
	if err != nil {
		log.ErrorContextf(ctx, "[Collector] update task rule failed: %v", err)
		return &pb.UpdateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	s.refreshRealtimeInventory(ctx)
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
	s.refreshRealtimeInventory(ctx)
	return &pb.DisableTaskRuleRsp{RetInfo: retOK()}, nil
}

func (s *Service) refreshRealtimeInventory(ctx context.Context) {
	if s.inventory == nil {
		return
	}
	s.inventory.MarkDirty()
	if err := s.inventory.Refresh(ctx); err != nil {
		log.WarnContextf(ctx, "[Collector] refresh realtime dataset inventory failed: %v", err)
	}
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
	if err := s.observeAcceptedTaskResult(ctx, spaceID, taskID, status, result); err != nil {
		log.WarnContextf(
			ctx,
			"[Collector] accepted task result metrics rejected space_id=%s task_id=%s job_item_id=%s error=%v",
			spaceID, taskID, jobItemID, err,
		)
	}
	log.InfoContextf(ctx, "[Collector] task status space_id=%s task_id=%s job_item_id=%s node_id=%s status=%d",
		spaceID, taskID, jobItemID, nodeID, status)
	return &pb.ReportInstanceStatusRsp{RetInfo: retOK()}, nil
}

type taskExecutionResult struct {
	DataType  string `json:"data_type"`
	DatasetID string `json:"dataset_id"`
	Freq      string `json:"freq,omitempty"`
	sources.CollectResult
}

type taskExecutionEnvelope struct {
	Tasks []taskExecutionResult `json:"tasks"`
}

func (s *Service) observeAcceptedTaskResult(
	ctx context.Context,
	spaceID string,
	taskID string,
	status int,
	raw string,
) error {
	if s == nil || s.datasetMetrics == nil {
		return nil
	}
	finishedAt := time.Now().UTC()
	if s.now != nil {
		finishedAt = s.now().UTC()
	}
	if status == domain.InstanceStatusFailed {
		instance, err := s.instanceRepo.Get(ctx, spaceID, taskID)
		if err != nil {
			return fmt.Errorf("load accepted failed task: %w", err)
		}
		if !strings.EqualFold(instance.DataType, "kline") {
			return nil
		}
		freq, err := sources.NormalizeFreq(instance.Interval)
		if err != nil {
			return err
		}
		return s.datasetMetrics.ObserveRun(mooxreport.DatasetObservation{
			Key: mooxreport.DatasetKey{
				SpaceID: spaceID, DatasetID: instance.DatasetID, Freq: freq,
			},
			Result: "error", FinishedAt: finishedAt,
		})
	}
	if status != domain.InstanceStatusSuccess {
		return nil
	}

	var envelope taskExecutionEnvelope
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return fmt.Errorf("decode accepted collection result: %w", err)
	}
	if len(envelope.Tasks) == 0 {
		var single taskExecutionResult
		if err := json.Unmarshal([]byte(raw), &single); err != nil {
			return fmt.Errorf("decode accepted collection summary: %w", err)
		}
		if strings.TrimSpace(single.DataType) != "" {
			envelope.Tasks = []taskExecutionResult{single}
		}
	}
	if len(envelope.Tasks) == 0 {
		return fmt.Errorf("accepted success result has no task summaries")
	}
	for i, summary := range envelope.Tasks {
		if strings.TrimSpace(summary.DataType) == "" {
			return fmt.Errorf("task summary %d data_type is required", i)
		}
		if strings.TrimSpace(summary.DatasetID) == "" {
			return fmt.Errorf("task summary %d dataset_id is required", i)
		}
		if err := sources.ValidateCollectResult(summary.DataType, summary.CollectResult); err != nil {
			return fmt.Errorf("task summary %d: %w", i, err)
		}
		if !strings.EqualFold(summary.DataType, "kline") {
			continue
		}
		freq, err := sources.NormalizeFreq(summary.Freq)
		if err != nil {
			return fmt.Errorf("task summary %d frequency: %w", i, err)
		}
		observation := mooxreport.DatasetObservation{
			Key: mooxreport.DatasetKey{
				SpaceID: spaceID, DatasetID: strings.TrimSpace(summary.DatasetID), Freq: freq,
			},
			Result: "success", Rows: summary.RowsWritten, FinishedAt: finishedAt,
		}
		if summary.RowsWritten == 0 {
			observation.Result = "empty"
		} else {
			outputWatermark, err := time.Parse(time.RFC3339Nano, summary.OutputWatermark)
			if err != nil {
				return fmt.Errorf("task summary %d output watermark: %w", i, err)
			}
			observation.OutputWatermark = outputWatermark
		}
		if err := s.datasetMetrics.ObserveRun(observation); err != nil {
			return fmt.Errorf("task summary %d observe: %w", i, err)
		}
	}
	return nil
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
		params, err := domain.ParseCollectParams(rules[i].CollectParams, rules[i].Exchange, rules[i].DataType)
		if err != nil {
			log.ErrorContextf(ctx, "[Collector] parse rule %s params failed: %v", rules[i].RuleID, err)
			scheduleErr = errors.Join(scheduleErr, fmt.Errorf("rule %s: %w", rules[i].RuleID, err))
			continue
		}
		executeAt, due, err := domain.ScheduleDecision(now, params.Schedule.Interval)
		if err != nil {
			log.ErrorContextf(ctx, "[Collector] decide rule %s schedule failed: %v", rules[i].RuleID, err)
			scheduleErr = errors.Join(scheduleErr, fmt.Errorf("rule %s: %w", rules[i].RuleID, err))
			continue
		}
		if !due {
			log.DebugContextf(
				ctx,
				"[Collector] schedule rule skipped: rule_id=%s interval=%s",
				rules[i].RuleID,
				params.Schedule.Interval,
			)
			continue
		}
		created, err := s.scheduleRule(ctx, &rules[i], params, executeAt)
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

func (s *Service) scheduleRule(
	ctx context.Context,
	rule *domain.TaskRule,
	params *domain.CollectParams,
	executeAt time.Time,
) (int, error) {
	var subjects []domain.DatasetSubject
	if params.Source.Kind == "dataset_subjects" {
		var err error
		subjects, err = s.datasetSrc.ListSubjects(ctx, rule.SpaceID, params.Source.DatasetID, rule.Exchange)
		if err != nil {
			return 0, fmt.Errorf("load dataset subjects for %s: %w", params.Source.DatasetID, err)
		}
	}
	instances, err := s.builder.BuildInstancesWithParams(ctx, rule, params, subjects)
	if err != nil {
		return 0, fmt.Errorf("build instances for rule %s: %w", rule.RuleID, err)
	}
	instances, err = taskpublisher.PrepareScheduledInstances(instances, executeAt)
	if err != nil {
		return 0, fmt.Errorf("prepare instances for rule %s: %w", rule.RuleID, err)
	}
	if err := s.reconcilePendingInstances(ctx, instances); err != nil {
		return 0, fmt.Errorf("reconcile pending instances for rule %s: %w", rule.RuleID, err)
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

func (s *Service) reconcilePendingInstances(ctx context.Context, desired []domain.TaskInstance) error {
	for i := range desired {
		current, err := s.instanceRepo.Get(ctx, desired[i].SpaceID, desired[i].TaskID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if current.LastExecStatus != domain.InstanceStatusPending ||
			current.CloudJobItemID == desired[i].CloudJobItemID {
			continue
		}
		state, stateErr := s.cloudJobs.GetTerminalState(ctx, current.SpaceID, current.CloudJobItemID)
		if stateErr != nil {
			log.WarnContextf(ctx,
				"reconcile pending collector job failed: task_id=%s job_item_id=%s error=%v",
				current.TaskID, current.CloudJobItemID, stateErr,
			)
			continue
		}
		if !state.Terminal {
			continue
		}
		updated, updateErr := s.instanceRepo.UpdateStatus(
			ctx,
			current.SpaceID,
			current.TaskID,
			current.CloudJobItemID,
			state.NodeID,
			state.Status,
			state.Result,
		)
		if updateErr != nil {
			return updateErr
		}
		if !updated {
			log.WarnContextf(ctx,
				"pending collector job changed during reconciliation: task_id=%s job_item_id=%s",
				current.TaskID, current.CloudJobItemID,
			)
		}
	}
	return nil
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

func (s *Service) validateTaskRuleDatasets(ctx context.Context, rule domain.TaskRule) error {
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Exchange, rule.DataType)
	if err != nil {
		return err
	}
	switch params.Collector.DataType {
	case "symbol":
		return s.validateDataset(
			ctx,
			rule.SpaceID,
			params.Target.DatasetID,
			params.Collector.Exchange,
			storagepb.DataKind_DATA_KIND_RECORD,
			"target",
			false,
		)
	case "kline":
		if err := s.validateDataset(
			ctx,
			rule.SpaceID,
			params.Source.DatasetID,
			params.Collector.Exchange,
			storagepb.DataKind_DATA_KIND_RECORD,
			"source",
			false,
		); err != nil {
			return err
		}
		return s.validateDataset(
			ctx,
			rule.SpaceID,
			params.Target.DatasetID,
			params.Collector.Exchange,
			storagepb.DataKind_DATA_KIND_TIME_SERIES,
			"target",
			true,
		)
	default:
		return fmt.Errorf("unsupported collector data_type: %s", params.Collector.DataType)
	}
}

func (s *Service) validateDataset(
	ctx context.Context,
	spaceID string,
	datasetID string,
	exchange string,
	expectedKind storagepb.DataKind,
	role string,
	allowSharedMarket bool,
) error {
	info, err := s.datasetSrc.GetDataset(ctx, spaceID, datasetID)
	if err != nil {
		return fmt.Errorf("%s Dataset %s is unavailable: %w", role, datasetID, err)
	}
	if info.Status != "active" {
		return fmt.Errorf("%s Dataset %s must be active", role, datasetID)
	}
	sourceMatches := strings.EqualFold(info.DataSourceID, exchange) ||
		(allowSharedMarket && strings.EqualFold(info.DataSourceID, "crypto_market"))
	if !sourceMatches {
		return fmt.Errorf(
			"%s Dataset %s data_source_id=%s does not match collector exchange=%s",
			role,
			datasetID,
			info.DataSourceID,
			exchange,
		)
	}
	if info.DataKind != expectedKind {
		return fmt.Errorf(
			"%s Dataset %s data_kind=%s does not match collector data_type",
			role,
			datasetID,
			info.DataKind.String(),
		)
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
