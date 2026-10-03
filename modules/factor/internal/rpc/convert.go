package rpc

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
)

func factorSetToPB(set domain.FactorSet) *factorpb.FactorSet {
	return &factorpb.FactorSet{
		SetId:           set.SetID,
		SpaceId:         set.SpaceID,
		SourceDatasetId: set.SourceDatasetID,
		Freq:            set.Freq,
		SubjectMode:     set.SubjectMode,
		Subjects:        cloneStrings(set.Subjects),
		ResultDatasetId: set.ResultDatasetID,
		Status:          set.Status,
		CreatedAt:       formatTime(set.CreatedAt),
		UpdatedAt:       formatTime(set.UpdatedAt),
	}
}

func factorSetFromPB(pb *factorpb.FactorSet) (domain.FactorSet, error) {
	if pb == nil {
		return domain.FactorSet{}, fmt.Errorf("factor_set is required")
	}
	createdAt, err := parseTime(pb.GetCreatedAt())
	if err != nil {
		return domain.FactorSet{}, fmt.Errorf("created_at: %w", err)
	}
	updatedAt, err := parseTime(pb.GetUpdatedAt())
	if err != nil {
		return domain.FactorSet{}, fmt.Errorf("updated_at: %w", err)
	}
	return domain.FactorSet{
		SetID:           pb.GetSetId(),
		SpaceID:         pb.GetSpaceId(),
		SourceDatasetID: pb.GetSourceDatasetId(),
		Freq:            pb.GetFreq(),
		SubjectMode:     pb.GetSubjectMode(),
		Subjects:        cloneStrings(pb.GetSubjects()),
		ResultDatasetID: pb.GetResultDatasetId(),
		Status:          pb.GetStatus(),
		CreatedAt:       createdAt,
		UpdatedAt:       updatedAt,
	}, nil
}

func factorDefToPB(factor domain.FactorDef) *factorpb.FactorDef {
	return &factorpb.FactorDef{
		FactorId:             factor.FactorID,
		SetId:                factor.SetID,
		Name:                 factor.Name,
		FactorType:           factor.FactorType,
		SourceCode:           factor.SourceCode,
		SourceHash:           factor.SourceHash,
		InputColumns:         cloneStrings(factor.InputColumns),
		Outputs:              cloneStrings(factor.Outputs),
		ParamsJson:           factor.ParamsJSON,
		LookbackPeriods:      int32(factor.LookbackPeriods),
		AllowPartialUniverse: factor.AllowPartialUniverse,
		Status:               factor.Status,
		CreatedAt:            formatTime(factor.CreatedAt),
		UpdatedAt:            formatTime(factor.UpdatedAt),
	}
}

func factorDefFromPB(pb *factorpb.FactorDef) (domain.FactorDef, error) {
	if pb == nil {
		return domain.FactorDef{}, fmt.Errorf("factor is required")
	}
	createdAt, err := parseTime(pb.GetCreatedAt())
	if err != nil {
		return domain.FactorDef{}, fmt.Errorf("created_at: %w", err)
	}
	updatedAt, err := parseTime(pb.GetUpdatedAt())
	if err != nil {
		return domain.FactorDef{}, fmt.Errorf("updated_at: %w", err)
	}
	return domain.FactorDef{
		FactorID:             pb.GetFactorId(),
		SetID:                pb.GetSetId(),
		Name:                 pb.GetName(),
		FactorType:           pb.GetFactorType(),
		SourceCode:           pb.GetSourceCode(),
		SourceHash:           pb.GetSourceHash(),
		InputColumns:         cloneStrings(pb.GetInputColumns()),
		Outputs:              cloneStrings(pb.GetOutputs()),
		ParamsJSON:           pb.GetParamsJson(),
		LookbackPeriods:      int(pb.GetLookbackPeriods()),
		AllowPartialUniverse: pb.GetAllowPartialUniverse(),
		Status:               pb.GetStatus(),
		CreatedAt:            createdAt,
		UpdatedAt:            updatedAt,
	}, nil
}

func setRunSummaryToPB(summary SetRunSummary) *factorpb.SetRunSummary {
	return &factorpb.SetRunSummary{
		SetId:          summary.SetID,
		LastPeriodTime: summary.LastPeriodTime,
		LastStatus:     summary.LastStatus,
		LagSeconds:     summary.LagSeconds,
	}
}

func setRunSummaryToPBIfPresent(summary SetRunSummary) *factorpb.SetRunSummary {
	if summary.LastPeriodTime == 0 && summary.LastStatus == "" && summary.LagSeconds == 0 {
		return nil
	}
	return setRunSummaryToPB(summary)
}

func recalcJobToPB(job RecalcJob) *factorpb.RecalcJob {
	return &factorpb.RecalcJob{
		JobId:        job.JobID,
		RequestId:    job.RequestID,
		SetId:        job.SetID,
		FactorIds:    cloneStrings(job.FactorIDs),
		Subjects:     cloneStrings(job.Subjects),
		StartTime:    formatTime(job.StartTime),
		EndTime:      formatTime(job.EndTime),
		Status:       job.Status,
		ProgressTime: formatTime(job.ProgressTime),
		Error:        job.Error,
		CreatedAt:    formatTime(job.CreatedAt),
		UpdatedAt:    formatTime(job.UpdatedAt),
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
	job.FactorIDs = cloneStrings(pb.GetFactorIds())
	job.Subjects = cloneStrings(pb.GetSubjects())
	job.Status = pb.GetStatus()
	job.Error = pb.GetError()
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
		parsed, err := parseTime(field.value)
		if err != nil {
			return RecalcJob{}, fmt.Errorf("%s: %w", field.name, err)
		}
		*field.target = parsed
	}
	return job, nil
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}
