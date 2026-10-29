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
	Snapshot(ctx context.Context, knownHash string) (string, bool, []domain.EngineTask, error)
	Heartbeat(ctx context.Context, id domain.EngineIdentity, status domain.EngineStatus) (time.Duration, error)
	Pull(ctx context.Context, id domain.EngineIdentity) (store.RecalcJob, domain.EngineTask, bool, error)
	Report(ctx context.Context, id domain.EngineIdentity, jobID, leaseToken string, progress time.Time, status, errText string, defaultFilled map[string][]store.TimeRange) (store.RecalcJob, error)
	HoldsLease(id domain.EngineIdentity) bool
}

// ParamAPI 是引擎用到的参数读取能力（由目录服务实现）。
type ParamAPI interface {
	GetParams(ctx context.Context, taskID string) ([]domain.FactorTaskMember, error)
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

// GetTaskParams 返回任务各可变成员最近一次被接受的提交；只有持有引擎租约的进程可以调用。
func (s *EngineService) GetTaskParams(ctx context.Context, req *factorpb.GetTaskParamsReq) (*factorpb.GetTaskParamsRsp, error) {
	if !s.hub.HoldsLease(factorwire.EngineIdentityFromPB(req.GetEngine())) {
		return &factorpb.GetTaskParamsRsp{RetInfo: engineError(enginehub.ErrLeaseConflict)}, nil
	}
	if strings.TrimSpace(req.GetTaskId()) == "" {
		return &factorpb.GetTaskParamsRsp{RetInfo: invalid(fmt.Errorf("task_id 必填"))}, nil
	}
	members, err := s.params.GetParams(ctx, req.GetTaskId())
	if err != nil {
		return &factorpb.GetTaskParamsRsp{RetInfo: engineError(err)}, nil
	}
	rsp := &factorpb.GetTaskParamsRsp{RetInfo: success()}
	for _, member := range members {
		rsp.Members = append(rsp.Members, factorwire.MemberParamStateToPB(member))
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
	out := make([]*factorpb.EngineTask, 0, len(sets))
	for _, set := range sets {
		out = append(out, factorwire.EngineTaskToPB(set))
	}
	return &factorpb.SyncEngineCatalogRsp{RetInfo: success(), CatalogHash: hash, NotModified: notModified, Tasks: out}, nil
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
		LeaseToken: job.LeaseToken, Task: factorwire.EngineTaskToPB(set),
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
	job, err := s.hub.Report(ctx, factorwire.EngineIdentityFromPB(req.GetEngine()), req.GetJobId(), req.GetLeaseToken(), progress, req.GetStatus(), req.GetError(), defaultFilledFromPB(req.GetDefaultFilled()))
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

// UdfModuleAPI 是引擎拉取自定义算子模块的来源（由目录服务实现）。
type UdfModuleAPI interface {
	UdfModule(ctx context.Context, hash string) (string, []byte, error)
}

// GetUdfModule 按版本哈希返回一个自定义算子模块的 wasm 字节；只有持有引擎租约的进程可以调用。
func (s *EngineService) GetUdfModule(ctx context.Context, req *factorpb.GetUdfModuleReq) (*factorpb.GetUdfModuleRsp, error) {
	if !s.hub.HoldsLease(factorwire.EngineIdentityFromPB(req.GetEngine())) {
		return &factorpb.GetUdfModuleRsp{RetInfo: engineError(enginehub.ErrLeaseConflict)}, nil
	}
	hash := strings.TrimSpace(req.GetHash())
	if hash == "" {
		return &factorpb.GetUdfModuleRsp{RetInfo: invalid(fmt.Errorf("hash 必填"))}, nil
	}
	source, ok := s.params.(UdfModuleAPI)
	if !ok {
		return &factorpb.GetUdfModuleRsp{RetInfo: inner(fmt.Errorf("自定义算子未启用"))}, nil
	}
	name, wasm, err := source.UdfModule(ctx, hash)
	if err != nil {
		return &factorpb.GetUdfModuleRsp{RetInfo: engineError(err)}, nil
	}
	return &factorpb.GetUdfModuleRsp{RetInfo: success(), Name: name, Wasm: wasm}, nil
}
