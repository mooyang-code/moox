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
	"google.golang.org/protobuf/proto"
)

// ErrLeaseConflict means the manager rejected the call because another engine
// holds the engine lease or the recalc job lease moved on.
var ErrLeaseConflict = errors.New("factor engine lease conflict")

const managerServicePath = "trpc.moox.factor.FactorEngine"

// ManagerClient borrows the engine's external client, also used by Storage.
type ManagerClient struct {
	gateway  gatewayclient.Invoker
	timeout  time.Duration
	identity domain.EngineIdentity
}

func NewManagerClient(gateway gatewayclient.Invoker, timeout time.Duration, identity domain.EngineIdentity) (*ManagerClient, error) {
	if gateway == nil || timeout <= 0 {
		return nil, errors.New("factor manager requires a shared external gateway and positive timeout")
	}
	return &ManagerClient{gateway: gateway, timeout: timeout, identity: identity}, nil
}

// CatalogSnapshot is one SyncEngineCatalog answer.
type CatalogSnapshot struct {
	Hash        string
	NotModified bool
	Sets        []domain.EngineSet
}

func (c *ManagerClient) SyncCatalog(ctx context.Context, knownHash string) (CatalogSnapshot, error) {
	var rsp factorpb.SyncEngineCatalogRsp
	req := &factorpb.SyncEngineCatalogReq{Engine: factorwire.EngineIdentityToPB(c.identity), KnownHash: knownHash}
	if err := c.call(ctx, "SyncEngineCatalog", req, &rsp, rsp.GetRetInfo); err != nil {
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
	var rsp factorpb.EngineHeartbeatRsp
	req := &factorpb.EngineHeartbeatReq{Engine: factorwire.EngineIdentityToPB(c.identity), Status: factorwire.EngineStatusToPB(status)}
	if err := c.call(ctx, "EngineHeartbeat", req, &rsp, rsp.GetRetInfo); err != nil {
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
	var rsp factorpb.PullRecalcJobRsp
	req := &factorpb.PullRecalcJobReq{Engine: factorwire.EngineIdentityToPB(c.identity)}
	if err := c.call(ctx, "PullRecalcJob", req, &rsp, rsp.GetRetInfo); err != nil {
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
	var rsp factorpb.ReportRecalcProgressRsp
	req := &factorpb.ReportRecalcProgressReq{
		Engine: factorwire.EngineIdentityToPB(c.identity), JobId: jobID, LeaseToken: leaseToken,
		ProgressTime: factorwire.FormatTime(progress), Status: status, Error: errText,
	}
	if err := c.call(ctx, "ReportRecalcProgress", req, &rsp, rsp.GetRetInfo); err != nil {
		return "", err
	}
	return rsp.GetJobStatus(), nil
}

func (c *ManagerClient) call(ctx context.Context, method string, req, rsp proto.Message, retInfo func() *commonpb.RetInfo) error {
	if c == nil || c.gateway == nil {
		return errors.New("factor manager gateway is unavailable")
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := c.gateway.Invoke(callCtx, managerServicePath, method, req, rsp); err != nil {
		return fmt.Errorf("call %s: %w", method, err)
	}
	ret := retInfo()
	switch {
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
