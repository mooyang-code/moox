package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"gorm.io/gorm"
)

type SeriesValues struct {
	SeriesID string
	Values   []TimedValue
}
type TimedValue struct {
	At    time.Time
	Value float64
}

// ReduceTimeSeries reduces one ordered series. RATE and INCREASE treat a
// decrease as a counter reset and therefore add the post-reset value.
func ReduceTimeSeries(reducer monitorpb.TimeReducer, values []TimedValue) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	sorted := append([]TimedValue(nil), values...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	switch reducer {
	case monitorpb.TimeReducer_TIME_REDUCER_CURRENT:
		return sorted[len(sorted)-1].Value, true
	case monitorpb.TimeReducer_TIME_REDUCER_AVG:
		var sum float64
		for _, v := range sorted {
			sum += v.Value
		}
		return sum / float64(len(sorted)), true
	case monitorpb.TimeReducer_TIME_REDUCER_MIN:
		v := sorted[0].Value
		for _, x := range sorted[1:] {
			if x.Value < v {
				v = x.Value
			}
		}
		return v, true
	case monitorpb.TimeReducer_TIME_REDUCER_MAX:
		v := sorted[0].Value
		for _, x := range sorted[1:] {
			if x.Value > v {
				v = x.Value
			}
		}
		return v, true
	case monitorpb.TimeReducer_TIME_REDUCER_SUM:
		var sum float64
		for _, v := range sorted {
			sum += v.Value
		}
		return sum, true
	case monitorpb.TimeReducer_TIME_REDUCER_INCREASE, monitorpb.TimeReducer_TIME_REDUCER_RATE:
		if len(sorted) < 2 {
			return 0, false
		}
		var increase float64
		for i := 1; i < len(sorted); i++ {
			delta := sorted[i].Value - sorted[i-1].Value
			if delta < 0 {
				delta = sorted[i].Value
			}
			increase += delta
		}
		if reducer == monitorpb.TimeReducer_TIME_REDUCER_INCREASE {
			return increase, true
		}
		seconds := sorted[len(sorted)-1].At.Sub(sorted[0].At).Seconds()
		if seconds <= 0 {
			return 0, false
		}
		return increase / seconds, true
	default:
		return 0, false
	}
}

func ReduceSeries(reducer monitorpb.SeriesReducer, values []float64) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	sorted := append([]float64(nil), values...)
	switch reducer {
	case monitorpb.SeriesReducer_SERIES_REDUCER_AVG:
		var s float64
		for _, v := range sorted {
			s += v
		}
		return s / float64(len(sorted)), true
	case monitorpb.SeriesReducer_SERIES_REDUCER_MIN:
		v := sorted[0]
		for _, x := range sorted[1:] {
			if x < v {
				v = x
			}
		}
		return v, true
	case monitorpb.SeriesReducer_SERIES_REDUCER_MAX:
		v := sorted[0]
		for _, x := range sorted[1:] {
			if x > v {
				v = x
			}
		}
		return v, true
	case monitorpb.SeriesReducer_SERIES_REDUCER_SUM:
		var s float64
		for _, v := range sorted {
			s += v
		}
		return s, true
	default:
		return 0, false
	}
}
func Compare(op monitorpb.CompareOperator, value, threshold float64) bool {
	switch op {
	case monitorpb.CompareOperator_COMPARE_OPERATOR_GT:
		return value > threshold
	case monitorpb.CompareOperator_COMPARE_OPERATOR_GTE:
		return value >= threshold
	case monitorpb.CompareOperator_COMPARE_OPERATOR_LT:
		return value < threshold
	case monitorpb.CompareOperator_COMPARE_OPERATOR_LTE:
		return value <= threshold
	case monitorpb.CompareOperator_COMPARE_OPERATOR_EQ:
		return value == threshold
	case monitorpb.CompareOperator_COMPARE_OPERATOR_NEQ:
		return value != threshold
	default:
		return false
	}
}

type ConditionResult struct {
	ConditionID         string
	SelectedSeriesCount int
	Value               float64
	Threshold           float64
	HasData             bool
	Result              bool
	NoDataReason        string
}
type RuleEvaluation struct {
	EvaluationID, SpaceID, RuleID string
	EvaluatedAt                   time.Time
	Status                        string
	Result                        bool
	Conditions                    []ConditionResult
	ResultJSON                    string
	KeepState                     bool
}

type MetricEvaluator struct {
	repo     *RuleRepository
	catalog  *MetricCatalog
	storage  *StorageAdapter
	notifier MetricNotifier
	webhooks func(context.Context, string, string) (*domain.WebhookChannel, error)
	instance string
	now      func() time.Time
}
type EvaluatorOptions struct {
	Repository *RuleRepository
	Catalog    *MetricCatalog
	Storage    *StorageAdapter
	Notifier   MetricNotifier
	Webhook    func(context.Context, string, string) (*domain.WebhookChannel, error)
	InstanceID string
	Now        func() time.Time
}

func NewMetricEvaluator(opts EvaluatorOptions) *MetricEvaluator {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MetricEvaluator{repo: opts.Repository, catalog: opts.Catalog, storage: opts.Storage, notifier: opts.Notifier, webhooks: opts.Webhook, instance: opts.InstanceID, now: now}
}

func (e *MetricEvaluator) Evaluate(ctx context.Context, rule *monitorpb.MetricRule, preview bool) (*RuleEvaluation, error) {
	if err := ValidateMetricRule(rule); err != nil {
		return nil, err
	}
	if e == nil || e.catalog == nil || e.storage == nil {
		return nil, ErrMetricsRepositoryUnavailable
	}
	now := e.now()
	result := &RuleEvaluation{EvaluationID: fmt.Sprintf("%s-%d", rule.GetRuleId(), now.UnixNano()), SpaceID: rule.GetSpaceId(), RuleID: rule.GetRuleId(), EvaluatedAt: now, Status: domain.AlertStatusOK}
	for _, condition := range rule.GetConditions() {
		cr, err := e.evaluateCondition(ctx, condition, now)
		if err != nil {
			return nil, err
		}
		result.Conditions = append(result.Conditions, cr)
	}
	for _, c := range result.Conditions {
		if !c.HasData && c.NoDataReason == "keep_state" {
			result.KeepState = true
		}
	}
	if rule.GetConnector() == monitorpb.LogicalOperator_LOGICAL_OPERATOR_AND {
		result.Result = true
		for _, c := range result.Conditions {
			if !c.HasData {
				result.Result = noDataResult(c.NoDataReason, rule.GetConditions(), c.ConditionID)
				break
			}
			if !c.Result {
				result.Result = false
			}
		}
	} else {
		for _, c := range result.Conditions {
			if c.HasData && c.Result {
				result.Result = true
				break
			}
			if !c.HasData && c.NoDataReason == "firing" {
				result.Result = true
			}
		}
	}
	result.Status = domain.AlertStatusOK
	if result.Result {
		result.Status = domain.AlertStatusFiring
	}
	raw, _ := json.Marshal(result)
	result.ResultJSON = string(raw)
	if !preview {
		if err := e.applyState(ctx, rule, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}
func noDataResult(reason string, conditions []*monitorpb.MetricCondition, id string) bool {
	for _, c := range conditions {
		if c.GetConditionId() == id {
			switch c.GetNoDataPolicy() {
			case monitorpb.NoDataPolicy_NO_DATA_POLICY_FIRING:
				return true
			case monitorpb.NoDataPolicy_NO_DATA_POLICY_OK:
				return true
			default:
				return false
			}
		}
	}
	return reason == "firing"
}
func (e *MetricEvaluator) evaluateCondition(ctx context.Context, condition *monitorpb.MetricCondition, now time.Time) (ConditionResult, error) {
	out := ConditionResult{ConditionID: condition.GetConditionId(), Threshold: condition.GetThreshold()}
	selector := condition.GetQuery().GetSelector()
	series, err := e.catalog.FindSeries(ctx, "", selector.GetMetricName(), "", "", 500)
	if err != nil {
		return out, err
	}
	filtered := series[:0]
	for _, s := range series {
		if s.ServiceName != selector.GetServiceName() {
			continue
		}
		if labelsMatch(s.LabelsJSON, selector.GetMatchers()) {
			filtered = append(filtered, s)
		}
	}
	out.SelectedSeriesCount = len(filtered)
	if len(filtered) == 0 {
		out.NoDataReason = noDataReason(condition.GetNoDataPolicy())
		return out, nil
	}
	values := make([]float64, 0, len(filtered))
	for _, s := range filtered {
		start := time.Time{}
		if condition.GetQuery().GetTimeReducer() != monitorpb.TimeReducer_TIME_REDUCER_CURRENT {
			start = now.Add(-time.Duration(condition.GetQuery().GetWindowSeconds()) * time.Second)
		}
		points, err := e.storage.QueryHistorySelectors(ctx, []HistorySelector{HistorySelectorForSeries(s)}, start, now, false, 500)
		if err != nil {
			return out, err
		}
		tv := make([]TimedValue, 0, len(points))
		for _, p := range points {
			tv = append(tv, TimedValue{At: p.ObservedAt, Value: p.Value})
		}
		v, ok := ReduceTimeSeries(condition.GetQuery().GetTimeReducer(), tv)
		if ok {
			values = append(values, v)
		}
	}
	if len(values) == 0 {
		out.NoDataReason = noDataReason(condition.GetNoDataPolicy())
		return out, nil
	}
	out.HasData = true
	out.Value, _ = ReduceSeries(condition.GetQuery().GetSeriesReducer(), values)
	out.Result = Compare(condition.GetCompare(), out.Value, out.Threshold)
	return out, nil
}
func noDataReason(policy monitorpb.NoDataPolicy) string {
	switch policy {
	case monitorpb.NoDataPolicy_NO_DATA_POLICY_KEEP_STATE:
		return "keep_state"
	case monitorpb.NoDataPolicy_NO_DATA_POLICY_OK:
		return "ok"
	case monitorpb.NoDataPolicy_NO_DATA_POLICY_FIRING:
		return "firing"
	default:
		return "unspecified"
	}
}
func labelsMatch(raw string, matchers []*monitorpb.LabelMatcher) bool {
	if len(matchers) == 0 {
		return true
	}
	var labels map[string]string
	if json.Unmarshal([]byte(raw), &labels) != nil {
		return false
	}
	for _, m := range matchers {
		got, ok := labels[m.GetName()]
		equal := ok && got == m.GetValue()
		if m.GetNegate() {
			if equal {
				return false
			}
		} else if !equal {
			return false
		}
	}
	return true
}

func (e *MetricEvaluator) applyState(ctx context.Context, rule *monitorpb.MetricRule, evaluation *RuleEvaluation) error {
	if e.repo == nil {
		return errors.New("metric rule repository is required")
	}
	state, err := e.repo.GetState(ctx, rule.GetSpaceId(), rule.GetRuleId())
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if state == nil || errors.Is(err, gorm.ErrRecordNotFound) {
		state = &MetricRuleStateRow{SpaceID: rule.GetSpaceId(), RuleID: rule.GetRuleId(), Status: domain.AlertStatusOK}
	}
	state.OwnerInstanceID = e.instance
	state.LastEvaluatedAt = &evaluation.EvaluatedAt
	var transition string
	if evaluation.Result {
		state.RecoveryCount = 0
		state.TriggerCount++
		if state.Status != domain.AlertStatusFiring && state.TriggerCount >= int(rule.GetConsecutiveTriggerCount()) {
			state.Status = domain.AlertStatusFiring
			state.LastTriggeredAt = &evaluation.EvaluatedAt
			transition = domain.AlertEventTriggered
		}
	} else {
		state.TriggerCount = 0
		state.RecoveryCount++
		if state.Status == domain.AlertStatusFiring && state.RecoveryCount >= int(rule.GetConsecutiveRecoveryCount()) {
			state.Status = domain.AlertStatusResolved
			state.LastRecoveredAt = &evaluation.EvaluatedAt
			transition = domain.AlertEventResolved
		} else if state.Status == domain.AlertStatusResolved && state.RecoveryCount >= int(rule.GetConsecutiveRecoveryCount()) {
			state.Status = domain.AlertStatusOK
		}
	}
	evaluation.Status = state.Status
	raw, _ := json.Marshal(evaluation)
	evaluation.ResultJSON = string(raw)
	if evaluation.KeepState {
		evalJSON, _ := json.Marshal(evaluation)
		return e.repo.InsertEvaluation(ctx, &MetricRuleEvaluationRow{SpaceID: rule.GetSpaceId(), RuleID: rule.GetRuleId(), EvaluatedAt: evaluation.EvaluatedAt, Status: state.Status, ResultJSON: string(evalJSON)})
	}
	if err := e.repo.UpsertState(ctx, state); err != nil {
		return err
	}
	evalJSON, _ := json.Marshal(evaluation)
	if err := e.repo.InsertEvaluation(ctx, &MetricRuleEvaluationRow{SpaceID: rule.GetSpaceId(), RuleID: rule.GetRuleId(), EvaluatedAt: evaluation.EvaluatedAt, Status: state.Status, ResultJSON: string(evalJSON)}); err != nil {
		return err
	}
	if transition != "" && !stringsEqual(state.Status, domain.AlertStatusOK) {
		return e.notify(ctx, rule, evaluation, transition)
	}
	return nil
}
func stringsEqual(a, b string) bool { return a == b }
func (e *MetricEvaluator) notify(ctx context.Context, rule *monitorpb.MetricRule, evaluation *RuleEvaluation, eventType string) error {
	if e.notifier == nil || e.webhooks == nil {
		return nil
	}
	for _, id := range rule.GetWebhookIds() {
		wh, err := e.webhooks(ctx, rule.GetSpaceId(), id)
		if err != nil {
			return err
		}
		if wh == nil || !wh.Enabled {
			continue
		}
		if err := e.notifier.SendMetric(ctx, *wh, MetricEvent{EventType: eventType, Rule: rule, Evaluation: evaluation, OwnerInstanceID: e.instance}); err != nil {
			return err
		}
	}
	return nil
}

type MetricNotifier interface {
	SendMetric(context.Context, domain.WebhookChannel, MetricEvent) error
}
