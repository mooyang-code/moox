package rpc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorwire"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

var _ factorpb.FactorMgrService = (*Service)(nil)

// CatalogAPI is the catalog boundary exposed to FactorMgr.
type CatalogAPI interface {
	CreateSet(ctx context.Context, in domain.FactorSet) (domain.FactorSet, error)
	UpdateSetSubjects(ctx context.Context, setID, mode string, subjects []string) (domain.FactorSet, error)
	SetSetStatus(ctx context.Context, setID, status string) (domain.FactorSet, error)
	DeleteSet(ctx context.Context, setID string, purge bool) error
	GetSet(ctx context.Context, setID string) (domain.FactorSet, []domain.SetMember, error)
	ListSets(ctx context.Context) ([]domain.FactorSet, error)
	CreateFactor(ctx context.Context, in domain.FactorDef) (domain.FactorDef, error)
	UpdateFactor(ctx context.Context, in domain.FactorDef) (domain.FactorDef, error)
	DeleteFactor(ctx context.Context, factorID string) error
	GetFactor(ctx context.Context, factorID string) (domain.FactorInfo, error)
	ListFactors(ctx context.Context, setID, status string) ([]domain.FactorInfo, error)
	AddFactorToSet(ctx context.Context, setID, factorID string) (domain.SetMember, error)
	RemoveFactorFromSet(ctx context.Context, setID, factorID string) error
	// SetFactorMemberStatus returns the backfill job id when the member is enabled.
	SetFactorMemberStatus(ctx context.Context, setID, factorID, status string) (domain.SetMember, string, error)
}

// RecalcJob is the RPC-facing representation of one durable recalc request.
type RecalcJob struct {
	JobID        string
	RequestID    string
	SetID        string
	FactorIDs    []string
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
		JobID: job.JobID, RequestID: job.RequestID, SetID: job.SetID,
		FactorIDs: append([]string(nil), job.FactorIDs...), Subjects: append([]string(nil), job.Subjects...),
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
	Submit(ctx context.Context, setID string, factorIDs, subjects []string, requestID string, start, end time.Time) (RecalcJob, error)
	List(ctx context.Context, setID string, statuses []string) ([]RecalcJob, error)
	Get(ctx context.Context, jobID string) (RecalcJob, error)
	Cancel(ctx context.Context, jobID string) (RecalcJob, error)
}

// EngineAPI is the manager's view of the compute engine, fed by heartbeats.
type EngineAPI interface {
	Engine(ctx context.Context) (domain.EngineInfo, domain.EngineStatus, bool, error)
	LatestRun(setID string) domain.SetRunSummary
}

type ServiceOption func(*Service)

func WithEngineAPI(engine EngineAPI) ServiceOption {
	return func(s *Service) { s.engine = engine }
}

// Service implements the FactorMgr RPC contract and delegates domain work.
type Service struct {
	catalog CatalogAPI
	recalc  RecalcAPI
	engine  EngineAPI
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

func (s *Service) CreateFactorSet(ctx context.Context, req *factorpb.CreateFactorSetReq) (*factorpb.CreateFactorSetRsp, error) {
	if req == nil || req.GetFactorSet() == nil {
		return &factorpb.CreateFactorSetRsp{RetInfo: invalid(fmt.Errorf("factor_set is required"))}, nil
	}
	in, err := factorwire.SetFromPB(req.GetFactorSet())
	if err != nil {
		return &factorpb.CreateFactorSetRsp{RetInfo: invalid(err)}, nil
	}
	set, err := s.catalog.CreateSet(ctx, in)
	if err != nil {
		return &factorpb.CreateFactorSetRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.CreateFactorSetRsp{RetInfo: success(), FactorSet: factorwire.SetToPB(set)}, nil
}

func (s *Service) UpdateFactorSet(ctx context.Context, req *factorpb.UpdateFactorSetReq) (*factorpb.UpdateFactorSetRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSetId()) == "" {
		return &factorpb.UpdateFactorSetRsp{RetInfo: invalid(fmt.Errorf("set_id is required"))}, nil
	}
	set, err := s.catalog.UpdateSetSubjects(ctx, req.GetSetId(), req.GetSubjectMode(), factorwire.CloneStrings(req.GetSubjects()))
	if err != nil {
		return &factorpb.UpdateFactorSetRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.UpdateFactorSetRsp{RetInfo: success(), FactorSet: factorwire.SetToPB(set)}, nil
}

func (s *Service) SetFactorSetStatus(ctx context.Context, req *factorpb.SetFactorSetStatusReq) (*factorpb.SetFactorSetStatusRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSetId()) == "" {
		return &factorpb.SetFactorSetStatusRsp{RetInfo: invalid(fmt.Errorf("set_id is required"))}, nil
	}
	status := req.GetStatus()
	if status != domain.SetStatusEnabled && status != domain.SetStatusDisabled {
		return &factorpb.SetFactorSetStatusRsp{RetInfo: invalid(fmt.Errorf("status must be %q or %q", domain.SetStatusEnabled, domain.SetStatusDisabled))}, nil
	}
	set, err := s.catalog.SetSetStatus(ctx, req.GetSetId(), status)
	if err != nil {
		return &factorpb.SetFactorSetStatusRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.SetFactorSetStatusRsp{RetInfo: success(), FactorSet: factorwire.SetToPB(set)}, nil
}

func (s *Service) DeleteFactorSet(ctx context.Context, req *factorpb.DeleteFactorSetReq) (*factorpb.DeleteFactorSetRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSetId()) == "" {
		return &factorpb.DeleteFactorSetRsp{RetInfo: invalid(fmt.Errorf("set_id is required"))}, nil
	}
	if err := s.catalog.DeleteSet(ctx, req.GetSetId(), req.GetPurge()); err != nil {
		return &factorpb.DeleteFactorSetRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.DeleteFactorSetRsp{RetInfo: success()}, nil
}

func (s *Service) GetFactorSet(ctx context.Context, req *factorpb.GetFactorSetReq) (*factorpb.GetFactorSetRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSetId()) == "" {
		return &factorpb.GetFactorSetRsp{RetInfo: invalid(fmt.Errorf("set_id is required"))}, nil
	}
	set, members, err := s.catalog.GetSet(ctx, req.GetSetId())
	if err != nil {
		return &factorpb.GetFactorSetRsp{RetInfo: inner(err)}, nil
	}
	summary := s.latestRun(set.SetID)
	return &factorpb.GetFactorSetRsp{
		RetInfo: success(), FactorSet: factorwire.SetToPB(set), Members: membersToPB(members), LastRun: setRunSummaryToPBIfPresent(summary),
	}, nil
}

func (s *Service) ListFactorSets(ctx context.Context, req *factorpb.ListFactorSetsReq) (*factorpb.ListFactorSetsRsp, error) {
	sets, err := s.catalog.ListSets(ctx)
	if err != nil {
		return &factorpb.ListFactorSetsRsp{RetInfo: inner(err)}, nil
	}
	filtered := make([]domain.FactorSet, 0, len(sets))
	for _, set := range sets {
		if req == nil || req.GetStatus() == "" || set.Status == req.GetStatus() {
			filtered = append(filtered, set)
		}
	}
	page, size := pageParams(pageFromSetReq(req))
	items, total := paginate(filtered, page, size)
	infos := make([]*factorpb.FactorSetInfo, 0, len(items))
	for _, set := range items {
		_, members, err := s.catalog.GetSet(ctx, set.SetID)
		if err != nil {
			return &factorpb.ListFactorSetsRsp{RetInfo: inner(err)}, nil
		}
		summary := s.latestRun(set.SetID)
		infos = append(infos, &factorpb.FactorSetInfo{FactorSet: factorwire.SetToPB(set), Members: membersToPB(members), LastRun: setRunSummaryToPBIfPresent(summary)})
	}
	return &factorpb.ListFactorSetsRsp{RetInfo: success(), FactorSets: infos, PageResult: pageResult(page, size, total)}, nil
}

func (s *Service) CreateFactor(ctx context.Context, req *factorpb.CreateFactorReq) (*factorpb.CreateFactorRsp, error) {
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

func (s *Service) AddFactorToSet(ctx context.Context, req *factorpb.AddFactorToSetReq) (*factorpb.AddFactorToSetRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSetId()) == "" || strings.TrimSpace(req.GetFactorId()) == "" {
		return &factorpb.AddFactorToSetRsp{RetInfo: invalid(fmt.Errorf("set_id and factor_id are required"))}, nil
	}
	member, err := s.catalog.AddFactorToSet(ctx, req.GetSetId(), req.GetFactorId())
	if err != nil {
		return &factorpb.AddFactorToSetRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.AddFactorToSetRsp{RetInfo: success(), Member: memberToPB(member, false)}, nil
}

func (s *Service) RemoveFactorFromSet(ctx context.Context, req *factorpb.RemoveFactorFromSetReq) (*factorpb.RemoveFactorFromSetRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSetId()) == "" || strings.TrimSpace(req.GetFactorId()) == "" {
		return &factorpb.RemoveFactorFromSetRsp{RetInfo: invalid(fmt.Errorf("set_id and factor_id are required"))}, nil
	}
	if err := s.catalog.RemoveFactorFromSet(ctx, req.GetSetId(), req.GetFactorId()); err != nil {
		return &factorpb.RemoveFactorFromSetRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.RemoveFactorFromSetRsp{RetInfo: success()}, nil
}

func (s *Service) SetFactorMemberStatus(ctx context.Context, req *factorpb.SetFactorMemberStatusReq) (*factorpb.SetFactorMemberStatusRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSetId()) == "" || strings.TrimSpace(req.GetFactorId()) == "" {
		return &factorpb.SetFactorMemberStatusRsp{RetInfo: invalid(fmt.Errorf("set_id and factor_id are required"))}, nil
	}
	status := req.GetStatus()
	if status != domain.MemberStatusEnabled && status != domain.MemberStatusDisabled {
		return &factorpb.SetFactorMemberStatusRsp{RetInfo: invalid(fmt.Errorf("status must be %q or %q", domain.MemberStatusEnabled, domain.MemberStatusDisabled))}, nil
	}
	member, backfillJobID, err := s.catalog.SetFactorMemberStatus(ctx, req.GetSetId(), req.GetFactorId(), status)
	if err != nil {
		return &factorpb.SetFactorMemberStatusRsp{RetInfo: inner(err)}, nil
	}
	rsp := &factorpb.SetFactorMemberStatusRsp{RetInfo: success(), Member: memberToPB(member, false)}
	if backfillJobID != "" {
		job, err := s.recalc.Get(ctx, backfillJobID)
		if err != nil {
			return &factorpb.SetFactorMemberStatusRsp{RetInfo: inner(fmt.Errorf("load backfill job %s: %w", backfillJobID, err))}, nil
		}
		rsp.BackfillJob = recalcJobToPB(job)
	}
	return rsp, nil
}

func (s *Service) DeleteFactor(ctx context.Context, req *factorpb.DeleteFactorReq) (*factorpb.DeleteFactorRsp, error) {
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
	// GetFactor is the one read that always carries the source code.
	return &factorpb.GetFactorRsp{RetInfo: success(), Factor: factorwire.DefToPB(info.Factor), Usages: usagesToPB(info.Usages)}, nil
}

func (s *Service) ListFactors(ctx context.Context, req *factorpb.ListFactorsReq) (*factorpb.ListFactorsRsp, error) {
	setID, status, includeSource := "", "", false
	if req != nil {
		setID, status, includeSource = req.GetSetId(), req.GetStatus(), req.GetIncludeSource()
	}
	factors, err := s.catalog.ListFactors(ctx, setID, status)
	if err != nil {
		return &factorpb.ListFactorsRsp{RetInfo: inner(err)}, nil
	}
	page, size := pageParams(pageFromFactorReq(req))
	items, total := paginate(factors, page, size)
	return &factorpb.ListFactorsRsp{RetInfo: success(), Factors: factorInfosToPB(items, includeSource), PageResult: pageResult(page, size, total)}, nil
}

func (s *Service) RecalcFactors(ctx context.Context, req *factorpb.RecalcFactorsReq) (*factorpb.RecalcFactorsRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSetId()) == "" || strings.TrimSpace(req.GetRequestId()) == "" {
		return &factorpb.RecalcFactorsRsp{RetInfo: invalid(fmt.Errorf("set_id and request_id are required"))}, nil
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
	set, _, err := s.catalog.GetSet(ctx, req.GetSetId())
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
	job, err := s.recalc.Submit(ctx, req.GetSetId(), factorwire.CloneStrings(req.GetFactorIds()), factorwire.CloneStrings(req.GetSubjects()), req.GetRequestId(), start, end)
	if err != nil {
		return &factorpb.RecalcFactorsRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.RecalcFactorsRsp{RetInfo: success(), Job: recalcJobToPB(job)}, nil
}

func (s *Service) ListRecalcJobs(ctx context.Context, req *factorpb.ListRecalcJobsReq) (*factorpb.ListRecalcJobsRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSetId()) == "" {
		return &factorpb.ListRecalcJobsRsp{RetInfo: invalid(fmt.Errorf("set_id is required"))}, nil
	}
	jobs, err := s.recalc.List(ctx, req.GetSetId(), factorwire.CloneStrings(req.GetStatuses()))
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
	job, err := s.recalc.Get(ctx, req.GetJobId())
	if err != nil {
		return &factorpb.GetRecalcJobRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.GetRecalcJobRsp{RetInfo: success(), Job: recalcJobToPB(job)}, nil
}

func (s *Service) CancelRecalcJob(ctx context.Context, req *factorpb.CancelRecalcJobReq) (*factorpb.CancelRecalcJobRsp, error) {
	if req == nil || strings.TrimSpace(req.GetJobId()) == "" {
		return &factorpb.CancelRecalcJobRsp{RetInfo: invalid(fmt.Errorf("job_id is required"))}, nil
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
		return &factorpb.GetStatusRsp{RetInfo: success(), Engine: &factorpb.EngineInfo{}}, nil
	}
	recent := make([]*factorpb.SetRunSummary, 0, len(status.RecentRuns))
	for _, run := range status.RecentRuns {
		recent = append(recent, factorwire.RunSummaryToPB(run))
	}
	return &factorpb.GetStatusRsp{
		RetInfo: success(), ConsumerRunning: status.ConsumerRunning, PythonWorkers: status.PythonWorkers,
		PythonBusy: status.PythonBusy, Lanes: factorwire.LanesToPB(status.Lanes), RecentRuns: recent,
		Engine: &factorpb.EngineInfo{
			EngineId: info.EngineID, BootId: info.BootID, Version: info.Version, Online: info.Online,
			LastHeartbeatAt: factorwire.FormatTime(info.LastHeartbeatAt), CatalogHash: info.CatalogHash,
			CatalogSyncedAt: factorwire.FormatTime(info.CatalogSyncedAt), CatalogInSync: info.CatalogInSync,
		},
	}, nil
}

func (s *Service) latestRun(setID string) domain.SetRunSummary {
	if s.engine == nil {
		return domain.SetRunSummary{SetID: setID}
	}
	return s.engine.LatestRun(setID)
}

func pageFromSetReq(req *factorpb.ListFactorSetsReq) *commonpb.Page {
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

func inner(err error) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: err.Error()}
}
