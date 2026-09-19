// Package rpc implements the independent Collector management RPC service.
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	collectorresample "github.com/mooyang-code/moox/modules/collector/internal/resample"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/report"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// Dependencies contains external service endpoints used by CollectMgr.
type Dependencies struct {
	StorageRPCGatewayTarget string
	// PlannerStorageRPCGatewayTarget is the control-plane's local metadata target.
	// It overrides the runtime Storage target when both are available.
	PlannerStorageRPCGatewayTarget string
	RealtimeInventory              RealtimeInventory
	DefaultResampleSettleDelay     time.Duration
	ResultManager                  *taskresult.Manager
	ResultDataNodeID               string
}

// RealtimeInventory reconciles the derived expected Dataset registry.
type RealtimeInventory interface {
	MarkDirty()
	Refresh(context.Context) error
}

// Service implements the independent CollectMgr RPC service.
type Service struct {
	pb.UnimplementedCollectMgr
	persistence                *store.Store
	ruleRepo                   *store.CollectionTaskRepository
	instanceRepo               *store.TaskInstanceRepository
	datasetSrc                 datasetSource
	inventory                  RealtimeInventory
	defaultResampleSettleDelay time.Duration
	resultManager              *taskresult.Manager
	resultDataNodeID           string
}

const defaultResampleSettleDelay = 10 * time.Second

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
	settleDelay := deps.DefaultResampleSettleDelay
	if settleDelay < 0 {
		settleDelay = defaultResampleSettleDelay
	}
	return &Service{
		persistence:                persistence,
		ruleRepo:                   persistence.Tasks(),
		instanceRepo:               persistence.TaskInstances(),
		datasetSrc:                 storagesource.NewDatasetSource(plannerMetadataTarget),
		inventory:                  deps.RealtimeInventory,
		defaultResampleSettleDelay: settleDelay,
		resultManager:              deps.ResultManager,
		resultDataNodeID:           strings.TrimSpace(deps.ResultDataNodeID),
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

// GetTaskList returns rule data from the new collector DB.
func (s *Service) GetTaskList(ctx context.Context, req *pb.GetTaskListReq) (*pb.GetTaskListRsp, error) {
	spaceID := strings.TrimSpace(req.GetSpaceId())
	if spaceID == "" {
		return &pb.GetTaskListRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	page, size := pageParams(req.GetPage())
	tasks, total, err := s.ruleRepo.List(ctx, store.TaskFilter{
		SpaceID:    spaceID,
		DataType:   req.GetDataType(),
		Provider:   req.GetProvider(),
		MarketType: req.GetMarketType(),
		Enabled:    req.Enabled,
		TaskID:     req.GetTaskId(),
		Page:       page,
		PageSize:   size,
	})
	if err != nil {
		log.ErrorContextf(ctx, "[Collector] list task rules failed: %v", err)
		return &pb.GetTaskListRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	out := make([]*pb.CollectionTask, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, toPBTask(task))
	}
	return &pb.GetTaskListRsp{RetInfo: retOK(), Tasks: out, Page: pageResult(page, size, total)}, nil
}

// GetTaskDetail returns a single collection task.
func (s *Service) GetTaskDetail(ctx context.Context, req *pb.GetTaskDetailReq) (*pb.GetTaskDetailRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.GetTaskDetailRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if strings.TrimSpace(req.GetTaskId()) == "" {
		return &pb.GetTaskDetailRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task_id is required")}, nil
	}
	rule, err := s.ruleRepo.GetByTaskID(ctx, req.GetSpaceId(), req.GetTaskId())
	if err != nil {
		return &pb.GetTaskDetailRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, err.Error())}, nil
	}
	return &pb.GetTaskDetailRsp{RetInfo: retOK(), Task: toPBTask(*rule)}, nil
}

// CreateTask creates a collection task through the independent collector service.
func (s *Service) CreateTask(ctx context.Context, req *pb.CreateTaskReq) (*pb.CreateTaskRsp, error) {
	rule := normalizeCollectionTask(fromPBTask(req.GetTask()))
	if strings.TrimSpace(rule.TaskName) == "" {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task_name is required")}, nil
	}
	if err := validateCollectionTask(rule); err != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	var err error
	rule, err = canonicalizeCollectionTask(rule)
	if err != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if existing, lookupErr := s.ruleRepo.GetByTaskID(ctx, rule.SpaceID, rule.TaskID); lookupErr == nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("task %s already exists", existing.TaskID))}, nil
	} else if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, lookupErr.Error())}, nil
	}
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
	if err != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	resultConfig := req.GetResultConfig()
	dataNodeID := s.resultDataNodeID
	keepDuration, description := "0", rule.Description
	if resultConfig != nil {
		if strings.TrimSpace(resultConfig.GetDataNodeId()) != "" {
			dataNodeID = strings.TrimSpace(resultConfig.GetDataNodeId())
		}
		if strings.TrimSpace(resultConfig.GetKeepDuration()) != "" {
			keepDuration = strings.TrimSpace(resultConfig.GetKeepDuration())
		}
		if strings.TrimSpace(resultConfig.GetDescription()) != "" {
			description = strings.TrimSpace(resultConfig.GetDescription())
		}
	}
	var resultIDs taskresult.IDs
	if strings.EqualFold(strings.TrimSpace(rule.DataType), "kline_resample") {
		// Resample results use the same task-exclusive identity, but their
		// Dataset/View must be created by resample.Preparer with source lineage.
		// Do not provision them through the generic raw-collection manager.
		resultIDs = taskresult.ResultIDs(rule.SpaceID, rule.TaskID)
		rule.ResultDatasetID = resultIDs.DatasetID
		rule.ResultViewID = collectorresample.DefaultTargetViewID(resultIDs.DatasetID)
		rule.CollectParams = setTaskResultDatasetID(rule.CollectParams, resultIDs.DatasetID)
		params, err = domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
		if err != nil {
			return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
		}
	} else {
		if s.resultManager == nil {
			return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "task result manager is not configured")}, nil
		}
		var err error
		resultIDs, err = s.resultManager.Ensure(ctx, rule.SpaceID, rule.TaskID, rule.DataType, rule.MarketType, taskresult.Config{DataNodeID: dataNodeID, KeepDuration: keepDuration, Description: description, DataSourceID: rule.Provider, Frequency: firstTaskFrequency(params)})
		if err != nil {
			return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
		rule.ResultDatasetID, rule.ResultViewID = resultIDs.DatasetID, resultIDs.ViewID
		rule.CollectParams = setTaskResultDatasetID(rule.CollectParams, resultIDs.DatasetID)
	}
	if err := s.validateCollectionTaskDatasets(ctx, rule); err != nil {
		if s.resultManager != nil && !strings.EqualFold(strings.TrimSpace(rule.DataType), "kline_resample") {
			_ = s.resultManager.Delete(ctx, rule.SpaceID, resultIDs)
		}
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if err := s.ruleRepo.Create(ctx, rule); err != nil {
		if s.resultManager != nil && !strings.EqualFold(strings.TrimSpace(rule.DataType), "kline_resample") {
			_ = s.resultManager.Delete(ctx, rule.SpaceID, resultIDs)
		}
		log.ErrorContextf(ctx, "[Collector] create task rule failed: %v", err)
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	s.refreshRealtimeInventory(ctx)
	return &pb.CreateTaskRsp{RetInfo: retOK(), TaskId: rule.TaskID}, nil
}

// UpdateTask updates a collection task through the independent collector service.
func (s *Service) UpdateTask(ctx context.Context, req *pb.UpdateTaskReq) (*pb.UpdateTaskRsp, error) {
	spaceID := strings.TrimSpace(req.GetSpaceId())
	ruleID := strings.TrimSpace(req.GetTaskId())
	rule := fromPBTask(req.GetTask())
	if spaceID == "" {
		spaceID = strings.TrimSpace(rule.SpaceID)
	}
	if spaceID == "" {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if ruleID == "" {
		ruleID = strings.TrimSpace(rule.TaskID)
	}
	if ruleID == "" {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task_id is required")}, nil
	}
	if rule.TaskID == "" {
		rule.TaskID = ruleID
	}
	rule.SpaceID = spaceID
	rule = normalizeCollectionTask(rule)
	if strings.TrimSpace(rule.TaskName) == "" {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task_name is required")}, nil
	}
	if err := validateCollectionTask(rule); err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	existing, err := s.ruleRepo.GetByTaskID(ctx, spaceID, ruleID)
	if err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, err.Error())}, nil
	}
	if err := validateCollectionTaskUpdate(*existing, rule); err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	rule.ResultDatasetID, rule.ResultViewID = existing.ResultDatasetID, existing.ResultViewID
	rule.CollectParams = setTaskResultDatasetID(rule.CollectParams, existing.ResultDatasetID)
	preserveCollectionTaskCoverageStart(*existing, &rule)
	rule, err = canonicalizeCollectionTask(rule)
	if err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if strings.EqualFold(existing.DataType, "kline_resample") {
		rule.Creator = existing.Creator
		// Re-running preparation on an enabled update also recovers a transient
		// Metadata/Storage error without requiring a second operator-only API.
		if rule.Enabled {
			rule.PrepareState = domain.PrepareStatePending
			rule.LastError = ""
		} else {
			rule.PrepareState = existing.PrepareState
			rule.LastError = existing.LastError
		}
	}
	if err := s.validateCollectionTaskDatasets(ctx, rule); err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	updated, err := s.ruleRepo.UpdateByTaskID(ctx, spaceID, ruleID, rule)
	if err != nil {
		log.ErrorContextf(ctx, "[Collector] update task rule failed: %v", err)
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	s.refreshRealtimeInventory(ctx)
	return &pb.UpdateTaskRsp{RetInfo: retOK(), Task: toPBTask(*updated)}, nil
}

// DisableTask disables a collection task without deleting runtime history.
func (s *Service) DisableTask(ctx context.Context, req *pb.DisableTaskReq) (*pb.DisableTaskRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.DisableTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if strings.TrimSpace(req.GetTaskId()) == "" {
		return &pb.DisableTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task_id is required")}, nil
	}
	if err := s.ruleRepo.SetEnabled(ctx, req.GetSpaceId(), req.GetTaskId(), false); err != nil {
		log.ErrorContextf(ctx, "[Collector] disable task rule failed: %v", err)
		return &pb.DisableTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	s.refreshRealtimeInventory(ctx)
	return &pb.DisableTaskRsp{RetInfo: retOK()}, nil
}

// DeleteTask removes a task and its Collector runtime records. Result data is
// deleted only when the caller explicitly opts in; otherwise Storage metadata
// remains available for operators while the task disappears from Collector.
func (s *Service) DeleteTask(ctx context.Context, req *pb.DeleteTaskReq) (*pb.DeleteTaskRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetTaskId()) == "" {
		return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id and task_id are required")}, nil
	}
	spaceID, taskID := strings.TrimSpace(req.GetSpaceId()), strings.TrimSpace(req.GetTaskId())
	task, err := s.ruleRepo.GetByTaskID(ctx, spaceID, taskID)
	if err != nil {
		return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, err.Error())}, nil
	}
	if err := s.ruleRepo.SetEnabled(ctx, spaceID, taskID, false); err != nil {
		return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if err := s.persistence.DeleteTaskRuntime(ctx, spaceID, taskID); err != nil {
		return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if req.GetDeleteResultData() {
		if s.resultManager == nil {
			return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "task result manager is not configured; task remains disabled for retry")}, nil
		}
		ids := taskresult.IDs{DatasetID: task.ResultDatasetID, ViewID: task.ResultViewID}
		if ids.DatasetID == "" || ids.ViewID == "" {
			ids = taskresult.ResultIDs(spaceID, taskID)
		}
		if err := s.resultManager.DeleteForTask(ctx, spaceID, taskID, ids); err != nil {
			return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, fmt.Sprintf("delete task result failed; task remains disabled for retry: %v", err))}, nil
		}
	}
	if err := s.ruleRepo.DeleteByTaskID(ctx, spaceID, taskID); err != nil {
		return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	s.refreshRealtimeInventory(ctx)
	return &pb.DeleteTaskRsp{RetInfo: retOK()}, nil
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
		repoFilter.CollectionTaskID = filter.GetTaskId()
		repoFilter.InstanceID = filter.GetInstanceId()
		repoFilter.Provider = filter.GetProvider()
		repoFilter.SourceID = filter.GetSourceId()
		repoFilter.MarketType = filter.GetMarketType()
		repoFilter.DataType = filter.GetDataType()
		repoFilter.DatasetID = filter.GetDatasetId()
		repoFilter.SubjectID = filter.GetSubjectId()
		repoFilter.Frequency = filter.GetFrequency()
		repoFilter.FunctionName = filter.GetFunctionName()
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

// GetDataTypeConfigs returns the currently supported collection task data types.
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

func (s *Service) StartKlineResampleBackfill(ctx context.Context, req *pb.StartKlineResampleBackfillReq) (*pb.StartKlineResampleBackfillRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetTaskId()) == "" || strings.TrimSpace(req.GetRequestId()) == "" {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id, task_id and request_id are required")}, nil
	}
	start, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(req.GetStart()))
	if err != nil {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "start must be RFC3339")}, nil
	}
	end, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(req.GetEnd()))
	if err != nil {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "end must be RFC3339")}, nil
	}
	rule, err := s.ruleRepo.GetByTaskID(ctx, req.GetSpaceId(), req.GetTaskId())
	if err != nil {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, err.Error())}, nil
	}
	if !strings.EqualFold(rule.DataType, "kline_resample") {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "rule is not kline_resample")}, nil
	}
	if !rule.Enabled {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "rule is disabled")}, nil
	}
	if rule.PrepareState != domain.PrepareStateReady {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("rule is not ready (prepare_state=%s)", rule.PrepareState))}, nil
	}
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
	if err != nil {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	target, err := collectorresample.ParseFixedFrequency(params.TargetFrequency)
	if err != nil {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	backfill := domain.ResampleBackfillRequest{RequestID: req.GetRequestId(), Start: start.UTC(), End: end.UTC()}
	sourceKeepDuration := ""
	if s.datasetSrc != nil {
		source, sourceErr := s.datasetSrc.GetDataset(ctx, req.GetSpaceId(), params.SourceDatasetID)
		if sourceErr != nil {
			return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, sourceErr.Error())}, nil
		}
		sourceKeepDuration = source.KeepDuration
	}
	settleDelay := s.defaultResampleSettleDelay
	if settleDelay < 0 {
		settleDelay = defaultResampleSettleDelay
	}
	if err := validateResampleBackfillWindow(backfill, target.Duration, params.SettleDelayOr(settleDelay), sourceKeepDuration, time.Now().UTC()); err != nil {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if _, err := s.instanceRepo.StartResampleBackfill(ctx, req.GetSpaceId(), req.GetTaskId(), backfill); err != nil {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	return &pb.StartKlineResampleBackfillRsp{RetInfo: retOK()}, nil
}

func validateResampleBackfillWindow(request domain.ResampleBackfillRequest, target time.Duration, settleDelay time.Duration, sourceKeepDuration string, now time.Time) error {
	if err := request.ValidateForFrequency(target); err != nil {
		return err
	}
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if request.End.Add(settleDelay).After(now) {
		return fmt.Errorf("backfill end must be a closed bucket after settle delay")
	}
	if raw := strings.TrimSpace(sourceKeepDuration); raw != "" && raw != "0" {
		keep, err := time.ParseDuration(raw)
		if err != nil || keep <= 0 {
			return fmt.Errorf("source Dataset keep_duration %q is invalid", sourceKeepDuration)
		}
		if request.Start.Before(now.Add(-keep)) {
			return fmt.Errorf("backfill start is older than source Dataset retention %s", raw)
		}
	}
	return nil
}

func (s *Service) CancelKlineResampleBackfill(ctx context.Context, req *pb.CancelKlineResampleBackfillReq) (*pb.CancelKlineResampleBackfillRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetTaskId()) == "" || strings.TrimSpace(req.GetRequestId()) == "" {
		return &pb.CancelKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id, task_id and request_id are required")}, nil
	}
	if _, err := s.instanceRepo.CancelResampleBackfill(ctx, req.GetSpaceId(), req.GetTaskId(), req.GetRequestId()); err != nil {
		return &pb.CancelKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	return &pb.CancelKlineResampleBackfillRsp{RetInfo: retOK()}, nil
}

// GetKlineResampleBackfill returns one durable, server-side aggregate instead
// of forcing callers to scan an unbounded TaskInstance history.
func (s *Service) GetKlineResampleBackfill(ctx context.Context, req *pb.GetKlineResampleBackfillReq) (*pb.GetKlineResampleBackfillRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetTaskId()) == "" {
		return &pb.GetKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id and task_id are required")}, nil
	}
	instances := make([]domain.TaskInstance, 0)
	for page := 1; ; page++ {
		rows, total, err := s.instanceRepo.List(ctx, store.TaskInstanceFilter{SpaceID: req.GetSpaceId(), CollectionTaskID: req.GetTaskId(), DataType: "kline_resample", Page: page, PageSize: 1000})
		if err != nil {
			return &pb.GetKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
		instances = append(instances, rows...)
		if len(rows) == 0 || int64(len(instances)) >= total {
			break
		}
	}
	requestID := strings.TrimSpace(req.GetRequestId())
	if requestID == "" {
		for _, instance := range instances {
			result, err := domain.ParseResampleTaskResult(instance.Result)
			if err == nil && result.Backfill != nil && result.Backfill.RequestID != "" {
				requestID = result.Backfill.RequestID
				break
			}
		}
	}
	if requestID == "" {
		return &pb.GetKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "backfill request not found")}, nil
	}
	response := &pb.GetKlineResampleBackfillRsp{RetInfo: retOK(), RequestId: requestID}
	for _, instance := range instances {
		result, err := domain.ParseResampleTaskResult(instance.Result)
		if err != nil || result.Backfill == nil || result.Backfill.RequestID != requestID {
			continue
		}
		backfill := result.Backfill
		response.Participants++
		if response.Start == "" {
			response.Start = backfill.Start.UTC().Format(time.RFC3339Nano)
			response.End = backfill.End.UTC().Format(time.RFC3339Nano)
		}
		next := backfill.NextBucket.UTC().Format(time.RFC3339Nano)
		if response.NextBucket == "" || next < response.NextBucket {
			response.NextBucket = next
		}
		switch backfill.State {
		case domain.ResampleBackfillRunning:
			response.Running++
		case domain.ResampleBackfillWaitingSource:
			response.WaitingSource++
		case domain.ResampleBackfillSyncing:
			response.Syncing++
		case domain.ResampleBackfillComplete:
			response.Complete++
		case domain.ResampleBackfillFailed:
			response.Failed++
		case domain.ResampleBackfillCanceled:
			response.Canceled++
		}
		if len(response.Errors) < 20 && strings.TrimSpace(result.LastError) != "" {
			response.Errors = append(response.Errors, result.LastError)
		}
	}
	if response.Participants == 0 {
		return &pb.GetKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "backfill request not found")}, nil
	}
	// An active participant must remain visible even when another participant
	// has already failed. This lets the operator cancel the remaining work and
	// retry the request instead of seeing a terminal state that is not actually
	// terminal for the whole request.
	if response.Syncing > 0 {
		response.State = string(domain.ResampleBackfillSyncing)
	} else if response.WaitingSource > 0 {
		response.State = string(domain.ResampleBackfillWaitingSource)
	} else if response.Running > 0 {
		response.State = string(domain.ResampleBackfillRunning)
	} else if response.Failed == response.Participants {
		response.State = string(domain.ResampleBackfillFailed)
	} else if response.Canceled == response.Participants {
		response.State = string(domain.ResampleBackfillCanceled)
	} else if response.Complete == response.Participants {
		response.State = string(domain.ResampleBackfillComplete)
	} else if response.Failed > 0 {
		response.State = string(domain.ResampleBackfillFailed)
	} else if response.Canceled > 0 {
		response.State = string(domain.ResampleBackfillCanceled)
	} else {
		response.State = string(domain.ResampleBackfillRunning)
	}
	return response, nil
}

func normalizeCollectionTask(rule domain.CollectionTask) domain.CollectionTask {
	rule.TaskID = strings.TrimSpace(rule.TaskID)
	if rule.TaskID == "" {
		rule.TaskID = fmt.Sprintf("task_%d", time.Now().UnixNano())
	}
	if strings.TrimSpace(rule.CollectParams) == "" {
		rule.CollectParams = "{}"
	}
	if params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType); err == nil && strings.TrimSpace(params.TargetDatasetID) == "" {
		rule.CollectParams = setTaskResultDatasetID(rule.CollectParams, taskresult.ResultIDs(rule.SpaceID, rule.TaskID).DatasetID)
	}
	return rule
}

func setTaskResultDatasetID(raw, datasetID string) string {
	values := make(map[string]any)
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return raw
	}
	values["target_dataset_id"] = strings.TrimSpace(datasetID)
	encoded, err := json.Marshal(values)
	if err != nil {
		return raw
	}
	return string(encoded)
}

func firstTaskFrequency(params *domain.CollectParams) string {
	if params == nil {
		return ""
	}
	if len(params.Collector.Intervals) > 0 {
		return params.Collector.Intervals[0]
	}
	return params.TargetFrequency
}

func validateCollectionTask(rule domain.CollectionTask) error {
	if strings.TrimSpace(rule.TaskID) == "" {
		return fmt.Errorf("task_id is required")
	}
	if strings.TrimSpace(rule.SpaceID) == "" {
		return fmt.Errorf("space_id is required")
	}
	if strings.TrimSpace(rule.DataType) == "" {
		return fmt.Errorf("data_type is required")
	}
	if strings.TrimSpace(rule.Provider) == "" {
		return fmt.Errorf("provider is required")
	}
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
	if err != nil {
		return fmt.Errorf("invalid collect_params: %w", err)
	}
	if err := params.Validate(); err != nil {
		return fmt.Errorf("invalid collect_params: %w", err)
	}
	if strings.EqualFold(params.Provider, "stockcn_multi") &&
		!strings.EqualFold(params.Collector.DataType, "kline") &&
		!strings.EqualFold(params.Collector.DataType, domain.InstrumentDataType) {
		return fmt.Errorf("stockcn_multi only supports kline and instrument collectors")
	}
	if !strings.EqualFold(params.Provider, "stockcn_multi") {
		definition, ok := jobs.JobDefinitionByDataType(params.Collector.DataType)
		if !ok || !definition.ExecutionMode.Valid() || !definition.Matches(params) {
			return fmt.Errorf(
				"unsupported collector: exchange=%s market=%s data_type=%s source_kind=%s",
				params.Collector.Exchange,
				params.Collector.Market,
				params.Collector.DataType,
				params.Source.Kind,
			)
		}
		if definition.ExecutionMode == jobs.ExecutionModeCloudInvoke {
			if _, routeOK := jobs.JobRouteFor(params.Collector.Exchange, params.Collector.DataType); !routeOK {
				return fmt.Errorf("cloud collector route not found: exchange=%s data_type=%s", params.Collector.Exchange, params.Collector.DataType)
			}
		}
	}
	if !strings.EqualFold(rule.Provider, params.Provider) ||
		!strings.EqualFold(rule.MarketType, params.MarketType) ||
		!strings.EqualFold(rule.DataType, params.Collector.DataType) {
		return fmt.Errorf("task identity does not match collect_params")
	}
	return nil
}

func preserveCollectionTaskCoverageStart(existing domain.CollectionTask, desired *domain.CollectionTask) {
	if desired == nil {
		return
	}
	if existing.CoverageStartTime == nil || existing.CoverageStartTime.IsZero() {
		desired.CoverageStartTime = nil
		return
	}
	at := existing.CoverageStartTime.UTC()
	desired.CoverageStartTime = &at
}

func canonicalizeCollectionTask(rule domain.CollectionTask) (domain.CollectionTask, error) {
	if !strings.EqualFold(strings.TrimSpace(rule.DataType), "kline_resample") {
		if rule.PrepareState == "" {
			rule.PrepareState = domain.PrepareStateReady
		}
		return rule, nil
	}
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
	if err != nil {
		return rule, err
	}
	canonical, err := params.CanonicalJSON()
	if err != nil {
		return rule, err
	}
	rule.CollectParams = canonical
	if rule.PrepareState == "" || rule.PrepareState == domain.PrepareStateReady {
		rule.PrepareState = domain.PrepareStatePending
	}
	rule.LastError = ""
	return rule, nil
}

func validateCollectionTaskUpdate(existing, desired domain.CollectionTask) error {
	if existing.SpaceID != desired.SpaceID || existing.TaskID != desired.TaskID || !strings.EqualFold(existing.DataType, desired.DataType) || !strings.EqualFold(existing.Provider, desired.Provider) || !strings.EqualFold(existing.MarketType, desired.MarketType) {
		return fmt.Errorf("task identity and data source cannot change; create a new task")
	}
	existingParams, err := domain.ParseCollectParams(existing.CollectParams, existing.Provider, existing.MarketType, existing.DataType)
	if err != nil {
		return fmt.Errorf("parse existing resample rule: %w", err)
	}
	desiredParams, err := domain.ParseCollectParams(desired.CollectParams, desired.Provider, desired.MarketType, desired.DataType)
	if err != nil {
		return fmt.Errorf("parse desired resample rule: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(existing.DataType), "kline_resample") {
		existingCanonical, _ := existingParams.CanonicalJSON()
		desiredCanonical, _ := desiredParams.CanonicalJSON()
		if existingCanonical != desiredCanonical {
			return fmt.Errorf("collection parameters cannot change; create a new task")
		}
		return nil
	}
	if err := domain.ValidateSameResampleIdentity(existingParams, desiredParams); err != nil {
		return fmt.Errorf("%w; create a new rule/task", err)
	}
	return nil
}

func (s *Service) validateCollectionTaskDatasets(ctx context.Context, rule domain.CollectionTask) error {
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
	if err != nil {
		return err
	}
	// stockcn_multi is the public multi-provider collector identity, while
	// the shared stock datasets belong to the stockcn data source.
	datasetSourceID := collectorDatasetSourceID(params.Collector.Exchange)
	switch params.Collector.DataType {
	case domain.InstrumentDataType:
		return s.validateDataset(
			ctx,
			rule.SpaceID,
			params.Target.DatasetID,
			datasetSourceID,
			storagepb.DataKind_DATA_KIND_RECORD,
			"target",
			false,
			params.MarketType,
			nil,
		)
	case "kline":
		if err := s.validateDataset(
			ctx,
			rule.SpaceID,
			params.Source.DatasetID,
			datasetSourceID,
			storagepb.DataKind_DATA_KIND_RECORD,
			"source",
			false,
			params.Collector.Market,
			nil,
		); err != nil {
			return err
		}
		return s.validateDataset(
			ctx,
			rule.SpaceID,
			params.Target.DatasetID,
			datasetSourceID,
			storagepb.DataKind_DATA_KIND_TIME_SERIES,
			"target",
			true,
			params.Collector.Market,
			params.Collector.Intervals,
		)
	case "kline_resample":
		return s.validateResampleSourceDataset(ctx, rule, params)
	default:
		return fmt.Errorf("unsupported collector data_type: %s", params.Collector.DataType)
	}
}

func collectorDatasetSourceID(exchange string) string {
	if strings.EqualFold(strings.TrimSpace(exchange), "stockcn_multi") {
		return "stockcn"
	}
	return exchange
}

func (s *Service) validateResampleSourceDataset(ctx context.Context, rule domain.CollectionTask, params *domain.CollectParams) error {
	info, err := s.datasetSrc.GetDataset(ctx, rule.SpaceID, params.SourceDatasetID)
	if err != nil {
		return fmt.Errorf("source Dataset %s is unavailable: %w", params.SourceDatasetID, err)
	}
	if info.Status != "active" {
		return fmt.Errorf("source Dataset %s must be active", params.SourceDatasetID)
	}
	if info.DataKind != storagepb.DataKind_DATA_KIND_TIME_SERIES {
		return fmt.Errorf("source Dataset %s must be time_series", params.SourceDatasetID)
	}
	if strings.EqualFold(strings.TrimSpace(info.Attributes["dataset_role"]), "kline_resample_result") {
		return fmt.Errorf("source Dataset %s cannot be another resample result", params.SourceDatasetID)
	}
	actualMarket := strings.ToLower(strings.TrimSpace(info.Attributes["market_type"]))
	if actualMarket == "" || actualMarket != strings.ToLower(strings.TrimSpace(rule.MarketType)) {
		return fmt.Errorf("source Dataset %s market_type=%s does not match rule market_type=%s", params.SourceDatasetID, actualMarket, rule.MarketType)
	}
	wantedFrequency, normalizeErr := report.NormalizeDatasetFrequency(strings.TrimSpace(params.SourceFrequency))
	if normalizeErr != nil {
		return fmt.Errorf("source Dataset %s frequency %q is invalid: %w", params.SourceDatasetID, params.SourceFrequency, normalizeErr)
	}
	for _, frequency := range info.Freqs {
		actualFrequency, frequencyErr := report.NormalizeDatasetFrequency(strings.TrimSpace(frequency))
		if frequencyErr == nil && actualFrequency == wantedFrequency {
			// Dataset metadata adapters that expose column discovery return a
			// non-nil ColumnTypes map. Validate both presence and logical type so
			// an empty or malformed schema cannot enter ready and spin forever.
			if info.ColumnTypes != nil {
				expected := map[string]storagepb.FieldValueType{
					"open": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "high": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
					"low": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "close": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
					"volume": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "quote_volume": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
					"trade_num": storagepb.FieldValueType_FIELD_VALUE_TYPE_INT,
				}
				missing := make([]string, 0)
				for column, expectedType := range expected {
					actualType, ok := info.ColumnTypes[column]
					if !ok {
						missing = append(missing, column)
						continue
					}
					if actualType != expectedType {
						return fmt.Errorf("source Dataset %s column %s has value_type=%s, want %s", params.SourceDatasetID, column, actualType.String(), expectedType.String())
					}
				}
				if len(missing) != 0 {
					sort.Strings(missing)
					return fmt.Errorf("source Dataset %s missing active K-line columns: %s", params.SourceDatasetID, strings.Join(missing, ","))
				}
			}
			return nil
		}
	}
	return fmt.Errorf("source Dataset %s does not enable frequency %q", params.SourceDatasetID, params.SourceFrequency)
}

func (s *Service) validateDataset(
	ctx context.Context,
	spaceID string,
	datasetID string,
	exchange string,
	expectedKind storagepb.DataKind,
	role string,
	allowSharedMarket bool,
	marketType string,
	requiredFreqs []string,
) error {
	info, err := s.datasetSrc.GetDataset(ctx, spaceID, datasetID)
	if err != nil {
		return fmt.Errorf("%s Dataset %s is unavailable: %w", role, datasetID, err)
	}
	if info.Status != "active" {
		return fmt.Errorf("%s Dataset %s must be active", role, datasetID)
	}
	sourceMatches := strings.EqualFold(info.DataSourceID, exchange) ||
		(allowSharedMarket && strings.EqualFold(info.DataSourceID, "crypto"))
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
	if marketType = strings.ToLower(strings.TrimSpace(marketType)); marketType != "" {
		actual := strings.ToLower(strings.TrimSpace(info.Attributes["market_type"]))
		if actual == "" {
			return fmt.Errorf("%s Dataset %s must declare attributes.market_type", role, datasetID)
		}
		if actual != marketType {
			return fmt.Errorf("%s Dataset %s market_type=%s does not match rule market_type=%s", role, datasetID, actual, marketType)
		}
	}
	if len(requiredFreqs) > 0 {
		available := make(map[string]struct{}, len(info.Freqs))
		for _, value := range info.Freqs {
			available[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
		}
		for _, value := range requiredFreqs {
			if _, ok := available[strings.ToLower(strings.TrimSpace(value))]; !ok {
				return fmt.Errorf("%s Dataset %s does not enable frequency %q", role, datasetID, value)
			}
		}
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
