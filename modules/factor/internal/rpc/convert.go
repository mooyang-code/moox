package rpc

import (
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
)

func factorToPB(f domain.FactorDef) *factorpb.FactorDef {
	return &factorpb.FactorDef{
		FactorId:        f.FactorID,
		Name:            f.Name,
		SourceCode:      f.SourceCode,
		SourceHash:      f.SourceHash,
		InputColumns:    append([]string(nil), f.InputColumns...),
		Outputs:         append([]string(nil), f.Outputs...),
		ParamsJson:      f.ParamsJSON,
		LookbackPeriods: int32(f.LookbackPeriods),
		Status:          f.Status,
		CreatedAt:       formatTime(f.CreateTime),
		UpdatedAt:       formatTime(f.ModifyTime),
	}
}

func factorFromPB(pb *factorpb.FactorDef) domain.FactorDef {
	if pb == nil {
		return domain.FactorDef{}
	}
	return domain.FactorDef{
		FactorID:        pb.GetFactorId(),
		Name:            pb.GetName(),
		SourceCode:      pb.GetSourceCode(),
		SourceHash:      pb.GetSourceHash(),
		InputColumns:    append([]string(nil), pb.GetInputColumns()...),
		Outputs:         append([]string(nil), pb.GetOutputs()...),
		ParamsJSON:      pb.GetParamsJson(),
		LookbackPeriods: int(pb.GetLookbackPeriods()),
		Status:          pb.GetStatus(),
	}
}

func bindingToPB(b domain.FactorBinding) *factorpb.FactorBinding {
	return &factorpb.FactorBinding{
		BindingId:       b.BindingID,
		FactorId:        b.FactorID,
		SpaceId:         b.SpaceID,
		SourceViewId:    b.SourceViewID,
		Freq:            b.Freq,
		SubjectMode:     b.SubjectMode,
		SubjectsJson:    b.SubjectsJSON,
		ResultDatasetId: b.ResultDatasetID,
		ResultViewId:    b.ResultViewID,
		Status:          b.Status,
		CreatedAt:       formatTime(b.CreateTime),
		UpdatedAt:       formatTime(b.ModifyTime),
		SourceDataset:   b.SourceViewID,
		TargetDataset:   b.ResultDatasetID,
	}
}

func bindingFromPB(pb *factorpb.FactorBinding) domain.FactorBinding {
	if pb == nil {
		return domain.FactorBinding{}
	}
	sourceViewID := pb.GetSourceViewId()
	if sourceViewID == "" {
		sourceViewID = pb.GetSourceDataset()
	}
	resultDatasetID := pb.GetResultDatasetId()
	if resultDatasetID == "" && pb.GetTargetDataset() != domain.DefaultBindingTargetID {
		resultDatasetID = pb.GetTargetDataset()
	}
	return domain.FactorBinding{
		BindingID:       pb.GetBindingId(),
		FactorID:        pb.GetFactorId(),
		SpaceID:         pb.GetSpaceId(),
		SourceViewID:    sourceViewID,
		SourceDataset:   sourceViewID,
		Freq:            pb.GetFreq(),
		SubjectMode:     pb.GetSubjectMode(),
		SubjectsJSON:    pb.GetSubjectsJson(),
		ResultDatasetID: resultDatasetID,
		TargetDataset:   resultDatasetID,
		ResultViewID:    pb.GetResultViewId(),
		Status:          pb.GetStatus(),
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
