package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"github.com/mooyang-code/moox/packages/report"
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
	Enabled    bool
	SpaceID    string
	DatasetID  string
	ViewID     string
	Frequency  string
	MarketID   string
	CalendarID string
	Timezone   string
	Sessions   []string
	// Lateness 是应收的最新一根收线后允许迟到多久：取健康策略按周期给出的值与采集任务声明的允许迟到（数据源自身的
	// 发布延迟，例如 A 股 90 秒）中较大的一个。缺的根从收线起超过它仍没到，就判为过期。
	Lateness              time.Duration
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
	policy      report.RealtimeTimeSeriesPolicy
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

// NewKlineFreshnessEvaluatorWithInventory 按采集任务清单生成规则；允许迟到按健康策略的周期规则给出。
func NewKlineFreshnessEvaluatorWithInventory(query *QueryService, inventory *TaskResultInventoryCache, policy report.RealtimeTimeSeriesPolicy, maxSubjects int) *KlineFreshnessEvaluator {
	evaluator := NewKlineFreshnessEvaluator(query, nil, maxSubjects)
	evaluator.inventory = inventory
	evaluator.policy = policy
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
	inventoryStale := false
	if e.inventory != nil {
		inventory, stale, err := e.inventory.GetAllowStale(ctx, now)
		if err != nil {
			return nil, &TaskResultInventoryRefreshError{Cause: fmt.Errorf("load task-result inventory: %w", err), State: e.inventory.State(now)}
		}
		rules = klineFreshnessRulesFromInventory(inventory, e.policy)
		inventoryEvaluated = true
		inventoryStale = stale
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
		inventoryReport := KlineFreshnessReport{
			Rule:    KlineFreshnessRule{SpaceID: InternalMetricSpaceID, ViewID: "task-result-inventory"},
			CheckID: TaskResultInventoryCheckID, Success: true, Reason: "inventory_fresh",
			Diagnostic: fmt.Sprintf("cache_age_seconds=%.0f", state.Age.Seconds()),
		}
		if inventoryStale {
			// 控制面数据过期：继续用最后一份有效清单检查，同时单独告警。
			inventoryReport.Success, inventoryReport.Reason = false, "inventory_stale"
			inventoryReport.Diagnostic = fmt.Sprintf("cache_age_seconds=%.0f last_error=%s", state.Age.Seconds(), state.LastError)
		}
		reports = append(reports, inventoryReport)
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
		// 应收的最新一根（E）按市场交易日历算：休市、午休、未开盘时它停在上一时段的最后一根，所以不需要“休市跳过”“开盘预热”。
		// 日历覆盖不到、市场没有日历、周期对齐无法确认时不推测，按无法判断报出来。
		clock, freq, expected, clockErr := klineExpectedBar(rule, now)
		if rule.ResultStatus == "error" {
			report.Reason = "task_result_error"
			if clockErr == nil {
				report.StaleAge, _ = overdueAfter(clock, freq, rule.ViewLastDataTime, expected, now)
			}
			report.Diagnostic = formatKlineDiagnostic(report)
			reports = append(reports, report)
			continue
		}
		if clockErr != nil {
			report.Reason = klineClockReason(clockErr)
			report.Diagnostic = clockErr.Error()
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
		if lag, behind := completedPeriodViewLag(clock, freq, rule, summary.latest, now); behind {
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
			report.StaleAge, _ = overdueAfter(clock, freq, rule.ViewLastDataTime, expected, now)
			report.Diagnostic = formatKlineDiagnostic(report)
			reports = append(reports, report)
			continue
		}
		e.evaluateViewSummary(&report, rule, clock, freq, expected, summary, activeSubjects, now)
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].CheckID < reports[j].CheckID })
	return reports, nil
}

// completedPeriodViewLag 判断结果视图是否落后于采集已完成的最新一期；落后时返回该期之后视图还缺的第一根已过收线多久。
func completedPeriodViewLag(clock MarketClock, freq frequencypkg.Frequency, rule KlineFreshnessRule, viewLatest time.Time, now time.Time) (time.Duration, bool) {
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
	completed, err := clock.BarAt(rule.LatestCompletedPeriod, freq)
	if err != nil {
		return 0, true
	}
	if latestViewTime.IsZero() {
		return positive(now.Sub(completed.Close)), true
	}
	overdue, _ := overdueAfter(clock, freq, latestViewTime, completed, now)
	return overdue, true
}

// klineExpectedBar 按规则声明的市场日历算出此刻应已收线的最新一根。
func klineExpectedBar(rule KlineFreshnessRule, now time.Time) (MarketClock, frequencypkg.Frequency, Bar, error) {
	freq, err := frequencypkg.Parse(rule.Frequency)
	if err != nil {
		return MarketClock{}, "", Bar{}, fmt.Errorf("周期 %q 无效：%w", rule.Frequency, err)
	}
	clock, err := MarketClockFor(rule.MarketID, rule.CalendarID, rule.Timezone, rule.Sessions)
	if err != nil {
		return MarketClock{}, "", Bar{}, err
	}
	expected, err := clock.LatestClosed(now, freq)
	if err != nil {
		return MarketClock{}, "", Bar{}, err
	}
	return clock, freq, expected, nil
}

// klineClockReason 把无法按日历判断的原因转成报告原因码。
func klineClockReason(err error) string {
	switch {
	case errors.Is(err, ErrCalendarNotCovered):
		return "calendar_not_covered"
	case errors.Is(err, ErrNoTradingCalendar):
		return "calendar_unknown"
	case errors.Is(err, ErrAlignmentUnconfirmed):
		return "alignment_unconfirmed"
	default:
		return "calendar_error"
	}
}

// overdueAfter 返回 latest 之后、截至 expected 还缺的第一根已过收线多久；latest 已经是（或晚于）应收的最新一根时为 0。
// latest 为零（从未产出）时无法确定缺的是哪一根，返回 0。
func overdueAfter(clock MarketClock, freq frequencypkg.Frequency, latest time.Time, expected Bar, now time.Time) (time.Duration, error) {
	if latest.IsZero() {
		return 0, nil
	}
	bar, err := clock.BarAt(latest, freq)
	if err != nil {
		return 0, err
	}
	if !bar.Close.Before(expected.Close) {
		return 0, nil
	}
	next, err := clock.Next(bar, freq)
	if err != nil {
		return 0, err
	}
	return positive(now.Sub(next.Close)), nil
}

// ReportSnapshotGrace 是上报快照可以代表“此刻”的最长时间：生产者每 30 秒上报一次，超过它还没有新快照（漏报、
// 停止上报，或重启后某个视图一直没有新写入、不再产生摘要）就不能再按快照时刻判断，否则停更会一直被当作没逾期。
const ReportSnapshotGrace = time.Minute

// SnapshotTime 返回判逾期用的观测时刻：快照在 ReportSnapshotGrace 之内用快照时刻（之后才收线、可能已写完但还没上报的根
// 不算逾期）；没有快照、快照太旧或晚于 now（生产者时钟偏快）时用 now。
func SnapshotTime(at, now time.Time) time.Time {
	if at.IsZero() || at.After(now) || now.Sub(at) > ReportSnapshotGrace {
		return now
	}
	return at
}

func positive(value time.Duration) time.Duration {
	return max(value, 0)
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

func klineFreshnessRulesFromInventory(snapshot TaskResultInventorySnapshot, policy report.RealtimeTimeSeriesPolicy) []KlineFreshnessRule {
	rules := make([]KlineFreshnessRule, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if !entry.Enabled {
			rules = append(rules, inventoryRule(entry, false, policy))
			continue
		}
		if entry.ResultStatus == "error" {
			rules = append(rules, inventoryRule(entry, true, policy))
			continue
		}
		if entry.ResultStatus == "pending" {
			rules = append(rules, inventoryRule(entry, true, policy))
			continue
		}
		if !entry.OwnershipVerified || entry.ResultStatus != "ready" {
			unverified := inventoryRule(entry, true, policy)
			unverified.ResultStatus = "unverified"
			rules = append(rules, unverified)
			continue
		}
		rules = append(rules, inventoryRule(entry, true, policy))
	}
	return rules
}

func inventoryRule(entry TaskResultInventoryEntry, enabled bool, policy report.RealtimeTimeSeriesPolicy) KlineFreshnessRule {
	lateness := max(policy.Lateness(entry.SpaceID, entry.DatasetID, entry.Frequency), time.Duration(entry.AllowedLatenessSeconds)*time.Second)
	return KlineFreshnessRule{
		Enabled: enabled, SpaceID: entry.SpaceID, DatasetID: entry.DatasetID, ViewID: entry.ViewID,
		Frequency: entry.Frequency, MarketID: entry.MarketID, CalendarID: entry.CalendarID,
		Timezone: entry.Timezone, Sessions: append([]string(nil), entry.Sessions...), Lateness: lateness,
		ResultStatus: entry.ResultStatus, LatestCompletedPeriod: entry.LatestCompletedPeriod,
		LatestCompletedStatus: entry.LatestCompletedStatus, ViewLastDataTime: entry.ViewLastDataTime,
		InventoryObservedAt: entry.ObservedAt,
	}
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
func (e *KlineFreshnessEvaluator) evaluateViewSummary(report *KlineFreshnessReport, rule KlineFreshnessRule, clock MarketClock, freq frequencypkg.Frequency, expected Bar, summary viewSummary, active map[string]struct{}, now time.Time) {
	report.ObservedCount = summary.subjects
	report.OldestDataTime = summary.latest
	// 摘要是 Storage View 上报快照那一刻的状态（每 30 秒上报一次，整分与半分对齐）：逾期按快照时刻算。按当前时间算会把
	// 快照之后才写完、还没来得及上报的准时 K 线算成逾期——半分评估若早于那次快照入库，就会误判“整体停止更新”。
	seen := SnapshotTime(summary.snapshotAt, now)
	// 视图最新一根之后、应收的最新一根之前还缺的第一根，到快照时已晚超过允许迟到仍没有，就算整体停止更新。
	// 缺的根按交易日历数：午休、收盘后、节假日不会被算成缺失。
	overdue, err := overdueAfter(clock, freq, summary.latest, expected, seen)
	if err != nil {
		report.Success, report.Reason = false, klineClockReason(err)
		report.Diagnostic = err.Error()
		return
	}
	report.StaleAge = overdue
	if overdue > rule.Lateness {
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
	inactive, withinGrace := 0, 0
	threshold := rule.Lateness
	for _, item := range summary.laggingSubjects {
		name, ok := activeSubjectName(item.subjectID, active, aliases, suffix)
		if !ok {
			inactive++
			continue
		}
		// Storage 按连续时间判落后（比视图最新一根落后超过一根加容忍）：A 股午休、隔夜后新时段第一根刚到时，还没到的标的
		// 也会被它算进来。按交易日历再筛一遍：缺的第一根从收线起还在“允许迟到 + stale_after”之内的，不算过期。
		age, err := overdueAfter(clock, freq, item.dataTime, expected, seen)
		if err == nil && age <= threshold {
			withinGrace++
			continue
		}
		stale[name] = struct{}{}
		if item.dataTime.Before(report.OldestDataTime) {
			report.OldestDataTime = item.dataTime
		}
		if err == nil && age > report.StaleAge {
			report.StaleAge = age
		}
	}
	report.StaleCount = max(summary.lagging-inactive-withinGrace, 0)
	namesTruncated := active != nil && summary.lagging > len(summary.laggingSubjects)
	// 名单按最落后在前：名单里的活跃标的都还在允许迟到之内，没列出的落后得更少，同样不算过期。
	allWithinGrace := withinGrace > 0 && len(stale) == 0
	// 视图开始跟踪之后至少又前进了一根（data_time 都是开盘时间，比跟踪起点晚就是后面的根）。
	trackingMoved := !summary.trackedSince.IsZero() && summary.latest.After(summary.trackedSince)
	if active != nil && namesTruncated && !allWithinGrace {
		// 落后对象名单被截断（只上报最落后的若干个）：名单只能用于展示，不能用来扣减完整计数，
		// 否则超过名单长度的已停用对象会被当作过期持续误报。改用集合算术：没有落后的对象视为活跃，
		// 活跃集合里放不下它们的那部分才是落后或缺失的；不会低于名单里已确认的活跃过期对象数。
		if trackingMoved {
			activeUpToDate := max(summary.subjects-summary.lagging, 0)
			report.StaleCount = max(len(active)-activeUpToDate, len(stale))
			report.MissingCount = 0
		} else {
			report.StaleCount = len(stale)
		}
	} else if active != nil && trackingMoved {
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
	if namesTruncated && report.StaleCount == 0 && !allWithinGrace {
		// 名单被截断，只能推出“落后数下界为零”：无法确认活跃对象是否都在更新，不能据此宣告健康（也不触发新告警）。
		// 按“无法判定”跳过本轮，已有告警保持，等名单不再截断或有确定结论。
		report.Skipped, report.Success = true, false
		report.Reason = "lagging_subjects_truncated"
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
		canonicalRuleFrequency(rule.Frequency) == identity.Frequency
}

// canonicalRuleFrequency lets freshness rules use frequency aliases, while
// Storage identities are always canonical.
func canonicalRuleFrequency(raw string) string {
	canonical, err := frequencypkg.Normalize(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return canonical
}

func isValidKlineFrequency(value string) bool {
	return frequencypkg.IsCanonical(value)
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
