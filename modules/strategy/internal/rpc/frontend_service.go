package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"trpc.group/trpc-go/trpc-go"
)

func pageFromProto(page *strategypb.PageReq) store.Page {
	if page == nil {
		return store.Page{Number: 1, Size: 20}
	}
	number := int(page.GetPage())
	size := int(page.GetPageSize())
	if number < 1 {
		number = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 200 {
		size = 200
	}
	return store.Page{Number: number, Size: size}
}

func (s *Service) ListRunningStrategies(ctx context.Context, req *strategypb.ListRunningStrategiesReq) (*strategypb.ListRunningStrategiesRsp, error) {
	if s == nil || s.Repo == nil {
		return &strategypb.ListRunningStrategiesRsp{RetInfo: invalid(errors.New("strategy repository is unavailable"))}, nil
	}
	var filter store.RunningFilter
	if req != nil {
		filter = store.RunningFilter{SpaceID: req.GetSpaceId(), Status: req.GetStatus(), Mode: req.GetMode(), StrategyID: req.GetStrategyId()}
	}
	if scopedSpace := requestSpaceID(ctx); scopedSpace != "" {
		filter.SpaceID = scopedSpace
	}
	items, total, err := s.Repo.ListRunningStrategies(ctx, filter, pageFromProto(req.GetPage()))
	if err != nil {
		return &strategypb.ListRunningStrategiesRsp{RetInfo: invalid(err)}, nil
	}
	page := pageFromProto(req.GetPage())
	out := make([]*strategypb.RunningStrategySummary, 0, len(items))
	for _, item := range items {
		out = append(out, summaryProto(item))
	}
	return &strategypb.ListRunningStrategiesRsp{RetInfo: success(), Items: out, Total: total, Page: int32(page.Number), PageSize: int32(page.Size)}, nil
}

func (s *Service) GetStrategyOverview(ctx context.Context, req *strategypb.GetStrategyOverviewReq) (*strategypb.GetStrategyOverviewRsp, error) {
	if req == nil || req.GetBindingId() == "" {
		return &strategypb.GetStrategyOverviewRsp{RetInfo: invalid(errors.New("binding_id is required"))}, nil
	}
	binding, err := s.Repo.GetBinding(ctx, req.GetBindingId())
	if err != nil {
		return &strategypb.GetStrategyOverviewRsp{RetInfo: invalid(err)}, nil
	}
	if err := ensureBindingScope(ctx, binding); err != nil {
		return &strategypb.GetStrategyOverviewRsp{RetInfo: invalid(err)}, nil
	}
	d, err := s.Repo.GetDefinition(ctx, binding.StrategyID, binding.StrategyVersion)
	if err != nil {
		return &strategypb.GetStrategyOverviewRsp{RetInfo: invalid(err)}, nil
	}
	state, err := s.Repo.GetState(ctx, binding.BindingID)
	if err != nil {
		return &strategypb.GetStrategyOverviewRsp{RetInfo: invalid(err)}, nil
	}
	health, err := s.Repo.GetHealth(ctx, binding.BindingID)
	if err != nil {
		return &strategypb.GetStrategyOverviewRsp{RetInfo: invalid(err)}, nil
	}
	summary := domain.RunningStrategySummary{StrategyID: d.StrategyID, Version: d.Version, BindingID: binding.BindingID, SpaceID: binding.SpaceID, ViewID: binding.ViewID, Freq: binding.Freq, Mode: health.Mode, Status: binding.Status, SourceHash: d.SourceHash, LastRunID: state.LastRunID, Health: health}
	return &strategypb.GetStrategyOverviewRsp{RetInfo: success(), Summary: summaryProto(summary), Binding: bindingProto(binding), Definition: definitionProto(d), State: stateProto(state), Health: healthProto(health)}, nil
}

func (s *Service) ListStrategyRuns(ctx context.Context, req *strategypb.ListStrategyRunsReq) (*strategypb.ListStrategyRunsRsp, error) {
	if req == nil || req.GetBindingId() == "" {
		return &strategypb.ListStrategyRunsRsp{RetInfo: invalid(errors.New("binding_id is required"))}, nil
	}
	from, to, err := parseStrictTimeRange(req.GetRange())
	if err != nil {
		return &strategypb.ListStrategyRunsRsp{RetInfo: invalid(err)}, nil
	}
	if s == nil || s.Repo == nil {
		return &strategypb.ListStrategyRunsRsp{RetInfo: invalid(errors.New("strategy repository is unavailable"))}, nil
	}
	if binding, err := s.Repo.GetBinding(ctx, req.GetBindingId()); err != nil {
		return &strategypb.ListStrategyRunsRsp{RetInfo: invalid(err)}, nil
	} else if err := ensureBindingScope(ctx, binding); err != nil {
		return &strategypb.ListStrategyRunsRsp{RetInfo: invalid(err)}, nil
	}
	filter := store.RunFilter{BindingID: req.GetBindingId(), From: from, To: to}
	runs, total, err := s.Repo.ListRuns(ctx, filter, pageFromProto(req.GetPage()))
	if err != nil {
		return &strategypb.ListStrategyRunsRsp{RetInfo: invalid(err)}, nil
	}
	page := pageFromProto(req.GetPage())
	items := make([]*strategypb.StrategyRun, 0, len(runs))
	for _, run := range runs {
		items = append(items, runProto(run))
	}
	return &strategypb.ListStrategyRunsRsp{RetInfo: success(), Items: items, Total: total, Page: int32(page.Number), PageSize: int32(page.Size)}, nil
}

func (s *Service) GetStrategyRun(ctx context.Context, req *strategypb.GetStrategyRunReq) (*strategypb.GetStrategyRunRsp, error) {
	if req == nil || req.GetRunId() == "" {
		return &strategypb.GetStrategyRunRsp{RetInfo: invalid(errors.New("run_id is required"))}, nil
	}
	run, err := s.Repo.GetRun(ctx, req.GetRunId())
	if err != nil {
		return &strategypb.GetStrategyRunRsp{RetInfo: invalid(err)}, nil
	}
	if binding, err := s.Repo.GetBinding(ctx, run.BindingID); err != nil {
		return &strategypb.GetStrategyRunRsp{RetInfo: invalid(err)}, nil
	} else if err := ensureBindingScope(ctx, binding); err != nil {
		return &strategypb.GetStrategyRunRsp{RetInfo: invalid(err)}, nil
	}
	var metrics domain.RunMetrics
	metricsJSON := "{}"
	if metrics, err = s.Repo.GetRunMetrics(ctx, run.RunID); err == nil {
		b, _ := json.Marshal(metrics)
		metricsJSON = string(b)
	}
	return &strategypb.GetStrategyRunRsp{RetInfo: success(), Run: runProto(run), MetricsJson: metricsJSON}, nil
}

func (s *Service) ListStrategyTargets(ctx context.Context, req *strategypb.ListStrategyTargetsReq) (*strategypb.ListStrategyTargetsRsp, error) {
	if req == nil || req.GetRunId() == "" {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(errors.New("run_id is required"))}, nil
	}
	targetRun, err := s.Repo.GetRun(ctx, req.GetRunId())
	if err != nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(err)}, nil
	}
	if binding, err := s.Repo.GetBinding(ctx, targetRun.BindingID); err != nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(err)}, nil
	} else if err := ensureBindingScope(ctx, binding); err != nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(err)}, nil
	}
	targets, total, err := s.Repo.ListTargets(ctx, req.GetRunId(), pageFromProto(req.GetPage()))
	if err != nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(err)}, nil
	}
	items := make([]*strategypb.TargetWeight, 0, len(targets))
	for _, target := range targets {
		items = append(items, &strategypb.TargetWeight{InstrumentId: target.InstrumentID, TargetWeight: target.TargetWeight, PortfolioTarget: target.PortfolioTarget, ActualPosition: target.ActualPosition, Deviation: target.Deviation, SourceTime: target.SourceTime, DataRevision: target.DataRevision})
	}
	page := pageFromProto(req.GetPage())
	return &strategypb.ListStrategyTargetsRsp{RetInfo: success(), Targets: items, Total: total, Page: int32(page.Number), PageSize: int32(page.Size)}, nil
}

func (s *Service) GetStrategyStateSummary(ctx context.Context, req *strategypb.GetStrategyStateSummaryReq) (*strategypb.GetStrategyStateSummaryRsp, error) {
	if req == nil || req.GetBindingId() == "" {
		return &strategypb.GetStrategyStateSummaryRsp{RetInfo: invalid(errors.New("binding_id is required"))}, nil
	}
	if binding, err := s.Repo.GetBinding(ctx, req.GetBindingId()); err != nil {
		return &strategypb.GetStrategyStateSummaryRsp{RetInfo: invalid(err)}, nil
	} else if err := ensureBindingScope(ctx, binding); err != nil {
		return &strategypb.GetStrategyStateSummaryRsp{RetInfo: invalid(err)}, nil
	}
	state, err := s.Repo.GetState(ctx, req.GetBindingId())
	if err != nil {
		return &strategypb.GetStrategyStateSummaryRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.GetStrategyStateSummaryRsp{RetInfo: success(), State: stateProto(state), SizeBytes: int64(len(state.StateJSON))}, nil
}

func (s *Service) GetStrategyHealth(ctx context.Context, req *strategypb.GetStrategyHealthReq) (*strategypb.GetStrategyHealthRsp, error) {
	if req == nil || req.GetBindingId() == "" {
		return &strategypb.GetStrategyHealthRsp{RetInfo: invalid(errors.New("binding_id is required"))}, nil
	}
	if binding, err := s.Repo.GetBinding(ctx, req.GetBindingId()); err != nil {
		return &strategypb.GetStrategyHealthRsp{RetInfo: invalid(err)}, nil
	} else if err := ensureBindingScope(ctx, binding); err != nil {
		return &strategypb.GetStrategyHealthRsp{RetInfo: invalid(err)}, nil
	}
	health, err := s.Repo.GetHealth(ctx, req.GetBindingId())
	if err != nil {
		return &strategypb.GetStrategyHealthRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.GetStrategyHealthRsp{RetInfo: success(), Health: healthProto(health)}, nil
}

func (s *Service) GetStrategyPerformance(ctx context.Context, req *strategypb.GetStrategyPerformanceReq) (*strategypb.GetStrategyPerformanceRsp, error) {
	if req == nil || req.GetBindingId() == "" {
		return &strategypb.GetStrategyPerformanceRsp{RetInfo: invalid(errors.New("binding_id is required"))}, nil
	}
	if !domain.ValidPerformanceSource(req.GetPerformanceSource()) {
		return &strategypb.GetStrategyPerformanceRsp{RetInfo: invalid(errors.New("performance_source must be one of backtest, observe, paper, or live"))}, nil
	}
	if interval := req.GetInterval(); interval != "" && interval != "auto" && interval != "daily" {
		return &strategypb.GetStrategyPerformanceRsp{RetInfo: invalid(errors.New("interval must be auto or daily"))}, nil
	}
	from, to, err := parseStrictTimeRange(req.GetRange())
	if err != nil {
		return &strategypb.GetStrategyPerformanceRsp{RetInfo: invalid(err)}, nil
	}
	if s == nil || s.Repo == nil {
		return &strategypb.GetStrategyPerformanceRsp{RetInfo: invalid(errors.New("strategy repository is unavailable"))}, nil
	}
	if binding, err := s.Repo.GetBinding(ctx, req.GetBindingId()); err != nil {
		return &strategypb.GetStrategyPerformanceRsp{RetInfo: invalid(err)}, nil
	} else if err := ensureBindingScope(ctx, binding); err != nil {
		return &strategypb.GetStrategyPerformanceRsp{RetInfo: invalid(err)}, nil
	}
	filter := store.PerformanceFilter{BindingID: req.GetBindingId(), Source: req.GetPerformanceSource(), From: from, To: to}
	out := make([]*strategypb.PerformancePoint, 0)
	if req.GetInterval() == "daily" {
		daily, dailyErr := s.Repo.ListPerformanceDaily(ctx, filter)
		if dailyErr != nil {
			return &strategypb.GetStrategyPerformanceRsp{RetInfo: invalid(dailyErr)}, nil
		}
		for _, row := range daily {
			out = append(out, &strategypb.PerformancePoint{PointTime: row.TradeDate, Nav: row.EndNAV, CumulativeReturn: row.Return, Drawdown: row.MaxDrawdown, Turnover: row.Turnover, Fees: row.Fees, DataRevision: row.DataRevision})
		}
	} else {
		points, err := s.Repo.ListPerformancePoints(ctx, filter)
		if err != nil {
			return &strategypb.GetStrategyPerformanceRsp{RetInfo: invalid(err)}, nil
		}
		for _, point := range points {
			out = append(out, &strategypb.PerformancePoint{PointTime: point.PointTime.Format(time.RFC3339Nano), Nav: point.NAV, CumulativeReturn: point.CumulativeReturn, Drawdown: point.Drawdown, GrossExposure: point.GrossExposure, NetExposure: point.NetExposure, Turnover: point.Turnover, Fees: point.Fees, DataRevision: point.DataRevision})
		}
	}
	if len(out) > 1000 {
		stride := (len(out) + 999) / 1000
		sampled := make([]*strategypb.PerformancePoint, 0, 1001)
		for i := 0; i < len(out); i += stride {
			sampled = append(sampled, out[i])
		}
		if sampled[len(sampled)-1] != out[len(out)-1] {
			sampled = append(sampled, out[len(out)-1])
		}
		out = sampled
	}
	status := "ok"
	if len(out) == 0 {
		status = "insufficient_data"
	}
	summary := &strategypb.PerformanceSummary{Status: status}
	if len(out) > 0 {
		last := out[len(out)-1]
		summary.Nav = last.Nav
		summary.ReturnValue = last.CumulativeReturn
		summary.MaxDrawdown = last.Drawdown
		summary.Turnover = last.Turnover
		summary.Fees = last.Fees
		summary.AsOf = last.PointTime
	}
	return &strategypb.GetStrategyPerformanceRsp{RetInfo: success(), PerformanceSource: filter.Source, Summary: summary, Points: out, AsOf: summary.AsOf}, nil
}

func (s *Service) PauseBinding(ctx context.Context, req *strategypb.BindingOperationReq) (*strategypb.BindingOperationRsp, error) {
	return s.changeBindingStatus(ctx, req, "disabled", "pause")
}

func (s *Service) ResumeBinding(ctx context.Context, req *strategypb.BindingOperationReq) (*strategypb.BindingOperationRsp, error) {
	return s.changeBindingStatus(ctx, req, "enabled", "resume")
}

func (s *Service) SetExecutionMode(ctx context.Context, req *strategypb.SetExecutionModeReq) (*strategypb.BindingOperationRsp, error) {
	if req == nil || req.GetBindingId() == "" || req.GetMode() == "" || req.GetOperationId() == "" {
		return &strategypb.BindingOperationRsp{RetInfo: invalid(errors.New("binding_id, mode and operation_id are required"))}, nil
	}
	if req.GetMode() != "observe" && req.GetMode() != "paper" && req.GetMode() != "live" {
		return &strategypb.BindingOperationRsp{RetInfo: invalid(errors.New("unsupported execution mode"))}, nil
	}
	if req.GetMode() == "live" && (s == nil || !s.LiveExecutionEnabled) {
		return &strategypb.BindingOperationRsp{RetInfo: invalid(errors.New("live execution is disabled by server capability"))}, nil
	}
	quoteAsset := strings.TrimSpace(req.GetQuoteAsset())
	if quoteAsset == "" {
		quoteAsset = "USDT"
	}
	if req.GetMode() == "paper" || req.GetMode() == "live" {
		capital, ok := new(big.Rat).SetString(strings.TrimSpace(req.GetCapitalAmount()))
		if strings.TrimSpace(req.GetChannelId()) == "" || !ok || capital.Sign() <= 0 {
			return &strategypb.BindingOperationRsp{RetInfo: invalid(errors.New("paper/live mode requires channel_id and positive capital_amount"))}, nil
		}
	}
	if !operatorAllowed(ctx) {
		return &strategypb.BindingOperationRsp{RetInfo: invalid(errors.New("strategy operation requires operator permission"))}, nil
	}
	if audit, ok := s.findAudit(ctx, req.GetOperationId()); ok {
		return &strategypb.BindingOperationRsp{RetInfo: success(), OperationId: audit.OperationID, Status: audit.NewValue}, nil
	}
	binding, err := s.Repo.GetBinding(ctx, req.GetBindingId())
	if err != nil {
		return &strategypb.BindingOperationRsp{RetInfo: invalid(err)}, nil
	}
	err = s.Repo.SetExecutionMode(ctx, binding, req.GetMode(), req.GetChannelId(), req.GetCapitalAmount(), quoteAsset, domain.OperationAudit{OperationID: req.GetOperationId(), Operator: "admin", Action: "set_mode", BindingID: binding.BindingID, Reason: req.GetReason(), RequestID: req.GetOperationId()})
	if err != nil {
		return &strategypb.BindingOperationRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.BindingOperationRsp{RetInfo: success(), OperationId: req.GetOperationId(), Status: req.GetMode()}, nil
}

func (s *Service) changeBindingStatus(ctx context.Context, req *strategypb.BindingOperationReq, status, action string) (*strategypb.BindingOperationRsp, error) {
	if req == nil || req.GetBindingId() == "" || req.GetOperationId() == "" || req.GetReason() == "" {
		return &strategypb.BindingOperationRsp{RetInfo: invalid(errors.New("binding_id, reason and operation_id are required"))}, nil
	}
	if !operatorAllowed(ctx) {
		return &strategypb.BindingOperationRsp{RetInfo: invalid(errors.New("strategy operation requires operator permission"))}, nil
	}
	if audit, ok := s.findAudit(ctx, req.GetOperationId()); ok {
		return &strategypb.BindingOperationRsp{RetInfo: success(), OperationId: audit.OperationID, Status: audit.NewValue}, nil
	}
	binding, err := s.Repo.GetBinding(ctx, req.GetBindingId())
	if err != nil {
		return &strategypb.BindingOperationRsp{RetInfo: invalid(err)}, nil
	}
	err = s.Repo.SetBindingStatus(ctx, binding, status, domain.OperationAudit{OperationID: req.GetOperationId(), Operator: "admin", Action: action, BindingID: binding.BindingID, Reason: req.GetReason(), RequestID: req.GetOperationId()})
	if err != nil {
		return &strategypb.BindingOperationRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.BindingOperationRsp{RetInfo: success(), OperationId: req.GetOperationId(), Status: status}, nil
}

func (s *Service) findAudit(ctx context.Context, operationID string) (domain.OperationAudit, bool) {
	var audit domain.OperationAudit
	if operationID == "" || s == nil || s.Repo == nil {
		return audit, false
	}
	audit, err := s.Repo.FindAudit(ctx, operationID)
	if err != nil {
		return domain.OperationAudit{}, false
	}
	return audit, true
}

func operatorAllowed(ctx context.Context) bool {
	role := string(trpc.GetMetaData(ctx, "X-User-Role"))
	if role == "" {
		return requestSpaceID(ctx) == "" // internal calls and tests have no gateway metadata.
	}
	value, err := strconv.Atoi(role)
	return err == nil && value >= 2
}

func requestSpaceID(ctx context.Context) string {
	for _, key := range []string{"space_id", "X-Space-Id", "x-space-id"} {
		if value := string(trpc.GetMetaData(ctx, key)); value != "" {
			return value
		}
	}
	return ""
}

func ensureBindingScope(ctx context.Context, binding domain.Binding) error {
	if scopedSpace := requestSpaceID(ctx); scopedSpace != "" && scopedSpace != binding.SpaceID {
		return fmt.Errorf("binding %q is outside the current space", binding.BindingID)
	}
	return nil
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

func parseStrictTimeRange(value *strategypb.TimeRange) (time.Time, time.Time, error) {
	if value == nil {
		return time.Time{}, time.Time{}, nil
	}
	from, err := parseTime(value.GetFrom())
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("range.from must be RFC3339Nano: %w", err)
	}
	to, err := parseTime(value.GetTo())
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("range.to must be RFC3339Nano: %w", err)
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return time.Time{}, time.Time{}, errors.New("range.from must be before range.to")
	}
	return from, to, nil
}

func summaryProto(v domain.RunningStrategySummary) *strategypb.RunningStrategySummary {
	return &strategypb.RunningStrategySummary{StrategyId: v.StrategyID, Version: v.Version, BindingId: v.BindingID, SpaceId: v.SpaceID, ViewId: v.ViewID, Freq: v.Freq, Mode: v.Mode, Status: v.Status, SourceHash: v.SourceHash, LastRunId: v.LastRunID, LastDataRevision: v.LastDataRevision, Health: healthProto(v.Health)}
}

func healthProto(v domain.BindingHealth) *strategypb.StrategyHealth {
	return &strategypb.StrategyHealth{Status: v.Status, Mode: v.Mode, LastRunId: v.LastRunID, LastSuccessAt: formatTime(v.LastSuccessAt), LastErrorType: v.LastErrorType, LastErrorMessage: v.LastErrorMessage, LastDataRevision: v.LastDataRevision, DataCutoff: formatTime(v.DataCutoff), WorkerStatus: v.WorkerStatus, OutboxLagSeconds: v.OutboxLagSeconds, ObservedAt: formatTime(v.ObservedAt)}
}

func bindingProto(v domain.Binding) *strategypb.StrategyBinding {
	return &strategypb.StrategyBinding{BindingId: v.BindingID, StrategyId: v.StrategyID, StrategyVersion: v.StrategyVersion, SpaceId: v.SpaceID, ViewId: v.ViewID, Freq: v.Freq, ParamsJson: v.ParamsJSON, GroupId: v.GroupID, CapitalWeight: v.CapitalWeight, Status: v.Status}
}

func definitionProto(v domain.StrategyDefinition) *strategypb.StrategyDef {
	return &strategypb.StrategyDef{StrategyId: v.StrategyID, Version: v.Version, ApiVersion: v.API, ManifestYaml: v.ManifestYAML, SourceHash: v.SourceHash, StateSchemaVersion: int32(v.StateSchemaVersion), Status: v.Status}
}

func stateProto(v domain.State) *strategypb.StrategyState {
	return &strategypb.StrategyState{BindingId: v.BindingID, Revision: v.Revision, StateJson: v.StateJSON, LastRunId: v.LastRunID}
}

func runProto(v domain.StrategyRun) *strategypb.StrategyRun {
	return &strategypb.StrategyRun{RunId: v.RunID, BindingId: v.BindingID, TriggerBarTime: v.TriggerBarTime, DataRevision: v.DataRevision, Action: v.Action, Status: v.Status, OutputJson: v.OutputJSON}
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
