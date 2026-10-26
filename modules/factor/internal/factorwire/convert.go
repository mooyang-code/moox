// Package factorwire converts between factor domain types and their protobuf
// form. It is shared by moox-factor-mgr's RPC layer and moox-factor-engine.
package factorwire

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
)

func SetToPB(set domain.FactorTask) *factorpb.FactorTask {
	return &factorpb.FactorTask{
		TaskId:          set.TaskID,
		SpaceId:         set.SpaceID,
		SourceDatasetId: set.SourceDatasetID,
		Freq:            set.Freq,
		ResultDatasetId: set.ResultDatasetID,
		OwnerInstanceId: set.OwnerInstanceID,
		Status:          set.Status,
		CreatedAt:       FormatTime(set.CreatedAt),
		UpdatedAt:       FormatTime(set.UpdatedAt),
	}
}

func SetFromPB(pb *factorpb.FactorTask) (domain.FactorTask, error) {
	if pb == nil {
		return domain.FactorTask{}, fmt.Errorf("factor_task is required")
	}
	createdAt, err := ParseTime(pb.GetCreatedAt())
	if err != nil {
		return domain.FactorTask{}, fmt.Errorf("created_at: %w", err)
	}
	updatedAt, err := ParseTime(pb.GetUpdatedAt())
	if err != nil {
		return domain.FactorTask{}, fmt.Errorf("updated_at: %w", err)
	}
	return domain.FactorTask{
		TaskID:          pb.GetTaskId(),
		SpaceID:         pb.GetSpaceId(),
		SourceDatasetID: pb.GetSourceDatasetId(),
		Freq:            pb.GetFreq(),
		ResultDatasetID: pb.GetResultDatasetId(),
		OwnerInstanceID: pb.GetOwnerInstanceId(),
		Status:          pb.GetStatus(),
		CreatedAt:       createdAt,
		UpdatedAt:       updatedAt,
	}, nil
}

func DefToPB(factor domain.FactorDef) *factorpb.FactorDef {
	var params []*factorpb.FactorParamDecl
	if compiled := factor.Compiled(); compiled != nil {
		for _, param := range compiled.Params {
			params = append(params, &factorpb.FactorParamDecl{Name: param.Name, Type: string(param.Type), Min: param.Min,
				Max: param.Max, DefaultValue: param.Default})
		}
	}
	return &factorpb.FactorDef{
		FactorId:             factor.FactorID,
		Name:                 factor.Name,
		CategoryId:           factor.CategoryID,
		Params:               params,
		FactorType:           factor.FactorType,
		Spec:                 factor.Spec,
		SpecHash:             factor.SpecHash,
		InputColumns:         CloneStrings(factor.InputColumns),
		Outputs:              CloneStrings(factor.Outputs),
		LookbackPeriods:      int32(factor.LookbackPeriods),
		AllowPartialUniverse: factor.AllowPartialUniverse,
		DefinitionHash:       domain.DefinitionHash(factor),
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
	// 派生字段（类型、输入、输出、回看）不从请求读取，由接收方编译 Spec 得到。
	def := domain.FactorDef{
		FactorID:             pb.GetFactorId(),
		Name:                 pb.GetName(),
		CategoryID:           pb.GetCategoryId(),
		Spec:                 pb.GetSpec(),
		SpecHash:             pb.GetSpecHash(),
		AllowPartialUniverse: pb.GetAllowPartialUniverse(),
		CreatedAt:            createdAt,
		UpdatedAt:            updatedAt,
	}
	if compiled, err := domain.CompileDefinition(def); err == nil {
		def = compiled
	} else {
		// 接收方编译不了（管理端与引擎的 expr 版本不一致）时保留发送方推导的派生字段：计算仍按失败处理，
		// 但组装能据输出列清掉这些列上的旧值，而不是留下陈旧结果。
		def.FactorType, def.LookbackPeriods = pb.GetFactorType(), int(pb.GetLookbackPeriods())
		def.InputColumns, def.Outputs = CloneStrings(pb.GetInputColumns()), CloneStrings(pb.GetOutputs())
	}
	return def, nil
}

func RunSummaryToPB(summary domain.TaskRunSummary) *factorpb.TaskRunSummary {
	factors := make([]*factorpb.FactorPeriodState, 0, len(summary.Factors))
	for _, factor := range summary.Factors {
		factors = append(factors, &factorpb.FactorPeriodState{
			FactorId:       factor.FactorID,
			MemberAlias:    factor.MemberAlias,
			Status:         factor.Status,
			FailedSubjects: CloneStrings(factor.FailedSubjects),
			SpecHash:       factor.SpecHash,
			DefinitionHash: factor.DefinitionHash,
			GapSubjects:    CloneStrings(factor.GapSubjects),
			ParamsSource:   factor.ParamsSource,
			ParamsDigest:   factor.ParamsDigest,
		})
	}
	return &factorpb.TaskRunSummary{
		TaskId:         summary.TaskID,
		LastPeriodTime: summary.LastPeriodTime, WarmingSubjects: summary.WarmingSubjects, CompletedAt: summary.CompletedAt,
		LastStatus:     summary.LastStatus,
		LagSeconds:     summary.LagSeconds,
		Factors:        factors,
		FailedSubjects: CloneStrings(summary.FailedSubjects),
	}
}

func RunSummaryFromPB(pb *factorpb.TaskRunSummary) domain.TaskRunSummary {
	factors := make([]domain.FactorPeriodState, 0, len(pb.GetFactors()))
	for _, factor := range pb.GetFactors() {
		factors = append(factors, domain.FactorPeriodState{
			FactorID: factor.GetFactorId(), MemberAlias: factor.GetMemberAlias(), Status: factor.GetStatus(),
			FailedSubjects: CloneStrings(factor.GetFailedSubjects()), SpecHash: factor.GetSpecHash(),
			DefinitionHash: factor.GetDefinitionHash(), GapSubjects: CloneStrings(factor.GetGapSubjects()),
			ParamsSource: factor.GetParamsSource(), ParamsDigest: factor.GetParamsDigest(),
		})
	}
	return domain.TaskRunSummary{
		TaskID: pb.GetTaskId(), LastPeriodTime: pb.GetLastPeriodTime(), LastStatus: pb.GetLastStatus(),
		LagSeconds: pb.GetLagSeconds(), Factors: factors, FailedSubjects: CloneStrings(pb.GetFailedSubjects()),
		WarmingSubjects: pb.GetWarmingSubjects(), CompletedAt: pb.GetCompletedAt(),
	}
}

func RunnersToPB(runners []domain.RunnerStatus) []*factorpb.FactorRunnerStatus {
	out := make([]*factorpb.FactorRunnerStatus, 0, len(runners))
	for _, runner := range runners {
		out = append(out, &factorpb.FactorRunnerStatus{
			TaskId: runner.TaskID, Queued: runner.Queued, Active: runner.Active,
			WarmupState: runner.WarmupState, WarmSubjects: runner.WarmSubjects, ExpectedSubjects: runner.ExpectedSubjects,
		})
	}
	return out
}

func RunnersFromPB(runners []*factorpb.FactorRunnerStatus) []domain.RunnerStatus {
	out := make([]domain.RunnerStatus, 0, len(runners))
	for _, runner := range runners {
		out = append(out, domain.RunnerStatus{
			TaskID: runner.GetTaskId(), Queued: runner.GetQueued(), Active: runner.GetActive(),
			WarmupState: runner.GetWarmupState(), WarmSubjects: runner.GetWarmSubjects(), ExpectedSubjects: runner.GetExpectedSubjects(),
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
	runs := make([]*factorpb.TaskRunSummary, 0, len(status.RecentRuns))
	for _, run := range status.RecentRuns {
		runs = append(runs, RunSummaryToPB(run))
	}
	watermarks := make([]*factorpb.TaskWatermarkStatus, 0, len(status.Watermarks))
	for _, item := range status.Watermarks {
		watermarks = append(watermarks, &factorpb.TaskWatermarkStatus{TaskId: item.TaskID, CommittedThrough: FormatTime(item.CommittedThrough), Target: FormatTime(item.Target),
			BlockedSince: FormatTime(item.BlockedSince), BlockedPeriod: FormatTime(item.BlockedPeriod), BlockedReason: item.BlockedReason})
	}
	health := make([]*factorpb.MemberParamHealth, 0, len(status.ParamHealth))
	for _, item := range status.ParamHealth {
		health = append(health, &factorpb.MemberParamHealth{TaskId: item.TaskID, MemberAlias: item.Member, CarriedPeriods: int32(item.CarriedPeriods),
			AlertAfter: int32(item.AlertAfter), LastOfflineAt: FormatTime(item.LastOfflineAt)})
	}
	return &factorpb.EngineRuntimeStatus{
		ConsumerRunning: status.ConsumerRunning, ComputeWorkers: status.ComputeWorkers, ExprVersion: status.ExprVersion,
		Runners: RunnersToPB(status.Runners), RecentRuns: runs,
		CatalogHash: status.CatalogHash, CatalogSyncedAt: FormatTime(status.CatalogSyncedAt), Watermarks: watermarks,
		ParamHealth: health, OfflinePending: int32(status.OfflinePending),
	}
}

func EngineStatusFromPB(pb *factorpb.EngineRuntimeStatus) (domain.EngineStatus, error) {
	syncedAt, err := ParseTime(pb.GetCatalogSyncedAt())
	if err != nil {
		return domain.EngineStatus{}, fmt.Errorf("catalog_synced_at: %w", err)
	}
	runs := make([]domain.TaskRunSummary, 0, len(pb.GetRecentRuns()))
	for _, run := range pb.GetRecentRuns() {
		runs = append(runs, RunSummaryFromPB(run))
	}
	watermarks := make([]domain.SetWatermark, 0, len(pb.GetWatermarks()))
	for _, item := range pb.GetWatermarks() {
		committed, _ := ParseTime(item.GetCommittedThrough())
		target, _ := ParseTime(item.GetTarget())
		blockedSince, _ := ParseTime(item.GetBlockedSince())
		blockedPeriod, _ := ParseTime(item.GetBlockedPeriod())
		watermarks = append(watermarks, domain.SetWatermark{TaskID: item.GetTaskId(), CommittedThrough: committed, Target: target, BlockedSince: blockedSince, BlockedPeriod: blockedPeriod, BlockedReason: item.GetBlockedReason()})
	}
	health := make([]domain.MemberParamHealth, 0, len(pb.GetParamHealth()))
	for _, item := range pb.GetParamHealth() {
		offlineAt, _ := ParseTime(item.GetLastOfflineAt())
		health = append(health, domain.MemberParamHealth{TaskID: item.GetTaskId(), Member: item.GetMemberAlias(), CarriedPeriods: int(item.GetCarriedPeriods()),
			AlertAfter: int(item.GetAlertAfter()), LastOfflineAt: offlineAt})
	}
	return domain.EngineStatus{
		ConsumerRunning: pb.GetConsumerRunning(), ComputeWorkers: pb.GetComputeWorkers(), ExprVersion: pb.GetExprVersion(),
		Runners: RunnersFromPB(pb.GetRunners()), RecentRuns: runs, CatalogHash: pb.GetCatalogHash(), CatalogSyncedAt: syncedAt, Watermarks: watermarks,
		ParamHealth: health, OfflinePending: int(pb.GetOfflinePending()),
	}, nil
}

// EngineTaskToPB includes each member's definition spec: the engine compiles it.
func EngineTaskToPB(set domain.EngineTask) *factorpb.EngineTask {
	members := make([]*factorpb.EngineMember, 0, len(set.Members))
	for _, member := range set.Members {
		members = append(members, &factorpb.EngineMember{MemberAlias: member.Alias, Factor: DefToPB(member.Factor), CarryAlertPeriods: int32(member.CarryAlertPeriods), Params: CloneParams(member.Params)})
	}
	return &factorpb.EngineTask{FactorTask: SetToPB(set.Set), Members: members, ResultReady: set.ResultReady}
}

func EngineTaskFromPB(pb *factorpb.EngineTask) (domain.EngineTask, error) {
	set, err := SetFromPB(pb.GetFactorTask())
	if err != nil {
		return domain.EngineTask{}, err
	}
	members := make([]domain.MemberDef, 0, len(pb.GetMembers()))
	for _, raw := range pb.GetMembers() {
		factor, err := DefFromPB(raw.GetFactor())
		if err != nil {
			return domain.EngineTask{}, fmt.Errorf("member %s: %w", raw.GetMemberAlias(), err)
		}
		members = append(members, domain.MemberDef{Alias: raw.GetMemberAlias(), Factor: factor, CarryAlertPeriods: int(raw.GetCarryAlertPeriods()), Params: CloneParams(raw.GetParams())})
	}
	return domain.EngineTask{Set: set, Members: members, ResultReady: pb.GetResultReady()}, nil
}

// SnapshotToPB 把参数快照转换成线上格式。
func SnapshotToPB(snapshot domain.ParamSnapshot) map[string]*factorpb.ParamValue {
	out := make(map[string]*factorpb.ParamValue, len(snapshot))
	for name, value := range snapshot {
		item := &factorpb.ParamValue{DefaultValue: value.Default}
		if len(value.Overrides) > 0 {
			item.Overrides = make(map[string]float64, len(value.Overrides))
			for subject, override := range value.Overrides {
				item.Overrides[subject] = override
			}
		}
		out[name] = item
	}
	return out
}

// SnapshotFromPB 把线上格式的参数快照还原。
func SnapshotFromPB(params map[string]*factorpb.ParamValue) domain.ParamSnapshot {
	out := make(domain.ParamSnapshot, len(params))
	for name, value := range params {
		item := domain.ParamValue{Default: value.GetDefaultValue()}
		if len(value.GetOverrides()) > 0 {
			item.Overrides = make(map[string]float64, len(value.GetOverrides()))
			for subject, override := range value.GetOverrides() {
				item.Overrides[subject] = override
			}
		}
		out[name] = item
	}
	return out
}

// IntervalToPB 转换一个冻结区间。
func IntervalToPB(interval domain.ParamInterval) *factorpb.ParamIntervalInfo {
	return &factorpb.ParamIntervalInfo{MemberAlias: interval.MemberAlias, StartPeriod: interval.Start.Unix(), EndPeriod: interval.End.Unix(),
		Params: SnapshotToPB(interval.Snapshot), Source: interval.Source, SnapshotHash: interval.SnapshotHash}
}

// IntervalFromPB 还原一个冻结区间。
func IntervalFromPB(taskID string, pb *factorpb.ParamIntervalInfo) domain.ParamInterval {
	snapshot := SnapshotFromPB(pb.GetParams())
	return domain.ParamInterval{TaskID: taskID, MemberAlias: pb.GetMemberAlias(), Start: time.Unix(pb.GetStartPeriod(), 0).UTC(),
		End: time.Unix(pb.GetEndPeriod(), 0).UTC(), Snapshot: snapshot, SnapshotHash: snapshot.Hash(), Source: pb.GetSource()}
}

// FreezeToPB 转换一个成员一期的冻结结果。
func FreezeToPB(freeze domain.ParamFreeze) *factorpb.MemberParamFreeze {
	return &factorpb.MemberParamFreeze{MemberAlias: freeze.MemberAlias, Params: SnapshotToPB(freeze.Snapshot), Source: freeze.Source,
		SnapshotHash: freeze.SnapshotHash, RequestId: freeze.RequestID}
}

// FreezeFromPB 还原一个成员一期的冻结结果。
func FreezeFromPB(taskID string, period time.Time, pb *factorpb.MemberParamFreeze) domain.ParamFreeze {
	snapshot := SnapshotFromPB(pb.GetParams())
	return domain.ParamFreeze{TaskID: taskID, MemberAlias: pb.GetMemberAlias(), PeriodTime: period.UTC(), Snapshot: snapshot,
		SnapshotHash: snapshot.Hash(), Source: pb.GetSource(), RequestID: pb.GetRequestId()}
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

// CloneParams 复制成员的固定参数；没有固定参数时返回 nil。
func CloneParams(params map[string]float64) map[string]float64 {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]float64, len(params))
	for name, value := range params {
		out[name] = value
	}
	return out
}
