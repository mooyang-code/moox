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
}

// EngineService implements the internal FactorEngine contract used only by
// moox-factor-engine.
type EngineService struct {
	hub EngineHub
}

func NewEngineService(hub EngineHub) *EngineService { return &EngineService{hub: hub} }

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
	return &factorpb.EngineHeartbeatRsp{RetInfo: success(), LeaseTtlSeconds: int64(ttl / time.Second)}, nil
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
