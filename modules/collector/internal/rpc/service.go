// Package rpc implements the independent Collector management RPC service.
package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/tagattr"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	collectorresample "github.com/mooyang-code/moox/modules/collector/internal/resample"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/rs/xid"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// Dependencies contains external service endpoints used by CollectMgr.
type Dependencies struct {
	DatasetSource     datasetSource
	RealtimeInventory RealtimeInventory
	ResultManager     *taskresult.Manager
	// FieldOffers 是行情源目录里可用的附加字段（由 marketwiring 注入）；为空表示没有。
	FieldOffers marketdata.FieldOffers
	// CheckKlineFrequency 校验（市场，品种，来源）能否采该频率的 K 线（由 marketwiring 注入，查行情源目录与路由文件）；为空时不校验。
	CheckKlineFrequency KlineFrequencyChecker
}

// KlineFrequencyChecker 校验 K 线任务的（市场 ID，市场类型，来源，频率）组合，不支持时返回中文原因。
type KlineFrequencyChecker func(marketID, marketType, providerID, frequency string) error

// RealtimeInventory reconciles the derived expected Dataset registry.
type RealtimeInventory interface {
	MarkDirty()
	Refresh(context.Context) error
}

// Service implements the independent CollectMgr RPC service.
type Service struct {
	pb.UnimplementedCollectMgr
	persistence         *store.Store
	taskRepo            *store.TaskRepository
	datasetSrc          datasetSource
	inventory           RealtimeInventory
	resultManager       *taskresult.Manager
	createTaskMu        ctxMutex
	taskMutationLocks   keyedTaskLocker
	subjectSyncer       SubjectSyncer
	tradingStatus       TradingStatusRunner
	fieldOffers         marketdata.FieldOffers
	checkKlineFrequency KlineFrequencyChecker
	klineOps            KlineOps
	fetchPlanControl    FetchPlanControl
	sourceIDResolver    marketfetch.SourceIDResolver
	stream              *StreamService
	activity            activityLogger
	delistRegistrar     DelistRegistrar
	cloud               CloudDeps
	life                *lifecycle.Controller
}

// ctxMutex 是可响应 context 取消的互斥锁：等待者超时或被取消后立即返回，而不是一直排队。
type ctxMutex struct {
	once sync.Once
	slot chan struct{}
}

func (m *ctxMutex) lock(ctx context.Context) (func(), error) {
	m.once.Do(func() { m.slot = make(chan struct{}, 1) })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case m.slot <- struct{}{}:
		return func() { <-m.slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type keyedTaskLocker struct {
	mu    sync.Mutex
	locks map[string]*taskMutationLock
}

type taskMutationLock struct {
	mu   ctxMutex
	refs int
}

// lock 获取任务级互斥锁；等待期间 ctx 取消会回收引用并返回错误。
func (l *keyedTaskLocker) lock(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*taskMutationLock)
	}
	entry := l.locks[key]
	if entry == nil {
		entry = &taskMutationLock{}
		l.locks[key] = entry
	}
	entry.refs++
	l.mu.Unlock()

	release := func() {
		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
	unlock, err := entry.mu.lock(ctx)
	if err != nil {
		release()
		return nil, err
	}
	return func() {
		unlock()
		release()
	}, nil
}

func taskMutationKey(spaceID, taskID string) string {
	return strings.TrimSpace(spaceID) + "\x00" + strings.TrimSpace(taskID)
}

type datasetSource interface {
	GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error)
	ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error)
}

// New creates a collector management service.
func New(persistence *store.Store, deps Dependencies) *Service {
	return &Service{
		persistence:         persistence,
		taskRepo:            persistence.Tasks(),
		datasetSrc:          deps.DatasetSource,
		inventory:           deps.RealtimeInventory,
		resultManager:       deps.ResultManager,
		fieldOffers:         deps.FieldOffers,
		checkKlineFrequency: deps.CheckKlineFrequency,
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
		SpaceID:  spaceID,
		DataType: req.GetDataType(),
		Enabled:  req.Enabled,
		TaskID:   req.GetTaskId(),
		Keyword:  req.GetKeyword(),
		Page:     page,
		PageSize: size,
	})
	if err != nil {
		log.ErrorContextf(ctx, "[Collector] list collection tasks failed: %v", err)
		return &pb.GetTaskListRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	out := s.toPBTaskList(ctx, tasks)
	return &pb.GetTaskListRsp{RetInfo: retOK(), Tasks: out, PageResult: pageResult(page, size, total)}, nil
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
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &pb.GetTaskDetailRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "collection task not found")}, nil
		}
		log.ErrorContextf(ctx, "[Collector] get collection task failed: %v", err)
		return &pb.GetTaskDetailRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "failed to load collection task")}, nil
	}
	return &pb.GetTaskDetailRsp{RetInfo: retOK(), Task: s.toPBTask(ctx, *task)}, nil
}

// toPBTask enriches the public task with the current task-owned Storage
// metadata state. The Collector database stores the stable result identity,
// while readiness and indexed watermarks are derived from Storage.
func (s *Service) toPBTask(ctx context.Context, task domain.CollectionTask) *pb.CollectionTask {
	result := toPBTask(task)
	s.enrichPBTaskResult(ctx, task, result)
	return result
}

func (s *Service) toPBTaskList(_ context.Context, tasks []domain.CollectionTask) []*pb.CollectionTask {
	out := make([]*pb.CollectionTask, len(tasks))
	for i, task := range tasks {
		out[i] = toPBTask(task)
		if task.PrepareState != domain.PrepareStatePending && task.PrepareState != domain.PrepareStateWaitingView && task.PrepareState != domain.PrepareStateError && out[i].GetResult() != nil {
			out[i].GetResult().Status = taskresult.ResultStatusUnknown
		}
	}
	return out
}

func (s *Service) enrichPBTaskResult(ctx context.Context, task domain.CollectionTask, result *pb.CollectionTask) {
	if s == nil || s.resultManager == nil || result == nil || result.Result == nil {
		return
	}
	ids := collectionTaskResultIDs(task)
	inspection, err := s.resultManager.InspectIDs(ctx, task.SpaceID, task.TaskID, ids)
	if err != nil {
		log.WarnContextf(ctx, "[Collector] inspect task result failed space=%s task=%s: %v", task.SpaceID, task.TaskID, err)
		if errors.Is(err, taskresult.ErrResultContract) {
			result.Result.Status = taskresult.ResultStatusError
			result.LastError = err.Error()
		} else {
			result.Result.Status = taskresult.ResultStatusUnknown
		}
		return
	}
	if task.PrepareState == domain.PrepareStateError || strings.TrimSpace(task.LastError) != "" {
		result.Result.Status = taskresult.ResultStatusError
		return
	}
	result.Result.Status = inspection.Status
	result.Result.LastDataTime = inspection.LastDataTime
	result.Result.CoverageStart = inspection.CoverageStart
	result.Result.CoverageEnd = inspection.CoverageEnd
	if inspection.Error != "" && result.LastError == "" {
		result.LastError = inspection.Error
	}
}

// CreateTask creates a collection task through the independent collector service.
func (s *Service) CreateTask(ctx context.Context, req *pb.CreateTaskReq) (*pb.CreateTaskRsp, error) {
	// Result metadata is task-exclusive. Serialize the local check/provision/
	// insert sequence so two UI retries cannot compensate resources provisioned
	// by the winning request.
	unlockCreate, lockErr := s.createTaskMu.lock(ctx)
	if lockErr != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "等待创建任务锁失败："+lockErr.Error())}, nil
	}
	defer unlockCreate()
	if req == nil || req.GetTask() == nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task is required")}, nil
	}
	requestedTaskID := strings.TrimSpace(req.GetTask().GetTaskId())
	task := fromPBTask(req.GetTask())
	if err := s.deriveResampleSource(ctx, &task); err != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	task = normalizeCollectionTask(task)
	if err := domain.ValidateCollectionTaskName(task.TaskName); err != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	params, err := domain.ParseCollectParams(task.CollectParams, task.DataType)
	if err != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	// 采集方式在准备结果数据集之前定下：不填用市场默认值（任务仓库是唯一入口），取值不对在这里就拒绝。
	if task.Ingest, err = s.taskRepo.ResolveIngest(task); err != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	resultIDs := taskresult.ResultIDs(task.TaskID)
	task.ResultDatasetID = resultIDs.DatasetID
	subjectTags, err := s.resolveSubjectTags(ctx, task, params, "")
	if err != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	task.TagIDs = normalizeTagIDs(subjectTags)
	if err := s.validateCollectionTaskTagRoutes(ctx, task); err != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if err := validateCollectionTask(task); err != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	task, err = canonicalizeCollectionTask(task)
	if err != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	// Hash the normalized, persisted definition so duplicate checks include the
	// resolved tag range and canonical collector parameters.
	task.DefinitionHash = CollectionTaskDefinitionHash(task)
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
	if existing, lookupErr := s.taskRepo.GetByDefinitionHash(ctx, task.SpaceID, task.DefinitionHash); lookupErr == nil && existing != nil {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("same tag, task type, frequency and parameters already exist in task %s", existing.TaskID))}, nil
	} else if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, lookupErr.Error())}, nil
	}
	if requestedTaskID != "" && s.resultManager != nil {
		inspection, inspectErr := s.resultManager.InspectIDs(ctx, task.SpaceID, task.TaskID, taskresult.IDs{DatasetID: task.ResultDatasetID})
		if inspectErr != nil {
			return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, fmt.Sprintf("inspect retained task result before create: %v", inspectErr))}, nil
		}
		if inspection.Dataset != nil {
			return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "task_id has retained result data; create the task with a new task_id")}, nil
		}
	}
	resultConfig := req.GetResultConfig()
	description := task.Description
	if strings.TrimSpace(resultConfig.GetDescription()) != "" {
		description = strings.TrimSpace(resultConfig.GetDescription())
	}
	var cleanupResult func(context.Context) error
	if strings.EqualFold(strings.TrimSpace(task.DataType), "kline_resample") {
		// 合成结果的数据集由 resample.Preparer 按源数据集的来历异步准备。
		task.ResultDatasetID = resultIDs.DatasetID
	} else {
		if s.resultManager == nil {
			return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "task result manager is not configured")}, nil
		}
		var err error
		resultIDs, cleanupResult, err = s.resultManager.EnsureWithCleanup(ctx, task.SpaceID, task.TaskID, task.DataType, "", taskresult.Config{Name: task.TaskName, Description: description, Frequency: firstTaskFrequency(params), SubjectTags: subjectTags, OutputFields: params.OutputFields})
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "already exists") {
				return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
			}
			return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
		task.ResultDatasetID = resultIDs.DatasetID
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
	// Serialize all mutations of this task through metadata preparation and
	// compensation. The task definition's unique index arbitrates create/update
	// races without holding the global create lock during remote metadata calls.
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
	unlockTask, lockErr := s.taskMutationLocks.lock(ctx, taskMutationKey(spaceID, taskID))
	if lockErr != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "等待任务锁失败："+lockErr.Error())}, nil
	}
	defer unlockTask()
	if err := validateTaskResultConfigUpdate(req.GetResultConfig()); err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	existing, err := s.taskRepo.GetByTaskID(ctx, spaceID, taskID)
	if err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, err.Error())}, nil
	}
	// 采集方式不随修改任务变：切换要生成交接计划，只走专门的命令。请求里带回现值（例如原样回传的任务）照常放行。
	if requested.Ingest != "" && requested.Ingest != existing.Ingest {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "修改任务不能改采集方式，请用 moox-cli collector ingest-mode set")}, nil
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
	if dataType := strings.ToLower(strings.TrimSpace(requested.DataType)); dataType != "" {
		task.DataType = dataType
	}
	if creator := strings.TrimSpace(requested.Creator); creator != "" {
		task.Creator = creator
	}
	if req.GetTask().GetCollectParams() != nil {
		task.CollectParams = jsonStringFromStruct(req.GetTask().GetCollectParams())
	}
	if len(requested.TagIDs) > 0 {
		task.TagIDs = normalizeTagIDs(requested.TagIDs)
	}
	requestedParams, err := domain.ParseCollectParams(task.CollectParams, task.DataType)
	if err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	subjectTags, err := s.resolveSubjectTags(ctx, task, requestedParams, existing.ResultDatasetID)
	if err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	existingTags, err := s.existingCollectionSubjectTags(ctx, *existing)
	if err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if !equalStringSlices(existingTags, subjectTags) {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "采集标的范围不可修改，请创建新任务")}, nil
	}
	task.ResultDatasetID = existing.ResultDatasetID
	if err := domain.ValidateCollectionTaskName(task.TaskName); err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if err := s.validateCollectionTaskTagRoutes(ctx, task); err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if err := validateCollectionTaskUpdate(*existing, task); err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if strings.EqualFold(strings.TrimSpace(task.DataType), "kline_resample") {
		if err := collectorresample.ValidateTaskParamsBasic(requestedParams, task.ResultDatasetID); err != nil {
			return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
		}
	}
	task, err = canonicalizeCollectionTask(task)
	if err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if strings.EqualFold(existing.DataType, "kline_resample") {
		task.Creator = existing.Creator
		// 准备状态属于异步运行态，由 resample.Preparer 负责推进（仅处理非 ready 状态）。
		task.PrepareState = existing.PrepareState
		task.LastError = existing.LastError
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
	existingParams, err := domain.ParseCollectParams(existing.CollectParams, existing.DataType)
	if err != nil {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, fmt.Sprintf("parse existing task collect_params: %v", err))}, nil
	}
	outputFieldsChanged := !equalStringSlices(existingParams.OutputFields, requestedParams.OutputFields)
	if outputFieldsChanged && len(requestedParams.OutputFields) == 0 {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "output_fields cannot be cleared; specify the fields to persist")}, nil
	}
	if added := s.addedOutputFields(task.SpaceID, existingParams.OutputFields, requestedParams.OutputFields); len(added) > 0 {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("输出字段只能减少，不能新增（%s）；要增加字段请创建新任务", strings.Join(added, "、")))}, nil
	}
	task.DefinitionHash = CollectionTaskDefinitionHash(task)
	if duplicate, lookupErr := s.taskRepo.GetByDefinitionHash(ctx, task.SpaceID, task.DefinitionHash); lookupErr == nil {
		if duplicate != nil && duplicate.TaskID != task.TaskID {
			return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("same tag, task type, frequency and parameters already exist in task %s", duplicate.TaskID))}, nil
		}
	} else if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, lookupErr.Error())}, nil
	}
	// 合成任务的目标数据集及列（含可选列的 Required=false 语义）由 resample Preparer/Catalog 异步维护，
	// 这里只对直接采集任务同步结果数据集列，避免未就绪时阻断合法缩减输出字段或覆写可选列的 Required 标记。
	if outputFieldsChanged && !strings.EqualFold(strings.TrimSpace(task.DataType), "kline_resample") {
		if s.resultManager == nil {
			return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "task result manager is not configured")}, nil
		}
		// Provision task-owned result metadata before changing the active definition.
		if err := s.resultManager.EnsureOutputFields(ctx, task.SpaceID, task.TaskID, task.ResultDatasetID, task.DataType, requestedParams.OutputFields); err != nil {
			code := pb.ErrorCode_INNER_ERR
			if errors.Is(err, taskresult.ErrUnsupportedOutputFields) {
				code = pb.ErrorCode_INVALID_PARAM
			}
			return &pb.UpdateTaskRsp{RetInfo: retErr(code, err.Error())}, nil
		}
	}
	updated, err := s.taskRepo.UpdateValidatedTaskByTaskID(ctx, spaceID, taskID, task)
	if err != nil {
		log.ErrorContextf(ctx, "[Collector] update collection task failed: %v", err)
		if named, lookupErr := s.taskRepo.GetByTaskName(ctx, spaceID, task.TaskName); lookupErr == nil && named != nil && named.TaskID != existing.TaskID {
			return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("task_name %q already exists in space %q", task.TaskName, spaceID))}, nil
		}
		if duplicate, lookupErr := s.taskRepo.GetByDefinitionHash(ctx, spaceID, task.DefinitionHash); lookupErr == nil && duplicate != nil && duplicate.TaskID != existing.TaskID {
			return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("same tag, task type, frequency and parameters already exist in task %s", duplicate.TaskID))}, nil
		}
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if s.resultManager != nil && !strings.EqualFold(strings.TrimSpace(task.DataType), "kline_resample") {
		if err := s.resultManager.UpdateSubjectTags(ctx, task.SpaceID, task.ResultDatasetID, subjectTags); err != nil {
			// The task row is the local source of truth. Restore it when the
			// cross-service Dataset update fails so a retry cannot observe a task
			// definition that points at an unapplied subject scope.
			rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancelRollback()
			if _, rollbackErr := s.taskRepo.UpdateValidatedTaskByTaskID(rollbackCtx, spaceID, taskID, *existing); rollbackErr != nil {
				log.ErrorContextf(ctx, "[Collector] rollback collection task after Dataset tag update failure: %v", rollbackErr)
				return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, fmt.Sprintf("%v; task rollback failed: %v", err, rollbackErr))}, nil
			}
			return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
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
	unlockTask, lockErr := s.taskMutationLocks.lock(ctx, taskMutationKey(req.GetSpaceId(), req.GetTaskId()))
	if lockErr != nil {
		return &pb.DisableTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "等待任务锁失败："+lockErr.Error())}, nil
	}
	defer unlockTask()
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
	unlockTask, lockErr := s.taskMutationLocks.lock(ctx, taskMutationKey(spaceID, taskID))
	if lockErr != nil {
		return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "等待任务锁失败："+lockErr.Error())}, nil
	}
	defer unlockTask()
	task, err := s.taskRepo.GetByTaskID(ctx, spaceID, taskID)
	if err != nil {
		return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, err.Error())}, nil
	}
	var resultIDs taskresult.IDs
	if req.GetDeleteResultData() {
		if s.resultManager == nil {
			return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "task result manager is not configured; task remains enabled for retry")}, nil
		}
		resultIDs = collectionTaskResultIDs(*task)
		if err := s.resultManager.ValidateOwnedForTask(ctx, spaceID, taskID, resultIDs); err != nil {
			return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, fmt.Sprintf("delete task result preflight failed; task remains enabled: %v", err))}, nil
		}
	}
	if err := s.taskRepo.SetEnabled(ctx, spaceID, taskID, false); err != nil {
		return &pb.DeleteTaskRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
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

// GetDataTypeConfigs 返回当前支持的采集任务类型及其频率选项、基础输出字段和附加字段。
func (s *Service) GetDataTypeConfigs(ctx context.Context, req *pb.GetDataTypeConfigsReq) (*pb.GetDataTypeConfigsRsp, error) {
	jobDefinitions := jobs.ListJobDefinitions()
	configs := make([]*pb.DataTypeConfig, 0, len(jobDefinitions))
	for _, definition := range jobDefinitions {
		configs = append(configs, s.dataTypeConfigFromDefinition(definition))
	}
	return &pb.GetDataTypeConfigsRsp{
		RetInfo: retOK(),
		Configs: configs,
	}, nil
}

// addedOutputFields 返回改任务时新增的输出字段。新增的列不会出现在已有的因子结果数据集里，因子引擎写入会被 Storage 拒绝，
// 所以输出字段只能减少。原来没有显式选择（默认字段）时，收窄到基础字段算减少，选附加字段算新增。
func (s *Service) addedOutputFields(spaceID string, existing, requested []string) []string {
	var added []string
	for _, field := range requested {
		if len(existing) == 0 {
			if _, extra := s.fieldOffers.Spec(spaceID, field); !extra {
				continue
			}
		} else if domain.HasOutputField(existing, field) {
			continue
		}
		added = append(added, field)
	}
	return added
}

// baseOutputFieldOptions 列出任务可选的基础输出字段（与建结果数据集列用同一份定义）：
// kline 返回全部 K 线基础字段；kline_resample 返回可合成的 K 线基础字段；其他任务类型没有。
func baseOutputFieldOptions(dataType string) []*pb.OutputFieldOption {
	switch strings.ToLower(strings.TrimSpace(dataType)) {
	case "kline":
		options := make([]*pb.OutputFieldOption, 0, len(taskresult.KlineOutputFields))
		for _, field := range taskresult.KlineOutputFields {
			options = append(options, &pb.OutputFieldOption{FieldId: field.Name, DisplayName: field.DisplayName, Group: field.Group, ValueType: taskresult.ValueTypeName(field.Kind)})
		}
		return options
	case "kline_resample":
		names := collectorresample.AllOutputFieldNames()
		options := make([]*pb.OutputFieldOption, 0, len(names))
		for _, name := range names {
			field, ok := taskresult.KlineOutputField(name)
			if !ok {
				continue
			}
			options = append(options, &pb.OutputFieldOption{FieldId: field.Name, DisplayName: field.DisplayName, Group: field.Group, ValueType: taskresult.ValueTypeName(field.Kind)})
		}
		return options
	default:
		return nil
	}
}

// extraFieldOptions 列出 K 线任务可选的附加字段及其适用范围；其他任务类型没有。
func (s *Service) extraFieldOptions(dataType string) []*pb.ExtraFieldOption {
	if !strings.EqualFold(strings.TrimSpace(dataType), "kline") {
		return nil
	}
	options := make([]*pb.ExtraFieldOption, 0, len(s.fieldOffers))
	for _, offer := range s.fieldOffers {
		options = append(options, &pb.ExtraFieldOption{
			FieldId: offer.Spec.ID, DisplayName: offer.Spec.Name, Group: offer.Spec.Group, MarketId: string(offer.MarketID), InstrumentType: string(offer.Instrument),
			Frequencies: append([]string(nil), offer.Spec.Frequencies...), ProviderId: offer.ProviderID,
			ValueType: string(offer.Spec.Type), Fill: offer.Spec.Fill,
		})
	}
	return options
}

func normalizeCollectionTask(task domain.CollectionTask) domain.CollectionTask {
	task.SpaceID = strings.TrimSpace(task.SpaceID)
	task.TaskID = strings.TrimSpace(task.TaskID)
	// Task IDs are server identities; never accept a caller supplied format.
	task.TaskID = xid.New().String()
	task.TaskName = domain.NormalizeCollectionTaskName(task.TaskName)
	task.Description = strings.TrimSpace(task.Description)
	// 数据类型只存小写：写成 Kline 也按 K 线任务处理，采集方式、表上的 CHECK 与计划生成看到的是同一个值。
	task.DataType = strings.ToLower(strings.TrimSpace(task.DataType))
	// Persist one canonical representation so reads, uniqueness checks and
	// scheduler joins all observe the same tag set.
	task.TagIDs = normalizeTagIDs(task.TagIDs)
	if strings.TrimSpace(task.CollectParams) == "" {
		task.CollectParams = "{}"
	}
	task.DefinitionHash = CollectionTaskDefinitionHash(task)
	return task
}

// CollectionTaskDefinitionHash 是任务定义的去重哈希（空间、标签、类型与规范化后的采集参数）。
func CollectionTaskDefinitionHash(task domain.CollectionTask) string {
	params := strings.TrimSpace(task.CollectParams)
	if parsed, err := domain.ParseCollectParams(params, task.DataType); err == nil {
		if encoded, encodeErr := parsed.CanonicalJSON(); encodeErr == nil {
			params = encoded
		}
	}
	tagIDs := stringSliceJSON(normalizeTagIDs(task.TagIDs))
	canonical := strings.Join([]string{strings.TrimSpace(task.SpaceID), tagIDs, strings.TrimSpace(task.DataType), params}, "|")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

func validateTaskResultConfigUpdate(config *pb.ResultConfig) error {
	if strings.TrimSpace(config.GetDescription()) != "" {
		return fmt.Errorf("result_config cannot change an existing task result")
	}
	return nil
}

func collectionTaskResultIDs(task domain.CollectionTask) taskresult.IDs {
	return taskresult.PersistedResultIDs(task.TaskID, task.ResultDatasetID)
}

func firstTaskFrequency(params *domain.CollectParams) string {
	if params == nil {
		return ""
	}
	if params.Frequency != "" {
		return params.Frequency
	}
	return params.TargetFrequency
}

// resolveSubjectTags 校验任务所选的标签能解析出有效标的；重采样任务继承源数据集标签，
// 更新任务省略 tag_ids 时沿用已有结果数据集的标签范围。
func (s *Service) resolveSubjectTags(ctx context.Context, task domain.CollectionTask, params *domain.CollectParams, fallbackDatasetID string) ([]string, error) {
	tags := normalizeTagIDs(task.TagIDs)
	if strings.EqualFold(strings.TrimSpace(task.DataType), "kline_resample") {
		// 重采样任务继承源行情的标的范围；显式给出的标签必须与源一致。
		if s.datasetSrc == nil {
			return nil, errors.New("source Dataset metadata is unavailable")
		}
		info, sourceErr := s.datasetSrc.GetDataset(ctx, task.SpaceID, params.SourceDatasetID)
		if sourceErr != nil {
			return nil, fmt.Errorf("resolve source Dataset tags: %w", sourceErr)
		}
		sourceTags := normalizeTagIDs(info.SubjectTags)
		if len(tags) > 0 && !equalStringSlices(tags, sourceTags) {
			return nil, errors.New("重采样任务继承源行情的标的范围，不能单独选择标签")
		}
		tags = sourceTags
	}
	if len(tags) == 0 {
		if fallbackDatasetID != "" && s.datasetSrc != nil {
			info, sourceErr := s.datasetSrc.GetDataset(ctx, task.SpaceID, fallbackDatasetID)
			if sourceErr != nil {
				return nil, fmt.Errorf("resolve existing Dataset tags: %w", sourceErr)
			}
			tags = normalizeTagIDs(info.SubjectTags)
		}
	}
	if len(tags) == 0 {
		return nil, errors.New("请选择标的标签")
	}
	if s.datasetSrc == nil {
		return nil, errors.New("subject metadata source is unavailable")
	}
	subjects, resolveErr := s.datasetSrc.ResolveSubjects(ctx, task.SpaceID, tags)
	if resolveErr != nil {
		return nil, fmt.Errorf("resolve selected subject tags: %w", resolveErr)
	}
	if len(subjects) == 0 {
		return nil, errors.New("所选标签没有有效标的")
	}
	return tags, nil
}

func normalizeTagIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (s *Service) existingCollectionSubjectTags(ctx context.Context, task domain.CollectionTask) ([]string, error) {
	if tags := normalizeTagIDs(task.TagIDs); len(tags) > 0 {
		return tags, nil
	}
	params, err := domain.ParseCollectParams(task.CollectParams, task.DataType)
	if err != nil {
		return nil, err
	}
	datasetID := strings.TrimSpace(task.ResultDatasetID)
	if strings.EqualFold(strings.TrimSpace(task.DataType), "kline_resample") {
		datasetID = strings.TrimSpace(params.SourceDatasetID)
	}
	if datasetID == "" || s.datasetSrc == nil {
		return nil, nil
	}
	info, err := s.datasetSrc.GetDataset(ctx, task.SpaceID, datasetID)
	if err != nil {
		return nil, fmt.Errorf("read existing collection tag range: %w", err)
	}
	return normalizeTagIDs(info.SubjectTags), nil
}

func equalStringSlices(left, right []string) bool {
	left = normalizeTagIDs(left)
	right = normalizeTagIDs(right)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func validateCollectionTask(task domain.CollectionTask) error {
	if strings.TrimSpace(task.TaskID) == "" {
		return fmt.Errorf("task_id is required")
	}
	if strings.Contains(task.TaskID, "::") {
		return fmt.Errorf("task_id cannot contain the reserved delimiter ::")
	}
	if strings.TrimSpace(task.SpaceID) == "" {
		return fmt.Errorf("space_id is required")
	}
	if err := domain.ValidateCollectionTaskName(task.TaskName); err != nil {
		return err
	}
	dataType := strings.ToLower(strings.TrimSpace(task.DataType))
	if dataType == "" {
		return fmt.Errorf("data_type is required")
	}
	tagIDs := normalizeTagIDs(task.TagIDs)
	if len(tagIDs) == 0 && dataType != "kline_resample" {
		return fmt.Errorf("tag_ids are required")
	}
	params, err := domain.ParseCollectParams(task.CollectParams, dataType)
	if err != nil {
		return fmt.Errorf("invalid collect_params: %w", err)
	}
	switch dataType {
	case "kline":
		if err := params.ValidateKline(); err != nil {
			return fmt.Errorf("invalid collect_params: %w", err)
		}
	case "kline_resample":
		if err := collectorresample.ValidateTaskParamsBasic(params, collectionTaskResultIDs(task).DatasetID); err != nil {
			return fmt.Errorf("invalid collect_params: %w", err)
		}
	default:
		return fmt.Errorf("unsupported collector data_type: %s", task.DataType)
	}
	return nil
}

type collectionTaskTagSource interface {
	GetTag(context.Context, string, string) (*storagepb.Tag, error)
}

func (s *Service) validateCollectionTaskTagRoutes(ctx context.Context, task domain.CollectionTask) error {
	extraFields, err := s.selectedExtraFields(task)
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(task.DataType), "kline") {
		// 重采样的字段表是固定的，不合成附加字段。
		if len(extraFields) > 0 {
			return fmt.Errorf("重采样不能合成附加字段 %s，请从输出字段里去掉它", extraFields[0].ID)
		}
		return nil
	}
	tagSource, ok := s.datasetSrc.(collectionTaskTagSource)
	if !ok {
		// Some focused unit fakes expose only Dataset/Subject resolution. The
		// production DatasetSource implements GetTag and always takes this path.
		return nil
	}
	params, err := domain.ParseCollectParams(task.CollectParams, task.DataType)
	if err != nil {
		return fmt.Errorf("invalid collect_params: %w", err)
	}
	if err := params.ValidateKline(); err != nil {
		return fmt.Errorf("invalid collect_params: %w", err)
	}
	type tagRoute struct{ id, source, market string }
	routes := make([]tagRoute, 0, len(task.TagIDs))
	for _, tagID := range normalizeTagIDs(task.TagIDs) {
		tag, err := tagSource.GetTag(ctx, task.SpaceID, tagID)
		if err != nil {
			return fmt.Errorf("load tag %s: %w", tagID, err)
		}
		route := tagRoute{id: tagID, source: tagattr.Source(tag), market: tagattr.MarketType(tag)}
		if route.source == "" || route.market == "" {
			return fmt.Errorf("tag %s must have sync.source and market_type attributes", tagID)
		}
		// 同一任务的结果数据集只放一个数据源、一个市场的序列：现货与永续要拆成两个任务（各一个数据集）。
		if first := routes; len(first) > 0 && route.source != first[0].source {
			return fmt.Errorf("标签 %s（数据源 %s）与标签 %s（数据源 %s）的数据源不同，一个任务只能使用同一数据源的标签", first[0].id, first[0].source, tagID, route.source)
		}
		if first := routes; len(first) > 0 && route.market != first[0].market {
			return fmt.Errorf("标签 %s（市场 %s）与标签 %s（市场 %s）的市场不同，一个任务只能使用同一市场的标签；现货与永续请拆成两个任务", first[0].id, first[0].market, tagID, route.market)
		}
		routes = append(routes, route)
	}
	for _, route := range routes {
		if s.checkKlineFrequency == nil {
			continue
		}
		if err := s.checkKlineFrequency(task.SpaceID, route.market, route.source, params.Frequency); err != nil {
			return fmt.Errorf("标签 %s 采不了这个任务：%w", route.id, err)
		}
	}
	// 选中的附加字段在任务的（市场, 品种）下要有来源：例如资金费率只有永续有来源，现货任务选不了。
	for _, field := range extraFields {
		for _, route := range routes {
			if _, ok := s.fieldOffers.Find(task.SpaceID, route.market, field.ID); !ok {
				return fmt.Errorf("输出字段 %s 在标签 %s 的市场 %s 下没有来源", field.ID, route.id, route.market)
			}
		}
	}
	return nil
}

// selectedExtraFields 返回任务输出字段里选中的附加字段，并校验任务频率在各字段适用的频率里。
func (s *Service) selectedExtraFields(task domain.CollectionTask) ([]marketdata.FieldSpec, error) {
	params, err := domain.ParseCollectParams(task.CollectParams, task.DataType)
	if err != nil {
		return nil, nil
	}
	var selected []marketdata.FieldSpec
	for _, name := range params.OutputFields {
		spec, ok := s.fieldOffers.Spec(task.SpaceID, name)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(task.DataType), "kline") && !spec.SupportsFrequency(params.Frequency) {
			return nil, fmt.Errorf("输出字段 %s 只支持 %s 频率，当前是 %q", spec.ID, strings.Join(spec.Frequencies, "、"), params.Frequency)
		}
		selected = append(selected, spec)
	}
	return selected, nil
}

func canonicalizeCollectionTask(task domain.CollectionTask) (domain.CollectionTask, error) {
	if !strings.EqualFold(strings.TrimSpace(task.DataType), "kline_resample") {
		if task.PrepareState == "" {
			task.PrepareState = domain.PrepareStateReady
		}
		return task, nil
	}
	params, err := domain.ParseCollectParams(task.CollectParams, task.DataType)
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
	if existing.SpaceID != desired.SpaceID || existing.TaskID != desired.TaskID || strings.TrimSpace(existing.DataType) != strings.TrimSpace(desired.DataType) {
		return fmt.Errorf("task identity cannot change; create a new task")
	}
	if strings.TrimSpace(existing.ResultDatasetID) != strings.TrimSpace(desired.ResultDatasetID) {
		return fmt.Errorf("task result identity cannot change; create a new task")
	}
	if strings.TrimSpace(existing.Creator) != strings.TrimSpace(desired.Creator) {
		return fmt.Errorf("task creator cannot change; create a new task")
	}
	if err := domain.ValidateCollectionTaskName(desired.TaskName); err != nil {
		return err
	}
	existingParams, err := domain.ParseCollectParams(existing.CollectParams, existing.DataType)
	if err != nil {
		return fmt.Errorf("parse existing task collect_params: %w", err)
	}
	desiredParams, err := domain.ParseCollectParams(desired.CollectParams, desired.DataType)
	if err != nil {
		return fmt.Errorf("parse desired task collect_params: %w", err)
	}
	// output_fields is a per-target projection. It changes which fields are
	// persisted, but not the provider request or the task's series scope.
	existingParams.OutputFields = nil
	desiredParams.OutputFields = nil
	// 每函数标的数与允许迟到是可变的运维属性，不属于任务的序列范围。
	for _, params := range []*domain.CollectParams{existingParams, desiredParams} {
		params.SymbolsPerFunction = 0
		params.AllowedLatenessSeconds = 0
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
	params, err := domain.ParseCollectParams(task.CollectParams, task.DataType)
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(task.DataType)) {
	case "kline":
		return s.validateDataset(
			ctx,
			task.SpaceID,
			collectionTaskResultIDs(task).DatasetID,
			storagepb.DataKind_DATA_KIND_TIME_SERIES,
			"target",
			params.Frequency,
		)
	case "kline_resample":
		return s.validateResampleSourceDataset(ctx, task, params)
	default:
		return fmt.Errorf("unsupported collector data_type: %s", task.DataType)
	}
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
	wantedFrequency, normalizeErr := frequencypkg.Normalize(params.SourceFrequency)
	if normalizeErr != nil {
		return fmt.Errorf("source Dataset %s frequency %q is invalid: %w", params.SourceDatasetID, params.SourceFrequency, normalizeErr)
	}
	if info.Freq != wantedFrequency {
		return fmt.Errorf("source Dataset %s freq is %q, not %q", params.SourceDatasetID, info.Freq, params.SourceFrequency)
	}
	return collectorresample.ValidateTaskParamsForSource(info, params, collectionTaskResultIDs(task).DatasetID)
}

func (s *Service) validateDataset(
	ctx context.Context,
	spaceID string,
	datasetID string,
	expectedKind storagepb.DataKind,
	role string,
	requiredFreq string,
) error {
	info, err := s.datasetSrc.GetDataset(ctx, spaceID, datasetID)
	if err != nil {
		return fmt.Errorf("%s Dataset %s is unavailable: %w", role, datasetID, err)
	}
	if info.Status != "active" {
		return fmt.Errorf("%s Dataset %s must be active", role, datasetID)
	}
	if info.DataKind != expectedKind {
		return fmt.Errorf(
			"%s Dataset %s data_kind=%s does not match collector data_type",
			role,
			datasetID,
			info.DataKind.String(),
		)
	}
	if requiredFreq != "" && info.Freq != requiredFreq {
		return fmt.Errorf("%s Dataset %s freq is %q, not %q", role, datasetID, info.Freq, requiredFreq)
	}
	return nil
}

func (s *Service) dataTypeConfigFromDefinition(definition jobs.JobDefinition) *pb.DataTypeConfig {
	freqOptions := make([]*pb.FrequencyOption, 0, len(definition.FrequencyOptions))
	for _, opt := range definition.FrequencyOptions {
		freqOptions = append(freqOptions, &pb.FrequencyOption{Value: opt.Value, Label: opt.Label})
	}
	return &pb.DataTypeConfig{
		DataType:         definition.DataType,
		TypeName:         definition.TypeName,
		TypeDesc:         definition.TypeDesc,
		FrequencyOptions: freqOptions,
		BaseOutputFields: baseOutputFieldOptions(definition.DataType),
		ExtraFields:      s.extraFieldOptions(definition.DataType),
	}
}
