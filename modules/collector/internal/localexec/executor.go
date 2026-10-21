// Package localexec 是拉取与补采的本地执行端：与 SCF 函数同一套处理代码（serverless/market_data.Execute），
// 以内部 RPC MarketFetchExecutor.Invoke 提供给 Collector，先挂在 collector-stream 进程里。
package localexec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	marketserverless "github.com/mooyang-code/moox/modules/collector/internal/serverless/market_data"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
)

// DefaultConcurrency 是本地执行端同时执行的调用数上限：补采并发大时不能拖慢同进程的推送接收。
const DefaultConcurrency = 4

// MaxEventBytes 与 SCF 调用事件的上限一致。
const MaxEventBytes = 128 << 10

// Executor 实现 MarketFetchExecutor。
type Executor struct {
	pb.UnimplementedMarketFetchExecutor
	spaceID    string
	newHandler func() *marketfetch.Handler
	slots      chan struct{}
}

// New 构造本地执行端；newHandler 每次调用返回一个已装配存储的处理器（存储写入走本进程的网关客户端）。
func New(spaceID string, concurrency int, newHandler func() *marketfetch.Handler) *Executor {
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	return &Executor{spaceID: strings.ToLower(strings.TrimSpace(spaceID)), newHandler: newHandler, slots: make(chan struct{}, concurrency)}
}

// FunctionName 是本地执行端在写入来源与日志里使用的名字。
func FunctionName(spaceID string) string {
	return "local-" + strings.ToLower(strings.TrimSpace(spaceID))
}

// Invoke 执行一次与 SCF 函数相同的事件；并发超过上限时排队，直到调用方超时。
func (e *Executor) Invoke(ctx context.Context, req *pb.InvokeMarketFetchReq) (*pb.InvokeMarketFetchRsp, error) {
	fail := func(code pb.ErrorCode, message string) (*pb.InvokeMarketFetchRsp, error) {
		return &pb.InvokeMarketFetchRsp{RetInfo: &pb.RetInfo{Code: code, Msg: message}}, nil
	}
	if e == nil || e.newHandler == nil {
		return fail(pb.ErrorCode_INNER_ERR, "本地执行端未初始化")
	}
	if strings.ToLower(strings.TrimSpace(req.GetSpaceId())) != e.spaceID {
		return fail(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("本地执行端只服务空间 %s", e.spaceID))
	}
	if len(req.GetEvent()) == 0 || len(req.GetEvent()) > MaxEventBytes {
		return fail(pb.ErrorCode_INVALID_PARAM, "事件为空或超过 128KB")
	}
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	case <-ctx.Done():
		return fail(pb.ErrorCode_INNER_ERR, "本地执行端繁忙："+ctx.Err().Error())
	}
	handler := e.newHandler()
	response, err := marketserverless.Execute(ctx, req.GetEvent(), marketserverless.Env{SpaceID: e.spaceID, FunctionName: FunctionName(e.spaceID), Handler: handler})
	if err != nil {
		return fail(pb.ErrorCode_INNER_ERR, err.Error())
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return fail(pb.ErrorCode_INNER_ERR, err.Error())
	}
	return &pb.InvokeMarketFetchRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "ok"}, Response: raw}, nil
}

// InFlight 返回正在执行的调用数（指标与健康检查用）。
func (e *Executor) InFlight() int { return len(e.slots) }
