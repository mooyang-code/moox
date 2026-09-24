// Package rpc implements the independent Collector management RPC service.
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
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
	taskRepo                   *store.TaskRepository
	instanceRepo               *store.TaskInstanceRepository
	datasetSrc                 datasetSource
	inventory                  RealtimeInventory
	defaultResampleSettleDelay time.Duration
	resultManager              *taskresult.Manager
	resultDataNodeID           string
	createTaskMu               sync.Mutex
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
		taskRepo:                   persistence.Tasks(),
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

// GetTaskList returns task data from the new collector DB.
func (s *Service) GetTaskList(ctx context.Context, req *pb.GetTaskListReq) (*pb.GetTaskListRsp, error) {
	if req == nil {
		return &pb.GetTaskListRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "request is required")}, nil
	}
	spaceID := strings.TrimSpace(req.GetSpaceId())
	if spaceID == "" {
		return &pb.GetTaskListRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	page, size := pageParams(req.GetPage())
	tasks, total, err := s.taskRepo.List(ctx, store.TaskFilter{
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
		log.ErrorContextf(ctx, "[Collector] list collection tasks failed: %v", err)
		return &pb.GetTaskListRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	out := make([]*pb.CollectionTask, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, s.toPBTask(ctx, task))
	}
	return &pb.GetTaskListRsp{RetInfo: retOK(), Tasks: out, Page: pageResult(page, size, total)}, nil
}

// GetTaskDetail returns a single collection task.
func (s *Service) GetTaskDetail(ctx context.Context, req *pb.GetTaskDetailReq) (*pb.GetTaskDetailRsp, error) {
	if req == nil {
		return &pb.GetTaskDetailRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "request is required")}, nil
	}
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.GetTaskDetailRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if strings.TrimSpace(req.GetTaskId()) == "" {
		return &pb.GetTaskDetailRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task_id is required")}, nil
	}
	task, err := s.taskRepo.GetByTaskID(ctx, req.GetSpaceId(), req.GetTaskId())
	if err != nil {
		return &pb.GetTaskDetailRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, err.Error())}, nil
	}
	return &pb.GetTaskDetailRsp{RetInfo: retOK(), Task: s.toPBTask(ctx, *task)}, nil
}

// toPBTask enriches the public task with the current task-owned Storage
// metadata state. The Collector database stores the stable result identity,
// while readiness and indexed watermarks are derived from Storage.
func (s *Service) toPBTask(ctx context.Context, task domain.CollectionTask) *pb.CollectionTask {
	result := toPBTask(task)
	if s == nil || s.resultManager == nil || result == nil || result.Result == nil {
		return result
	}
	inspection, err := s.resultManager.Inspect(ctx, task.SpaceID, task.TaskID)
	if err != nil {
		log.WarnContextf(ctx, "[Collector] inspect task result failed space=%s task=%s: %v", task.SpaceID, task.TaskID, err)
		result.Result.Status = taskresult.ResultStatusError
		if result.LastError == "" {
			result.LastError = inspection.Error
		}
		return result
	}
	if task.PrepareState == domain.PrepareStateError || strings.TrimSpace(task.LastError) != "" {
		result.Result.Status = taskresult.ResultStatusError
		return result
	}
	result.Result.Status = inspection.Status
	result.Result.LastDataTime = inspection.LastDataTime
	result.Result.CoverageStart = inspection.CoverageStart
	result.Result.CoverageEnd = inspection.CoverageEnd
	if inspection.Error != "" && result.LastError == "" {
		result.LastError = inspection.Error
	}
	return result
}

// CreateTask creates a collection task through the independent collector service.
func (s *Service) CreateTask(ctx context.Context, req *pb.CreateTaskReq) (*pb.CreateTaskRsp, error) {
	// Result metadata is task-exclusive. Serialize the local check/provision/
	// insert sequence so two UI retries cannot compensate resources provisioned
	// by the winning request.
	s.createTaskMu.Lock()
	defer s.createTaskMu.Unlock()
	if req == nil || req.GetTask() == nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task is required")}, nil
	}
	requestedTaskID := strings.TrimSpace(req.GetTask().GetTaskId())
	if result := req.GetTask().GetResult(); result != nil && strings.TrimSpace(result.GetViewId()) != "" {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "result.view_id cannot be specified when creating a task")}, nil
	}
	task := fromPBTask(req.GetTask())
	if err := validateCreateTaskResultIdentity(task.CollectParams); err != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	task = normalizeCollectionTask(task)
	if err := domain.ValidateCollectionTaskName(task.TaskName); err != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	var err error
	params, err := domain.ParseCollectParams(task.CollectParams, task.Provider, task.MarketType, task.DataType)
	if err != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	task, _, err = assignGeneratedCollectionTaskResult(task, params)
	if err != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	params, err = domain.ParseCollectParams(task.CollectParams, task.Provider, task.MarketType, task.DataType)
	if err != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if err := validateCollectionTask(task); err != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	task, err = canonicalizeCollectionTask(task)
	if err != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if _, lookupErr := s.taskRepo.GetByTaskID(ctx, task.SpaceID, task.TaskID); lookupErr == nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("task %s already exists", task.TaskID))}, nil
	} else if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, lookupErr.Error())}, nil
	}
	if _, lookupErr := s.taskRepo.GetByTaskName(ctx, task.SpaceID, task.TaskName); lookupErr == nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("task_name %q already exists in space %q", task.TaskName, task.SpaceID))}, nil
	} else if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, lookupErr.Error())}, nil
	}
	if requestedTaskID != "" && s.resultManager != nil {
		inspection, inspectErr := s.resultManager.Inspect(ctx, task.SpaceID, task.TaskID)
		if inspectErr != nil {
			return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, fmt.Sprintf("inspect retained task result before create: %v", inspectErr))}, nil
		}
		if inspection.Dataset != nil || inspection.View != nil {
			return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task_id has retained result data; create the task with a new task_id")}, nil
		}
	}
	resultConfig := req.GetResultConfig()
	dataNodeID := s.resultDataNodeID
	keepDuration, description := "0", task.Description
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
	var cleanupResult func(context.Context) error
	if strings.EqualFold(strings.TrimSpace(task.DataType), "kline_resample") {
		// Resample results use the same task-exclusive identity. Their
		// Dataset/View are prepared asynchronously with source lineage.
		resultIDs = taskresult.ResultIDs(task.SpaceID, task.TaskID)
		task.ResultDatasetID = resultIDs.DatasetID
		task.ResultViewID = resultIDs.ViewID
		task.CollectParams = setTaskResultDatasetID(task.CollectParams, resultIDs.DatasetID)
		params, err = domain.ParseCollectParams(task.CollectParams, task.Provider, task.MarketType, task.DataType)
		if err != nil {
			return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
		}
	} else {
		if s.resultManager == nil {
			return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "task result manager is not configured")}, nil
		}
		var err error
		resultIDs, cleanupResult, err = s.resultManager.EnsureWithCleanup(ctx, task.SpaceID, task.TaskID, task.DataType, task.MarketType, taskresult.Config{DataNodeID: dataNodeID, KeepDuration: keepDuration, Name: task.TaskName, Description: description, DataSourceID: task.Provider, Frequency: firstTaskFrequency(params), Frequencies: append([]string(nil), params.Collector.Intervals...)})
		if err != nil {
			return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
		task.ResultDatasetID, task.ResultViewID = resultIDs.DatasetID, resultIDs.ViewID
		task.CollectParams = setTaskResultDatasetID(task.CollectParams, resultIDs.DatasetID)
	}
	if err := s.validateCollectionTaskDatasets(ctx, task); err != nil {
		if cleanupResult != nil {
			if cleanupErr := cleanupResult(ctx); cleanupErr != nil {
				log.ErrorContextf(ctx, "[Collector] cleanup uncommitted task result failed: %v", cleanupErr)
				return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, fmt.Sprintf("%v; result compensation failed: %v", err, cleanupErr))}, nil
			}
		}
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if err := s.taskRepo.Create(ctx, task); err != nil {
		log.ErrorContextf(ctx, "[Collector] create collection task failed: %v", err)
		// A second process may still win the same task-id race. Preserve the
		// winner's result resources; cleanup remains appropriate for a distinct
		// conflict such as a duplicate task name.
		preserveResult := false
		if existing, lookupErr := s.taskRepo.GetByTaskID(ctx, task.SpaceID, task.TaskID); lookupErr == nil && existing != nil {
			preserveResult = true
		}
		if cleanupResult != nil && !preserveResult {
			if cleanupErr := cleanupResult(ctx); cleanupErr != nil {
				log.ErrorContextf(ctx, "[Collector] cleanup uncommitted task result failed: %v", cleanupErr)
				return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, fmt.Sprintf("%v; result compensation failed: %v", err, cleanupErr))}, nil
			}
		}
		if existing, lookupErr := s.taskRepo.GetByTaskName(ctx, task.SpaceID, task.TaskName); lookupErr == nil && existing != nil && existing.TaskID != task.TaskID {
			return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("task_name %q already exists in space %q", task.TaskName, task.SpaceID))}, nil
		}
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	s.refreshRealtimeInventory(ctx)
	return &pb.CreateTaskRsp{RetInfo: retOK(), TaskId: task.TaskID}, nil
}

// UpdateTask updates a collection task through the independent collector service.
func (s *Service) UpdateTask(ctx context.Context, req *pb.UpdateTaskReq) (*pb.UpdateTaskRsp, error) {
	if req == nil || req.GetTask() == nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task is required")}, nil
	}
	spaceID := strings.TrimSpace(req.GetSpaceId())
	taskID := strings.TrimSpace(req.GetTaskId())
	requested := fromPBTask(req.GetTask())
	requestedSpaceID := strings.TrimSpace(requested.SpaceID)
	requestedTaskID := strings.TrimSpace(requested.TaskID)
	if spaceID != "" && requestedSpaceID != "" && spaceID != requestedSpaceID {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id cannot change")}, nil
	}
	if taskID != "" && requestedTaskID != "" && taskID != requestedTaskID {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task_id cannot change")}, nil
	}
	if spaceID == "" {
		spaceID = requestedSpaceID
	}
	if spaceID == "" {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if taskID == "" {
		taskID = requestedTaskID
	}
	if taskID == "" {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task_id is required")}, nil
	}
	if err := validateTaskResultConfigUpdate(req.GetResultConfig()); err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	existing, err := s.taskRepo.GetByTaskID(ctx, spaceID, taskID)
	if err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, err.Error())}, nil
	}
	if err := validateTaskResultIdentityUpdate(*existing, req.GetTask()); err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	task := *existing
	task.TaskID = taskID
	task.SpaceID = spaceID
	if strings.TrimSpace(requested.TaskName) == "" {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task_name is required")}, nil
	}
	task.TaskName = domain.NormalizeCollectionTaskName(requested.TaskName)
	task.Description = requested.Description
	if req.GetTask().Enabled != nil {
		task.Enabled = req.GetTask().GetEnabled()
	}
	if dataType := strings.TrimSpace(requested.DataType); dataType != "" {
		task.DataType = dataType
	}
	if provider := strings.TrimSpace(requested.Provider); provider != "" {
		task.Provider = provider
	}
	if marketType := strings.TrimSpace(requested.MarketType); marketType != "" {
		task.MarketType = marketType
	}
	if creator := strings.TrimSpace(requested.Creator); creator != "" {
		task.Creator = creator
	}
	if req.GetTask().GetCollectParams() != nil {
		task.CollectParams, err = inheritCollectionTaskResultDatasetID(
			jsonStringFromStruct(req.GetTask().GetCollectParams()),
			existing.ResultDatasetID,
		)
		if err != nil {
			return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
		}
	}
	task.ResultDatasetID, task.ResultViewID = existing.ResultDatasetID, existing.ResultViewID
	if err := domain.ValidateCollectionTaskName(task.TaskName); err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if err := validateCollectionTaskUpdate(*existing, task); err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if task.Enabled {
		preserveCollectionTaskCoverageStart(*existing, &task)
	} else {
		task.CoverageStartTime = nil
	}
	task, err = canonicalizeCollectionTask(task)
	if err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if strings.EqualFold(existing.DataType, "kline_resample") {
		task.Creator = existing.Creator
		// Re-running preparation on an enabled update also recovers a transient
		// metadata/storage error without requiring a second operator-only API.
		if task.Enabled {
			task.PrepareState = domain.PrepareStateWaitingView
			task.LastError = ""
		} else {
			task.PrepareState = existing.PrepareState
			task.LastError = existing.LastError
		}
	}
	if task.TaskName != existing.TaskName {
		if named, lookupErr := s.taskRepo.GetByTaskName(ctx, spaceID, task.TaskName); lookupErr == nil && named.TaskID != existing.TaskID {
			return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("task_name %q already exists in space %q", task.TaskName, spaceID))}, nil
		} else if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, lookupErr.Error())}, nil
		}
	}
	if err := s.validateCollectionTaskDatasets(ctx, task); err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	updated, err := s.taskRepo.UpdateMutableByTaskID(ctx, spaceID, taskID, task)
	if err != nil {
		log.ErrorContextf(ctx, "[Collector] update collection task failed: %v", err)
		if named, lookupErr := s.taskRepo.GetByTaskName(ctx, spaceID, task.TaskName); lookupErr == nil && named != nil && named.TaskID != existing.TaskID {
			return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("task_name %q already exists in space %q", task.TaskName, spaceID))}, nil
		}
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	s.refreshRealtimeInventory(ctx)
	return &pb.UpdateTaskRsp{RetInfo: retOK(), Task: s.toPBTask(ctx, *updated)}, nil
}

// DisableTask disables a collection task without deleting runtime history.
func (s *Service) DisableTask(ctx context.Context, req *pb.DisableTaskReq) (*pb.DisableTaskRsp, error) {
	if req == nil {
		return &pb.DisableTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "request is required")}, nil
	}
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.DisableTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if strings.TrimSpace(req.GetTaskId()) == "" {
		return &pb.DisableTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task_id is required")}, nil
	}
	if err := s.taskRepo.SetEnabled(ctx, req.GetSpaceId(), req.GetTaskId(), false); err != nil {
		log.ErrorContextf(ctx, "[Collector] disable collection task failed: %v", err)
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
	task, err := s.taskRepo.GetByTaskID(ctx, spaceID, taskID)
	if err != nil {
		return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, err.Error())}, nil
	}
	var resultIDs taskresult.IDs
	if req.GetDeleteResultData() {
		if s.resultManager == nil {
			return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "task result manager is not configured; task remains enabled for retry")}, nil
		}
		resultIDs, err = collectionTaskResultIDs(*task)
		if err != nil {
			return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
		}
		if err := s.resultManager.ValidateOwnedForTask(ctx, spaceID, taskID, resultIDs); err != nil {
			return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, fmt.Sprintf("delete task result preflight failed; task remains enabled: %v", err))}, nil
		}
	}
	if err := s.taskRepo.SetEnabled(ctx, spaceID, taskID, false); err != nil {
		return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if err := s.persistence.WaitTaskDrain(ctx, spaceID, taskID, 30*time.Second); err != nil {
		return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, fmt.Sprintf("task drain failed; task remains disabled for retry: %v", err))}, nil
	}
	if err := s.persistence.DeleteTaskRuntime(ctx, spaceID, taskID); err != nil {
		return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if req.GetDeleteResultData() {
		if err := s.resultManager.DeleteForTask(ctx, spaceID, taskID, resultIDs); err != nil {
			return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, fmt.Sprintf("delete task result failed; task remains disabled for retry: %v", err))}, nil
		}
	}
	if err := s.taskRepo.DeleteByTaskID(ctx, spaceID, taskID); err != nil {
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

// GetDataTypeConfigWithFields returns field metadata for the task form.
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
	task, err := s.taskRepo.GetByTaskID(ctx, req.GetSpaceId(), req.GetTaskId())
	if err != nil {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, err.Error())}, nil
	}
	if !strings.EqualFold(task.DataType, "kline_resample") {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task is not kline_resample")}, nil
	}
	if !task.Enabled {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task is disabled")}, nil
	}
	if task.PrepareState != domain.PrepareStateReady {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("task is not ready (prepare_state=%s)", task.PrepareState))}, nil
	}
	params, err := domain.ParseCollectParams(task.CollectParams, task.Provider, task.MarketType, task.DataType)
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

func normalizeCollectionTask(task domain.CollectionTask) domain.CollectionTask {
	task.SpaceID = strings.TrimSpace(task.SpaceID)
	task.TaskID = strings.TrimSpace(task.TaskID)
	if task.TaskID == "" {
		task.TaskID = "task_" + uuid.NewString()
	}
	task.TaskName = domain.NormalizeCollectionTaskName(task.TaskName)
	task.Description = strings.TrimSpace(task.Description)
	task.DataType = strings.TrimSpace(task.DataType)
	task.Provider = strings.TrimSpace(task.Provider)
	task.MarketType = strings.TrimSpace(task.MarketType)
	if strings.TrimSpace(task.CollectParams) == "" {
		task.CollectParams = "{}"
	}
	return task
}

func validateCreateTaskResultIdentity(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "{}"
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	for _, field := range []string{
		"target_dataset_id",
		"target_view_id",
		"result_dataset_id",
		"result_view_id",
		"result_id",
		"view_id",
	} {
		if _, exists := values[field]; exists {
			return fmt.Errorf("collect_params.%s cannot be specified when creating a task", field)
		}
	}
	return nil
}

func assignGeneratedCollectionTaskResult(task domain.CollectionTask, params *domain.CollectParams) (domain.CollectionTask, taskresult.IDs, error) {
	if params == nil {
		return task, taskresult.IDs{}, fmt.Errorf("collect params are required")
	}
	var ids taskresult.IDs
	ids = taskresult.ResultIDs(task.SpaceID, task.TaskID)
	task.ResultDatasetID, task.ResultViewID = ids.DatasetID, ids.ViewID
	task.CollectParams = setTaskResultDatasetID(task.CollectParams, ids.DatasetID)
	return task, ids, nil
}

func inheritCollectionTaskResultDatasetID(raw, datasetID string) (string, error) {
	var values map[string]any
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return raw, fmt.Errorf("invalid collect_params: %w", err)
	}
	if values == nil {
		return raw, fmt.Errorf("invalid collect_params: object is required")
	}
	if _, exists := values["target_dataset_id"]; !exists {
		values["target_dataset_id"] = strings.TrimSpace(datasetID)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return raw, fmt.Errorf("encode collect_params: %w", err)
	}
	return string(encoded), nil
}

func validateTaskResultConfigUpdate(config *pb.ResultConfig) error {
	if config == nil {
		return nil
	}
	if strings.TrimSpace(config.GetDataNodeId()) != "" ||
		strings.TrimSpace(config.GetKeepDuration()) != "" ||
		strings.TrimSpace(config.GetDescription()) != "" {
		return fmt.Errorf("result_config cannot change an existing task result")
	}
	return nil
}

func validateTaskResultIdentityUpdate(existing domain.CollectionTask, requested *pb.CollectionTask) error {
	if requested == nil || requested.GetResult() == nil {
		return nil
	}
	if viewID := strings.TrimSpace(requested.GetResult().GetViewId()); viewID != "" && viewID != existing.ResultViewID {
		return fmt.Errorf("task result identity cannot change; create a new task")
	}
	return nil
}

func collectionTaskResultIDs(task domain.CollectionTask) (taskresult.IDs, error) {
	return taskresult.ResultIDs(task.SpaceID, task.TaskID), nil
}

func setTaskResultDatasetID(raw, datasetID string) string {
	values := make(map[string]any)
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return raw
	}
	if values == nil {
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

func validateCollectionTask(task domain.CollectionTask) error {
	if strings.TrimSpace(task.TaskID) == "" {
		return fmt.Errorf("task_id is required")
	}
	if strings.TrimSpace(task.SpaceID) == "" {
		return fmt.Errorf("space_id is required")
	}
	if err := domain.ValidateCollectionTaskName(task.TaskName); err != nil {
		return err
	}
	if strings.TrimSpace(task.DataType) == "" {
		return fmt.Errorf("data_type is required")
	}
	if strings.TrimSpace(task.Provider) == "" {
		return fmt.Errorf("provider is required")
	}
	params, err := domain.ParseCollectParams(task.CollectParams, task.Provider, task.MarketType, task.DataType)
	if err != nil {
		return fmt.Errorf("invalid collect_params: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(task.DataType), "kline_resample") {
		if err := collectorresample.ValidateTaskParams(params); err != nil {
			return fmt.Errorf("invalid collect_params: %w", err)
		}
	} else if err := params.Validate(); err != nil {
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
	if !strings.EqualFold(task.Provider, params.Provider) ||
		!strings.EqualFold(task.MarketType, params.MarketType) ||
		!strings.EqualFold(task.DataType, params.Collector.DataType) {
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

func canonicalizeCollectionTask(task domain.CollectionTask) (domain.CollectionTask, error) {
	if !strings.EqualFold(strings.TrimSpace(task.DataType), "kline_resample") {
		if task.PrepareState == "" {
			task.PrepareState = domain.PrepareStateReady
		}
		return task, nil
	}
	params, err := domain.ParseCollectParams(task.CollectParams, task.Provider, task.MarketType, task.DataType)
	if err != nil {
		return task, err
	}
	canonical, err := params.CanonicalJSON()
	if err != nil {
		return task, err
	}
	task.CollectParams = canonical
	if task.PrepareState == "" || task.PrepareState == domain.PrepareStateReady {
		task.PrepareState = domain.PrepareStateWaitingView
	}
	task.LastError = ""
	return task, nil
}

func validateCollectionTaskUpdate(existing, desired domain.CollectionTask) error {
	if existing.SpaceID != desired.SpaceID ||
		existing.TaskID != desired.TaskID ||
		strings.TrimSpace(existing.DataType) != strings.TrimSpace(desired.DataType) ||
		strings.TrimSpace(existing.Provider) != strings.TrimSpace(desired.Provider) ||
		strings.TrimSpace(existing.MarketType) != strings.TrimSpace(desired.MarketType) {
		return fmt.Errorf("task identity and data source cannot change; create a new task")
	}
	if strings.TrimSpace(existing.ResultDatasetID) != strings.TrimSpace(desired.ResultDatasetID) ||
		strings.TrimSpace(existing.ResultViewID) != strings.TrimSpace(desired.ResultViewID) {
		return fmt.Errorf("task result identity cannot change; create a new task")
	}
	if strings.TrimSpace(existing.Creator) != strings.TrimSpace(desired.Creator) {
		return fmt.Errorf("task creator cannot change; create a new task")
	}
	if err := domain.ValidateCollectionTaskName(desired.TaskName); err != nil {
		return err
	}
	existingParams, err := domain.ParseCollectParams(existing.CollectParams, existing.Provider, existing.MarketType, existing.DataType)
	if err != nil {
		return fmt.Errorf("parse existing task collect_params: %w", err)
	}
	desiredParams, err := domain.ParseCollectParams(desired.CollectParams, desired.Provider, desired.MarketType, desired.DataType)
	if err != nil {
		return fmt.Errorf("parse desired task collect_params: %w", err)
	}
	existingCanonical, err := existingParams.CanonicalJSON()
	if err != nil {
		return fmt.Errorf("canonicalize existing task collect_params: %w", err)
	}
	desiredCanonical, err := desiredParams.CanonicalJSON()
	if err != nil {
		return fmt.Errorf("canonicalize desired task collect_params: %w", err)
	}
	if existingCanonical != desiredCanonical {
		return fmt.Errorf("task collect_params cannot change; create a new task")
	}
	return nil
}

func (s *Service) validateCollectionTaskDatasets(ctx context.Context, task domain.CollectionTask) error {
	params, err := domain.ParseCollectParams(task.CollectParams, task.Provider, task.MarketType, task.DataType)
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
			task.SpaceID,
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
			task.SpaceID,
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
			task.SpaceID,
			params.Target.DatasetID,
			datasetSourceID,
			storagepb.DataKind_DATA_KIND_TIME_SERIES,
			"target",
			true,
			params.Collector.Market,
			params.Collector.Intervals,
		)
	case "kline_resample":
		return s.validateResampleSourceDataset(ctx, task, params)
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

func (s *Service) validateResampleSourceDataset(ctx context.Context, task domain.CollectionTask, params *domain.CollectParams) error {
	info, err := s.datasetSrc.GetDataset(ctx, task.SpaceID, params.SourceDatasetID)
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
	if actualMarket == "" || actualMarket != strings.ToLower(strings.TrimSpace(task.MarketType)) {
		return fmt.Errorf("source Dataset %s market_type=%s does not match task market_type=%s", params.SourceDatasetID, actualMarket, task.MarketType)
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
			return fmt.Errorf("%s Dataset %s market_type=%s does not match task market_type=%s", role, datasetID, actual, marketType)
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
