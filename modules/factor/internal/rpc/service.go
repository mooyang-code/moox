package rpc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalog"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorwire"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/factorexpr"
)

var _ factorpb.FactorMgrService = (*Service)(nil)

// CatalogAPI is the catalog boundary exposed to FactorMgr.
type CatalogAPI interface {
	CreateTask(ctx context.Context, in domain.FactorTask) (domain.FactorTask, error)
	SetTaskStatus(ctx context.Context, taskID, status string) (domain.FactorTask, error)
	DeleteTask(ctx context.Context, taskID string, purge bool) error
	GetTask(ctx context.Context, taskID string) (domain.FactorTask, []domain.TaskMember, error)
	ListTasks(ctx context.Context) ([]domain.FactorTask, error)
	CreateFactor(ctx context.Context, in domain.FactorDef) (domain.FactorDef, error)
	UpdateFactor(ctx context.Context, in domain.FactorDef) (domain.FactorDef, error)
	DeleteFactor(ctx context.Context, factorID string) error
	GetFactor(ctx context.Context, factorID string) (domain.FactorInfo, error)
	ListFactors(ctx context.Context, taskID, status string) ([]domain.FactorInfo, error)
	AddTaskMember(ctx context.Context, taskID, alias, factorID string, params map[string]float64) (domain.TaskMember, error)
	RemoveTaskMember(ctx context.Context, taskID, alias string) error
	SetTaskMemberStatus(ctx context.Context, taskID, alias, status string) (domain.TaskMember, error)
	ListCategories(ctx context.Context) ([]domain.FactorCategory, error)
	UpsertCategory(ctx context.Context, category domain.FactorCategory) (domain.FactorCategory, error)
	DeleteCategory(ctx context.Context, categoryID string) error
	SetFactorCategory(ctx context.Context, factorID, categoryID string) (domain.FactorDef, error)
	EnsureInstanceTask(ctx context.Context, request catalog.InstanceTaskRequest) (catalog.InstanceTask, error)
	SetInstanceTaskEnabled(ctx context.Context, instanceID string, enabled bool) (domain.FactorTask, error)
	ReleaseInstanceTask(ctx context.Context, instanceID string) error
	SubmitParams(ctx context.Context, writer string, submissions []catalog.ParamSubmission) ([]catalog.ParamSubmitResult, error)
	ListParamIntervals(ctx context.Context, taskID, alias string, from, to time.Time) ([]domain.ParamInterval, error)
	ListParamRequests(ctx context.Context, taskID, alias string, from, to time.Time, limit int) ([]domain.ParamRequest, error)
	FrozenThrough(ctx context.Context, taskID string) (time.Time, bool, error)
}

// RecalcJob is the RPC-facing representation of one durable recalc request.
type RecalcJob struct {
	JobID        string
	RequestID    string
	TaskID       string
	Members      []string
	Subjects     []string
	StartTime    time.Time
	EndTime      time.Time
	Status       string
	ProgressTime time.Time
	Error        string
	EngineID     string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// JobFromStore converts a stored recalc job to its RPC representation.
func JobFromStore(job store.RecalcJob) RecalcJob {
	return RecalcJob{
		JobID: job.JobID, RequestID: job.RequestID, TaskID: job.TaskID,
		Members: append([]string(nil), job.Members...), Subjects: append([]string(nil), job.Subjects...),
		StartTime: unixTime(job.StartTime), EndTime: unixTime(job.EndTime), ProgressTime: unixTime(job.ProgressTime),
		Status: job.Status, Error: job.Error, EngineID: job.EngineID, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
	}
}

func unixTime(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(value, 0).UTC()
}

// RecalcAPI isolates asynchronous recalculation orchestration from RPC.
type RecalcAPI interface {
	Submit(ctx context.Context, taskID string, members, subjects []string, requestID string, start, end time.Time) (RecalcJob, error)
	List(ctx context.Context, taskID string, statuses []string) ([]RecalcJob, error)
	Get(ctx context.Context, jobID string) (RecalcJob, error)
	Cancel(ctx context.Context, jobID string) (RecalcJob, error)
}

// EngineAPI is the manager's view of the compute engine, fed by heartbeats.
type EngineAPI interface {
	Engine(ctx context.Context) (domain.EngineInfo, domain.EngineStatus, bool, error)
	LatestRun(taskID string) domain.TaskRunSummary
}

type ServiceOption func(*Service)

func WithEngineAPI(engine EngineAPI) ServiceOption {
	return func(s *Service) { s.engine = engine }
}

// WithMutationGate 设置修改类接口的生命周期门禁：返回错误（暂停、恢复追赶、重置中）时，创建、修改、删除、启停因子与因子任务、
// 提交与取消重算都被拒绝；读接口与引擎的同步、心跳、领取任务不受影响。不设置表示一直放行。
func WithMutationGate(gate func() error) ServiceOption {
	return func(s *Service) { s.mutationGate = gate }
}

func (s *Service) mutationAllowed() error {
	if s.mutationGate == nil {
		return nil
	}
	return s.mutationGate()
}

// WithPreviewer 启用 PreviewFactor；不设置时该接口返回错误。
func WithPreviewer(previewer Previewer) ServiceOption {
	return func(s *Service) { s.previewer = previewer }
}

// Service implements the FactorMgr RPC contract and delegates domain work.
type Service struct {
	catalog CatalogAPI
	recalc  RecalcAPI
	engine  EngineAPI
	// previewer 执行 PreviewFactor 的试算。
	previewer Previewer
	// mutationGate 是修改类接口的生命周期门禁（见 WithMutationGate）。
	mutationGate func() error
	// filled 暂存补齐通知。
	filled *FilledInbox
}

func NewService(catalog CatalogAPI, recalc RecalcAPI, opts ...ServiceOption) *Service {
	s := &Service{catalog: catalog, recalc: recalc}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

func (s *Service) CreateFactorTask(ctx context.Context, req *factorpb.CreateFactorTaskReq) (*factorpb.CreateFactorTaskRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.CreateFactorTaskRsp{RetInfo: unavailable(err)}, nil
	}
	if req == nil || req.GetFactorTask() == nil {
		return &factorpb.CreateFactorTaskRsp{RetInfo: invalid(fmt.Errorf("factor_task is required"))}, nil
	}
	in, err := factorwire.SetFromPB(req.GetFactorTask())
	if err != nil {
		return &factorpb.CreateFactorTaskRsp{RetInfo: invalid(err)}, nil
	}
	if space, scoped, err := callerSpace(ctx); err != nil || scoped && in.SpaceID != space {
		if err == nil {
			err = fmt.Errorf("factor task space %q does not match the request space %q", in.SpaceID, space)
		}
		return &factorpb.CreateFactorTaskRsp{RetInfo: invalid(err)}, nil
	}
	if in.OwnerInstanceID != "" {
		return &factorpb.CreateFactorTaskRsp{RetInfo: invalid(fmt.Errorf("私有因子任务由策略实例启用时自动建立，不能手工创建"))}, nil
	}
	set, err := s.catalog.CreateTask(ctx, in)
	if err != nil {
		return &factorpb.CreateFactorTaskRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.CreateFactorTaskRsp{RetInfo: success(), FactorTask: factorwire.SetToPB(set)}, nil
}

func (s *Service) SetFactorTaskStatus(ctx context.Context, req *factorpb.SetFactorTaskStatusReq) (*factorpb.SetFactorTaskStatusRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.SetFactorTaskStatusRsp{RetInfo: unavailable(err)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetTaskId()) == "" {
		return &factorpb.SetFactorTaskStatusRsp{RetInfo: invalid(fmt.Errorf("task_id is required"))}, nil
	}
	if err := s.checkTaskSpace(ctx, req.GetTaskId()); err != nil {
		return &factorpb.SetFactorTaskStatusRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.rejectOwnedTask(ctx, req.GetTaskId()); err != nil {
		return &factorpb.SetFactorTaskStatusRsp{RetInfo: invalid(err)}, nil
	}
	status := req.GetStatus()
	if status != domain.TaskStatusEnabled && status != domain.TaskStatusDisabled {
		return &factorpb.SetFactorTaskStatusRsp{RetInfo: invalid(fmt.Errorf("status must be %q or %q", domain.TaskStatusEnabled, domain.TaskStatusDisabled))}, nil
	}
	set, err := s.catalog.SetTaskStatus(ctx, req.GetTaskId(), status)
	if err != nil {
		return &factorpb.SetFactorTaskStatusRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.SetFactorTaskStatusRsp{RetInfo: success(), FactorTask: factorwire.SetToPB(set)}, nil
}

func (s *Service) DeleteFactorTask(ctx context.Context, req *factorpb.DeleteFactorTaskReq) (*factorpb.DeleteFactorTaskRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.DeleteFactorTaskRsp{RetInfo: unavailable(err)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetTaskId()) == "" {
		return &factorpb.DeleteFactorTaskRsp{RetInfo: invalid(fmt.Errorf("task_id is required"))}, nil
	}
	if err := s.checkTaskSpace(ctx, req.GetTaskId()); err != nil {
		return &factorpb.DeleteFactorTaskRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.catalog.DeleteTask(ctx, req.GetTaskId(), req.GetPurge()); err != nil {
		return &factorpb.DeleteFactorTaskRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.DeleteFactorTaskRsp{RetInfo: success()}, nil
}

func (s *Service) GetFactorTask(ctx context.Context, req *factorpb.GetFactorTaskReq) (*factorpb.GetFactorTaskRsp, error) {
	if req == nil || strings.TrimSpace(req.GetTaskId()) == "" {
		return &factorpb.GetFactorTaskRsp{RetInfo: invalid(fmt.Errorf("task_id is required"))}, nil
	}
	if err := s.checkTaskSpace(ctx, req.GetTaskId()); err != nil {
		return &factorpb.GetFactorTaskRsp{RetInfo: invalid(err)}, nil
	}
	set, members, err := s.catalog.GetTask(ctx, req.GetTaskId())
	if err != nil {
		return &factorpb.GetFactorTaskRsp{RetInfo: inner(err)}, nil
	}
	summary := s.latestRun(set.TaskID)
	return &factorpb.GetFactorTaskRsp{
		RetInfo: success(), FactorTask: factorwire.SetToPB(set), Members: membersToPB(members), LastRun: taskRunSummaryToPBIfPresent(summary),
	}, nil
}

func (s *Service) ListFactorTasks(ctx context.Context, req *factorpb.ListFactorTasksReq) (*factorpb.ListFactorTasksRsp, error) {
	sets, err := s.catalog.ListTasks(ctx)
	if err != nil {
		return &factorpb.ListFactorTasksRsp{RetInfo: inner(err)}, nil
	}
	space, scoped, scopeErr := callerSpace(ctx)
	if scopeErr != nil {
		return &factorpb.ListFactorTasksRsp{RetInfo: invalid(scopeErr)}, nil
	}
	filtered := make([]domain.FactorTask, 0, len(sets))
	for _, set := range sets {
		if scoped && set.SpaceID != space {
			continue // 只返回调用方所属空间的计算任务，分页在过滤之后
		}
		if req == nil || req.GetStatus() == "" || set.Status == req.GetStatus() {
			filtered = append(filtered, set)
		}
	}
	page, size := pageParams(pageFromTaskReq(req))
	items, total := paginate(filtered, page, size)
	infos := make([]*factorpb.FactorTaskInfo, 0, len(items))
	for _, set := range items {
		_, members, err := s.catalog.GetTask(ctx, set.TaskID)
		if err != nil {
			return &factorpb.ListFactorTasksRsp{RetInfo: inner(err)}, nil
		}
		summary := s.latestRun(set.TaskID)
		infos = append(infos, &factorpb.FactorTaskInfo{FactorTask: factorwire.SetToPB(set), Members: membersToPB(members), LastRun: taskRunSummaryToPBIfPresent(summary)})
	}
	return &factorpb.ListFactorTasksRsp{RetInfo: success(), FactorTasks: infos, PageResult: pageResult(page, size, total)}, nil
}

func (s *Service) CreateFactor(ctx context.Context, req *factorpb.CreateFactorReq) (*factorpb.CreateFactorRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.CreateFactorRsp{RetInfo: unavailable(err)}, nil
	}
	if req == nil || req.GetFactor() == nil {
		return &factorpb.CreateFactorRsp{RetInfo: invalid(fmt.Errorf("factor is required"))}, nil
	}
	factor, err := factorwire.DefFromPB(req.GetFactor())
	if err != nil {
		return &factorpb.CreateFactorRsp{RetInfo: invalid(err)}, nil
	}
	if strings.TrimSpace(factor.FactorID) == "" {
		return &factorpb.CreateFactorRsp{RetInfo: invalid(fmt.Errorf("factor_id is required"))}, nil
	}
	created, err := s.catalog.CreateFactor(ctx, factor)
	if err != nil {
		return &factorpb.CreateFactorRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.CreateFactorRsp{RetInfo: success(), Factor: factorwire.DefToPB(created)}, nil
}

func (s *Service) UpdateFactor(ctx context.Context, req *factorpb.UpdateFactorReq) (*factorpb.UpdateFactorRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.UpdateFactorRsp{RetInfo: unavailable(err)}, nil
	}
	if req == nil || req.GetFactor() == nil {
		return &factorpb.UpdateFactorRsp{RetInfo: invalid(fmt.Errorf("factor is required"))}, nil
	}
	factor, err := factorwire.DefFromPB(req.GetFactor())
	if err != nil {
		return &factorpb.UpdateFactorRsp{RetInfo: invalid(err)}, nil
	}
	if strings.TrimSpace(factor.FactorID) == "" {
		return &factorpb.UpdateFactorRsp{RetInfo: invalid(fmt.Errorf("factor_id is required"))}, nil
	}
	updated, err := s.catalog.UpdateFactor(ctx, factor)
	if err != nil {
		return &factorpb.UpdateFactorRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.UpdateFactorRsp{RetInfo: success(), Factor: factorwire.DefToPB(updated)}, nil
}

func (s *Service) AddTaskMember(ctx context.Context, req *factorpb.AddTaskMemberReq) (*factorpb.AddTaskMemberRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.AddTaskMemberRsp{RetInfo: unavailable(err)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetTaskId()) == "" || strings.TrimSpace(req.GetFactorId()) == "" {
		return &factorpb.AddTaskMemberRsp{RetInfo: invalid(fmt.Errorf("task_id and factor_id are required"))}, nil
	}
	if err := s.checkTaskSpace(ctx, req.GetTaskId()); err != nil {
		return &factorpb.AddTaskMemberRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.rejectOwnedTask(ctx, req.GetTaskId()); err != nil {
		return &factorpb.AddTaskMemberRsp{RetInfo: invalid(err)}, nil
	}
	member, err := s.catalog.AddTaskMember(ctx, req.GetTaskId(), req.GetMemberAlias(), req.GetFactorId(), factorwire.CloneParams(req.GetParams()))
	if err != nil {
		return &factorpb.AddTaskMemberRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.AddTaskMemberRsp{RetInfo: success(), Member: memberToPB(member, false)}, nil
}

func (s *Service) RemoveTaskMember(ctx context.Context, req *factorpb.RemoveTaskMemberReq) (*factorpb.RemoveTaskMemberRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.RemoveTaskMemberRsp{RetInfo: unavailable(err)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetTaskId()) == "" || strings.TrimSpace(req.GetMemberAlias()) == "" {
		return &factorpb.RemoveTaskMemberRsp{RetInfo: invalid(fmt.Errorf("task_id and member_alias are required"))}, nil
	}
	if err := s.checkTaskSpace(ctx, req.GetTaskId()); err != nil {
		return &factorpb.RemoveTaskMemberRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.rejectOwnedTask(ctx, req.GetTaskId()); err != nil {
		return &factorpb.RemoveTaskMemberRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.catalog.RemoveTaskMember(ctx, req.GetTaskId(), req.GetMemberAlias()); err != nil {
		return &factorpb.RemoveTaskMemberRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.RemoveTaskMemberRsp{RetInfo: success()}, nil
}

func (s *Service) SetTaskMemberStatus(ctx context.Context, req *factorpb.SetTaskMemberStatusReq) (*factorpb.SetTaskMemberStatusRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.SetTaskMemberStatusRsp{RetInfo: unavailable(err)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetTaskId()) == "" || strings.TrimSpace(req.GetMemberAlias()) == "" {
		return &factorpb.SetTaskMemberStatusRsp{RetInfo: invalid(fmt.Errorf("task_id and member_alias are required"))}, nil
	}
	if err := s.checkTaskSpace(ctx, req.GetTaskId()); err != nil {
		return &factorpb.SetTaskMemberStatusRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.rejectOwnedTask(ctx, req.GetTaskId()); err != nil {
		return &factorpb.SetTaskMemberStatusRsp{RetInfo: invalid(err)}, nil
	}
	status := req.GetStatus()
	if status != domain.MemberStatusEnabled && status != domain.MemberStatusDisabled {
		return &factorpb.SetTaskMemberStatusRsp{RetInfo: invalid(fmt.Errorf("status must be %q or %q", domain.MemberStatusEnabled, domain.MemberStatusDisabled))}, nil
	}
	member, err := s.catalog.SetTaskMemberStatus(ctx, req.GetTaskId(), req.GetMemberAlias(), status)
	if err != nil {
		return &factorpb.SetTaskMemberStatusRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.SetTaskMemberStatusRsp{RetInfo: success(), Member: memberToPB(member, false)}, nil
}

func (s *Service) DeleteFactor(ctx context.Context, req *factorpb.DeleteFactorReq) (*factorpb.DeleteFactorRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.DeleteFactorRsp{RetInfo: unavailable(err)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetFactorId()) == "" {
		return &factorpb.DeleteFactorRsp{RetInfo: invalid(fmt.Errorf("factor_id is required"))}, nil
	}
	if err := s.catalog.DeleteFactor(ctx, req.GetFactorId()); err != nil {
		return &factorpb.DeleteFactorRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.DeleteFactorRsp{RetInfo: success()}, nil
}

func (s *Service) GetFactor(ctx context.Context, req *factorpb.GetFactorReq) (*factorpb.GetFactorRsp, error) {
	if req == nil || strings.TrimSpace(req.GetFactorId()) == "" {
		return &factorpb.GetFactorRsp{RetInfo: invalid(fmt.Errorf("factor_id is required"))}, nil
	}
	info, err := s.catalog.GetFactor(ctx, req.GetFactorId())
	if err != nil {
		return &factorpb.GetFactorRsp{RetInfo: inner(err)}, nil
	}
	usages, err := s.scopeUsages(ctx, info.Usages)
	if err != nil {
		return &factorpb.GetFactorRsp{RetInfo: invalid(err)}, nil
	}
	// GetFactor is the one read that always carries the spec.
	return &factorpb.GetFactorRsp{RetInfo: success(), Factor: factorwire.DefToPB(info.Factor), Usages: usagesToPB(usages)}, nil
}

func (s *Service) ListFactors(ctx context.Context, req *factorpb.ListFactorsReq) (*factorpb.ListFactorsRsp, error) {
	taskID, status, includeSpec := "", "", false
	if req != nil {
		taskID, status, includeSpec = req.GetTaskId(), req.GetStatus(), req.GetIncludeSpec()
	}
	if taskID != "" {
		if err := s.checkTaskSpace(ctx, taskID); err != nil {
			return &factorpb.ListFactorsRsp{RetInfo: invalid(err)}, nil
		}
	}
	factors, err := s.catalog.ListFactors(ctx, taskID, status)
	if err != nil {
		return &factorpb.ListFactorsRsp{RetInfo: inner(err)}, nil
	}
	page, size := pageParams(pageFromFactorReq(req))
	items, total := paginate(factors, page, size)
	scoped := make([]domain.FactorInfo, len(items))
	for i, info := range items {
		usages, err := s.scopeUsages(ctx, info.Usages)
		if err != nil {
			return &factorpb.ListFactorsRsp{RetInfo: invalid(err)}, nil
		}
		info.Usages = usages
		scoped[i] = info
	}
	items = scoped
	return &factorpb.ListFactorsRsp{RetInfo: success(), Factors: factorInfosToPB(items, includeSpec), PageResult: pageResult(page, size, total)}, nil
}

func (s *Service) RecalcFactors(ctx context.Context, req *factorpb.RecalcFactorsReq) (*factorpb.RecalcFactorsRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.RecalcFactorsRsp{RetInfo: unavailable(err)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetTaskId()) == "" || strings.TrimSpace(req.GetRequestId()) == "" {
		return &factorpb.RecalcFactorsRsp{RetInfo: invalid(fmt.Errorf("task_id and request_id are required"))}, nil
	}
	if err := s.checkTaskSpace(ctx, req.GetTaskId()); err != nil {
		return &factorpb.RecalcFactorsRsp{RetInfo: invalid(err)}, nil
	}
	start, err := factorwire.ParseTime(req.GetStartTime())
	if err != nil || start.IsZero() {
		return &factorpb.RecalcFactorsRsp{RetInfo: invalid(fmt.Errorf("start_time must be an RFC3339 timestamp"))}, nil
	}
	end, err := factorwire.ParseTime(req.GetEndTime())
	if err != nil || end.IsZero() {
		return &factorpb.RecalcFactorsRsp{RetInfo: invalid(fmt.Errorf("end_time must be an RFC3339 timestamp"))}, nil
	}
	if !end.After(start) {
		return &factorpb.RecalcFactorsRsp{RetInfo: invalid(fmt.Errorf("end_time must be after start_time"))}, nil
	}
	set, _, err := s.catalog.GetTask(ctx, req.GetTaskId())
	if err != nil {
		return &factorpb.RecalcFactorsRsp{RetInfo: inner(err)}, nil
	}
	clock, err := periodclock.ForSpace(set.SpaceID)
	if err != nil {
		return &factorpb.RecalcFactorsRsp{RetInfo: invalid(err)}, nil
	}
	if _, err := clock.Align(start, set.Freq); err != nil {
		return &factorpb.RecalcFactorsRsp{RetInfo: invalid(fmt.Errorf("start_time must be aligned to %s: %w", set.Freq, err))}, nil
	}
	if _, err := clock.Align(end, set.Freq); err != nil {
		return &factorpb.RecalcFactorsRsp{RetInfo: invalid(fmt.Errorf("end_time must be aligned to %s: %w", set.Freq, err))}, nil
	}
	job, err := s.recalc.Submit(ctx, req.GetTaskId(), factorwire.CloneStrings(req.GetMembers()), factorwire.CloneStrings(req.GetSubjects()), req.GetRequestId(), start, end)
	if err != nil {
		return &factorpb.RecalcFactorsRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.RecalcFactorsRsp{RetInfo: success(), Job: recalcJobToPB(job)}, nil
}

func (s *Service) ListRecalcJobs(ctx context.Context, req *factorpb.ListRecalcJobsReq) (*factorpb.ListRecalcJobsRsp, error) {
	if req == nil || strings.TrimSpace(req.GetTaskId()) == "" {
		return &factorpb.ListRecalcJobsRsp{RetInfo: invalid(fmt.Errorf("task_id is required"))}, nil
	}
	if err := s.checkTaskSpace(ctx, req.GetTaskId()); err != nil {
		return &factorpb.ListRecalcJobsRsp{RetInfo: invalid(err)}, nil
	}
	jobs, err := s.recalc.List(ctx, req.GetTaskId(), factorwire.CloneStrings(req.GetStatuses()))
	if err != nil {
		return &factorpb.ListRecalcJobsRsp{RetInfo: inner(err)}, nil
	}
	page, size := pageParams(req.GetPage())
	items, total := paginate(jobs, page, size)
	out := make([]*factorpb.RecalcJob, 0, len(items))
	for _, job := range items {
		out = append(out, recalcJobToPB(job))
	}
	return &factorpb.ListRecalcJobsRsp{RetInfo: success(), Jobs: out, PageResult: pageResult(page, size, total)}, nil
}

func (s *Service) GetRecalcJob(ctx context.Context, req *factorpb.GetRecalcJobReq) (*factorpb.GetRecalcJobRsp, error) {
	if req == nil || strings.TrimSpace(req.GetJobId()) == "" {
		return &factorpb.GetRecalcJobRsp{RetInfo: invalid(fmt.Errorf("job_id is required"))}, nil
	}
	if err := s.checkJobSpace(ctx, req.GetJobId()); err != nil {
		return &factorpb.GetRecalcJobRsp{RetInfo: invalid(err)}, nil
	}
	job, err := s.recalc.Get(ctx, req.GetJobId())
	if err != nil {
		return &factorpb.GetRecalcJobRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.GetRecalcJobRsp{RetInfo: success(), Job: recalcJobToPB(job)}, nil
}

func (s *Service) CancelRecalcJob(ctx context.Context, req *factorpb.CancelRecalcJobReq) (*factorpb.CancelRecalcJobRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.CancelRecalcJobRsp{RetInfo: unavailable(err)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetJobId()) == "" {
		return &factorpb.CancelRecalcJobRsp{RetInfo: invalid(fmt.Errorf("job_id is required"))}, nil
	}
	if err := s.checkJobSpace(ctx, req.GetJobId()); err != nil {
		return &factorpb.CancelRecalcJobRsp{RetInfo: invalid(err)}, nil
	}
	job, err := s.recalc.Cancel(ctx, req.GetJobId())
	if err != nil {
		return &factorpb.CancelRecalcJobRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.CancelRecalcJobRsp{RetInfo: success(), Job: recalcJobToPB(job)}, nil
}

func (s *Service) GetStatus(ctx context.Context, _ *factorpb.GetStatusReq) (*factorpb.GetStatusRsp, error) {
	if s.engine == nil {
		return &factorpb.GetStatusRsp{RetInfo: inner(fmt.Errorf("engine status is not initialized"))}, nil
	}
	info, status, seen, err := s.engine.Engine(ctx)
	if err != nil {
		return &factorpb.GetStatusRsp{RetInfo: inner(err)}, nil
	}
	if !seen {
		return &factorpb.GetStatusRsp{RetInfo: success(), Engine: &factorpb.EngineInfo{}, ManagerExprVersion: factorexpr.ExprVersion}, nil
	}
	recent := make([]*factorpb.TaskRunSummary, 0, len(status.RecentRuns))
	for _, run := range status.RecentRuns {
		recent = append(recent, factorwire.RunSummaryToPB(run))
	}
	return &factorpb.GetStatusRsp{
		RetInfo: success(), ConsumerRunning: status.ConsumerRunning, ComputeWorkers: status.ComputeWorkers,
		EngineExprVersion: status.ExprVersion, ManagerExprVersion: factorexpr.ExprVersion, Runners: factorwire.RunnersToPB(status.Runners), RecentRuns: recent,
		Engine: &factorpb.EngineInfo{
			EngineId: info.EngineID, BootId: info.BootID, Version: info.Version, Online: info.Online,
			LastHeartbeatAt: factorwire.FormatTime(info.LastHeartbeatAt), CatalogHash: info.CatalogHash,
			CatalogSyncedAt: factorwire.FormatTime(info.CatalogSyncedAt), CatalogInSync: info.CatalogInSync,
		},
	}, nil
}

func (s *Service) latestRun(taskID string) domain.TaskRunSummary {
	if s.engine == nil {
		return domain.TaskRunSummary{TaskID: taskID}
	}
	return s.engine.LatestRun(taskID)
}

func pageFromTaskReq(req *factorpb.ListFactorTasksReq) *commonpb.Page {
	if req == nil {
		return nil
	}
	return req.GetPage()
}

func pageFromFactorReq(req *factorpb.ListFactorsReq) *commonpb.Page {
	if req == nil {
		return nil
	}
	return req.GetPage()
}

func pageParams(page *commonpb.Page) (uint32, uint32) {
	pageNo, size := page.GetPage(), page.GetSize()
	if pageNo == 0 {
		pageNo = 1
	}
	if size == 0 {
		size = 50
	}
	return pageNo, size
}

func pageResult(page, size uint32, total int) *commonpb.PageResult {
	return &commonpb.PageResult{
		Page: page, Size: size, Total: uint32(total), HasMore: page*size < uint32(total),
		TotalState: commonpb.TotalState_EXACT,
	}
}

func paginate[T any](items []T, page, size uint32) ([]T, int) {
	total := len(items)
	start := min(uint64(total), uint64(page-1)*uint64(size))
	end := min(uint64(total), start+uint64(size))
	return items[start:end], total
}

func success() *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS, Msg: "success"}
}

func invalid(err error) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: err.Error()}
}

// unavailable 表示服务暂时不接受这个操作（暂停、重置中）。
func unavailable(err error) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: err.Error()}
}

func inner(err error) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: err.Error()}
}
