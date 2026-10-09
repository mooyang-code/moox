package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorwire"
	"github.com/mooyang-code/moox/modules/factor/internal/recalcexec"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// ErrLeaseConflict means the manager rejected the call because another engine
// holds the engine lease or the recalc job lease moved on.
var ErrLeaseConflict = errors.New("factor engine lease conflict")

// ManagerClient 经外部接入调用 moox-factor-mgr 的 FactorEngine 服务（tRPC），以 factor-engine 身份签名。
type ManagerClient struct {
	proxy    factorpb.FactorEngineClientProxy
	identity domain.EngineIdentity
}

// NewManagerClient 用外部方式的 gatewayclient 创建 FactorEngine 客户端。
func NewManagerClient(gateway *gatewayclient.Client, cfg ManagerConfig, identity domain.EngineIdentity) *ManagerClient {
	return &ManagerClient{
		proxy:    factorpb.NewFactorEngineClientProxy(gateway.ClientOptions(gatewayclient.WithTimeout(cfg.Timeout))...),
		identity: identity,
	}
}

// CatalogSnapshot is one SyncEngineCatalog answer.
type CatalogSnapshot struct {
	Hash        string
	NotModified bool
	Sets        []domain.EngineSet
}

func (c *ManagerClient) SyncCatalog(ctx context.Context, knownHash string) (CatalogSnapshot, error) {
	rsp, err := c.proxy.SyncEngineCatalog(ctx, &factorpb.SyncEngineCatalogReq{Engine: factorwire.EngineIdentityToPB(c.identity), KnownHash: knownHash})
	if err := managerResult("SyncEngineCatalog", rsp.GetRetInfo(), err); err != nil {
		return CatalogSnapshot{}, err
	}
	snapshot := CatalogSnapshot{Hash: rsp.GetCatalogHash(), NotModified: rsp.GetNotModified()}
	for _, raw := range rsp.GetSets() {
		set, err := factorwire.EngineSetFromPB(raw)
		if err != nil {
			return CatalogSnapshot{}, fmt.Errorf("decode factor set %s: %w", raw.GetFactorSet().GetSetId(), err)
		}
		snapshot.Sets = append(snapshot.Sets, set)
	}
	return snapshot, nil
}

func (c *ManagerClient) Heartbeat(ctx context.Context, status domain.EngineStatus) (time.Duration, error) {
	rsp, err := c.proxy.EngineHeartbeat(ctx, &factorpb.EngineHeartbeatReq{Engine: factorwire.EngineIdentityToPB(c.identity), Status: factorwire.EngineStatusToPB(status)})
	if err := managerResult("EngineHeartbeat", rsp.GetRetInfo(), err); err != nil {
		return 0, err
	}
	return time.Duration(rsp.GetLeaseTtlSeconds()) * time.Second, nil
}

// PulledJob is a recalc job leased to this engine with its execution inputs.
type PulledJob struct {
	JobID      string
	LeaseToken string
	Window     recalcexec.Window
	Subjects   []string
	Set        domain.EngineSet
}

func (c *ManagerClient) PullRecalcJob(ctx context.Context) (PulledJob, bool, error) {
	rsp, err := c.proxy.PullRecalcJob(ctx, &factorpb.PullRecalcJobReq{Engine: factorwire.EngineIdentityToPB(c.identity)})
	if err := managerResult("PullRecalcJob", rsp.GetRetInfo(), err); err != nil {
		return PulledJob{}, false, err
	}
	if !rsp.GetFound() {
		return PulledJob{}, false, nil
	}
	set, err := factorwire.EngineSetFromPB(rsp.GetSet())
	if err != nil {
		return PulledJob{}, false, fmt.Errorf("decode recalc job set: %w", err)
	}
	job := PulledJob{JobID: rsp.GetJob().GetJobId(), LeaseToken: rsp.GetLeaseToken(), Subjects: rsp.GetJob().GetSubjects(), Set: set}
	for _, field := range []struct {
		name   string
		value  string
		target *time.Time
	}{
		{"start_time", rsp.GetJob().GetStartTime(), &job.Window.Start},
		{"end_time", rsp.GetJob().GetEndTime(), &job.Window.End},
		{"progress_time", rsp.GetJob().GetProgressTime(), &job.Window.Progress},
	} {
		parsed, err := factorwire.ParseTime(field.value)
		if err != nil {
			return PulledJob{}, false, fmt.Errorf("recalc job %s: %w", field.name, err)
		}
		*field.target = parsed
	}
	return job, true, nil
}

// ReportRecalcProgress returns the manager's current status of the job.
func (c *ManagerClient) ReportRecalcProgress(ctx context.Context, jobID, leaseToken string, progress time.Time, status, errText string) (string, error) {
	rsp, err := c.proxy.ReportRecalcProgress(ctx, &factorpb.ReportRecalcProgressReq{
		Engine: factorwire.EngineIdentityToPB(c.identity), JobId: jobID, LeaseToken: leaseToken,
		ProgressTime: factorwire.FormatTime(progress), Status: status, Error: errText,
	})
	if err := managerResult("ReportRecalcProgress", rsp.GetRetInfo(), err); err != nil {
		return "", err
	}
	return rsp.GetJobStatus(), nil
}

// managerResult 把一次 FactorEngine 调用的传输错误和业务返回码统一成错误。
func managerResult(method string, ret *commonpb.RetInfo, err error) error {
	switch {
	case err != nil:
		return fmt.Errorf("call %s: %w", method, err)
	case ret == nil:
		return fmt.Errorf("%s returned no ret_info", method)
	case ret.GetCode() == commonpb.ErrorCode_SUCCESS:
		return nil
	case ret.GetCode() == commonpb.ErrorCode_CONFLICT:
		return fmt.Errorf("%w: %s", ErrLeaseConflict, ret.GetMsg())
	default:
		return fmt.Errorf("%s failed: %s", method, ret.GetMsg())
	}
}
