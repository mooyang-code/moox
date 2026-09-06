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

	"github.com/mooyang-code/moox/packages/cloudjobpb"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/observabilitypb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type EventValidator func(*eventpb.EventMessage, proto.Message) error

func validateCloudJobExecutionRequested(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*cloudjobpb.JobExecutionRequested)
	if !ok {
		return fmt.Errorf("cloud job payload has type %T", value)
	}
	if strings.TrimSpace(payload.GetJobItemId()) == "" ||
		strings.TrimSpace(payload.GetJobType()) == "" {
		return fmt.Errorf("cloud job identity is incomplete")
	}
	if payload.GetJobItemId() != message.GetEventId() {
		return fmt.Errorf("cloud job item_id does not match event_id")
	}
	if message.GetSubjectId() != payload.GetJobType() {
		return fmt.Errorf("cloud job route does not match subject_id")
	}
	return nil
}

func validateObservabilityHostSnapshotReported(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*hostmetricpb.HostMetric)
	if !ok {
		return fmt.Errorf("host metric payload has type %T", value)
	}
	if !hostmetricpb.IsCompatibleAgentID(payload.GetAgentId()) ||
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

func validateObservabilityHealthCheckReported(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*observabilitypb.HealthCheckReport)
	if !ok {
		return fmt.Errorf("health check payload has type %T", value)
	}
	if strings.TrimSpace(payload.GetObserverId()) == "" ||
		strings.TrimSpace(payload.GetCheckId()) == "" ||
		strings.TrimSpace(payload.GetKind()) == "" {
		return fmt.Errorf("health check observer_id, check_id, and kind are required")
	}
	if len(payload.GetNodeId()) > 256 {
		return fmt.Errorf("health check node_id exceeds 256 bytes")
	}
	if len(payload.GetTarget()) > 512 {
		return fmt.Errorf("health check target exceeds 512 bytes")
	}
	if len(payload.GetErrorCode()) > 64 {
		return fmt.Errorf("health check error_code exceeds 64 bytes")
	}
	if len(payload.GetErrorSummary()) > 256 {
		return fmt.Errorf("health check error_summary exceeds 256 bytes")
	}
	if payload.GetLatencyMs() < 0 {
		return fmt.Errorf("health check latency_ms must be non-negative")
	}
	checkedAt := payload.GetCheckedAt()
	if checkedAt == nil {
		return fmt.Errorf("health check checked_at is required")
	}
	if err := checkedAt.CheckValid(); err != nil {
		return fmt.Errorf("health check checked_at: %w", err)
	}
	if message == nil || message.GetOccurredAt() == nil {
		return fmt.Errorf("health check envelope occurred_at is required")
	}
	delta := checkedAt.AsTime().Sub(message.GetOccurredAt().AsTime())
	if delta < -5*time.Minute || delta > 5*time.Minute {
		return fmt.Errorf("health check checked_at differs from occurred_at by more than 5 minutes")
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
	if len(payload.GetWriteSource()) > 256 || strings.TrimSpace(payload.GetWriteSource()) != payload.GetWriteSource() {
		return fmt.Errorf("storage event write_source is invalid")
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

func validateDatasetPeriodCollected(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*storagepb.DatasetPeriodCollected)
	if !ok {
		return fmt.Errorf("dataset period collected payload has type %T", value)
	}
	if err := validateStoragePeriod(message, payload.GetDatasetId(), payload.GetFrequency(), payload.GetPeriodTime(), payload.GetStatus(), payload.GetCollectedAt(), "dataset period collected"); err != nil {
		return err
	}
	subjects, err := validateUniqueTokens(payload.GetSubjectIds(), true, "dataset period collected subject_ids")
	if err != nil {
		return err
	}
	failed, err := validateUniqueTokens(payload.GetFailedSubjects(), false, "dataset period collected failed_subjects")
	if err != nil {
		return err
	}
	for subject := range failed {
		if _, ok := subjects[subject]; !ok {
			return fmt.Errorf("dataset period collected failed_subject %q is not expected", subject)
		}
	}
	if payload.GetStatus() == "complete" && len(failed) != 0 {
		return fmt.Errorf("dataset period collected complete status has failed_subjects")
	}
	return nil
}

func validateViewSourcePeriodReady(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*storagepb.ViewSourcePeriodReady)
	if !ok {
		return fmt.Errorf("view source period ready payload has type %T", value)
	}
	if err := validateStoragePeriod(message, payload.GetSourceViewId(), payload.GetFrequency(), payload.GetPeriodTime(), payload.GetStatus(), payload.GetReadyAt(), "view source period ready"); err != nil {
		return err
	}
	if len(payload.GetDatasets()) == 0 {
		return fmt.Errorf("view source period ready datasets are required")
	}
	seen := make(map[string]struct{}, len(payload.GetDatasets()))
	hasDegraded := false
	for i, state := range payload.GetDatasets() {
		if state == nil || !validRequiredToken(state.GetDatasetId()) {
			return fmt.Errorf("view source period ready dataset %d identity is invalid", i)
		}
		if _, ok := seen[state.GetDatasetId()]; ok {
			return fmt.Errorf("view source period ready dataset %q is duplicated", state.GetDatasetId())
		}
		seen[state.GetDatasetId()] = struct{}{}
		if !validCompletionStatus(state.GetStatus()) {
			return fmt.Errorf("view source period ready dataset %q status %q is invalid", state.GetDatasetId(), state.GetStatus())
		}
		failed, err := validateUniqueTokens(state.GetFailedSubjects(), false, fmt.Sprintf("view source period ready dataset %q failed_subjects", state.GetDatasetId()))
		if err != nil {
			return err
		}
		if state.GetStatus() == "complete" && len(failed) != 0 {
			return fmt.Errorf("view source period ready complete dataset %q has failed_subjects", state.GetDatasetId())
		}
		hasDegraded = hasDegraded || state.GetStatus() == "degraded"
	}
	if (payload.GetStatus() == "degraded") != hasDegraded {
		return fmt.Errorf("view source period ready status does not match datasets")
	}
	if (payload.GetActiveIndexId() == "") != (payload.GetActiveIndexRevision() == 0) {
		return fmt.Errorf("view source period ready active index provenance is incomplete")
	}
	_, err := validateUniqueTokens(payload.GetPrimarySubjects(), false, "view source period ready primary_subjects")
	return err
}

func validateFactorPeriodComputed(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*storagepb.FactorPeriodComputed)
	if !ok {
		return fmt.Errorf("factor period computed payload has type %T", value)
	}
	if !validRequiredToken(payload.GetSourceViewId()) || !validRequiredToken(payload.GetTriggerEventId()) {
		return fmt.Errorf("factor period computed source_view_id and trigger_event_id are required")
	}
	if err := validateStoragePeriod(message, payload.GetResultDatasetId(), payload.GetFrequency(), payload.GetPeriodTime(), payload.GetStatus(), payload.GetComputedAt(), "factor period computed"); err != nil {
		return err
	}
	// v1 markers written before provenance was introduced may still be queued
	// in the DataNode outbox. Keep those publishable; all new markers carry a
	// source index and are held to the stronger source-hash contract.
	if (payload.GetSourceIndexId() == "") != (payload.GetSourceIndexRevision() == 0) {
		return fmt.Errorf("factor period computed source index provenance is incomplete")
	}
	return validateFactorBindingStates(payload.GetBindings(), payload.GetStatus(), "factor period computed", payload.GetSourceIndexId() != "")
}

func validateViewFactorPeriodReady(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*storagepb.ViewFactorPeriodReady)
	if !ok {
		return fmt.Errorf("view factor period ready payload has type %T", value)
	}
	if !validRequiredToken(payload.GetSourceViewId()) {
		return fmt.Errorf("view factor period ready source_view_id is required")
	}
	if err := validateStoragePeriod(message, payload.GetResultViewId(), payload.GetFrequency(), payload.GetPeriodTime(), payload.GetStatus(), payload.GetReadyAt(), "view factor period ready"); err != nil {
		return err
	}
	hasSourceIndex := payload.GetSourceIndexId() != ""
	hasResultIndex := payload.GetResultIndexId() != ""
	if hasSourceIndex != (payload.GetSourceIndexRevision() != 0) || hasResultIndex != (payload.GetResultIndexRevision() != 0) {
		return fmt.Errorf("view factor period ready index provenance is incomplete")
	}
	if hasSourceIndex != hasResultIndex {
		return fmt.Errorf("view factor period ready source and result index provenance must be provided together")
	}
	return validateFactorBindingStates(payload.GetBindings(), payload.GetStatus(), "view factor period ready", hasSourceIndex)
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

func validateFactorBindingStates(states []*storagepb.FactorBindingPeriodState, status, label string, requireSourceHash bool) error {
	if len(states) == 0 {
		return fmt.Errorf("%s bindings are required", label)
	}
	seen := make(map[string]struct{}, len(states))
	hasDegraded := false
	for i, state := range states {
		if state == nil || !validRequiredToken(state.GetBindingId()) || !validRequiredToken(state.GetFactorId()) {
			return fmt.Errorf("%s binding %d identity is invalid", label, i)
		}
		if _, ok := seen[state.GetBindingId()]; ok {
			return fmt.Errorf("%s binding_id %q is duplicated", label, state.GetBindingId())
		}
		seen[state.GetBindingId()] = struct{}{}
		if !validCompletionStatus(state.GetStatus()) {
			return fmt.Errorf("%s binding %q status %q is invalid", label, state.GetBindingId(), state.GetStatus())
		}
		if requireSourceHash && !validRequiredToken(state.GetSourceHash()) {
			return fmt.Errorf("%s binding %q source_hash is required", label, state.GetBindingId())
		}
		skipped, err := validateUniqueTokens(state.GetSkippedSubjects(), false, fmt.Sprintf("%s binding %q skipped_subjects", label, state.GetBindingId()))
		if err != nil {
			return err
		}
		failed, err := validateUniqueTokens(state.GetFailedSubjects(), false, fmt.Sprintf("%s binding %q failed_subjects", label, state.GetBindingId()))
		if err != nil {
			return err
		}
		for subject := range skipped {
			if _, ok := failed[subject]; ok {
				return fmt.Errorf("%s binding %q subject %q is both skipped and failed", label, state.GetBindingId(), subject)
			}
		}
		if state.GetStatus() == "complete" && (len(skipped) != 0 || len(failed) != 0) {
			return fmt.Errorf("%s complete binding %q has skipped or failed subjects", label, state.GetBindingId())
		}
		hasDegraded = hasDegraded || state.GetStatus() == "degraded"
	}
	if (status == "degraded") != hasDegraded {
		return fmt.Errorf("%s status does not match bindings", label)
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

func validateMarketFetchBatchCompleted(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*marketfetchpb.MarketFetchBatchCompleted)
	if !ok {
		return fmt.Errorf("market fetch payload has type %T", value)
	}
	if strings.TrimSpace(payload.GetBatchId()) == "" ||
		strings.TrimSpace(payload.GetScheduleId()) == "" ||
		strings.TrimSpace(payload.GetDatasetId()) == "" ||
		strings.TrimSpace(payload.GetFrequency()) == "" ||
		strings.TrimSpace(payload.GetBatchKind()) == "" {
		return fmt.Errorf("market fetch batch identity is incomplete")
	}
	if payload.GetBatchId() != message.GetEventId() {
		return fmt.Errorf("market fetch batch_id does not match event_id")
	}
	switch payload.GetBatchKind() {
	case "realtime", "instrument_snapshot", "catchup", "backfill", "gap_repair":
	default:
		return fmt.Errorf("market fetch batch_kind %q is invalid", payload.GetBatchKind())
	}
	switch payload.GetStatus() {
	case "succeeded", "partial_failed", "failed", "timed_out":
	default:
		return fmt.Errorf("market fetch status %q is invalid", payload.GetStatus())
	}
	if message.GetSpaceId() != "" && payload.GetNodeId() == "" {
		return fmt.Errorf("market fetch node_id is required")
	}
	if message.GetSubjectId() != payload.GetDatasetId() {
		return fmt.Errorf("market fetch dataset does not match subject_id")
	}
	if payload.GetPlannedCount() < 0 || payload.GetSuccessCount() < 0 ||
		payload.GetRetryCount() < 0 || payload.GetPermanentFailedCount() < 0 {
		return fmt.Errorf("market fetch counts must be non-negative")
	}
	if payload.GetPlannedCount() != int32(len(payload.GetItems())) {
		return fmt.Errorf("market fetch planned_count does not match items")
	}
	var success, retryable, permanent int32
	if payload.GetCompletedAt() == nil || payload.GetCompletedAt().CheckValid() != nil {
		return fmt.Errorf("market fetch completed_at is invalid")
	}
	if len(payload.GetErrorSummary()) > 256 {
		return fmt.Errorf("market fetch error_summary exceeds 256 bytes")
	}
	for i, item := range payload.GetItems() {
		if item == nil || strings.TrimSpace(item.GetSubjectId()) == "" || strings.TrimSpace(item.GetOutcome()) == "" {
			return fmt.Errorf("market fetch item %d identity is incomplete", i)
		}
		if len(item.GetErrorSummary()) > 256 {
			return fmt.Errorf("market fetch item %d error_summary exceeds 256 bytes", i)
		}
		switch item.GetOutcome() {
		case "success":
			success++
		case "http_429", "http_5xx", "network_error", "storage_error", "provider_error":
			retryable++
		case "invalid_request":
			permanent++
		default:
			return fmt.Errorf("market fetch item %d outcome %q is invalid", i, item.GetOutcome())
		}
	}
	if payload.GetSuccessCount() != success || payload.GetRetryCount() != retryable || payload.GetPermanentFailedCount() != permanent {
		return fmt.Errorf("market fetch outcome counts do not match items")
	}
	if payload.GetSuccessCount()+payload.GetRetryCount()+payload.GetPermanentFailedCount() != payload.GetPlannedCount() {
		return fmt.Errorf("market fetch outcome counts do not sum to planned_count")
	}
	return nil
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
