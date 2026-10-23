package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/enginehub"
	"github.com/mooyang-code/moox/modules/factor/internal/factorwire"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

var _ factorpb.FactorEngineService = (*EngineService)(nil)

// EngineHub is the manager-side coordinator behind FactorEngine.
type EngineHub interface {
	Snapshot(ctx context.Context, knownHash string) (string, bool, []domain.EngineSet, error)
	Heartbeat(ctx context.Context, id domain.EngineIdentity, status domain.EngineStatus) (time.Duration, error)
	Pull(ctx context.Context, id domain.EngineIdentity) (store.RecalcJob, domain.EngineSet, bool, error)
	Report(ctx context.Context, id domain.EngineIdentity, jobID, leaseToken string, progress time.Time, status, errText string) (store.RecalcJob, error)
	HoldsLease(id domain.EngineIdentity) bool
}

// ParamAPI 是引擎用到的参数冻结能力（由目录服务实现）。
type ParamAPI interface {
	FreezeParams(ctx context.Context, setIDs []string, period time.Time) (map[string][]domain.ParamFreeze, error)
	ApplyOfflineFreezes(ctx context.Context, freezes []domain.ParamFreeze) ([]domain.ParamFreeze, error)
	ListParamIntervals(ctx context.Context, setID, alias string, from, to time.Time) ([]domain.ParamInterval, error)
}

// EngineService implements the internal FactorEngine contract used only by
// moox-factor-engine.
type EngineService struct {
	hub    EngineHub
	params ParamAPI
	filled *FilledInbox
}

// NewEngineService 构造服务；filled 非空时心跳响应带上暂存的补齐通知。
func NewEngineService(hub EngineHub, params ParamAPI, filled *FilledInbox) *EngineService {
	return &EngineService{hub: hub, params: params, filled: filled}
}

// FreezeFactorParams 冻结一组集合在一期的参数；只有持有引擎租约的进程可以调用。
func (s *EngineService) FreezeFactorParams(ctx context.Context, req *factorpb.FreezeFactorParamsReq) (*factorpb.FreezeFactorParamsRsp, error) {
	if !s.hub.HoldsLease(factorwire.EngineIdentityFromPB(req.GetEngine())) {
		return &factorpb.FreezeFactorParamsRsp{RetInfo: engineError(enginehub.ErrLeaseConflict)}, nil
	}
	if len(req.GetSetIds()) == 0 || req.GetPeriodTime() <= 0 {
		return &factorpb.FreezeFactorParamsRsp{RetInfo: invalid(fmt.Errorf("set_ids 与 period_time 必填"))}, nil
	}
	period := time.Unix(req.GetPeriodTime(), 0).UTC()
	frozen, err := s.params.FreezeParams(ctx, req.GetSetIds(), period)
	if err != nil {
		return &factorpb.FreezeFactorParamsRsp{RetInfo: engineError(err)}, nil
	}
	rsp := &factorpb.FreezeFactorParamsRsp{RetInfo: success()}
	for _, setID := range req.GetSetIds() {
		item := &factorpb.SetParamFreeze{SetId: setID}
		for _, freeze := range frozen[setID] {
			item.Members = append(item.Members, factorwire.FreezeToPB(freeze))
		}
		rsp.Sets = append(rsp.Sets, item)
	}
	return rsp, nil
}

// ReportOfflineFreezes 合并引擎离线冻结的期次；返回管理端已有不同记录的期次。
func (s *EngineService) ReportOfflineFreezes(ctx context.Context, req *factorpb.ReportOfflineFreezesReq) (*factorpb.ReportOfflineFreezesRsp, error) {
	if !s.hub.HoldsLease(factorwire.EngineIdentityFromPB(req.GetEngine())) {
		return &factorpb.ReportOfflineFreezesRsp{RetInfo: engineError(enginehub.ErrLeaseConflict)}, nil
	}
	freezes := make([]domain.ParamFreeze, 0, len(req.GetFreezes()))
	for _, item := range req.GetFreezes() {
		snapshot := factorwire.SnapshotFromPB(item.GetParams())
		freezes = append(freezes, domain.ParamFreeze{SetID: item.GetSetId(), MemberAlias: item.GetMemberAlias(),
			PeriodTime: time.Unix(item.GetPeriodTime(), 0).UTC(), Snapshot: snapshot, SnapshotHash: snapshot.Hash(), Source: domain.ParamsCarriedOffline})
	}
	conflicts, err := s.params.ApplyOfflineFreezes(ctx, freezes)
	if err != nil {
		return &factorpb.ReportOfflineFreezesRsp{RetInfo: engineError(err)}, nil
	}
	rsp := &factorpb.ReportOfflineFreezesRsp{RetInfo: success()}
	for _, conflict := range conflicts {
		rsp.Conflicts = append(rsp.Conflicts, &factorpb.OfflineFreeze{SetId: conflict.SetID, MemberAlias: conflict.MemberAlias,
			PeriodTime: conflict.PeriodTime.Unix(), Params: factorwire.SnapshotToPB(conflict.Snapshot), Source: conflict.Source})
	}
	return rsp, nil
}

// ListParamIntervals 返回集合在 [from, to] 内的冻结区间（补算读取已应用参数）。
func (s *EngineService) ListParamIntervals(ctx context.Context, req *factorpb.ListParamIntervalsReq) (*factorpb.ListParamIntervalsRsp, error) {
	if !s.hub.HoldsLease(factorwire.EngineIdentityFromPB(req.GetEngine())) {
		return &factorpb.ListParamIntervalsRsp{RetInfo: engineError(enginehub.ErrLeaseConflict)}, nil
	}
	if strings.TrimSpace(req.GetSetId()) == "" || req.GetTo() < req.GetFrom() {
		return &factorpb.ListParamIntervalsRsp{RetInfo: invalid(fmt.Errorf("set_id 必填，且时间范围要有效"))}, nil
	}
	intervals, err := s.params.ListParamIntervals(ctx, req.GetSetId(), "", time.Unix(req.GetFrom(), 0).UTC(), time.Unix(req.GetTo(), 0).UTC())
	if err != nil {
		return &factorpb.ListParamIntervalsRsp{RetInfo: engineError(err)}, nil
	}
	rsp := &factorpb.ListParamIntervalsRsp{RetInfo: success()}
	for _, interval := range intervals {
		rsp.Intervals = append(rsp.Intervals, factorwire.IntervalToPB(interval))
	}
	return rsp, nil
}

func (s *EngineService) SyncEngineCatalog(ctx context.Context, req *factorpb.SyncEngineCatalogReq) (*factorpb.SyncEngineCatalogRsp, error) {
	if strings.TrimSpace(req.GetEngine().GetEngineId()) == "" {
		return &factorpb.SyncEngineCatalogRsp{RetInfo: invalid(fmt.Errorf("engine.engine_id is required"))}, nil
	}
	hash, notModified, sets, err := s.hub.Snapshot(ctx, req.GetKnownHash())
	if err != nil {
		return &factorpb.SyncEngineCatalogRsp{RetInfo: engineError(err)}, nil
	}
	out := make([]*factorpb.EngineSet, 0, len(sets))
	for _, set := range sets {
		out = append(out, factorwire.EngineSetToPB(set))
	}
	return &factorpb.SyncEngineCatalogRsp{RetInfo: success(), CatalogHash: hash, NotModified: notModified, Sets: out}, nil
}

func (s *EngineService) EngineHeartbeat(ctx context.Context, req *factorpb.EngineHeartbeatReq) (*factorpb.EngineHeartbeatRsp, error) {
	id := factorwire.EngineIdentityFromPB(req.GetEngine())
	if strings.TrimSpace(id.EngineID) == "" {
		return &factorpb.EngineHeartbeatRsp{RetInfo: invalid(fmt.Errorf("engine.engine_id is required"))}, nil
	}
	status, err := factorwire.EngineStatusFromPB(req.GetStatus())
	if err != nil {
		return &factorpb.EngineHeartbeatRsp{RetInfo: invalid(err)}, nil
	}
	ttl, err := s.hub.Heartbeat(ctx, id, status)
	if err != nil {
		return &factorpb.EngineHeartbeatRsp{RetInfo: engineError(err)}, nil
	}
	rsp := &factorpb.EngineHeartbeatRsp{RetInfo: success(), LeaseTtlSeconds: int64(ttl / time.Second)}
	if s.filled != nil {
		// 心跳成功说明对方持有租约：补齐通知只交给它。
		rsp.Filled = s.filled.Drain()
	}
	return rsp, nil
}

func (s *EngineService) PullRecalcJob(ctx context.Context, req *factorpb.PullRecalcJobReq) (*factorpb.PullRecalcJobRsp, error) {
	id := factorwire.EngineIdentityFromPB(req.GetEngine())
	if strings.TrimSpace(id.EngineID) == "" {
		return &factorpb.PullRecalcJobRsp{RetInfo: invalid(fmt.Errorf("engine.engine_id is required"))}, nil
	}
	job, set, found, err := s.hub.Pull(ctx, id)
	if err != nil {
		return &factorpb.PullRecalcJobRsp{RetInfo: engineError(err)}, nil
	}
	if !found {
		return &factorpb.PullRecalcJobRsp{RetInfo: success()}, nil
	}
	return &factorpb.PullRecalcJobRsp{
		RetInfo: success(), Found: true, Job: recalcJobToPB(JobFromStore(job)),
		LeaseToken: job.LeaseToken, Set: factorwire.EngineSetToPB(set),
	}, nil
}

func (s *EngineService) ReportRecalcProgress(ctx context.Context, req *factorpb.ReportRecalcProgressReq) (*factorpb.ReportRecalcProgressRsp, error) {
	if strings.TrimSpace(req.GetJobId()) == "" || strings.TrimSpace(req.GetLeaseToken()) == "" {
		return &factorpb.ReportRecalcProgressRsp{RetInfo: invalid(fmt.Errorf("job_id and lease_token are required"))}, nil
	}
	progress, err := factorwire.ParseTime(req.GetProgressTime())
	if err != nil || progress.IsZero() {
		return &factorpb.ReportRecalcProgressRsp{RetInfo: invalid(fmt.Errorf("progress_time must be an RFC3339 timestamp"))}, nil
	}
	job, err := s.hub.Report(ctx, factorwire.EngineIdentityFromPB(req.GetEngine()), req.GetJobId(), req.GetLeaseToken(), progress, req.GetStatus(), req.GetError())
	if err != nil {
		return &factorpb.ReportRecalcProgressRsp{RetInfo: engineError(err)}, nil
	}
	return &factorpb.ReportRecalcProgressRsp{RetInfo: success(), JobStatus: job.Status}, nil
}

// engineError maps lease conflicts to CONFLICT so the engine can tell "another
// engine owns this" from a transient failure.
func engineError(err error) *commonpb.RetInfo {
	if errors.Is(err, enginehub.ErrLeaseConflict) || errors.Is(err, store.ErrConflict) {
		return &commonpb.RetInfo{Code: commonpb.ErrorCode_CONFLICT, Msg: err.Error()}
	}
	return inner(err)
}
