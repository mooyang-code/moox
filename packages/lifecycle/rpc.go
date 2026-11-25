package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/commonpb"
)

// 生命周期的 RPC（设计 3.7）：采集、因子引擎、策略、Trade、因子管理、归档管理的服务各有 GetLifecycle、ControlLifecycle
// 两个方法，请求与响应是 commonpb 里同一组消息。服务端每个模块只写一行转给控制器（下面两个方法）；调用方用 Remote 经网关调用。

// 两个方法的方法名。
const (
	GetMethod     = "GetLifecycle"
	ControlMethod = "ControlLifecycle"
)

// errNotReady 是控制器还没建好时的回答（模块启动中）。
const errNotReady = "模块的生命周期还没有就绪，稍后重试"

// Settled 先推进一次恢复追赶（重新读取追赶进度，追平且设为自动放行的就此放行），再返回状态。
func (c *Controller) Settled(ctx context.Context) State {
	c.settle(ctx)
	return c.State()
}

// CaughtUp 报告模块是否已追平：不在恢复追赶中为真；恢复追赶中要有追赶进度且已追平。
func (s State) CaughtUp() bool {
	if s.Stage != StageResuming {
		return true
	}
	return s.Catchup != nil && s.Catchup.CaughtUp
}

// GetLifecycle 实现各模块服务的 GetLifecycle：refresh_catchup 为真时先推进一次恢复追赶；控制器为 nil（模块启动中）时返回 INNER_ERR。
func (c *Controller) GetLifecycle(ctx context.Context, req *commonpb.GetLifecycleReq) (*commonpb.GetLifecycleRsp, error) {
	if c == nil {
		return &commonpb.GetLifecycleRsp{RetInfo: retInfo(commonpb.ErrorCode_INNER_ERR, errNotReady)}, nil
	}
	state := c.State()
	if req.GetRefreshCatchup() {
		state = c.Settled(ctx)
	}
	return &commonpb.GetLifecycleRsp{RetInfo: retInfo(commonpb.ErrorCode_SUCCESS, ""), Lifecycle: Snapshot(state)}, nil
}

// ControlLifecycle 实现各模块服务的 ControlLifecycle：请求不合法为 INVALID_PARAM，当前状态不允许为 CONFLICT，
// 其余失败（例如暂停后在途操作没有在时限内结束）为 INNER_ERR；后两种也带上模块当前的状态。
func (c *Controller) ControlLifecycle(ctx context.Context, req *commonpb.ControlLifecycleReq) (*commonpb.ControlLifecycleRsp, error) {
	if c == nil {
		return &commonpb.ControlLifecycleRsp{RetInfo: retInfo(commonpb.ErrorCode_INNER_ERR, errNotReady)}, nil
	}
	request, err := RequestFromProto(req)
	if err != nil {
		return &commonpb.ControlLifecycleRsp{RetInfo: retInfo(commonpb.ErrorCode_INVALID_PARAM, err.Error()), Lifecycle: Snapshot(c.State())}, nil
	}
	state, err := c.Execute(ctx, request)
	var denied *ErrNotAllowed
	switch {
	case errors.As(err, &denied):
		return &commonpb.ControlLifecycleRsp{RetInfo: retInfo(commonpb.ErrorCode_CONFLICT, err.Error()), Lifecycle: Snapshot(state)}, nil
	case err != nil:
		return &commonpb.ControlLifecycleRsp{RetInfo: retInfo(commonpb.ErrorCode_INNER_ERR, err.Error()), Lifecycle: Snapshot(state)}, nil
	}
	return &commonpb.ControlLifecycleRsp{RetInfo: retInfo(commonpb.ErrorCode_SUCCESS, ""), Lifecycle: Snapshot(state)}, nil
}

func retInfo(code commonpb.ErrorCode, msg string) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: code, Msg: msg}
}

// ---------------------------------------------------------------- 调用方

// Invoker 是网关客户端的调用接口（gatewayclient.Client 与 moox-cli 的操作员网关都实现它）。
type Invoker interface {
	Invoke(ctx context.Context, service, method string, req, rsp any) error
}

// Remote 经网关调用一个模块的生命周期接口；Service 是模块服务的完整名（例如 trpc.moox.collector.CollectMgr）。
type Remote struct {
	Gateway Invoker
	Service string
}

// Status 读模块的当前状态，不推进恢复追赶。
func (r *Remote) Status(ctx context.Context) (State, error) { return r.get(ctx, false) }

// Catchup 先让模块推进一次恢复追赶再读状态：进度是最新的，追平且设为自动放行的模块就此放行。
func (r *Remote) Catchup(ctx context.Context) (State, error) { return r.get(ctx, true) }

func (r *Remote) get(ctx context.Context, refresh bool) (State, error) {
	rsp := &commonpb.GetLifecycleRsp{}
	if err := r.Gateway.Invoke(ctx, r.Service, GetMethod, &commonpb.GetLifecycleReq{RefreshCatchup: refresh}, rsp); err != nil {
		return State{}, fmt.Errorf("调用 %s/%s：%w", r.Service, GetMethod, err)
	}
	if code := rsp.GetRetInfo().GetCode(); code != commonpb.ErrorCode_SUCCESS {
		return State{}, errors.New(failureText(code, rsp.GetRetInfo().GetMsg()))
	}
	return StateFromSnapshot(rsp.GetLifecycle())
}

// Exec 执行命令并返回模块的新状态。模块拒绝时错误就是模块给出的说明（编排器按说明区分“在途操作没有结束”），
// 状态仍是模块当前的状态。
func (r *Remote) Exec(ctx context.Context, req Request) (State, error) {
	rsp := &commonpb.ControlLifecycleRsp{}
	if err := r.Gateway.Invoke(ctx, r.Service, ControlMethod, RequestToProto(req), rsp); err != nil {
		return State{}, fmt.Errorf("调用 %s/%s：%w", r.Service, ControlMethod, err)
	}
	state, err := StateFromSnapshot(rsp.GetLifecycle())
	if code := rsp.GetRetInfo().GetCode(); code != commonpb.ErrorCode_SUCCESS {
		return state, errors.New(failureText(code, rsp.GetRetInfo().GetMsg()))
	}
	return state, err
}

func failureText(code commonpb.ErrorCode, msg string) string {
	if msg == "" {
		return "模块返回 " + code.String()
	}
	return msg
}

// ---------------------------------------------------------------- 转换

// Snapshot 把状态转成 RPC 消息。
func Snapshot(s State) *commonpb.LifecycleSnapshot {
	out := &commonpb.LifecycleSnapshot{
		Module: s.Module, Stage: string(s.Stage), Reason: s.Reason, Since: toMillis(s.Since),
		PausedSince: toMillis(s.PausedSince), QuiescedAt: toMillis(s.QuiescedAt), ReleasedAt: toMillis(s.ReleasedAt),
	}
	if p := s.Pause; p != nil {
		out.Pause = &commonpb.LifecyclePause{Kind: string(p.Kind), Reason: p.Reason, By: p.By, Since: toMillis(p.Since), ExpiresAt: toMillis(p.ExpiresAt)}
	}
	if c := s.Catchup; c != nil {
		out.Catchup = c.Progress()
	}
	return out
}

// Progress 把恢复追赶进度转成消息（生命周期 RPC 与上报 Monitor 共用）。since 是时刻（毫秒）；target 是周期（data_time 文本）。
func (c Catchup) Progress() *commonpb.CatchupProgress {
	target := ""
	if !c.Target.IsZero() {
		target = c.Target.UTC().Format(time.RFC3339Nano)
	}
	return &commonpb.CatchupProgress{
		Since: toMillis(c.Since), Target: target, Done: c.Done, Total: c.Total, CaughtUp: c.CaughtUp,
		BlockedBy: c.BlockedBy, Unfillable: int32(c.Unfillable), Message: c.Message,
	}
}

// StateFromSnapshot 把 RPC 消息转回状态。
func StateFromSnapshot(in *commonpb.LifecycleSnapshot) (State, error) {
	if in == nil {
		return State{}, errors.New("模块的生命周期响应缺少状态")
	}
	var p timeParser
	out := State{
		Module: in.GetModule(), Stage: Stage(in.GetStage()), Reason: in.GetReason(), Since: p.millis("since", in.GetSince()),
		PausedSince: p.millis("paused_since", in.GetPausedSince()), QuiescedAt: p.millis("quiesced_at", in.GetQuiescedAt()),
		ReleasedAt: p.millis("released_at", in.GetReleasedAt()),
	}
	if pause := in.GetPause(); pause != nil {
		out.Pause = &PauseInfo{
			Kind: PauseKind(pause.GetKind()), Reason: pause.GetReason(), By: pause.GetBy(),
			Since: p.millis("pause.since", pause.GetSince()), ExpiresAt: p.millis("pause.expires_at", pause.GetExpiresAt()),
		}
	}
	if c := in.GetCatchup(); c != nil {
		out.Catchup = p.catchup(c)
	}
	return out, p.err
}

// CatchupFromProgress 把恢复追赶进度消息转回结构，是 Catchup.Progress 的反向。
func CatchupFromProgress(in *commonpb.CatchupProgress) (*Catchup, error) {
	if in == nil {
		return nil, nil
	}
	var p timeParser
	out := p.catchup(in)
	return out, p.err
}

func (p *timeParser) catchup(c *commonpb.CatchupProgress) *Catchup {
	return &Catchup{
		Since: p.millis("catchup.since", c.GetSince()), Target: p.text("catchup.target", c.GetTarget()), Done: c.GetDone(), Total: c.GetTotal(),
		CaughtUp: c.GetCaughtUp(), BlockedBy: c.GetBlockedBy(), Unfillable: int(c.GetUnfillable()), Message: c.GetMessage(),
	}
}

// RequestToProto 把命令请求转成 RPC 消息。
func RequestToProto(r Request) *commonpb.ControlLifecycleReq {
	return &commonpb.ControlLifecycleReq{
		Command: string(r.Command), PauseKind: string(r.PauseKind), Reason: r.Reason, By: r.By,
		ManualRelease: r.ManualRelease, ExpiresAt: toMillis(r.ExpiresAt),
	}
}

// RequestFromProto 解析 RPC 命令：只认三个命令与两种暂停类型。
func RequestFromProto(in *commonpb.ControlLifecycleReq) (Request, error) {
	command := Command(strings.TrimSpace(in.GetCommand()))
	switch command {
	case CmdPause, CmdResume, CmdRelease:
	default:
		return Request{}, fmt.Errorf("未知命令 %q（只有 pause、resume、release）", in.GetCommand())
	}
	kind := PauseKind(strings.TrimSpace(in.GetPauseKind()))
	if kind != "" && kind.rank() == 0 {
		return Request{}, fmt.Errorf("未知的暂停类型 %q（manual、system）", in.GetPauseKind())
	}
	var p timeParser
	out := Request{Command: command, PauseKind: kind, Reason: in.GetReason(), By: in.GetBy(), ManualRelease: in.GetManualRelease(),
		ExpiresAt: p.millis("expires_at", in.GetExpiresAt())}
	return out, p.err
}

// toMillis 把时刻转成 Unix 毫秒，零值时间是 0（表示没有）。
func toMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// timeParser 解析一串时间字段，记下第一个错误。
type timeParser struct{ err error }

// millis 把 Unix 毫秒转回时刻：0 是没有，负数是错误。
func (p *timeParser) millis(field string, value int64) time.Time {
	switch {
	case value == 0:
		return time.Time{}
	case value < 0:
		if p.err == nil {
			p.err = fmt.Errorf("%s 不是有效的毫秒时刻：%d", field, value)
		}
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}

// text 解析数据坐标的 RFC3339 文本（追赶目标），空串是没有。
func (p *timeParser) text(field, value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil && p.err == nil {
		p.err = fmt.Errorf("%s 不是 RFC3339 时间：%q", field, value)
	}
	return t.UTC()
}
