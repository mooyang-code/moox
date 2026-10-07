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
	// Storage View publishes these per-View subject summaries; see
	// modules/storage/internal/observability/view_subject_tracker.go.
	ViewOutputLatestMetric         = "moox_storage_view_output_latest_data_time_seconds"
	ViewOutputTrackedSinceMetric   = "moox_storage_view_output_tracked_since_data_time_seconds"
	ViewOutputSubjectsMetric       = "moox_storage_view_output_subjects"
	ViewOutputLaggingMetric        = "moox_storage_view_output_lagging_subjects"
	ViewOutputLaggingSubjectMetric = "moox_storage_view_output_lagging_subject_data_time_seconds"
	KlineDefaultSeriesTag          = "default"
	TaskResultInventoryCheckID     = "kline_freshness:task_result_inventory"
	klineFutureTolerance           = 10 * time.Minute
)

type KlineFreshnessRule struct {
	Enabled               bool
	SpaceID               string
	DatasetID             string
	ViewID                string
	Frequency             string
	MarketID              string
	CalendarID            string
	Timezone              string
	Sessions              []string
	StaleAfter            time.Duration
	ResultStatus          string
	LatestCompletedPeriod time.Time
	LatestCompletedStatus string
	ViewLastDataTime      time.Time
	InventoryObservedAt   time.Time
}

type KlineFreshnessReport struct {
	Rule       KlineFreshnessRule
	CheckID    string
	Success    bool
	Skipped    bool
	Reason     string
	Diagnostic string
	// ViewStale means the View as a whole stopped updating, as opposed to
	// some of its subjects lagging.
	ViewStale      bool
	StaleCount     int
	MissingCount   int
	ObservedCount  int
	OldestDataTime time.Time
	StaleAge       time.Duration
	StaleSubjects  []string
}

type KlineFreshnessEvaluator struct {
	query       *QueryService
	rules       []KlineFreshnessRule
	maxSubjects int
	inventory   *TaskResultInventoryCache
	staleAfter  time.Duration
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

func NewKlineFreshnessEvaluatorWithInventory(query *QueryService, inventory *TaskResultInventoryCache, staleAfter time.Duration, maxSubjects int) *KlineFreshnessEvaluator {
	evaluator := NewKlineFreshnessEvaluator(query, nil, maxSubjects)
	evaluator.inventory = inventory
	evaluator.staleAfter = staleAfter
	return evaluator
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
	rules := e.rules
	inventoryEvaluated := false
	if e.inventory != nil {
		inventory, err := e.inventory.Get(ctx, now)
		if err != nil {
			return nil, &TaskResultInventoryRefreshError{Cause: fmt.Errorf("load task-result inventory: %w", err), State: e.inventory.State(now)}
		}
		rules = klineFreshnessRulesFromInventory(inventory, e.staleAfter)
		inventoryEvaluated = true
	}
	filters := make([]ViewMetricScope, 0, len(rules))
	for _, rule := range rules {
		if !rule.Enabled || rule.ResultStatus == "pending" || rule.ResultStatus == "unverified" {
			continue
		}
		filters = append(filters, ViewMetricScope{SpaceID: rule.SpaceID, ViewID: rule.ViewID, DatasetID: rule.DatasetID, Frequency: rule.Frequency})
	}
	summaries, err := e.loadViewSummaries(ctx, filters, now)
	if err != nil {
		return nil, err
	}

	reports := make([]KlineFreshnessReport, 0, len(rules)+1)
	if inventoryEvaluated {
		state := e.inventory.State(now)
		reports = append(reports, KlineFreshnessReport{
			Rule:    KlineFreshnessRule{SpaceID: InternalMetricSpaceID, ViewID: "task-result-inventory"},
			CheckID: TaskResultInventoryCheckID, Success: true, Reason: "inventory_fresh",
			Diagnostic: fmt.Sprintf("cache_age_seconds=%.0f", state.Age.Seconds()),
		})
	}
	for _, rule := range rules {
		if !rule.Enabled {
			reports = append(reports, KlineFreshnessReport{
				Rule: rule, CheckID: KlineFreshnessCheckID(rule), Skipped: true, Reason: "disabled",
			})
			continue
		}
		report := KlineFreshnessReport{Rule: rule, CheckID: KlineFreshnessCheckID(rule)}
		if rule.ResultStatus == "pending" || rule.ResultStatus == "unverified" {
			report.Skipped = true
			report.Reason = "task_result_not_ready"
			reports = append(reports, report)
			continue
		}
		if rule.ResultStatus == "error" {
			report.Reason = "task_result_error"
			report.StaleAge = inventoryDataAge(now, rule.ViewLastDataTime)
			report.Diagnostic = formatKlineDiagnostic(report)
			reports = append(reports, report)
			continue
		}
		if reason := klineMarketSkipReason(rule, now); reason != "" {
			report.Skipped, report.Reason = true, reason
			reports = append(reports, report)
			continue
		}
		summary := summaries.find(rule)
		var activeSubjects map[string]struct{}
		if strings.TrimSpace(rule.DatasetID) != "" {
			var catalogErr error
			activeSubjects, catalogErr = e.query.ActiveDatasetSubjects(ctx, rule.SpaceID, rule.DatasetID)
			if catalogErr != nil {
				report.Success, report.Reason = false, "subject_catalog_unavailable"
				report.Diagnostic = catalogErr.Error()
				reports = append(reports, report)
				continue
			}
		}
		if lag, behind := completedPeriodViewLag(rule, summary.latest, now); behind {
			report.Success = false
			report.Reason = "view_behind_latest_completed_period"
			report.StaleAge = lag
			// The View's latest bar; zero when it has produced nothing yet.
			report.OldestDataTime = summary.latest
			if rule.ViewLastDataTime.After(report.OldestDataTime) {
				report.OldestDataTime = rule.ViewLastDataTime
			}
			report.ObservedCount = summary.subjects
			report.Diagnostic = formatKlineDiagnostic(report)
			reports = append(reports, report)
			continue
		}
		if summary.latest.IsZero() {
			// An enabled rule with no output is a real failure: a misrouted
			// View or an unbound consumer must not remain silent.
			report.Success, report.Reason = false, "no_observation"
			report.StaleAge = inventoryDataAge(now, rule.ViewLastDataTime)
			report.Diagnostic = formatKlineDiagnostic(report)
			reports = append(reports, report)
			continue
		}
		e.evaluateViewSummary(&report, rule, summary, activeSubjects, now)
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].CheckID < reports[j].CheckID })
	return reports, nil
}

func completedPeriodViewLag(rule KlineFreshnessRule, viewLatest time.Time, now time.Time) (time.Duration, bool) {
	status := strings.ToLower(strings.TrimSpace(rule.LatestCompletedStatus))
	if rule.LatestCompletedPeriod.IsZero() || (status != "complete" && status != "degraded") {
		return 0, false
	}
	latestViewTime := rule.ViewLastDataTime
	if viewLatest.After(latestViewTime) {
		latestViewTime = viewLatest
	}
	if !latestViewTime.IsZero() && !latestViewTime.Before(rule.LatestCompletedPeriod) {
		return 0, false
	}
	if latestViewTime.IsZero() {
		return inventoryDataAge(now, rule.LatestCompletedPeriod), true
	}
	return rule.LatestCompletedPeriod.Sub(latestViewTime), true
}

// activeSubjectName maps a View subject onto the active metadata catalog. It
// tolerates a subject identity migration that only adds a market suffix to the
// View output (for example, OPG-USDT -> OPG-USDT-SPOT): a suffix match is used
// only when it resolves to one active catalog subject. Without a catalog every
// subject counts as active.
func activeSubjectName(subjectID string, active map[string]struct{}, aliases map[string][]string, expectedSuffix string) (string, bool) {
	subjectID = strings.TrimSpace(subjectID)
	if active == nil {
		return subjectID, true
	}
	if _, ok := active[subjectID]; ok {
		return subjectID, subjectSuffixCompatible(subjectID, expectedSuffix)
	}
	candidates := aliases[canonicalKlineSubjectID(subjectID)]
	if len(candidates) != 1 || !subjectSuffixCompatible(subjectID, expectedSuffix) || !subjectSuffixCompatible(candidates[0], expectedSuffix) {
		return "", false
	}
	return candidates[0], true
}

func activeSubjectAliases(active map[string]struct{}) map[string][]string {
	aliases := make(map[string][]string, len(active))
	for subjectID := range active {
		canonical := canonicalKlineSubjectID(subjectID)
		aliases[canonical] = append(aliases[canonical], subjectID)
	}
	return aliases
}

func expectedKlineSubjectSuffix(datasetID string) string {
	datasetID = strings.ToLower(strings.TrimSpace(datasetID))
	if strings.Contains(datasetID, "swap") || strings.Contains(datasetID, "perpetual") {
		return "SWAP"
	}
	if strings.Contains(datasetID, "spot") {
		return "SPOT"
	}
	return ""
}

func subjectSuffixCompatible(subjectID, expected string) bool {
	if expected == "" {
		return true
	}
	suffix := subjectProductSuffix(subjectID)
	return suffix == "" || suffix == expected
}

func canonicalKlineSubjectID(subjectID string) string {
	subjectID = strings.TrimSpace(subjectID)
	upper := strings.ToUpper(subjectID)
	for _, suffix := range []string{"-SPOT", "-SWAP"} {
		if strings.HasSuffix(upper, suffix) && len(subjectID) > len(suffix) {
			return subjectID[:len(subjectID)-len(suffix)]
		}
	}
	return subjectID
}

func subjectProductSuffix(subjectID string) string {
	upper := strings.ToUpper(strings.TrimSpace(subjectID))
	if strings.HasSuffix(upper, "-SPOT") {
		return "SPOT"
	}
	if strings.HasSuffix(upper, "-SWAP") {
		return "SWAP"
	}
	return ""
}

func KlineFreshnessCheckID(rule KlineFreshnessRule) string {
	return fmt.Sprintf("kline_freshness:%s:%s:%s", strings.TrimSpace(rule.SpaceID), strings.TrimSpace(rule.ViewID), strings.TrimSpace(rule.Frequency))
}

func klineFreshnessRulesFromInventory(snapshot TaskResultInventorySnapshot, staleAfter time.Duration) []KlineFreshnessRule {
	rules := make([]KlineFreshnessRule, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if !entry.Enabled {
			rules = append(rules, inventoryRule(entry, false, staleAfter))
			continue
		}
		if entry.ResultStatus == "error" {
			rules = append(rules, inventoryRule(entry, true, staleAfter))
			continue
		}
		if entry.ResultStatus == "pending" {
			rules = append(rules, inventoryRule(entry, true, staleAfter))
			continue
		}
		if !entry.OwnershipVerified || entry.ResultStatus != "ready" {
			unverified := inventoryRule(entry, true, staleAfter)
			unverified.ResultStatus = "unverified"
			rules = append(rules, unverified)
			continue
		}
		rules = append(rules, inventoryRule(entry, true, staleAfter))
	}
	return rules
}

func inventoryRule(entry TaskResultInventoryEntry, enabled bool, staleAfter time.Duration) KlineFreshnessRule {
	return KlineFreshnessRule{
		Enabled: enabled, SpaceID: entry.SpaceID, DatasetID: entry.DatasetID, ViewID: entry.ViewID,
		Frequency: entry.Frequency, MarketID: entry.MarketID, CalendarID: entry.CalendarID,
		Timezone: entry.Timezone, Sessions: append([]string(nil), entry.Sessions...), StaleAfter: staleAfter,
		ResultStatus: entry.ResultStatus, LatestCompletedPeriod: entry.LatestCompletedPeriod,
		LatestCompletedStatus: entry.LatestCompletedStatus, ViewLastDataTime: entry.ViewLastDataTime,
		InventoryObservedAt: entry.ObservedAt,
	}
}

func inventoryDataAge(now, dataTime time.Time) time.Duration {
	if dataTime.IsZero() || dataTime.After(now) {
		return 0
	}
	return now.Sub(dataTime)
}

type viewSummaryIdentity struct {
	SpaceID, ViewID, DatasetID, Frequency string
}

type laggingSubject struct {
	subjectID string
	dataTime  time.Time
}

// viewSummary is a View's subject summary from the latest Storage View
// snapshot: its latest bar, the bar it was first tracked at, and its tracked,
// lagging and most-lagging subjects.
type viewSummary struct {
	latest, trackedSince time.Time
	subjects, lagging    int
	snapshotAt           time.Time
	laggingSubjects      []laggingSubject
}

type viewSummaries map[viewSummaryIdentity]*viewSummary

// find returns the summary of the View a rule watches; with several matches
// (a rule without a dataset) the most recent View wins.
func (s viewSummaries) find(rule KlineFreshnessRule) viewSummary {
	var best *viewSummary
	for identity, summary := range s {
		if !viewRuleMatches(rule, identity) {
			continue
		}
		if best == nil || summary.latest.After(best.latest) {
			best = summary
		}
	}
	if best == nil {
		return viewSummary{}
	}
	return *best
}

var viewSummaryMetrics = []string{ViewOutputLatestMetric, ViewOutputTrackedSinceMetric, ViewOutputSubjectsMetric, ViewOutputLaggingMetric, ViewOutputLaggingSubjectMetric}

func (e *KlineFreshnessEvaluator) loadViewSummaries(ctx context.Context, filters []ViewMetricScope, now time.Time) (viewSummaries, error) {
	rowsByMetric := make(map[string][]MetricLatest, len(viewSummaryMetrics))
	for _, name := range viewSummaryMetrics {
		rows, err := e.query.ListLatestByViewScopes(ctx, name, filters, 0)
		if err != nil {
			return nil, err
		}
		rowsByMetric[name] = rows
	}
	summaries := make(viewSummaries)
	at := func(identity viewSummaryIdentity) *viewSummary {
		summary := summaries[identity]
		if summary == nil {
			summary = &viewSummary{}
			summaries[identity] = summary
		}
		return summary
	}
	for _, name := range viewSummaryMetrics[:4] {
		for _, row := range rowsByMetric[name] {
			identity, _, err := parseViewSummaryLabels(row.LabelsJSON, false)
			if err != nil || !isValidKlineFrequency(identity.Frequency) {
				continue
			}
			summary := at(identity)
			switch name {
			case ViewOutputLatestMetric, ViewOutputTrackedSinceMetric:
				value, err := klineUnixTime(row.Value)
				if err != nil || value.After(now.Add(klineFutureTolerance)) {
					continue
				}
				if name == ViewOutputLatestMetric {
					summary.latest = value
				} else {
					summary.trackedSince = value
				}
			case ViewOutputSubjectsMetric:
				summary.subjects, summary.snapshotAt = int(row.Value), row.ObservedAt
			case ViewOutputLaggingMetric:
				summary.lagging = int(row.Value)
			}
		}
	}
	for _, row := range rowsByMetric[ViewOutputLaggingSubjectMetric] {
		identity, subjectID, err := parseViewSummaryLabels(row.LabelsJSON, true)
		if err != nil {
			continue
		}
		summary := summaries[identity]
		// A subject named in an older snapshot may have caught up since; only
		// names from the View's latest snapshot count.
		if summary == nil || row.ObservedAt.Before(summary.snapshotAt) {
			continue
		}
		value, err := klineUnixTime(row.Value)
		if err != nil {
			continue
		}
		summary.laggingSubjects = append(summary.laggingSubjects, laggingSubject{subjectID: subjectID, dataTime: value})
	}
	return summaries, nil
}

func parseViewSummaryLabels(rawLabels string, withSubject bool) (viewSummaryIdentity, string, error) {
	var labels map[string]string
	if err := json.Unmarshal([]byte(rawLabels), &labels); err != nil {
		return viewSummaryIdentity{}, "", err
	}
	identity := viewSummaryIdentity{
		SpaceID: strings.TrimSpace(labels["space_id"]), ViewID: strings.TrimSpace(labels["view_id"]),
		DatasetID: strings.TrimSpace(labels["dataset_id"]), Frequency: strings.TrimSpace(labels["freq"]),
	}
	subjectID := strings.TrimSpace(labels["subject_id"])
	if identity.SpaceID == "" || identity.ViewID == "" || identity.DatasetID == "" || identity.Frequency == "" || (withSubject && subjectID == "") {
		return viewSummaryIdentity{}, "", fmt.Errorf("required view summary label is empty")
	}
	return identity, subjectID, nil
}

// evaluateViewSummary judges a View that has output. The View is stale as a
// whole once the bar after its latest bar is overdue. Otherwise its lagging
// subjects, and active subjects with no output once the View has moved a bar
// past the start of tracking, are stale.
func (e *KlineFreshnessEvaluator) evaluateViewSummary(report *KlineFreshnessReport, rule KlineFreshnessRule, summary viewSummary, active map[string]struct{}, now time.Time) {
	bar := klineFrequencyDuration(rule.Frequency)
	report.ObservedCount = summary.subjects
	report.OldestDataTime = summary.latest
	// A bar is stamped with its period start and exists only once the period
	// closes, so the newest bar is always one period behind. Data is stale
	// once the bar after it is overdue: that bar closes two periods after the
	// latest bar's start, plus stale_after.
	nextBarDue := summary.latest.Add(2 * bar)
	report.StaleAge = inventoryDataAge(now, nextBarDue)
	if now.Sub(nextBarDue) > rule.StaleAfter {
		report.ViewStale = true
		report.StaleCount = max(summary.subjects, len(active))
		report.Reason = "business_data_stale"
		report.Diagnostic = formatKlineDiagnostic(*report)
		return
	}
	report.StaleAge = 0
	aliases := activeSubjectAliases(active)
	suffix := expectedKlineSubjectSuffix(rule.DatasetID)
	stale := make(map[string]struct{})
	inactive := 0
	for _, item := range summary.laggingSubjects {
		name, ok := activeSubjectName(item.subjectID, active, aliases, suffix)
		if !ok {
			inactive++
			continue
		}
		stale[name] = struct{}{}
		if item.dataTime.Before(report.OldestDataTime) {
			report.OldestDataTime = item.dataTime
		}
		if age := inventoryDataAge(now, item.dataTime.Add(2*bar)); age > report.StaleAge {
			report.StaleAge = age
		}
	}
	report.StaleCount = max(summary.lagging-inactive, 0)
	if active != nil && !summary.trackedSince.IsZero() && !summary.latest.Before(summary.trackedSince.Add(bar)) {
		report.MissingCount = max(len(active)-(summary.subjects-inactive), 0)
		report.StaleCount += report.MissingCount
	}
	report.StaleSubjects = sortedLimitedSubjects(stale, e.maxSubjects)
	report.Success = report.StaleCount == 0
	if report.Success {
		report.Reason = "fresh"
	} else {
		report.Reason = "business_data_stale"
	}
	report.Diagnostic = formatKlineDiagnostic(*report)
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

func viewRuleMatches(rule KlineFreshnessRule, identity viewSummaryIdentity) bool {
	return strings.TrimSpace(rule.ViewID) == identity.ViewID && strings.TrimSpace(rule.SpaceID) == identity.SpaceID &&
		(strings.TrimSpace(rule.DatasetID) == "" || strings.TrimSpace(rule.DatasetID) == identity.DatasetID) &&
		strings.TrimSpace(rule.Frequency) == identity.Frequency
}

// klineFrequencyUnit normalizes a frequency unit the way Collector does: units
// are case-insensitive (Collector names hourly bars 1H) except M, which means
// month as opposed to m for minute.
func klineFrequencyUnit(unit string) string {
	if unit == "M" {
		return unit
	}
	return strings.ToLower(unit)
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
	switch klineFrequencyUnit(value[index:]) {
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
	return fmt.Sprintf("stale_count=%d missing_count=%d observed_count=%d oldest_output_data_time=%s stale_age=%s latest_completed_period=%s latest_completed_status=%s view_last_data_time=%s inventory_observed_at=%s stale_subjects=%s",
		report.StaleCount, report.MissingCount, report.ObservedCount, format(report.OldestDataTime), report.StaleAge,
		format(report.Rule.LatestCompletedPeriod), report.Rule.LatestCompletedStatus,
		format(report.Rule.ViewLastDataTime), format(report.Rule.InventoryObservedAt), strings.Join(report.StaleSubjects, ","))
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
	switch klineFrequencyUnit(value[index:]) {
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
