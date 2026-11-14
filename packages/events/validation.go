package events

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type EventValidator func(*eventpb.EventMessage, proto.Message) error

func validateObservabilityHostSnapshotReported(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*hostmetricpb.HostMetric)
	if !ok {
		return fmt.Errorf("host metric payload has type %T", value)
	}
	if !hostmetricpb.IsAgentID(payload.GetAgentId()) ||
		strings.TrimSpace(payload.GetHostname()) == "" ||
		payload.GetSnapshot() == nil {
		return fmt.Errorf("host metric identity or snapshot is incomplete")
	}
	if payload.GetAgentId() != message.GetSubjectId() {
		return fmt.Errorf("host metric agent_id does not match subject_id")
	}
	return nil
}

func validateObservabilityMetricsSnapshotReported(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*metricspb.MetricReport)
	if !ok {
		return fmt.Errorf("metric report payload has type %T", value)
	}
	if strings.TrimSpace(payload.GetServiceName()) == "" ||
		strings.TrimSpace(payload.GetInstanceId()) == "" ||
		payload.GetSnapshot() == nil {
		return fmt.Errorf("metric report producer identity or snapshot is incomplete")
	}
	if message.GetSubjectId() != payload.GetServiceName()+"/"+payload.GetInstanceId() {
		return fmt.Errorf("metric report producer does not match subject_id")
	}
	return nil
}

// 一批重要日志的上限：条数、单条正文与字段的大小。发送方按这些上限截断，超出的事件直接拒收。
const (
	MaxLogReportRecords  = 100
	MaxLogMessageBytes   = 4096
	MaxLogRecordFields   = 32
	MaxLogFieldKeyBytes  = 128
	MaxLogFieldValueSize = 1024
)

func validateObservabilityLogReported(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*metricspb.LogReport)
	if !ok {
		return fmt.Errorf("log report payload has type %T", value)
	}
	if strings.TrimSpace(payload.GetServiceName()) == "" || strings.TrimSpace(payload.GetInstanceId()) == "" || strings.TrimSpace(payload.GetBootId()) == "" {
		return fmt.Errorf("log report producer identity is incomplete")
	}
	if message.GetSubjectId() != payload.GetServiceName()+"/"+payload.GetInstanceId() {
		return fmt.Errorf("log report producer does not match subject_id")
	}
	if len(payload.GetRecords()) == 0 || len(payload.GetRecords()) > MaxLogReportRecords {
		return fmt.Errorf("log report must carry 1 to %d records", MaxLogReportRecords)
	}
	for _, record := range payload.GetRecords() {
		if record.GetSequence() == 0 || record.GetTimeUnixNano() <= 0 || strings.TrimSpace(record.GetLevel()) == "" {
			return fmt.Errorf("log record sequence, time and level are required")
		}
		if len(record.GetMessage()) > MaxLogMessageBytes || len(record.GetFields()) > MaxLogRecordFields {
			return fmt.Errorf("log record exceeds the message or field limit")
		}
		for key, fieldValue := range record.GetFields() {
			if len(key) > MaxLogFieldKeyBytes || len(fieldValue) > MaxLogFieldValueSize {
				return fmt.Errorf("log record field exceeds the size limit")
			}
		}
	}
	return nil
}

func validateDatasetPeriodCompleted(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*storagepb.DatasetPeriodCompleted)
	if !ok {
		return fmt.Errorf("dataset period completed payload has type %T", value)
	}
	const label = "dataset period completed"
	if err := validateStoragePeriod(message, payload.GetDatasetId(), payload.GetFrequency(), payload.GetPeriodTime(), payload.GetStatus(), payload.GetCompletedAt(), label); err != nil {
		return err
	}
	switch payload.GetSource() {
	case "ledger":
		if !validRequiredToken(payload.GetSnapshotRef()) {
			return fmt.Errorf("%s snapshot_ref is required for source ledger", label)
		}
	case "producer":
		if !validRequiredToken(payload.GetCauseEventId()) {
			return fmt.Errorf("%s cause_event_id is required for source producer", label)
		}
	default:
		return fmt.Errorf("%s source %q is invalid", label, payload.GetSource())
	}
	subjects, err := validateUniqueTokens(payload.GetUniverseSubjectIds(), false, label+" universe_subject_ids")
	if err != nil {
		return err
	}
	failed, err := validateUniqueTokens(payload.GetFailedSubjects(), false, label+" failed_subjects")
	if err != nil {
		return err
	}
	for subject := range failed {
		if _, ok := subjects[subject]; !ok {
			return fmt.Errorf("%s failed_subject %q is not in universe_subject_ids", label, subject)
		}
	}
	if payload.GetStatus() == "complete" && len(failed) != 0 {
		return fmt.Errorf("%s complete status has failed_subjects", label)
	}
	if err := validatePeriodSeries(payload, subjects, failed, label); err != nil {
		return err
	}
	// 明细对 Storage 是不透明字节：只要求有明细就有类型，不解析内容。
	if (len(payload.GetDetail()) != 0) != (payload.GetDetailType() != "") || (payload.GetDetailType() != "" && !validRequiredToken(payload.GetDetailType())) {
		return fmt.Errorf("%s detail and detail_type must be provided together", label)
	}
	return nil
}

// validatePeriodSeries 校验按序列的全集与失败名单：序列不重复、失败名单是全集的子集；
// 有序列全集时，它包含的标的必须正好是 universe_subject_ids；没有全集（单序列数据集）时不能带失败序列。
// 失败序列的标的必须在 failed_subjects 里：标的口径沿用原有定义（统一数据集执行计划 3.4），有一条序列失败的标的就记入失败标的，
// 只读按标的字段的消费方（单市场任务、K 线 View）才不会把失败序列的标的当成可用。
func validatePeriodSeries(payload *storagepb.DatasetPeriodCompleted, subjects, failedSubjects map[string]struct{}, label string) error {
	universe := make(map[string]struct{}, len(payload.GetUniverseSeries()))
	seenSubjects := make(map[string]struct{}, len(subjects))
	for i, series := range payload.GetUniverseSeries() {
		key, err := seriesRefKey(series)
		if err != nil {
			return fmt.Errorf("%s universe_series item %d: %w", label, i, err)
		}
		if _, ok := universe[key]; ok {
			return fmt.Errorf("%s universe_series item %q is duplicated", label, series.GetSubjectId()+"/"+series.GetSeriesTag())
		}
		universe[key] = struct{}{}
		if _, ok := subjects[series.GetSubjectId()]; !ok {
			return fmt.Errorf("%s universe_series subject %q is not in universe_subject_ids", label, series.GetSubjectId())
		}
		seenSubjects[series.GetSubjectId()] = struct{}{}
	}
	if len(universe) != 0 && len(seenSubjects) != len(subjects) {
		return fmt.Errorf("%s universe_series does not cover every subject in universe_subject_ids", label)
	}
	failed := make(map[string]struct{}, len(payload.GetFailedSeries()))
	for i, series := range payload.GetFailedSeries() {
		key, err := seriesRefKey(series)
		if err != nil {
			return fmt.Errorf("%s failed_series item %d: %w", label, i, err)
		}
		if _, ok := failed[key]; ok {
			return fmt.Errorf("%s failed_series item %q is duplicated", label, series.GetSubjectId()+"/"+series.GetSeriesTag())
		}
		failed[key] = struct{}{}
		if _, ok := universe[key]; !ok {
			return fmt.Errorf("%s failed_series %q is not in universe_series", label, series.GetSubjectId()+"/"+series.GetSeriesTag())
		}
		if _, ok := failedSubjects[series.GetSubjectId()]; !ok {
			return fmt.Errorf("%s failed_series subject %q is not in failed_subjects", label, series.GetSubjectId())
		}
	}
	if payload.GetStatus() == "complete" && len(failed) != 0 {
		return fmt.Errorf("%s complete status has failed_series", label)
	}
	return nil
}

// seriesRefKey 校验一条序列引用并返回它的键。series_tag 与行键里的 series_tag 同一套规则（validateSeriesTag），
// 否则事件里的序列可能对不上 Storage 实际存下的序列。
func seriesRefKey(series *storagepb.SeriesRef) (string, error) {
	if series == nil || !validRequiredToken(series.GetSubjectId()) || !validRequiredToken(series.GetSeriesTag()) {
		return "", fmt.Errorf("subject_id and series_tag are required")
	}
	if err := validateSeriesTag(series.GetSeriesTag()); err != nil {
		return "", fmt.Errorf("series_tag: %w", err)
	}
	return series.GetSubjectId() + "\x00" + series.GetSeriesTag(), nil
}

func validateViewDataReady(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*storagepb.ViewDataReady)
	if !ok {
		return fmt.Errorf("view data ready payload has type %T", value)
	}
	if err := validateStoragePeriod(message, payload.GetViewId(), payload.GetFrequency(), payload.GetPeriodTime(), payload.GetStatus(), payload.GetReadyAt(), "view data ready"); err != nil {
		return err
	}
	if !validRequiredToken(payload.GetCompletionEventId()) {
		return fmt.Errorf("view data ready completion_event_id is required")
	}
	if !validRequiredToken(payload.GetViewConfigId()) {
		return fmt.Errorf("view data ready view_config_id is required")
	}
	if !validRequiredToken(payload.GetDatasetId()) {
		return fmt.Errorf("view data ready dataset_id is required")
	}
	if !validRequiredToken(payload.GetVisibleScope()) {
		return fmt.Errorf("view data ready visible_scope is required")
	}
	if payload.GetStatus() == "complete" && validRequiredToken(payload.GetFailedScopeRef()) {
		return fmt.Errorf("view data ready complete status has failed_scope_ref")
	}
	if payload.GetStatus() == "degraded" && !validRequiredToken(payload.GetFailedScopeRef()) {
		return fmt.Errorf("view data ready degraded status requires failed_scope_ref")
	}
	if (len(payload.GetDetail()) != 0) != (payload.GetDetailType() != "") {
		return fmt.Errorf("view data ready detail and detail_type must be provided together")
	}
	if _, err := validateUniqueTokens(payload.GetUniverseSubjectIds(), false, "view data ready universe_subject_ids"); err != nil {
		return err
	}
	return nil
}

func validateCollectorKlineBackfilled(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*storagepb.CollectorKlineBackfilled)
	if !ok {
		return fmt.Errorf("collector kline backfilled payload has type %T", value)
	}
	if !validRequiredToken(payload.GetDatasetId()) || message == nil || payload.GetDatasetId() != message.GetSubjectId() {
		return fmt.Errorf("collector kline backfilled dataset_id does not match subject_id")
	}
	if !validRequiredToken(payload.GetFrequency()) {
		return fmt.Errorf("collector kline backfilled frequency is required")
	}
	if len(payload.GetSubjectIds()) == 0 {
		return fmt.Errorf("collector kline backfilled subject_ids are required")
	}
	for _, subject := range payload.GetSubjectIds() {
		if !validRequiredToken(subject) {
			return fmt.Errorf("collector kline backfilled subject_id %q is invalid", subject)
		}
	}
	if _, err := validateUniqueTokens(payload.GetSeriesTags(), false, "collector kline backfilled series_tags"); err != nil {
		return err
	}
	for _, tag := range payload.GetSeriesTags() {
		if err := validateSeriesTag(tag); err != nil {
			return fmt.Errorf("collector kline backfilled series_tag %q: %w", tag, err)
		}
	}
	start, end := payload.GetStart(), payload.GetEnd()
	if start == nil || end == nil || start.CheckValid() != nil || end.CheckValid() != nil || !end.AsTime().After(start.AsTime()) {
		return fmt.Errorf("collector kline backfilled range is invalid")
	}
	if payload.GetConfirmedAt() == nil || payload.GetConfirmedAt().CheckValid() != nil {
		return fmt.Errorf("collector kline backfilled confirmed_at is invalid")
	}
	switch payload.GetOrigin() {
	case "auto", "manual", "force", "resample":
		return nil
	default:
		return fmt.Errorf("collector kline backfilled origin %q is invalid", payload.GetOrigin())
	}
}

func validateStoragePeriod(message *eventpb.EventMessage, routeID, frequency string, periodTime int64, status string, timestamp *timestamppb.Timestamp, label string) error {
	if !validRequiredToken(routeID) || message == nil || routeID != message.GetSubjectId() {
		return fmt.Errorf("%s route identity does not match subject_id", label)
	}
	if !validRequiredToken(frequency) || periodTime <= 0 {
		return fmt.Errorf("%s frequency and period_time are required", label)
	}
	if !validCompletionStatus(status) {
		return fmt.Errorf("%s status %q is invalid", label, status)
	}
	if timestamp == nil || timestamp.CheckValid() != nil {
		return fmt.Errorf("%s timestamp is invalid", label)
	}
	return nil
}

func validateUniqueTokens(values []string, requireNonEmpty bool, label string) (map[string]struct{}, error) {
	if requireNonEmpty && len(values) == 0 {
		return nil, fmt.Errorf("%s are required", label)
	}
	seen := make(map[string]struct{}, len(values))
	for i, value := range values {
		if !validRequiredToken(value) {
			return nil, fmt.Errorf("%s item %d is invalid", label, i)
		}
		if _, ok := seen[value]; ok {
			return nil, fmt.Errorf("%s item %q is duplicated", label, value)
		}
		seen[value] = struct{}{}
	}
	return seen, nil
}

func validRequiredToken(value string) bool {
	return strings.TrimSpace(value) != "" && strings.TrimSpace(value) == value
}

func validCompletionStatus(status string) bool {
	return status == "complete" || status == "degraded"
}

const maxTargetWeightLength = 256
const maxSingleTargetWeight = 10
const maxGrossTargetWeight = 20

var decimalTargetWeightPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func validateLogicalAccountTargetWeightRequested(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*tradeeventpb.LogicalAccountTargetWeightRequested)
	if !ok {
		return fmt.Errorf("trade target weight payload has type %T", value)
	}
	if strings.TrimSpace(payload.GetTargetId()) == "" || strings.TrimSpace(payload.GetLogicalAccountId()) == "" {
		return fmt.Errorf("trade target weight identity is incomplete")
	}
	if strings.TrimSpace(payload.GetInstanceId()) == "" || strings.TrimSpace(payload.GetSessionId()) == "" || strings.TrimSpace(payload.GetStrategyId()) == "" {
		return fmt.Errorf("trade target weight session identity is incomplete")
	}
	if payload.GetBarEndTime() == nil || payload.GetValidUntil() == nil {
		return fmt.Errorf("trade target weight timestamps are required")
	}
	for name, timestamp := range map[string]*timestamppb.Timestamp{
		"bar_end_time": payload.GetBarEndTime(), "valid_until": payload.GetValidUntil(),
	} {
		if err := timestamp.CheckValid(); err != nil {
			return fmt.Errorf("trade target weight %s: %w", name, err)
		}
	}
	if !payload.GetValidUntil().AsTime().After(payload.GetBarEndTime().AsTime()) {
		return fmt.Errorf("trade target weight valid_until must be after bar_end_time")
	}
	type instrumentKey struct{ instrumentID, marketType string }
	seenInstruments := make(map[instrumentKey]struct{}, len(payload.GetTargets()))
	gross := new(big.Rat)
	for i, target := range payload.GetTargets() {
		if target == nil {
			return fmt.Errorf("trade target weight %d is nil", i)
		}
		instrumentID := target.GetInstrumentId()
		if strings.TrimSpace(instrumentID) == "" || strings.TrimSpace(instrumentID) != instrumentID {
			return fmt.Errorf("trade target weight %d instrument_id is empty", i)
		}
		marketType := target.GetMarketType()
		if !validTargetMarketType(marketType) {
			return fmt.Errorf("trade target weight %d market_type %q must be SPOT, SWAP or STOCK", i, marketType)
		}
		key := instrumentKey{instrumentID: instrumentID, marketType: marketType}
		if _, exists := seenInstruments[key]; exists {
			return fmt.Errorf("trade target weight instrument_id %q market_type %s is duplicated", instrumentID, marketType)
		}
		seenInstruments[key] = struct{}{}
		targetWeight := target.GetTargetWeight()
		if len(targetWeight) > maxTargetWeightLength || !decimalTargetWeightPattern.MatchString(targetWeight) {
			return fmt.Errorf("trade target weight %d target_weight is not decimal", i)
		}
		if _, ok := new(big.Rat).SetString(targetWeight); !ok {
			return fmt.Errorf("trade target weight %d target_weight is not decimal", i)
		}
		value, _ := new(big.Rat).SetString(targetWeight)
		if new(big.Rat).Abs(value).Cmp(new(big.Rat).SetInt64(maxSingleTargetWeight)) > 0 {
			return fmt.Errorf("trade target weight %d exceeds maximum single exposure", i)
		}
		gross.Add(gross, new(big.Rat).Abs(value))
	}
	if gross.Cmp(new(big.Rat).SetInt64(maxGrossTargetWeight)) > 0 {
		return fmt.Errorf("trade target gross exposure exceeds maximum")
	}
	if payload.GetTargetId() != message.GetEventId() {
		return fmt.Errorf("trade target weight target_id does not match event_id")
	}
	if payload.GetLogicalAccountId() != message.GetSubjectId() {
		return fmt.Errorf("trade target weight logical_account_id does not match subject_id")
	}
	return nil
}

// validTargetMarketType 判断目标里的市场是否是 Trade 认识的三种市场之一。
func validTargetMarketType(value string) bool {
	return value == "SPOT" || value == "SWAP" || value == "STOCK"
}

func validateSeriesTag(value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("must be valid UTF-8")
	}
	if len(value) > 128 {
		return fmt.Errorf("exceeds 128 bytes")
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("must not contain leading or trailing whitespace")
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("must not contain ASCII control characters")
		}
	}
	return nil
}
