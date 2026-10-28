package events

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strings"
	"time"
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

func validateDatasetRowsUpserted(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*storagepb.DatasetRowsUpserted)
	if !ok {
		return fmt.Errorf("storage event payload has type %T", value)
	}
	if payload.GetSpaceId() == "" ||
		payload.GetSpaceId() != message.GetSpaceId() ||
		payload.GetDatasetId() == "" ||
		payload.GetDatasetId() != message.GetSubjectId() {
		return fmt.Errorf("storage event payload identity mismatch")
	}
	if len(payload.GetRows()) == 0 {
		return fmt.Errorf("storage event rows payload is empty")
	}
	if !validRequiredToken(payload.GetNodeId()) || !validRequiredToken(payload.GetStoreId()) || payload.GetSequence() == 0 {
		return fmt.Errorf("storage event source position is required")
	}
	for i, row := range payload.GetRows() {
		if row == nil || row.GetKey() == nil {
			return fmt.Errorf("storage event row %d key is required", i)
		}
		if row.GetKey().GetSpaceId() != payload.GetSpaceId() ||
			row.GetKey().GetDatasetId() != payload.GetDatasetId() {
			return fmt.Errorf("storage event row %d identity mismatch", i)
		}
		if err := validateStorageRow(row); err != nil {
			return fmt.Errorf("storage event row %d: %w", i, err)
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
	// 明细对 Storage 是不透明字节：只要求有明细就有类型，不解析内容。
	if (len(payload.GetDetail()) != 0) != (payload.GetDetailType() != "") || (payload.GetDetailType() != "" && !validRequiredToken(payload.GetDetailType())) {
		return fmt.Errorf("%s detail and detail_type must be provided together", label)
	}
	return validateCommittedPositions(payload.GetCommittedPositions(), label)
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
	if err := validateCommittedPositions(payload.GetCommittedPositions(), "view data ready"); err != nil {
		return err
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

func validateCommittedPositions(positions []*storagepb.CommittedPosition, label string) error {
	if len(positions) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(positions))
	for i, position := range positions {
		if position == nil || !validRequiredToken(position.GetNodeId()) || !validRequiredToken(position.GetStoreId()) || position.GetSequence() == 0 {
			return fmt.Errorf("%s committed_position %d is incomplete", label, i)
		}
		key := position.GetNodeId() + "\x00" + position.GetStoreId()
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%s committed_position %q/%q is duplicated", label, position.GetNodeId(), position.GetStoreId())
		}
		seen[key] = struct{}{}
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
	if !validRequiredToken(payload.GetFrequency()) || !validRequiredToken(payload.GetSyncRequestId()) {
		return fmt.Errorf("collector kline backfilled frequency and sync_request_id are required")
	}
	if len(payload.GetSubjectIds()) == 0 {
		return fmt.Errorf("collector kline backfilled subject_ids are required")
	}
	for _, subject := range payload.GetSubjectIds() {
		if !validRequiredToken(subject) {
			return fmt.Errorf("collector kline backfilled subject_id %q is invalid", subject)
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

func validateDatasetSyncPoint(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*storagepb.DatasetSyncPoint)
	if !ok {
		return fmt.Errorf("dataset sync point payload has type %T", value)
	}
	if !validRequiredToken(payload.GetSyncPointId()) || !validRequiredToken(payload.GetRequestId()) || !validRequiredToken(payload.GetDatasetId()) {
		return fmt.Errorf("dataset sync point identity is incomplete")
	}
	if message == nil || payload.GetDatasetId() != message.GetSubjectId() {
		return fmt.Errorf("dataset sync point dataset_id does not match subject_id")
	}
	switch payload.GetSource() {
	case "import", "catchup":
		return nil
	default:
		return fmt.Errorf("dataset sync point source %q is invalid", payload.GetSource())
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
	if payload.GetBarEndTime() == nil || payload.GetEffectiveAt() == nil || payload.GetValidUntil() == nil {
		return fmt.Errorf("trade target weight timestamps are required")
	}
	for name, timestamp := range map[string]*timestamppb.Timestamp{
		"bar_end_time": payload.GetBarEndTime(), "effective_at": payload.GetEffectiveAt(), "valid_until": payload.GetValidUntil(),
	} {
		if err := timestamp.CheckValid(); err != nil {
			return fmt.Errorf("trade target weight %s: %w", name, err)
		}
	}
	if !payload.GetEffectiveAt().AsTime().Equal(payload.GetBarEndTime().AsTime()) {
		return fmt.Errorf("trade target weight effective_at must equal bar_end_time")
	}
	if !payload.GetValidUntil().AsTime().After(payload.GetEffectiveAt().AsTime()) {
		return fmt.Errorf("trade target weight valid_until must be after effective_at")
	}
	seenInstruments := make(map[string]struct{}, len(payload.GetTargets()))
	gross := new(big.Rat)
	for i, target := range payload.GetTargets() {
		if target == nil {
			return fmt.Errorf("trade target weight %d is nil", i)
		}
		instrumentID := target.GetInstrumentId()
		if strings.TrimSpace(instrumentID) == "" || strings.TrimSpace(instrumentID) != instrumentID {
			return fmt.Errorf("trade target weight %d instrument_id is empty", i)
		}
		if _, exists := seenInstruments[instrumentID]; exists {
			return fmt.Errorf("trade target weight instrument_id %q is duplicated", instrumentID)
		}
		seenInstruments[instrumentID] = struct{}{}
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

func validateStorageRow(row *storagepb.RowUpsert) error {
	key := row.GetKey()
	switch kind := key.GetKind().(type) {
	case *storagepb.RowKey_TimeSeries:
		series := kind.TimeSeries
		if series == nil || strings.TrimSpace(series.GetSubjectId()) == "" || strings.TrimSpace(series.GetFreq()) == "" || strings.TrimSpace(series.GetDataTime()) == "" {
			return fmt.Errorf("time-series key requires subject_id, freq, and data_time")
		}
		if err := validateStorageTime(series.GetDataTime()); err != nil {
			return fmt.Errorf("time-series data_time: %w", err)
		}
		if err := validateSeriesTag(series.GetSeriesTag()); err != nil {
			return fmt.Errorf("time-series series_tag: %w", err)
		}
	case *storagepb.RowKey_Record:
		record := kind.Record
		if record == nil || strings.TrimSpace(record.GetRecordId()) == "" || strings.TrimSpace(record.GetVersion()) == "" {
			return fmt.Errorf("record key requires record_id and version")
		}
	default:
		return fmt.Errorf("row key kind is required")
	}
	for i, field := range row.GetFields() {
		if field == nil || strings.TrimSpace(field.GetFieldId()) == "" {
			return fmt.Errorf("field %d requires field_id", i)
		}
		if err := validateStorageValue(field.GetValue()); err != nil {
			return fmt.Errorf("field %d: %w", i, err)
		}
	}
	for name, value := range row.GetAttributes() {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("attribute name is required")
		}
		if err := validateStorageValue(value); err != nil {
			return fmt.Errorf("attribute %q: %w", name, err)
		}
	}
	return nil
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

func validateStorageValue(value *storagepb.TypedValue) error {
	if value == nil || value.GetValue() == nil {
		return fmt.Errorf("typed value is required")
	}
	switch typed := value.GetValue().(type) {
	case *storagepb.TypedValue_DoubleValue:
		if math.IsNaN(typed.DoubleValue) || math.IsInf(typed.DoubleValue, 0) {
			return fmt.Errorf("double value must be finite")
		}
	case *storagepb.TypedValue_TimeValue:
		if err := validateStorageTime(typed.TimeValue); err != nil {
			return fmt.Errorf("time value: %w", err)
		}
	case *storagepb.TypedValue_JsonValue:
		if !json.Valid([]byte(typed.JsonValue)) {
			return fmt.Errorf("json value is invalid")
		}
	case *storagepb.TypedValue_ListValue:
		if typed.ListValue == nil {
			return fmt.Errorf("list value is nil")
		}
		for i, item := range typed.ListValue.GetValues() {
			if err := validateStorageValue(item); err != nil {
				return fmt.Errorf("list item %d: %w", i, err)
			}
		}
	case *storagepb.TypedValue_NullValue:
		if typed.NullValue != storagepb.NullValue_NULL_VALUE_NULL {
			return fmt.Errorf("null value must be explicitly NULL")
		}
	}
	return nil
}

func validateStorageTime(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("time is required")
	}
	if _, err := time.Parse(time.RFC3339Nano, raw); err != nil {
		return err
	}
	return nil
}
