package engine

import (
	"context"
	"sort"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorwire"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

// replayService 实现 EngineControl 的回放方法：线上格式与返回码的换算在这里，会话与计算在 replayManager。
// 失败都放在 ret_info 里返回，传输层只在网关转发失败时报错（调用方据此判断“因子引擎不在线”）。
type replayService struct{ m *replayManager }

func (s replayService) OpenReplay(ctx context.Context, req *factorpb.OpenReplayReq) (*factorpb.OpenReplayRsp, error) {
	ctx, cancel := context.WithTimeout(ctx, replayOpenTimeout)
	defer cancel()
	rsp, err := s.m.Open(ctx, ReplayOpenRequest{
		ReplayID: req.GetReplayId(), TaskID: req.GetTaskId(), From: req.GetFrom(), To: req.GetTo(),
		ExtraPeriods: int(req.GetExtraPeriods()), Columns: req.GetColumns(), AllowShortWarmup: req.GetAllowShortWarmup(),
	})
	if err != nil {
		return &factorpb.OpenReplayRsp{RetInfo: replayRetInfo(err)}, nil
	}
	out := &factorpb.OpenReplayRsp{
		RetInfo: replaySuccess(), ReplayId: rsp.ReplayID, Subjects: rsp.Subjects, PeriodTimes: rsp.PeriodTimes,
		StepSeconds: rsp.StepSecs, WarmupStart: rsp.Start, SnapshotBytes: rsp.Bytes, SourceFields: rsp.Fields,
		ShortWarmup: int32(rsp.ShortWarmup), ShortSamples: rsp.ShortSamples,
	}
	for _, item := range rsp.Excluded {
		out.Excluded = append(out.Excluded, &factorpb.ReplayExcluded{SubjectId: item.Subject, Reason: item.Reason})
	}
	for _, factor := range rsp.Factors {
		out.Factors = append(out.Factors, &factorpb.ReplayFactor{MemberKey: factor.Member, FactorId: factor.FactorID, Kind: factor.Kind,
			DefinitionHash: factor.DefinitionHash, Outputs: factor.Outputs})
	}
	return out, nil
}

func (s replayService) StepReplay(ctx context.Context, req *factorpb.StepReplayReq) (*factorpb.StepReplayRsp, error) {
	params, err := replayParamsFromPB(req.GetParams())
	if err != nil {
		return &factorpb.StepReplayRsp{RetInfo: replayRetInfo(err)}, nil
	}
	rsp, err := s.m.Step(ctx, ReplayStepRequest{ReplayID: req.GetReplayId(), Period: req.GetPeriodTime(), Periods: int(req.GetPeriods()), Params: params})
	if err != nil {
		return &factorpb.StepReplayRsp{RetInfo: replayRetInfo(err)}, nil
	}
	return &factorpb.StepReplayRsp{RetInfo: replaySuccess(), Periods: rsp.wire, ComputeMs: rsp.ComputeMillis}, nil
}

func (s replayService) CloseReplay(_ context.Context, req *factorpb.CloseReplayReq) (*factorpb.CloseReplayRsp, error) {
	return &factorpb.CloseReplayRsp{RetInfo: replaySuccess(), Closed: s.m.closeSession(req.GetReplayId())}, nil
}

func replaySuccess() *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS, Msg: "success"}
}

func replayRetInfo(err error) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: replayCode(err), Msg: err.Error()}
}

// replayParamsFromPB 把请求里的成员参数换成快照；同一个成员出现两次是调用方的错误。
func replayParamsFromPB(members []*factorpb.MemberParams) (map[string]domain.ParamSnapshot, error) {
	if len(members) == 0 {
		return nil, nil
	}
	out := make(map[string]domain.ParamSnapshot, len(members))
	for _, member := range members {
		key := member.GetMemberKey()
		if _, dup := out[key]; dup {
			return nil, replayFail(commonpb.ErrorCode_INVALID_PARAM, "成员 %s 的参数重复出现", key)
		}
		out[key] = factorwire.SnapshotFromPB(member.GetParams())
	}
	return out, nil
}

// replayPeriodToPB 把一期的帧换成线上格式：行按标的的顺序，成员参数按成员键排序。
func replayPeriodToPB(frame ReplayPeriodFrame) *factorpb.ReplayPeriod {
	out := &factorpb.ReplayPeriod{PeriodTime: frame.Period, Rows: make([]*factorpb.ReplayRow, 0, len(frame.Rows))}
	for _, row := range frame.Rows {
		item := &factorpb.ReplayRow{SubjectId: row.Series.SubjectID, SeriesTag: row.Series.SeriesTag, Values: row.Values,
			Previous: row.Previous, AgeBars: int32(row.AgeBars)}
		if row.Params != nil {
			item.Params = make(map[string]*factorpb.ReplayParamValues, len(row.Params))
			for member, values := range row.Params {
				item.Params[member] = &factorpb.ReplayParamValues{Values: values}
			}
		}
		out.Rows = append(out.Rows, item)
	}
	if len(frame.States) > 0 {
		out.States = make(map[string]*factorpb.ReplayMemberState, len(frame.States))
		for member, state := range frame.States {
			out.States[member] = &factorpb.ReplayMemberState{Status: state.Status, FactorId: state.FactorID, DefinitionHash: state.DefinitionHash,
				Kind: state.Kind, FailedSubjects: state.FailedSubjects}
		}
	}
	keys := make([]string, 0, len(frame.AppliedParams))
	for key := range frame.AppliedParams {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out.AppliedParams = append(out.AppliedParams, &factorpb.MemberParams{MemberKey: key, Params: factorwire.SnapshotToPB(frame.AppliedParams[key])})
	}
	for _, rejection := range frame.RejectedParams {
		out.RejectedParams = append(out.RejectedParams, &factorpb.ReplayParamRejection{MemberKey: rejection.Member, Reason: rejection.Reason})
	}
	return out
}
