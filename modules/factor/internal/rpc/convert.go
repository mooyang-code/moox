package rpc

import (
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
)

func factorToPB(f domain.FactorDef) *factorpb.FactorDef {
	return &factorpb.FactorDef{
		FactorId:      f.FactorID,
		Name:          f.Name,
		Kind:          f.Kind,
		SourceCode:    f.SourceCode,
		SourceHash:    f.SourceHash,
		ParamsJson:    f.ParamsJSON,
		LookbackBars:  int32(f.LookbackBars),
		WritebackBars: int32(f.WritebackBars),
		DependsJson:   f.DependsJSON,
		Status:        f.Status,
		CreatedAt:     formatTime(f.CreateTime),
		UpdatedAt:     formatTime(f.ModifyTime),
	}
}

func factorFromPB(pb *factorpb.FactorDef) domain.FactorDef {
	if pb == nil {
		return domain.FactorDef{}
	}
	return domain.FactorDef{
		FactorID:      pb.GetFactorId(),
		Name:          pb.GetName(),
		Kind:          pb.GetKind(),
		SourceCode:    pb.GetSourceCode(),
		SourceHash:    pb.GetSourceHash(),
		ParamsJSON:    pb.GetParamsJson(),
		LookbackBars:  int(pb.GetLookbackBars()),
		WritebackBars: int(pb.GetWritebackBars()),
		DependsJSON:   pb.GetDependsJson(),
		Status:        pb.GetStatus(),
	}
}

func bindingToPB(b domain.FactorBinding) *factorpb.FactorBinding {
	return &factorpb.FactorBinding{
		BindingId:     b.BindingID,
		FactorId:      b.FactorID,
		SpaceId:       b.SpaceID,
		SourceDataset: b.SourceDataset,
		Freq:          b.Freq,
		SubjectMode:   b.SubjectMode,
		SubjectsJson:  b.SubjectsJSON,
		TargetDataset: b.TargetDataset,
		Status:        b.Status,
		CreatedAt:     formatTime(b.CreateTime),
		UpdatedAt:     formatTime(b.ModifyTime),
	}
}

func bindingFromPB(pb *factorpb.FactorBinding) domain.FactorBinding {
	if pb == nil {
		return domain.FactorBinding{}
	}
	return domain.FactorBinding{
		BindingID:     pb.GetBindingId(),
		FactorID:      pb.GetFactorId(),
		SpaceID:       pb.GetSpaceId(),
		SourceDataset: pb.GetSourceDataset(),
		Freq:          pb.GetFreq(),
		SubjectMode:   pb.GetSubjectMode(),
		SubjectsJSON:  pb.GetSubjectsJson(),
		TargetDataset: pb.GetTargetDataset(),
		Status:        pb.GetStatus(),
	}
}

func runToPB(r domain.FactorRun) *factorpb.FactorRun {
	return &factorpb.FactorRun{
		RunId:         r.RunID,
		TriggerType:   r.TriggerType,
		SpaceId:       r.SpaceID,
		SourceDataset: r.SourceDataset,
		TargetDataset: r.TargetDataset,
		SubjectId:     r.SubjectID,
		Freq:          r.Freq,
		BarTime:       r.BarTime,
		FactorCount:   int32(r.FactorCount),
		Status:        r.Status,
		Error:         r.Error,
		ElapsedMs:     r.ElapsedMS,
		CreatedAt:     formatTime(r.CreateTime),
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
