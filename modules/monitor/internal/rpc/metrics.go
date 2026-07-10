package rpc

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"gorm.io/gorm"
)

func (s *Service) ListMetricServices(ctx context.Context, req *monitorpb.ListMetricServicesReq) (*monitorpb.ListMetricServicesRsp, error) {
	if s.metricsQuery == nil {
		return &monitorpb.ListMetricServicesRsp{RetInfo: inner(errors.New("metrics catalog is unavailable"))}, nil
	}
	o, l := metricPage(req.GetPage())
	rows, total, err := s.metricsQuery.Catalog().ListServices(ctx, req.GetSpaceId(), o, l)
	if err != nil {
		return &monitorpb.ListMetricServicesRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*monitorpb.MetricServiceInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, &monitorpb.MetricServiceInfo{ServiceName: r.ServiceName, InstanceId: r.InstanceID, BootId: r.BootID, NodeId: r.NodeID, Version: r.Version, LastSeenAt: timeToString(r.LastSeenAt), Stale: r.IsStale})
	}
	return &monitorpb.ListMetricServicesRsp{RetInfo: success(), Services: out, PageResult: metricPageResult(o, l, total)}, nil
}
func (s *Service) ListMetricNames(ctx context.Context, req *monitorpb.ListMetricNamesReq) (*monitorpb.ListMetricNamesRsp, error) {
	if s.metricsQuery == nil {
		return &monitorpb.ListMetricNamesRsp{RetInfo: inner(errors.New("metrics catalog is unavailable"))}, nil
	}
	o, l := metricPage(req.GetPage())
	rows, total, err := s.metricsQuery.Catalog().ListNames(ctx, req.GetServiceName(), o, l)
	if err != nil {
		return &monitorpb.ListMetricNamesRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*monitorpb.MetricNameInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, &monitorpb.MetricNameInfo{ServiceName: r.ServiceName, MetricName: r.MetricName, MetricType: r.MetricType, SeriesCount: uint32(r.SeriesCount), LastSeenAt: timeToString(r.LastSeenAt)})
	}
	return &monitorpb.ListMetricNamesRsp{RetInfo: success(), Names: out, PageResult: metricPageResult(o, l, total)}, nil
}
func (s *Service) ListMetricSeries(ctx context.Context, req *monitorpb.ListMetricSeriesReq) (*monitorpb.ListMetricSeriesRsp, error) {
	if s.metricsQuery == nil {
		return &monitorpb.ListMetricSeriesRsp{RetInfo: inner(errors.New("metrics catalog is unavailable"))}, nil
	}
	o, l := metricPage(req.GetPage())
	rows, total, err := s.metricsQuery.Catalog().ListSeries(ctx, req.GetServiceName(), req.GetMetricName(), req.GetLabelsJson(), o, l)
	if err != nil {
		return &monitorpb.ListMetricSeriesRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*monitorpb.MetricSeriesInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, seriesToPB(r))
	}
	return &monitorpb.ListMetricSeriesRsp{RetInfo: success(), Series: out, PageResult: metricPageResult(o, l, total)}, nil
}
func (s *Service) GetMetricLatest(ctx context.Context, req *monitorpb.GetMetricLatestReq) (*monitorpb.GetMetricLatestRsp, error) {
	if s.metricsQuery == nil {
		return &monitorpb.GetMetricLatestRsp{RetInfo: inner(errors.New("metrics query is unavailable"))}, nil
	}
	if req.GetSeriesId() == "" {
		return &monitorpb.GetMetricLatestRsp{RetInfo: invalid(errors.New("series_id is required"))}, nil
	}
	row, err := s.metricsQuery.Latest(ctx, req.GetSeriesId())
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &monitorpb.GetMetricLatestRsp{RetInfo: notFound("metric series not found")}, nil
		}
		return &monitorpb.GetMetricLatestRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.GetMetricLatestRsp{RetInfo: success(), Latest: latestToPB(*row)}, nil
}
func (s *Service) QueryMetricHistory(ctx context.Context, req *monitorpb.QueryMetricHistoryReq) (*monitorpb.QueryMetricHistoryRsp, error) {
	if s.metricsQuery == nil {
		return &monitorpb.QueryMetricHistoryRsp{RetInfo: inner(errors.New("metrics query is unavailable"))}, nil
	}
	start, err := monmetrics.ParseTime(req.GetStartAt())
	if err != nil {
		return &monitorpb.QueryMetricHistoryRsp{RetInfo: invalid(err)}, nil
	}
	end, err := monmetrics.ParseTime(req.GetEndAt())
	if err != nil {
		return &monitorpb.QueryMetricHistoryRsp{RetInfo: invalid(err)}, nil
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 500
	}
	if limit > 500 {
		limit = 500
	}
	desc := req.GetOrder() == monitorpb.MetricHistoryOrder_METRIC_HISTORY_ORDER_DESC
	points, err := s.metricsQuery.History(ctx, req.GetSeriesId(), req.GetServiceName(), req.GetMetricName(), req.GetLabelsJson(), start, end, desc, limit)
	if err != nil {
		return &monitorpb.QueryMetricHistoryRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*monitorpb.MetricHistoryPoint, 0, len(points))
	for _, p := range points {
		out = append(out, &monitorpb.MetricHistoryPoint{SeriesId: p.SeriesID, Value: p.Value, ObservedAt: timeToString(p.ObservedAt), LabelsJson: p.LabelsJSON, MessageId: p.MessageID})
	}
	return &monitorpb.QueryMetricHistoryRsp{RetInfo: success(), Points: out}, nil
}

func (s *Service) ListMetricRules(ctx context.Context, req *monitorpb.ListMetricRulesReq) (*monitorpb.ListMetricRulesRsp, error) {
	if s.metricRules == nil {
		return &monitorpb.ListMetricRulesRsp{RetInfo: inner(errors.New("metric rules are unavailable"))}, nil
	}
	o, l := metricPage(req.GetPage())
	rules, total, err := s.metricRules.ListRules(ctx, req.GetSpaceId(), req.GetEnabledOnly(), o, l)
	if err != nil {
		return &monitorpb.ListMetricRulesRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.ListMetricRulesRsp{RetInfo: success(), Rules: rules, PageResult: metricPageResult(o, l, total)}, nil
}
func (s *Service) GetMetricRule(ctx context.Context, req *monitorpb.GetMetricRuleReq) (*monitorpb.GetMetricRuleRsp, error) {
	if s.metricRules == nil {
		return &monitorpb.GetMetricRuleRsp{RetInfo: inner(errors.New("metric rules are unavailable"))}, nil
	}
	rule, err := s.metricRules.GetRule(ctx, req.GetSpaceId(), req.GetRuleId())
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &monitorpb.GetMetricRuleRsp{RetInfo: notFound("metric rule not found")}, nil
		}
		return &monitorpb.GetMetricRuleRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.GetMetricRuleRsp{RetInfo: success(), Rule: rule}, nil
}
func (s *Service) CreateMetricRule(ctx context.Context, req *monitorpb.CreateMetricRuleReq) (*monitorpb.CreateMetricRuleRsp, error) {
	if s.metricRules == nil {
		return &monitorpb.CreateMetricRuleRsp{RetInfo: inner(errors.New("metric rules are unavailable"))}, nil
	}
	rule := req.GetRule()
	if rule == nil {
		return &monitorpb.CreateMetricRuleRsp{RetInfo: invalid(errors.New("rule is required"))}, nil
	}
	if rule.GetRuleId() == "" {
		rule.RuleId = newID("metric-rule")
	}
	if err := s.metricRules.CreateRule(ctx, rule, s.webhookEnabled); err != nil {
		return &monitorpb.CreateMetricRuleRsp{RetInfo: invalid(err)}, nil
	}
	return &monitorpb.CreateMetricRuleRsp{RetInfo: success(), Rule: rule}, nil
}
func (s *Service) UpdateMetricRule(ctx context.Context, req *monitorpb.UpdateMetricRuleReq) (*monitorpb.UpdateMetricRuleRsp, error) {
	if s.metricRules == nil {
		return &monitorpb.UpdateMetricRuleRsp{RetInfo: inner(errors.New("metric rules are unavailable"))}, nil
	}
	if err := s.metricRules.UpdateRule(ctx, req.GetRule(), s.webhookEnabled); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &monitorpb.UpdateMetricRuleRsp{RetInfo: notFound("metric rule not found")}, nil
		}
		return &monitorpb.UpdateMetricRuleRsp{RetInfo: invalid(err)}, nil
	}
	rule, err := s.metricRules.GetRule(ctx, req.GetRule().GetSpaceId(), req.GetRule().GetRuleId())
	if err != nil {
		return &monitorpb.UpdateMetricRuleRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.UpdateMetricRuleRsp{RetInfo: success(), Rule: rule}, nil
}
func (s *Service) DeleteMetricRule(ctx context.Context, req *monitorpb.DeleteMetricRuleReq) (*monitorpb.DeleteMetricRuleRsp, error) {
	if s.metricRules == nil {
		return &monitorpb.DeleteMetricRuleRsp{RetInfo: inner(errors.New("metric rules are unavailable"))}, nil
	}
	if err := s.metricRules.DeleteRule(ctx, req.GetSpaceId(), req.GetRuleId()); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &monitorpb.DeleteMetricRuleRsp{RetInfo: notFound("metric rule not found")}, nil
		}
		return &monitorpb.DeleteMetricRuleRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.DeleteMetricRuleRsp{RetInfo: success()}, nil
}
func (s *Service) PreviewMetricRule(ctx context.Context, req *monitorpb.PreviewMetricRuleReq) (*monitorpb.PreviewMetricRuleRsp, error) {
	if s.metricEvaluator == nil {
		return &monitorpb.PreviewMetricRuleRsp{RetInfo: inner(errors.New("metric evaluator is unavailable"))}, nil
	}
	eval, err := s.metricEvaluator.Evaluate(ctx, req.GetRule(), true)
	if err != nil {
		return &monitorpb.PreviewMetricRuleRsp{RetInfo: invalid(err)}, nil
	}
	return &monitorpb.PreviewMetricRuleRsp{RetInfo: success(), Evaluation: evaluationToPB(eval)}, nil
}
func (s *Service) ListMetricRuleEvaluations(ctx context.Context, req *monitorpb.ListMetricRuleEvaluationsReq) (*monitorpb.ListMetricRuleEvaluationsRsp, error) {
	if s.metricRules == nil {
		return &monitorpb.ListMetricRuleEvaluationsRsp{RetInfo: inner(errors.New("metric rules are unavailable"))}, nil
	}
	o, l := metricPage(req.GetPage())
	rows, total, err := s.metricRules.ListEvaluations(ctx, req.GetSpaceId(), req.GetRuleId(), o, l)
	if err != nil {
		return &monitorpb.ListMetricRuleEvaluationsRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*monitorpb.MetricRuleEvaluation, 0, len(rows))
	for _, row := range rows {
		out = append(out, evaluationRowToPB(row))
	}
	return &monitorpb.ListMetricRuleEvaluationsRsp{RetInfo: success(), Evaluations: out, PageResult: metricPageResult(o, l, total)}, nil
}
func (s *Service) GetMetricRuleState(ctx context.Context, req *monitorpb.GetMetricRuleStateReq) (*monitorpb.GetMetricRuleStateRsp, error) {
	if s.metricRules == nil {
		return &monitorpb.GetMetricRuleStateRsp{RetInfo: inner(errors.New("metric rules are unavailable"))}, nil
	}
	row, err := s.metricRules.GetState(ctx, req.GetSpaceId(), req.GetRuleId())
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &monitorpb.GetMetricRuleStateRsp{RetInfo: notFound("metric rule state not found")}, nil
		}
		return &monitorpb.GetMetricRuleStateRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.GetMetricRuleStateRsp{RetInfo: success(), State: stateToPB(*row)}, nil
}

func (s *Service) webhookEnabled(ctx context.Context, spaceID, id string) (bool, error) {
	var found domain.WebhookChannel
	err := s.db.WithContext(ctx).Where("c_space_id = ? AND c_webhook_id = ? AND c_is_deleted = 0", spaceID, id).First(&found).Error
	if err != nil {
		return false, err
	}
	return found.Enabled, nil
}
func metricPage(page *commonpb.Page) (int, int) {
	if page == nil {
		return 0, 50
	}
	n := int(page.GetPage())
	if n < 1 {
		n = 1
	}
	size := int(page.GetSize())
	if size <= 0 {
		size = 50
	}
	if size > 500 {
		size = 500
	}
	return (n - 1) * size, size
}
func metricPageResult(offset, limit int, total int64) *commonpb.PageResult {
	page := offset/limit + 1
	return &commonpb.PageResult{Page: uint32(page), Size: uint32(limit), Total: uint32(total), HasMore: int64(offset+limit) < total}
}
func seriesToPB(r monmetrics.MetricSeries) *monitorpb.MetricSeriesInfo {
	return &monitorpb.MetricSeriesInfo{SeriesId: r.SeriesID, ServiceName: r.ServiceName, InstanceId: r.InstanceID, MetricName: r.MetricName, MetricType: r.MetricType, LabelsJson: r.LabelsJSON, LastSeenAt: timeToString(r.LastSeenAt), Stale: r.IsStale}
}
func latestToPB(r monmetrics.MetricLatest) *monitorpb.MetricLatestPoint {
	return &monitorpb.MetricLatestPoint{SeriesId: r.SeriesID, ServiceName: r.ServiceName, InstanceId: r.InstanceID, MetricName: r.MetricName, MetricType: r.MetricType, LabelsJson: r.LabelsJSON, Value: r.Value, ObservedAt: timeToString(r.ObservedAt), MessageId: r.MessageID, Stale: r.IntervalSeconds > 0 && time.Since(r.ObservedAt) > time.Duration(2*r.IntervalSeconds)*time.Second}
}
func evaluationToPB(e *monmetrics.RuleEvaluation) *monitorpb.MetricRuleEvaluation {
	if e == nil {
		return nil
	}
	out := &monitorpb.MetricRuleEvaluation{EvaluationId: e.EvaluationID, SpaceId: e.SpaceID, RuleId: e.RuleID, EvaluatedAt: timeToString(e.EvaluatedAt), Result: e.Result, ResultJson: e.ResultJSON, Status: alertStatusToPB(e.Status)}
	for _, c := range e.Conditions {
		out.Conditions = append(out.Conditions, &monitorpb.MetricConditionEvaluation{ConditionId: c.ConditionID, SelectedSeriesCount: uint32(c.SelectedSeriesCount), Value: c.Value, Threshold: c.Threshold, HasData: c.HasData, Result: c.Result, NoDataReason: c.NoDataReason})
	}
	return out
}
func evaluationRowToPB(r monmetrics.MetricRuleEvaluationRow) *monitorpb.MetricRuleEvaluation {
	out := &monitorpb.MetricRuleEvaluation{SpaceId: r.SpaceID, RuleId: r.RuleID, EvaluatedAt: timeToString(r.EvaluatedAt), ResultJson: r.ResultJSON, Status: alertStatusToPB(r.Status)}
	return out
}
func stateToPB(r monmetrics.MetricRuleStateRow) *monitorpb.MetricRuleState {
	return &monitorpb.MetricRuleState{SpaceId: r.SpaceID, RuleId: r.RuleID, Status: alertStatusToPB(r.Status), TriggerCount: uint32(r.TriggerCount), RecoveryCount: uint32(r.RecoveryCount), OwnerInstanceId: r.OwnerInstanceID, LastEvaluatedAt: timePtrToString(r.LastEvaluatedAt), LastTriggeredAt: timePtrToString(r.LastTriggeredAt), LastRecoveredAt: timePtrToString(r.LastRecoveredAt)}
}
