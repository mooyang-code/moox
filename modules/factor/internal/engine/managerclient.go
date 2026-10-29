package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	// OnFilled 收到心跳响应里的补齐通知时调用（可为 nil）。
	OnFilled func([]*factorpb.PeriodsFilled)
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
	Sets        []domain.EngineTask
}

func (c *ManagerClient) SyncCatalog(ctx context.Context, knownHash string) (CatalogSnapshot, error) {
	var rsp factorpb.SyncEngineCatalogRsp
	req := &factorpb.SyncEngineCatalogReq{Engine: factorwire.EngineIdentityToPB(c.identity), KnownHash: knownHash}
	if err := c.call(ctx, "SyncEngineCatalog", req, &rsp, rsp.GetRetInfo); err != nil {
		return CatalogSnapshot{}, err
	}
	snapshot := CatalogSnapshot{Hash: rsp.GetCatalogHash(), NotModified: rsp.GetNotModified()}
	for _, raw := range rsp.GetTasks() {
		set, err := factorwire.EngineTaskFromPB(raw)
		if err != nil {
			return CatalogSnapshot{}, fmt.Errorf("decode factor task %s: %w", raw.GetFactorTask().GetTaskId(), err)
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
	if handler := c.OnFilled; handler != nil && len(rsp.GetFilled()) > 0 {
		handler(rsp.GetFilled())
	}
	return time.Duration(rsp.GetLeaseTtlSeconds()) * time.Second, nil
}

// PulledJob is a recalc job leased to this engine with its execution inputs.
type PulledJob struct {
	JobID      string
	LeaseToken string
	Window     recalcexec.Window
	Subjects   []string
	Set        domain.EngineTask
	// DefaultFilled 是管理端已经记下的、按默认参数新补的期次（成员键 → 闭区间）：作业中途换引擎或重启后接着累计。
	DefaultFilled map[string][]domain.TimeRange
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
	set, err := factorwire.EngineTaskFromPB(rsp.GetTask())
	if err != nil {
		return PulledJob{}, false, fmt.Errorf("decode recalc job set: %w", err)
	}
	job := PulledJob{JobID: rsp.GetJob().GetJobId(), LeaseToken: rsp.GetLeaseToken(), Subjects: rsp.GetJob().GetSubjects(), Set: set,
		DefaultFilled: defaultFilledFromRanges(rsp.GetJob().GetDefaultFilled())}
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

// ReportRecalcProgress returns the manager's current status of the job. defaultFilled 是到目前为止累计的按默认参数新补的期次，
// 为空表示没有变化。
func (c *ManagerClient) ReportRecalcProgress(ctx context.Context, jobID, leaseToken string, progress time.Time, status, errText string, defaultFilled map[string][]domain.TimeRange) (string, error) {
	var rsp factorpb.ReportRecalcProgressRsp
	req := &factorpb.ReportRecalcProgressReq{
		Engine: factorwire.EngineIdentityToPB(c.identity), JobId: jobID, LeaseToken: leaseToken,
		ProgressTime: factorwire.FormatTime(progress), Status: status, Error: errText,
		DefaultFilled: defaultFilledToRanges(defaultFilled),
	}
	if err := c.call(ctx, "ReportRecalcProgress", req, &rsp, rsp.GetRetInfo); err != nil {
		return "", err
	}
	return rsp.GetJobStatus(), nil
}

// GetUdfModule 按版本哈希向管理端取一个自定义算子模块的 wasm 字节。
func (c *ManagerClient) GetUdfModule(ctx context.Context, hash string) ([]byte, error) {
	var rsp factorpb.GetUdfModuleRsp
	req := &factorpb.GetUdfModuleReq{Engine: factorwire.EngineIdentityToPB(c.identity), Hash: hash}
	if err := c.call(ctx, "GetUdfModule", req, &rsp, rsp.GetRetInfo); err != nil {
		return nil, err
	}
	return rsp.GetWasm(), nil
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

func defaultFilledToRanges(filled map[string][]domain.TimeRange) []*factorpb.MemberRanges {
	keys := make([]string, 0, len(filled))
	for key := range filled {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]*factorpb.MemberRanges, 0, len(keys))
	for _, key := range keys {
		item := &factorpb.MemberRanges{MemberKey: key}
		for _, r := range filled[key] {
			item.Ranges = append(item.Ranges, &factorpb.TimeRange{Start: r.Start, End: r.End})
		}
		out = append(out, item)
	}
	return out
}

func defaultFilledFromRanges(items []*factorpb.MemberRanges) map[string][]domain.TimeRange {
	if len(items) == 0 {
		return nil
	}
	out := make(map[string][]domain.TimeRange, len(items))
	for _, item := range items {
		for _, r := range item.GetRanges() {
			out[item.GetMemberKey()] = append(out[item.GetMemberKey()], domain.TimeRange{Start: r.GetStart(), End: r.GetEnd()})
		}
	}
	return out
}
