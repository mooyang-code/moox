// Package manager is the worker's client for the archive manager's ArchiveWorker service.
package manager

import (
	"context"
	"errors"
	"fmt"

	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// ErrConflict 表示 mgr 返回冲突（重复实例、令牌失效或运行已结束）。
var ErrConflict = errors.New("archive manager conflict")

// Client 是 ArchiveWorker 服务的调用接口。
type Client interface {
	Heartbeat(ctx context.Context, req *archivepb.ArchiveWorkerHeartbeatReq) (*archivepb.ArchiveWorkerHeartbeatRsp, error)
	Pull(ctx context.Context, req *archivepb.PullArchiveRunReq) (*archivepb.PullArchiveRunRsp, error)
	Progress(ctx context.Context, req *archivepb.ReportArchiveRunProgressReq) (*archivepb.ReportArchiveRunProgressRsp, error)
	Complete(ctx context.Context, req *archivepb.CompleteArchiveRunReq) (*archivepb.CompleteArchiveRunRsp, error)
}

const servicePath = "trpc.moox.archive.ArchiveWorker"

type gateway struct{ invoker gatewayclient.Invoker }

// New 返回经网关调用 mgr 的客户端。
func New(invoker gatewayclient.Invoker) Client { return &gateway{invoker: invoker} }

func call[Rsp any](ctx context.Context, g *gateway, method string, req any, rsp *Rsp, ret func(*Rsp) *commonpb.RetInfo) (*Rsp, error) {
	if err := g.invoker.Invoke(ctx, servicePath, method, req, rsp); err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	info := ret(rsp)
	switch info.GetCode() {
	case commonpb.ErrorCode_SUCCESS:
		return rsp, nil
	case commonpb.ErrorCode_CONFLICT:
		return nil, fmt.Errorf("%s: %w: %s", method, ErrConflict, info.GetMsg())
	default:
		return nil, fmt.Errorf("%s: %s", method, info.GetMsg())
	}
}

func (g *gateway) Heartbeat(ctx context.Context, req *archivepb.ArchiveWorkerHeartbeatReq) (*archivepb.ArchiveWorkerHeartbeatRsp, error) {
	return call(ctx, g, "ArchiveWorkerHeartbeat", req, &archivepb.ArchiveWorkerHeartbeatRsp{}, (*archivepb.ArchiveWorkerHeartbeatRsp).GetRetInfo)
}

func (g *gateway) Pull(ctx context.Context, req *archivepb.PullArchiveRunReq) (*archivepb.PullArchiveRunRsp, error) {
	return call(ctx, g, "PullArchiveRun", req, &archivepb.PullArchiveRunRsp{}, (*archivepb.PullArchiveRunRsp).GetRetInfo)
}

func (g *gateway) Progress(ctx context.Context, req *archivepb.ReportArchiveRunProgressReq) (*archivepb.ReportArchiveRunProgressRsp, error) {
	return call(ctx, g, "ReportArchiveRunProgress", req, &archivepb.ReportArchiveRunProgressRsp{}, (*archivepb.ReportArchiveRunProgressRsp).GetRetInfo)
}

func (g *gateway) Complete(ctx context.Context, req *archivepb.CompleteArchiveRunReq) (*archivepb.CompleteArchiveRunRsp, error) {
	return call(ctx, g, "CompleteArchiveRun", req, &archivepb.CompleteArchiveRunRsp{}, (*archivepb.CompleteArchiveRunRsp).GetRetInfo)
}
