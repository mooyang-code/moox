package rpc

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorwire"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
)

// factorDefToPBListed is factorDefToPB for list responses: unless the caller
// asked for it, source_code is left out and only source_hash identifies the code.
func factorDefToPBListed(factor domain.FactorDef, includeSource bool) *factorpb.FactorDef {
	pb := factorwire.DefToPB(factor)
	if !includeSource {
		pb.SourceCode = ""
	}
	return pb
}

func usagesToPB(usages []domain.FactorUsage) []*factorpb.FactorUsage {
	out := make([]*factorpb.FactorUsage, 0, len(usages))
	for _, usage := range usages {
		out = append(out, &factorpb.FactorUsage{SetId: usage.SetID, Status: usage.Status})
	}
	return out
}

func factorInfosToPB(infos []domain.FactorInfo, includeSource bool) []*factorpb.FactorInfo {
	out := make([]*factorpb.FactorInfo, 0, len(infos))
	for _, info := range infos {
		out = append(out, &factorpb.FactorInfo{
			Factor: factorDefToPBListed(info.Factor, includeSource), Usages: usagesToPB(info.Usages),
		})
	}
	return out
}

func memberToPB(member domain.SetMember, includeSource bool) *factorpb.FactorMember {
	return &factorpb.FactorMember{
		SetId:     member.SetID,
		FactorId:  member.FactorID,
		Status:    member.Status,
		Factor:    factorDefToPBListed(member.Factor, includeSource),
		CreatedAt: factorwire.FormatTime(member.CreatedAt),
		UpdatedAt: factorwire.FormatTime(member.UpdatedAt),
	}
}

// membersToPB renders a set's members without source code (list-style response).
func membersToPB(members []domain.SetMember) []*factorpb.FactorMember {
	out := make([]*factorpb.FactorMember, 0, len(members))
	for _, member := range members {
		out = append(out, memberToPB(member, false))
	}
	return out
}

func setRunSummaryToPBIfPresent(summary domain.SetRunSummary) *factorpb.SetRunSummary {
	if summary.LastPeriodTime == 0 && summary.LastStatus == "" && summary.LagSeconds == 0 {
		return nil
	}
	return factorwire.RunSummaryToPB(summary)
}

func recalcJobToPB(job RecalcJob) *factorpb.RecalcJob {
	return &factorpb.RecalcJob{
		JobId:        job.JobID,
		RequestId:    job.RequestID,
		SetId:        job.SetID,
		FactorIds:    factorwire.CloneStrings(job.FactorIDs),
		Subjects:     factorwire.CloneStrings(job.Subjects),
		StartTime:    factorwire.FormatTime(job.StartTime),
		EndTime:      factorwire.FormatTime(job.EndTime),
		Status:       job.Status,
		ProgressTime: factorwire.FormatTime(job.ProgressTime),
		Error:        job.Error,
		EngineId:     job.EngineID,
		CreatedAt:    factorwire.FormatTime(job.CreatedAt),
		UpdatedAt:    factorwire.FormatTime(job.UpdatedAt),
	}
}

func recalcJobFromPB(pb *factorpb.RecalcJob) (RecalcJob, error) {
	if pb == nil {
		return RecalcJob{}, fmt.Errorf("recalc job is required")
	}
	var job RecalcJob
	job.JobID = pb.GetJobId()
	job.RequestID = pb.GetRequestId()
	job.SetID = pb.GetSetId()
	job.FactorIDs = factorwire.CloneStrings(pb.GetFactorIds())
	job.Subjects = factorwire.CloneStrings(pb.GetSubjects())
	job.Status = pb.GetStatus()
	job.Error = pb.GetError()
	job.EngineID = pb.GetEngineId()
	timeFields := []struct {
		name   string
		value  string
		target *time.Time
	}{
		{name: "start_time", value: pb.GetStartTime(), target: &job.StartTime},
		{name: "end_time", value: pb.GetEndTime(), target: &job.EndTime},
		{name: "progress_time", value: pb.GetProgressTime(), target: &job.ProgressTime},
		{name: "created_at", value: pb.GetCreatedAt(), target: &job.CreatedAt},
		{name: "updated_at", value: pb.GetUpdatedAt(), target: &job.UpdatedAt},
	}
	for _, field := range timeFields {
		parsed, err := factorwire.ParseTime(field.value)
		if err != nil {
			return RecalcJob{}, fmt.Errorf("%s: %w", field.name, err)
		}
		*field.target = parsed
	}
	return job, nil
}
