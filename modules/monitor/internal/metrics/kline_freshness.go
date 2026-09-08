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
	KlinePrimaryLastDataTimeMetric        = "moox_storage_kline_last_data_time_seconds"
	KlinePrimaryLastCommitTimestampMetric = "moox_storage_kline_last_commit_timestamp_seconds"
	KlineViewLastDataTimeMetric           = "moox_storage_view_kline_last_data_time_seconds"
	KlineViewLastCommitTimestampMetric    = "moox_storage_view_kline_last_commit_timestamp_seconds"

	KlineScopePrimary     = "primary"
	KlineScopeView        = "view"
	KlineDefaultSeriesTag = "default"
	klineFutureTolerance  = 10 * time.Minute
)

type KlineFreshnessRule struct {
	Enabled    bool
	Scope      string
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
	var now time.Time
	if len(nowValues) > 0 {
		now = nowValues[0]
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	rows, err := e.query.ListLatestByMetricNames(ctx, []string{
		KlinePrimaryLastDataTimeMetric,
		KlinePrimaryLastCommitTimestampMetric,
		KlineViewLastDataTimeMetric,
		KlineViewLastCommitTimestampMetric,
	}, 0)
	if err != nil {
		return nil, err
	}

	observations := make(map[klineIdentity]*klineObservation)
	invalid := make(map[klineIdentity]struct{})
	for _, row := range rows {
		identity, kind, err := parseKlineMetric(row.MetricName, row.LabelsJSON)
		if err != nil || !isValidKlineFrequency(identity.Frequency) {
			continue
		}
		value, err := klineUnixTime(row.Value)
		if err != nil || value.After(now.Add(klineFutureTolerance)) {
			invalid[identity] = struct{}{}
			continue
		}
		item := observations[identity]
		if item == nil {
			item = &klineObservation{identity: identity}
			observations[identity] = item
		}
		if kind == klineDataTime {
			item.dataTime = value
			item.hasData = true
		} else {
			item.commitTime = value
			item.hasCommit = true
		}
	}

	reports := make([]KlineFreshnessReport, 0, len(e.rules))
	for _, rule := range e.rules {
		if !rule.Enabled {
			continue
		}
		report := KlineFreshnessReport{Rule: rule, CheckID: KlineFreshnessCheckID(rule)}
		if reason := klineMarketSkipReason(rule, now); reason != "" {
			report.Skipped = true
			report.Reason = reason
			reports = append(reports, report)
			continue
		}
		observed := make([]klineObservation, 0)
		for identity, item := range observations {
			if _, bad := invalid[identity]; bad || !item.hasData || !klineRuleMatches(rule, identity) {
				continue
			}
			observed = append(observed, *item)
		}
		if len(observed) == 0 {
			report.Skipped = true
			report.Reason = "no_observation"
			reports = append(reports, report)
			continue
		}
		staleSubjects := make(map[string]struct{})
		observedSubjects := make(map[string]struct{})
		for _, item := range observed {
			report.ObservedCount++
			observedSubjects[item.identity.SubjectID] = struct{}{}
			if report.OldestDataTime.IsZero() || item.dataTime.Before(report.OldestDataTime) {
				report.OldestDataTime = item.dataTime
			}
			if item.hasCommit && (report.LatestCommitTime.IsZero() || item.commitTime.After(report.LatestCommitTime)) {
				report.LatestCommitTime = item.commitTime
			}
			if now.Sub(item.dataTime) > rule.StaleAfter {
				report.StaleCount++
				staleSubjects[item.identity.SubjectID] = struct{}{}
			}
		}
		report.ObservedSubjects = sortedLimitedSubjects(observedSubjects, len(observedSubjects))
		report.StaleSubjects = sortedLimitedSubjects(staleSubjects, e.maxSubjects)
		report.Success = report.StaleCount == 0
		if report.Success {
			report.Reason = "fresh"
		} else {
			report.Reason = "business_data_stale"
		}
		report.Diagnostic = formatKlineDiagnostic(report)
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].CheckID < reports[j].CheckID })
	return reports, nil
}

func KlineFreshnessCheckID(rule KlineFreshnessRule) string {
	target := rule.DatasetID
	if rule.Scope == KlineScopeView {
		target = rule.ViewID
	}
	return fmt.Sprintf("kline_freshness:%s:%s:%s:%s", rule.Scope, rule.SpaceID, target, rule.Frequency)
}

type klineMetricKind uint8

const (
	klineDataTime klineMetricKind = iota + 1
	klineCommitTime
)

type klineIdentity struct {
	Scope, SpaceID, Target, SubjectID, Frequency, SeriesTag string
}

type klineObservation struct {
	identity   klineIdentity
	dataTime   time.Time
	commitTime time.Time
	hasData    bool
	hasCommit  bool
}

func parseKlineMetric(metricName, rawLabels string) (klineIdentity, klineMetricKind, error) {
	var labels map[string]string
	if err := json.Unmarshal([]byte(rawLabels), &labels); err != nil {
		return klineIdentity{}, 0, err
	}
	if labels == nil {
		return klineIdentity{}, 0, fmt.Errorf("labels must be an object")
	}
	scope, target, kind := "", "", klineDataTime
	allowed := map[string]struct{}{"space_id": {}, "dataset_id": {}, "view_id": {}, "subject_id": {}, "freq": {}, "series_tag": {}}
	for key := range labels {
		if _, ok := allowed[key]; !ok {
			return klineIdentity{}, 0, fmt.Errorf("unknown label %q", key)
		}
	}
	switch metricName {
	case KlinePrimaryLastDataTimeMetric:
		scope, target = KlineScopePrimary, labels["dataset_id"]
	case KlinePrimaryLastCommitTimestampMetric:
		scope, target, kind = KlineScopePrimary, labels["dataset_id"], klineCommitTime
	case KlineViewLastDataTimeMetric:
		scope, target = KlineScopeView, labels["view_id"]
	case KlineViewLastCommitTimestampMetric:
		scope, target, kind = KlineScopeView, labels["view_id"], klineCommitTime
	default:
		return klineIdentity{}, 0, fmt.Errorf("unsupported metric %q", metricName)
	}
	if strings.TrimSpace(labels["space_id"]) == "" || strings.TrimSpace(target) == "" ||
		strings.TrimSpace(labels["subject_id"]) == "" || strings.TrimSpace(labels["freq"]) == "" {
		return klineIdentity{}, 0, fmt.Errorf("required kline label is empty")
	}
	if scope == KlineScopePrimary && strings.TrimSpace(labels["view_id"]) != "" ||
		scope == KlineScopeView && strings.TrimSpace(labels["dataset_id"]) != "" {
		return klineIdentity{}, 0, fmt.Errorf("scope target labels are not canonical")
	}
	seriesTag := strings.TrimSpace(labels["series_tag"])
	if seriesTag == "" {
		seriesTag = KlineDefaultSeriesTag
	}
	return klineIdentity{Scope: scope, SpaceID: strings.TrimSpace(labels["space_id"]), Target: strings.TrimSpace(target), SubjectID: strings.TrimSpace(labels["subject_id"]), Frequency: strings.TrimSpace(labels["freq"]), SeriesTag: seriesTag}, kind, nil
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

func klineRuleMatches(rule KlineFreshnessRule, identity klineIdentity) bool {
	target := rule.DatasetID
	if rule.Scope == KlineScopeView {
		target = rule.ViewID
	}
	return rule.Scope == identity.Scope && rule.SpaceID == identity.SpaceID && target == identity.Target && rule.Frequency == identity.Frequency
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
	oldest, latest := "", ""
	if !report.OldestDataTime.IsZero() {
		oldest = report.OldestDataTime.UTC().Format(time.RFC3339)
	}
	if !report.LatestCommitTime.IsZero() {
		latest = report.LatestCommitTime.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("stale_count=%d observed_count=%d oldest_data_time=%s latest_commit_time=%s stale_subjects=%s", report.StaleCount, report.ObservedCount, oldest, latest, strings.Join(report.StaleSubjects, ","))
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
			// At an opening or post-lunch restart the previous session's
			// watermark is expected to be old until the first closed bucket is
			// published. Allow two frequency buckets so a single missed minute
			// remains a transient hole rather than a daily false alert.
			if warmup := 2 * klineFrequencyDuration(rule.Frequency); warmup > 0 && localNow.Sub(start) < warmup {
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
