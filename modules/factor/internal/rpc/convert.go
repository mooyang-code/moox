package rpc

import (
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
)

func factorToPB(f domain.FactorDef) *factorpb.FactorDef {
	return &factorpb.FactorDef{
		FactorId:     f.FactorID,
		Name:         f.Name,
		SourceCode:   f.SourceCode,
		SourceHash:   f.SourceHash,
		Periods:      intsToInt32s(f.Periods),
		LookbackBars: int32(f.LookbackBars),
		Depends:      append([]string(nil), f.Depends...),
		Status:       f.Status,
		CreatedAt:    formatTime(f.CreateTime),
		UpdatedAt:    formatTime(f.ModifyTime),
	}
}

func factorFromPB(pb *factorpb.FactorDef) domain.FactorDef {
	if pb == nil {
		return domain.FactorDef{}
	}
	return domain.FactorDef{
		FactorID:     pb.GetFactorId(),
		Name:         pb.GetName(),
		SourceCode:   pb.GetSourceCode(),
		SourceHash:   pb.GetSourceHash(),
		Periods:      int32sToInts(pb.GetPeriods()),
		LookbackBars: int(pb.GetLookbackBars()),
		Depends:      append([]string(nil), pb.GetDepends()...),
		Status:       pb.GetStatus(),
	}
}

func intsToInt32s(values []int) []int32 {
	out := make([]int32, len(values))
	for i, value := range values {
		out[i] = int32(value)
	}
	return out
}

func int32sToInts(values []int32) []int {
	out := make([]int, len(values))
	for i, value := range values {
		out[i] = int(value)
	}
	return out
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

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
