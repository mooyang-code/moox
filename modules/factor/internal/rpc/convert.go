package rpc

import (
	"sort"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorwire"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
)

// factorDefToPBListed is factorDefToPB for list responses: unless the caller
// asked for it, spec is left out and only spec_hash identifies the definition.
func factorDefToPBListed(factor domain.FactorDef, includeSpec bool) *factorpb.FactorDef {
	pb := factorwire.DefToPB(factor)
	if !includeSpec {
		pb.Spec = ""
	}
	return pb
}

func usagesToPB(usages []domain.FactorUsage) []*factorpb.FactorUsage {
	out := make([]*factorpb.FactorUsage, 0, len(usages))
	for _, usage := range usages {
		out = append(out, &factorpb.FactorUsage{TaskId: usage.TaskID, MemberKey: usage.MemberKey, Status: usage.Status, Params: factorwire.CloneParams(usage.Params)})
	}
	return out
}

func factorInfosToPB(infos []domain.FactorInfo, includeSpec bool) []*factorpb.FactorInfo {
	out := make([]*factorpb.FactorInfo, 0, len(infos))
	for _, info := range infos {
		out = append(out, &factorpb.FactorInfo{
			Factor: factorDefToPBListed(info.Factor, includeSpec), Usages: usagesToPB(info.Usages),
		})
	}
	return out
}

func memberToPB(member domain.TaskMember, includeSpec bool) *factorpb.FactorMember {
	def := member.Def()
	pb := &factorpb.FactorMember{
		TaskId:              member.TaskID,
		MemberKey:           member.MemberKey,
		FactorId:            member.FactorID,
		Kind:                member.Kind,
		Status:              member.Status,
		Factor:              factorDefToPBListed(member.Factor, includeSpec),
		OutputColumns:       def.OutputColumns(),
		Params:              factorwire.CloneParams(member.Params),
		LastSubmittedPeriod: member.LastSubmittedPeriod,
		CreatedAt:           factorwire.FormatTime(member.CreatedAt),
		UpdatedAt:           factorwire.FormatTime(member.UpdatedAt),
	}
	if member.LastSubmittedAt != nil {
		pb.LastSubmittedAt = factorwire.FormatTime(*member.LastSubmittedAt)
	}
	return pb
}

// membersToPB renders a set's members without spec (list-style response).
func membersToPB(members []domain.TaskMember) []*factorpb.FactorMember {
	out := make([]*factorpb.FactorMember, 0, len(members))
	for _, member := range members {
		out = append(out, memberToPB(member, false))
	}
	return out
}

func taskRunSummaryToPBIfPresent(summary domain.TaskRunSummary) *factorpb.TaskRunSummary {
	if summary.LastPeriodTime == 0 && summary.LastStatus == "" && summary.LagSeconds == 0 {
		return nil
	}
	return factorwire.RunSummaryToPB(summary)
}

func recalcJobToPB(job RecalcJob) *factorpb.RecalcJob {
	return &factorpb.RecalcJob{
		JobId:         job.JobID,
		RequestId:     job.RequestID,
		TaskId:        job.TaskID,
		Members:       factorwire.CloneStrings(job.Members),
		Subjects:      factorwire.CloneStrings(job.Subjects),
		StartTime:     factorwire.FormatTime(job.StartTime),
		EndTime:       factorwire.FormatTime(job.EndTime),
		Status:        job.Status,
		ProgressTime:  factorwire.FormatTime(job.ProgressTime),
		Error:         job.Error,
		EngineId:      job.EngineID,
		DefaultFilled: defaultFilledToPB(job.DefaultFilled),
		CreatedAt:     factorwire.FormatTime(job.CreatedAt),
		UpdatedAt:     factorwire.FormatTime(job.UpdatedAt),
	}
}

// defaultFilledToPB 按成员键排序输出按默认参数新补的期次。
func defaultFilledToPB(filled map[string][]store.TimeRange) []*factorpb.MemberRanges {
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

// defaultFilledFromPB 还原引擎上报的按默认参数新补的期次；nil 表示这次没有变化。
func defaultFilledFromPB(items []*factorpb.MemberRanges) map[string][]store.TimeRange {
	if len(items) == 0 {
		return nil
	}
	out := make(map[string][]store.TimeRange, len(items))
	for _, item := range items {
		for _, r := range item.GetRanges() {
			out[item.GetMemberKey()] = append(out[item.GetMemberKey()], store.TimeRange{Start: r.GetStart(), End: r.GetEnd()})
		}
		if _, ok := out[item.GetMemberKey()]; !ok {
			out[item.GetMemberKey()] = nil
		}
	}
	return out
}
