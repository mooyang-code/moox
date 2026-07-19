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
	"google.golang.org/protobuf/proto"
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
	rules    *MetricRuleStore
	catalog  *MetricCatalog
	storage  *StorageAdapter
	notifier MetricNotifier
	webhooks func(context.Context, string, string) (*domain.WebhookChannel, error)
	now      func() time.Time
}
type EvaluatorOptions struct {
	RuleStore  *MetricRuleStore
	Catalog    *MetricCatalog
	Storage    *StorageAdapter
	Notifier   MetricNotifier
	Webhook    func(context.Context, string, string) (*domain.WebhookChannel, error)
	Now        func() time.Time
}

func NewMetricEvaluator(opts EvaluatorOptions) *MetricEvaluator {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MetricEvaluator{rules: opts.RuleStore, catalog: opts.Catalog, storage: opts.Storage, notifier: opts.Notifier, webhooks: opts.Webhook, now: now}
}

func (e *MetricEvaluator) Evaluate(ctx context.Context, rule *monitorpb.MetricRule, preview bool) (*RuleEvaluation, error) {
	return e.evaluate(ctx, rule, preview, 500)
}

// Preview evaluates an unsaved rule with a caller-supplied bounded series and
// history limit. It never persists state, evaluations, or notifications.
func (e *MetricEvaluator) Preview(ctx context.Context, rule *monitorpb.MetricRule, limit int) (*RuleEvaluation, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	return e.evaluate(ctx, rule, true, limit)
}

func (e *MetricEvaluator) evaluate(ctx context.Context, rule *monitorpb.MetricRule, preview bool, limit int) (*RuleEvaluation, error) {
	if preview {
		if err := ValidateMetricRuleForPreview(rule); err != nil {
			return nil, err
		}
		// Do not mutate the request object while assigning a stable synthetic ID.
		rule = proto.Clone(rule).(*monitorpb.MetricRule)
		if rule.GetRuleId() == "" {
			rule.RuleId = "preview"
		}
	} else if err := ValidateMetricRule(rule); err != nil {
		return nil, err
	}
	if e == nil || e.catalog == nil || e.storage == nil {
		return nil, ErrMetricsStoreUnavailable
	}
	now := e.now()
	result := &RuleEvaluation{EvaluationID: fmt.Sprintf("%s-%d", rule.GetRuleId(), now.UnixNano()), SpaceID: rule.GetSpaceId(), RuleID: rule.GetRuleId(), EvaluatedAt: now, Status: domain.AlertStatusOK}
	for _, condition := range rule.GetConditions() {
		cr, err := e.evaluateCondition(ctx, condition, now, limit)
		if err != nil {
			return nil, err
		}
		result.Conditions = append(result.Conditions, cr)
	}
	result.Result, result.KeepState = combineConditionResults(rule.GetConnector(), result.Conditions, rule.GetConditions())
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

// combineConditionResults applies no-data policy per condition. For OR, a
// KEEP_STATE condition must not suppress another condition that has data and
// fires; it freezes the rule only when no condition can provide a result.
func combineConditionResults(connector monitorpb.LogicalOperator, results []ConditionResult, conditions []*monitorpb.MetricCondition) (bool, bool) {
	if connector == monitorpb.LogicalOperator_LOGICAL_OPERATOR_AND {
		for _, c := range results {
			if !c.HasData {
				if c.NoDataReason == "keep_state" {
					return false, true
				}
				return noDataResult(c.NoDataReason, conditions, c.ConditionID), false
			}
			if !c.Result {
				return false, false
			}
		}
		return true, false
	}
	result, hasData, keepState := false, false, false
	for _, c := range results {
		if c.HasData {
			hasData = true
			if c.Result {
				result = true
			}
			continue
		}
		switch c.NoDataReason {
		case "firing":
			result = true
		case "keep_state":
			keepState = true
		}
	}
	return result, keepState && !hasData && !result
}

func noDataResult(reason string, conditions []*monitorpb.MetricCondition, id string) bool {
	for _, c := range conditions {
		if c.GetConditionId() == id {
			switch c.GetNoDataPolicy() {
			case monitorpb.NoDataPolicy_NO_DATA_POLICY_FIRING:
				return true
			case monitorpb.NoDataPolicy_NO_DATA_POLICY_OK:
				return false
			default:
				return false
			}
		}
	}
	return reason == "firing"
}
func (e *MetricEvaluator) evaluateCondition(ctx context.Context, condition *monitorpb.MetricCondition, now time.Time, limit int) (ConditionResult, error) {
	out := ConditionResult{ConditionID: condition.GetConditionId(), Threshold: condition.GetThreshold()}
	selector := condition.GetQuery().GetSelector()
	// Filter service_name in SQL before applying label matchers. Filtering the
	// first 500 rows in memory can hide the requested service when another
	// service has high-cardinality series.
	series, err := e.catalog.FindSeries(ctx, "", selector.GetServiceName(), selector.GetMetricName(), "", limit)
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
		} else {
			// CURRENT is still a freshness-sensitive query. Do not treat an old
			// latest value as a healthy sample after the reporter went silent.
			start = now.Add(-e.catalog.NoDataAfter())
		}
		points, err := e.storage.QueryHistorySelectors(ctx, []HistorySelector{HistorySelectorForSeries(s)}, start, now, false, limit)
		if err != nil {
			return out, err
		}
		tv := make([]TimedValue, 0, len(points))
		for _, p := range points {
			tv = append(tv, TimedValue{At: p.ObservedAt, Value: p.Value})
		}
		if condition.GetQuery().GetTimeReducer() == monitorpb.TimeReducer_TIME_REDUCER_CURRENT && (len(tv) == 0 || now.Sub(tv[len(tv)-1].At) > e.catalog.NoDataAfter()) {
			continue
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
	if e.rules == nil {
		return errors.New("metric rule store is required")
	}
	state, err := e.rules.GetState(ctx, rule.GetSpaceId(), rule.GetRuleId())
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if state == nil || errors.Is(err, gorm.ErrRecordNotFound) {
		state = &MetricRuleStateRow{SpaceID: rule.GetSpaceId(), RuleID: rule.GetRuleId(), Status: domain.AlertStatusOK}
	}
	if evaluation.KeepState {
		// A KEEP_STATE no-data result is observational only. In particular it
		// must not advance trigger/recovery counters or refresh ownership state.
		evaluation.Status = state.Status
		evalJSON, _ := json.Marshal(evaluation)
		return e.rules.InsertEvaluation(ctx, &MetricRuleEvaluationRow{SpaceID: rule.GetSpaceId(), RuleID: rule.GetRuleId(), EvaluatedAt: evaluation.EvaluatedAt, Status: state.Status, ResultJSON: string(evalJSON)})
	}
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
	notificationEvent := transition
	if notificationEvent != "" {
		state.NotificationEvent = notificationEvent
		state.NotificationKey = metricNotificationKey(rule, state, notificationEvent)
		state.NotificationStatus = "pending"
		state.NotificationError = ""
		state.NotificationAttempts++
	} else if (state.NotificationStatus == "failed" || state.NotificationStatus == "pending") && state.NotificationEvent != "" && state.NotificationKey != "" {
		// A failed webhook must remain retryable even though the state transition
		// itself was already committed before delivery was attempted.
		notificationEvent = state.NotificationEvent
		state.NotificationStatus = "pending"
		state.NotificationError = ""
		state.NotificationAttempts++
	}
	evaluation.Status = state.Status
	raw, _ := json.Marshal(evaluation)
	evaluation.ResultJSON = string(raw)
	if err := e.rules.UpsertState(ctx, state); err != nil {
		return err
	}
	evalJSON, _ := json.Marshal(evaluation)
	if err := e.rules.InsertEvaluation(ctx, &MetricRuleEvaluationRow{SpaceID: rule.GetSpaceId(), RuleID: rule.GetRuleId(), EvaluatedAt: evaluation.EvaluatedAt, Status: state.Status, ResultJSON: string(evalJSON)}); err != nil {
		return err
	}
	if notificationEvent != "" && !stringsEqual(state.Status, domain.AlertStatusOK) {
		if err := e.notify(ctx, rule, evaluation, notificationEvent, state.NotificationKey); err != nil {
			state.NotificationStatus = "failed"
			state.NotificationError = err.Error()
			state.LastNotificationAt = &evaluation.EvaluatedAt
			_ = e.rules.UpsertState(ctx, state)
			return err
		}
		state.NotificationStatus = "sent"
		state.NotificationError = ""
		state.LastNotificationAt = &evaluation.EvaluatedAt
		if err := e.rules.UpsertState(ctx, state); err != nil {
			return err
		}
	}
	return nil
}
func stringsEqual(a, b string) bool { return a == b }
func metricNotificationKey(rule *monitorpb.MetricRule, state *MetricRuleStateRow, eventType string) string {
	triggeredAt := time.Time{}
	if state != nil && state.LastTriggeredAt != nil {
		triggeredAt = state.LastTriggeredAt.UTC()
	}
	return fmt.Sprintf("%s:%s:%s:%d", rule.GetSpaceId(), rule.GetRuleId(), eventType, triggeredAt.UnixNano())
}
func (e *MetricEvaluator) notify(ctx context.Context, rule *monitorpb.MetricRule, evaluation *RuleEvaluation, eventType, dedupeKey string) error {
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
		if err := e.notifier.SendMetric(ctx, *wh, MetricEvent{EventType: eventType, DedupeKey: dedupeKey, Rule: rule, Evaluation: evaluation}); err != nil {
			return err
		}
	}
	return nil
}

type MetricNotifier interface {
	SendMetric(context.Context, domain.WebhookChannel, MetricEvent) error
}
