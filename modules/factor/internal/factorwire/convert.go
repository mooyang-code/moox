// Package factorwire converts between factor domain types and their protobuf
// form. It is shared by moox-factor-mgr's RPC layer and moox-factor-engine.
package factorwire

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
)

func SetToPB(set domain.FactorSet) *factorpb.FactorSet {
	return &factorpb.FactorSet{
		SetId:           set.SetID,
		SpaceId:         set.SpaceID,
		SourceDatasetId: set.SourceDatasetID,
		Freq:            set.Freq,
		SubjectMode:     set.SubjectMode,
		Subjects:        CloneStrings(set.Subjects),
		ResultDatasetId: set.ResultDatasetID,
		Status:          set.Status,
		CreatedAt:       FormatTime(set.CreatedAt),
		UpdatedAt:       FormatTime(set.UpdatedAt),
	}
}

func SetFromPB(pb *factorpb.FactorSet) (domain.FactorSet, error) {
	if pb == nil {
		return domain.FactorSet{}, fmt.Errorf("factor_set is required")
	}
	createdAt, err := ParseTime(pb.GetCreatedAt())
	if err != nil {
		return domain.FactorSet{}, fmt.Errorf("created_at: %w", err)
	}
	updatedAt, err := ParseTime(pb.GetUpdatedAt())
	if err != nil {
		return domain.FactorSet{}, fmt.Errorf("updated_at: %w", err)
	}
	return domain.FactorSet{
		SetID:           pb.GetSetId(),
		SpaceID:         pb.GetSpaceId(),
		SourceDatasetID: pb.GetSourceDatasetId(),
		Freq:            pb.GetFreq(),
		SubjectMode:     pb.GetSubjectMode(),
		Subjects:        CloneStrings(pb.GetSubjects()),
		ResultDatasetID: pb.GetResultDatasetId(),
		Status:          pb.GetStatus(),
		CreatedAt:       createdAt,
		UpdatedAt:       updatedAt,
	}, nil
}

func DefToPB(factor domain.FactorDef) *factorpb.FactorDef {
	return &factorpb.FactorDef{
		FactorId:             factor.FactorID,
		Name:                 factor.Name,
		FactorType:           factor.FactorType,
		SourceCode:           factor.SourceCode,
		SourceHash:           factor.SourceHash,
		InputColumns:         CloneStrings(factor.InputColumns),
		Outputs:              CloneStrings(factor.Outputs),
		ParamsJson:           factor.ParamsJSON,
		LookbackPeriods:      int32(factor.LookbackPeriods),
		AllowPartialUniverse: factor.AllowPartialUniverse,
		CreatedAt:            FormatTime(factor.CreatedAt),
		UpdatedAt:            FormatTime(factor.UpdatedAt),
	}
}

func DefFromPB(pb *factorpb.FactorDef) (domain.FactorDef, error) {
	if pb == nil {
		return domain.FactorDef{}, fmt.Errorf("factor is required")
	}
	createdAt, err := ParseTime(pb.GetCreatedAt())
	if err != nil {
		return domain.FactorDef{}, fmt.Errorf("created_at: %w", err)
	}
	updatedAt, err := ParseTime(pb.GetUpdatedAt())
	if err != nil {
		return domain.FactorDef{}, fmt.Errorf("updated_at: %w", err)
	}
	return domain.FactorDef{
		FactorID:             pb.GetFactorId(),
		Name:                 pb.GetName(),
		FactorType:           pb.GetFactorType(),
		SourceCode:           pb.GetSourceCode(),
		SourceHash:           pb.GetSourceHash(),
		InputColumns:         CloneStrings(pb.GetInputColumns()),
		Outputs:              CloneStrings(pb.GetOutputs()),
		ParamsJSON:           pb.GetParamsJson(),
		LookbackPeriods:      int(pb.GetLookbackPeriods()),
		AllowPartialUniverse: pb.GetAllowPartialUniverse(),
		CreatedAt:            createdAt,
		UpdatedAt:            updatedAt,
	}, nil
}

func RunSummaryToPB(summary domain.SetRunSummary) *factorpb.SetRunSummary {
	factors := make([]*factorpb.FactorPeriodState, 0, len(summary.Factors))
	for _, factor := range summary.Factors {
		factors = append(factors, &factorpb.FactorPeriodState{
			FactorId:       factor.FactorID,
			Status:         factor.Status,
			FailedSubjects: CloneStrings(factor.FailedSubjects),
			SourceHash:     factor.SourceHash,
		})
	}
	return &factorpb.SetRunSummary{
		SetId:          summary.SetID,
		LastPeriodTime: summary.LastPeriodTime,
		LastStatus:     summary.LastStatus,
		LagSeconds:     summary.LagSeconds,
		Factors:        factors,
		FailedSubjects: CloneStrings(summary.FailedSubjects),
	}
}

func RunSummaryFromPB(pb *factorpb.SetRunSummary) domain.SetRunSummary {
	factors := make([]domain.FactorPeriodState, 0, len(pb.GetFactors()))
	for _, factor := range pb.GetFactors() {
		factors = append(factors, domain.FactorPeriodState{
			FactorID: factor.GetFactorId(), Status: factor.GetStatus(),
			FailedSubjects: CloneStrings(factor.GetFailedSubjects()), SourceHash: factor.GetSourceHash(),
		})
	}
	return domain.SetRunSummary{
		SetID: pb.GetSetId(), LastPeriodTime: pb.GetLastPeriodTime(), LastStatus: pb.GetLastStatus(),
		LagSeconds: pb.GetLagSeconds(), Factors: factors, FailedSubjects: CloneStrings(pb.GetFailedSubjects()),
	}
}

func LanesToPB(lanes []domain.LaneStatus) []*factorpb.FactorLaneStatus {
	out := make([]*factorpb.FactorLaneStatus, 0, len(lanes))
	for _, lane := range lanes {
		out = append(out, &factorpb.FactorLaneStatus{
			SetId: lane.SetID, Queued: lane.Queued, Active: lane.Active,
			WarmupState: lane.WarmupState, WarmSubjects: lane.WarmSubjects, ExpectedSubjects: lane.ExpectedSubjects,
		})
	}
	return out
}

func LanesFromPB(lanes []*factorpb.FactorLaneStatus) []domain.LaneStatus {
	out := make([]domain.LaneStatus, 0, len(lanes))
	for _, lane := range lanes {
		out = append(out, domain.LaneStatus{
			SetID: lane.GetSetId(), Queued: lane.GetQueued(), Active: lane.GetActive(),
			WarmupState: lane.GetWarmupState(), WarmSubjects: lane.GetWarmSubjects(), ExpectedSubjects: lane.GetExpectedSubjects(),
		})
	}
	return out
}

func EngineIdentityToPB(id domain.EngineIdentity) *factorpb.EngineIdentity {
	return &factorpb.EngineIdentity{EngineId: id.EngineID, BootId: id.BootID, Version: id.Version}
}

func EngineIdentityFromPB(pb *factorpb.EngineIdentity) domain.EngineIdentity {
	return domain.EngineIdentity{EngineID: pb.GetEngineId(), BootID: pb.GetBootId(), Version: pb.GetVersion()}
}

func EngineStatusToPB(status domain.EngineStatus) *factorpb.EngineRuntimeStatus {
	runs := make([]*factorpb.SetRunSummary, 0, len(status.RecentRuns))
	for _, run := range status.RecentRuns {
		runs = append(runs, RunSummaryToPB(run))
	}
	return &factorpb.EngineRuntimeStatus{
		ConsumerRunning: status.ConsumerRunning, PythonWorkers: status.PythonWorkers, PythonBusy: status.PythonBusy,
		Lanes: LanesToPB(status.Lanes), RecentRuns: runs,
		CatalogHash: status.CatalogHash, CatalogSyncedAt: FormatTime(status.CatalogSyncedAt),
	}
}

func EngineStatusFromPB(pb *factorpb.EngineRuntimeStatus) (domain.EngineStatus, error) {
	syncedAt, err := ParseTime(pb.GetCatalogSyncedAt())
	if err != nil {
		return domain.EngineStatus{}, fmt.Errorf("catalog_synced_at: %w", err)
	}
	runs := make([]domain.SetRunSummary, 0, len(pb.GetRecentRuns()))
	for _, run := range pb.GetRecentRuns() {
		runs = append(runs, RunSummaryFromPB(run))
	}
	return domain.EngineStatus{
		ConsumerRunning: pb.GetConsumerRunning(), PythonWorkers: pb.GetPythonWorkers(), PythonBusy: pb.GetPythonBusy(),
		Lanes: LanesFromPB(pb.GetLanes()), RecentRuns: runs, CatalogHash: pb.GetCatalogHash(), CatalogSyncedAt: syncedAt,
	}, nil
}

// EngineSetToPB includes each factor's source code: the engine materializes it.
func EngineSetToPB(set domain.EngineSet) *factorpb.EngineSet {
	factors := make([]*factorpb.FactorDef, 0, len(set.Factors))
	for _, factor := range set.Factors {
		factors = append(factors, DefToPB(factor))
	}
	return &factorpb.EngineSet{FactorSet: SetToPB(set.Set), Factors: factors, ResultReady: set.ResultReady}
}

func EngineSetFromPB(pb *factorpb.EngineSet) (domain.EngineSet, error) {
	set, err := SetFromPB(pb.GetFactorSet())
	if err != nil {
		return domain.EngineSet{}, err
	}
	factors := make([]domain.FactorDef, 0, len(pb.GetFactors()))
	for _, raw := range pb.GetFactors() {
		factor, err := DefFromPB(raw)
		if err != nil {
			return domain.EngineSet{}, fmt.Errorf("factor %s: %w", raw.GetFactorId(), err)
		}
		factors = append(factors, factor)
	}
	return domain.EngineSet{Set: set, Factors: factors, ResultReady: pb.GetResultReady()}, nil
}

func FormatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func ParseTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func CloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}
