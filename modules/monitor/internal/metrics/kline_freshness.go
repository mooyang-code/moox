package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/marketcalendar"
)

const (
	ViewDatasetInputLastDataTimeMetric         = "moox_storage_view_dataset_input_last_data_time_seconds"
	ViewDatasetOutputLastDataTimeMetric        = "moox_storage_view_dataset_output_last_data_time_seconds"
	ViewDatasetOutputLastCommitTimestampMetric = "moox_storage_view_dataset_output_last_commit_timestamp_seconds"
	KlineDefaultSeriesTag                      = "default"
	klineFutureTolerance                       = 10 * time.Minute
)

type KlineFreshnessRule struct {
	Enabled    bool
	SpaceID    string
	DatasetID  string
	ViewID     string
	Frequency  string
	MarketID   string
	CalendarID string
	Timezone   string
	Sessions   []string
	StaleAfter time.Duration
}

type KlineFreshnessReport struct {
	Rule             KlineFreshnessRule
	CheckID          string
	Success          bool
	Skipped          bool
	Reason           string
	Diagnostic       string
	StaleCount       int
	ObservedCount    int
	OldestDataTime   time.Time
	LatestInputTime  time.Time
	LatestCommitTime time.Time
	StaleSubjects    []string
	ObservedSubjects []string
}

type KlineFreshnessEvaluator struct {
	query       *QueryService
	rules       []KlineFreshnessRule
	maxSubjects int
}

func NewKlineFreshnessEvaluator(query *QueryService, rules []KlineFreshnessRule, maxSubjects int) *KlineFreshnessEvaluator {
	if maxSubjects <= 0 {
		maxSubjects = 20
	}
	if maxSubjects > 100 {
		maxSubjects = 100
	}
	return &KlineFreshnessEvaluator{query: query, rules: append([]KlineFreshnessRule(nil), rules...), maxSubjects: maxSubjects}
}

func (e *KlineFreshnessEvaluator) Evaluate(ctx context.Context, nowValues ...time.Time) ([]KlineFreshnessReport, error) {
	if e == nil || e.query == nil {
		return nil, ErrMetricsStoreUnavailable
	}
	now := time.Time{}
	if len(nowValues) > 0 {
		now = nowValues[0]
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	rows, err := e.query.ListLatestByMetricNames(ctx, []string{
		ViewDatasetInputLastDataTimeMetric,
		ViewDatasetOutputLastDataTimeMetric,
		ViewDatasetOutputLastCommitTimestampMetric,
	}, 0)
	if err != nil {
		return nil, err
	}

	observations := make(map[viewObservationIdentity]*viewObservation)
	for _, row := range rows {
		identity, kind, err := parseViewDatasetMetric(row.MetricName, row.LabelsJSON)
		if err != nil || !isValidKlineFrequency(identity.Frequency) {
			continue
		}
		value, err := klineUnixTime(row.Value)
		if err != nil || value.After(now.Add(klineFutureTolerance)) {
			continue
		}
		item := observations[identity]
		if item == nil {
			item = &viewObservation{identity: identity}
			observations[identity] = item
		}
		switch kind {
		case viewInputDataTime:
			if preferMetricSample(item.inputObservedAt, row.ObservedAt, item.inputTime, value) {
				item.inputTime, item.inputObservedAt, item.hasInput = value, row.ObservedAt, true
			}
		case viewOutputDataTime:
			if preferMetricSample(item.outputObservedAt, row.ObservedAt, item.outputTime, value) {
				item.outputTime, item.outputObservedAt, item.hasOutput = value, row.ObservedAt, true
			}
		case viewOutputCommitTime:
			if preferMetricSample(item.commitObservedAt, row.ObservedAt, item.commitTime, value) {
				item.commitTime, item.commitObservedAt, item.hasCommit = value, row.ObservedAt, true
			}
		}
	}

	reports := make([]KlineFreshnessReport, 0, len(e.rules))
	for _, rule := range e.rules {
		if !rule.Enabled {
			continue
		}
		report := KlineFreshnessReport{Rule: rule, CheckID: KlineFreshnessCheckID(rule)}
		if reason := klineMarketSkipReason(rule, now); reason != "" {
			report.Skipped, report.Reason = true, reason
			reports = append(reports, report)
			continue
		}
		observed := make([]viewObservation, 0)
		for identity, item := range observations {
			if !item.hasOutput || !viewRuleMatches(rule, identity) {
				continue
			}
			observed = append(observed, *item)
		}
		if len(observed) == 0 {
			report.Skipped, report.Reason = true, "no_observation"
			report.Diagnostic = formatKlineDiagnostic(report)
			reports = append(reports, report)
			continue
		}

		staleSubjects := make(map[string]struct{})
		observedSubjects := make(map[string]struct{})
		viewOutputLag := false
		for _, item := range observed {
			report.ObservedCount++
			observedSubjects[item.identity.SubjectID] = struct{}{}
			if report.OldestDataTime.IsZero() || item.outputTime.Before(report.OldestDataTime) {
				report.OldestDataTime = item.outputTime
			}
			if item.hasInput && (report.LatestInputTime.IsZero() || item.inputTime.After(report.LatestInputTime)) {
				report.LatestInputTime = item.inputTime
			}
			if item.hasCommit && (report.LatestCommitTime.IsZero() || item.commitTime.After(report.LatestCommitTime)) {
				report.LatestCommitTime = item.commitTime
			}
			if item.hasInput && item.inputTime.After(item.outputTime) {
				viewOutputLag = true
			}
			if now.Sub(item.outputTime) > rule.StaleAfter {
				report.StaleCount++
				staleSubjects[item.identity.SubjectID] = struct{}{}
			}
		}
		report.ObservedSubjects = sortedLimitedSubjects(observedSubjects, len(observedSubjects))
		report.StaleSubjects = sortedLimitedSubjects(staleSubjects, e.maxSubjects)
		report.Success = report.StaleCount == 0
		switch {
		case report.Success:
			report.Reason = "fresh"
		case viewOutputLag:
			report.Reason = "view_output_stale"
		default:
			report.Reason = "business_data_stale"
		}
		report.Diagnostic = formatKlineDiagnostic(report)
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].CheckID < reports[j].CheckID })
	return reports, nil
}

func KlineFreshnessCheckID(rule KlineFreshnessRule) string {
	return fmt.Sprintf("kline_freshness:%s:%s:%s", strings.TrimSpace(rule.SpaceID), strings.TrimSpace(rule.ViewID), strings.TrimSpace(rule.Frequency))
}

type viewMetricKind uint8

const (
	viewInputDataTime viewMetricKind = iota + 1
	viewOutputDataTime
	viewOutputCommitTime
)

type viewObservationIdentity struct {
	SpaceID, ViewID, DatasetID, SubjectID, Frequency, SeriesTag string
}

type viewObservation struct {
	identity                                            viewObservationIdentity
	inputTime, outputTime                               time.Time
	commitTime                                          time.Time
	inputObservedAt, outputObservedAt, commitObservedAt time.Time
	hasInput, hasOutput, hasCommit                      bool
}

func preferMetricSample(currentObservedAt, candidateObservedAt, currentValue, candidateValue time.Time) bool {
	if currentObservedAt.IsZero() || candidateObservedAt.After(currentObservedAt) {
		return true
	}
	return candidateObservedAt.Equal(currentObservedAt) && candidateValue.After(currentValue)
}

func parseViewDatasetMetric(metricName, rawLabels string) (viewObservationIdentity, viewMetricKind, error) {
	var labels map[string]string
	if err := json.Unmarshal([]byte(rawLabels), &labels); err != nil {
		return viewObservationIdentity{}, 0, err
	}
	if labels == nil {
		return viewObservationIdentity{}, 0, fmt.Errorf("labels must be an object")
	}
	for key := range labels {
		switch key {
		case "space_id", "view_id", "dataset_id", "subject_id", "freq", "series_tag":
		default:
			return viewObservationIdentity{}, 0, fmt.Errorf("unknown label %q", key)
		}
	}
	kind := viewMetricKind(0)
	switch metricName {
	case ViewDatasetInputLastDataTimeMetric:
		kind = viewInputDataTime
	case ViewDatasetOutputLastDataTimeMetric:
		kind = viewOutputDataTime
	case ViewDatasetOutputLastCommitTimestampMetric:
		kind = viewOutputCommitTime
	default:
		return viewObservationIdentity{}, 0, fmt.Errorf("unsupported metric %q", metricName)
	}
	identity := viewObservationIdentity{
		SpaceID: strings.TrimSpace(labels["space_id"]), ViewID: strings.TrimSpace(labels["view_id"]),
		DatasetID: strings.TrimSpace(labels["dataset_id"]), SubjectID: strings.TrimSpace(labels["subject_id"]),
		Frequency: strings.TrimSpace(labels["freq"]), SeriesTag: strings.TrimSpace(labels["series_tag"]),
	}
	if identity.SeriesTag == "" {
		identity.SeriesTag = KlineDefaultSeriesTag
	}
	if identity.SpaceID == "" || identity.ViewID == "" || identity.DatasetID == "" || identity.SubjectID == "" || identity.Frequency == "" {
		return viewObservationIdentity{}, 0, fmt.Errorf("required view dataset label is empty")
	}
	return identity, kind, nil
}

func klineUnixTime(value float64) (time.Time, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > 4102444800 {
		return time.Time{}, fmt.Errorf("invalid unix timestamp %v", value)
	}
	seconds := int64(value)
	nanos := int64((value - float64(seconds)) * float64(time.Second))
	result := time.Unix(seconds, nanos).UTC()
	if result.Year() <= 0 {
		return time.Time{}, fmt.Errorf("invalid unix timestamp %v", value)
	}
	return result, nil
}

func viewRuleMatches(rule KlineFreshnessRule, identity viewObservationIdentity) bool {
	return strings.TrimSpace(rule.ViewID) == identity.ViewID && strings.TrimSpace(rule.SpaceID) == identity.SpaceID &&
		(strings.TrimSpace(rule.DatasetID) == "" || strings.TrimSpace(rule.DatasetID) == identity.DatasetID) &&
		strings.TrimSpace(rule.Frequency) == identity.Frequency
}

func isValidKlineFrequency(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	index := 0
	for index < len(value) && value[index] >= '0' && value[index] <= '9' {
		index++
	}
	if index == 0 || index == len(value) {
		return false
	}
	amount, err := strconv.Atoi(value[:index])
	if err != nil || amount <= 0 {
		return false
	}
	switch value[index:] {
	case "s", "m", "h", "d", "w", "M":
		return true
	default:
		return false
	}
}

func sortedLimitedSubjects(subjects map[string]struct{}, limit int) []string {
	result := make([]string, 0, len(subjects))
	for subject := range subjects {
		result = append(result, subject)
	}
	sort.Strings(result)
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result
}

func formatKlineDiagnostic(report KlineFreshnessReport) string {
	format := func(value time.Time) string {
		if value.IsZero() {
			return ""
		}
		return value.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("stale_count=%d observed_count=%d oldest_output_data_time=%s latest_input_data_time=%s latest_commit_time=%s stale_subjects=%s", report.StaleCount, report.ObservedCount, format(report.OldestDataTime), format(report.LatestInputTime), format(report.LatestCommitTime), strings.Join(report.StaleSubjects, ","))
}

func klineMarketSkipReason(rule KlineFreshnessRule, now time.Time) string {
	if rule.MarketID == "crypto" {
		return sessionSkipReason(rule, now, time.UTC)
	}
	if rule.MarketID != "stockcn" {
		return "skipped_calendar_unknown"
	}
	location, err := time.LoadLocation(rule.Timezone)
	if err != nil || strings.TrimSpace(rule.CalendarID) == "" {
		return "skipped_calendar_unknown"
	}
	calendar, err := marketcalendar.Load(rule.CalendarID)
	if err != nil {
		return "skipped_calendar_unknown"
	}
	localNow := now.In(location)
	civilDate, err := marketcalendar.NewCivilDate(localNow.Year(), localNow.Month(), localNow.Day())
	if err != nil {
		return "skipped_calendar_unknown"
	}
	status, err := calendar.Status(civilDate)
	if err != nil {
		return "skipped_calendar_unknown"
	}
	if status != marketcalendar.TradingDay {
		return "skipped_market_closed"
	}
	return sessionSkipReason(rule, now, location)
}

func sessionSkipReason(rule KlineFreshnessRule, now time.Time, location *time.Location) string {
	if len(rule.Sessions) == 0 {
		return ""
	}
	localNow := now.In(location)
	for _, raw := range rule.Sessions {
		parts := strings.Split(strings.TrimSpace(raw), "-")
		if len(parts) != 2 {
			continue
		}
		start, startErr := time.ParseInLocation("15:04", strings.TrimSpace(parts[0]), location)
		end, endErr := time.ParseInLocation("15:04", strings.TrimSpace(parts[1]), location)
		if startErr != nil || endErr != nil {
			continue
		}
		start = time.Date(localNow.Year(), localNow.Month(), localNow.Day(), start.Hour(), start.Minute(), 0, 0, location)
		end = time.Date(localNow.Year(), localNow.Month(), localNow.Day(), end.Hour(), end.Minute(), 0, 0, location)
		if !localNow.Before(start) && localNow.Before(end) {
			warmup := rule.StaleAfter
			if warmup <= 0 {
				warmup = 2 * klineFrequencyDuration(rule.Frequency)
			}
			if warmup > 0 && localNow.Sub(start) < warmup {
				return "skipped_session_warmup"
			}
			return ""
		}
	}
	return "skipped_market_closed"
}

func klineFrequencyDuration(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	index := 0
	for index < len(value) && value[index] >= '0' && value[index] <= '9' {
		index++
	}
	if index == 0 || index == len(value) {
		return 0
	}
	amount, err := strconv.Atoi(value[:index])
	if err != nil || amount <= 0 {
		return 0
	}
	unit := time.Duration(amount)
	switch value[index:] {
	case "s":
		return unit * time.Second
	case "m":
		return unit * time.Minute
	case "h":
		return unit * time.Hour
	case "d":
		return unit * 24 * time.Hour
	case "w":
		return unit * 7 * 24 * time.Hour
	default:
		return 0
	}
}
