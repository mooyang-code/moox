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
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/observabilitypb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"google.golang.org/protobuf/proto"
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
	if strings.TrimSpace(payload.GetAgentId()) == "" ||
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

const maxTargetQuantityLength = 256

var decimalQuantityPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func validateLogicalAccountTargetRequested(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*tradeeventpb.LogicalAccountTargetRequested)
	if !ok {
		return fmt.Errorf("trade target payload has type %T", value)
	}
	if strings.TrimSpace(payload.GetTargetId()) == "" ||
		strings.TrimSpace(payload.GetRunnerId()) == "" ||
		strings.TrimSpace(payload.GetLogicalAccountId()) == "" ||
		payload.GetCommandSequence() <= 0 {
		return fmt.Errorf("trade target identity or command_sequence is incomplete")
	}
	seenInstruments := make(map[string]struct{}, len(payload.GetTargets()))
	for i, target := range payload.GetTargets() {
		if target == nil {
			return fmt.Errorf("trade target %d is nil", i)
		}
		instrumentID := target.GetInstrumentId()
		if strings.TrimSpace(instrumentID) == "" || strings.TrimSpace(instrumentID) != instrumentID {
			return fmt.Errorf("trade target %d instrument_id is empty", i)
		}
		if _, exists := seenInstruments[instrumentID]; exists {
			return fmt.Errorf("trade target instrument_id %q is duplicated", instrumentID)
		}
		seenInstruments[instrumentID] = struct{}{}
		quantity := target.GetQuantity()
		if len(quantity) > maxTargetQuantityLength || !decimalQuantityPattern.MatchString(quantity) {
			return fmt.Errorf("trade target %d quantity is not decimal", i)
		}
		if _, ok := new(big.Rat).SetString(quantity); !ok {
			return fmt.Errorf("trade target %d quantity is not decimal", i)
		}
	}
	if payload.GetTargetId() != message.GetEventId() {
		return fmt.Errorf("trade target target_id does not match event_id")
	}
	if payload.GetLogicalAccountId() != message.GetSubjectId() {
		return fmt.Errorf("trade target logical_account_id does not match subject_id")
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
